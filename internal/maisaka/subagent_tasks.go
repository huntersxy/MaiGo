package maisaka

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// SubAgentTaskStatus 后台子代理任务状态
type SubAgentTaskStatus string

const (
	// SubAgentRunning 运行中
	SubAgentRunning SubAgentTaskStatus = "running"
	// SubAgentCompleted 已完成
	SubAgentCompleted SubAgentTaskStatus = "completed"
	// SubAgentFailed 已失败
	SubAgentFailed SubAgentTaskStatus = "failed"
	// SubAgentStopped 被停止
	SubAgentStopped SubAgentTaskStatus = "stopped"
)

const (
	// SubAgentMaxRunningTasks 同一会话里同时运行的后台子代理上限
	SubAgentMaxRunningTasks = 3
	// SubAgentFinishedTaskKeepCount 已结束任务的保留条数，供 subagent_status 回看
	SubAgentFinishedTaskKeepCount = 20
)

// SubAgentOutcome 子代理一次完整运行的产出
type SubAgentOutcome struct {
	Conclusion string
	ToolUsage  map[string]int
}

// SubAgentTask 一个后台子代理任务
type SubAgentTask struct {
	TaskID       string
	Description  string
	GrantedTools []string
	StartedAt    time.Time
	FinishedAt   *time.Time
	Status       SubAgentTaskStatus
	ToolUsage    map[string]int
	Conclusion   string
	ErrorMessage string

	cancel context.CancelFunc
	done   chan struct{}
}

// ElapsedSeconds 任务已经运行（或总共运行）的秒数
func (t *SubAgentTask) ElapsedSeconds() float64 {
	if t.FinishedAt != nil {
		return t.FinishedAt.Sub(t.StartedAt).Seconds()
	}
	return time.Since(t.StartedAt).Seconds()
}

// FormatToolUsage 把工具调用次数整理成一行说明
//
// Python 按 dict 插入序（首次调用顺序）；Go 的 map 无插入序，这里按名称排序，
// 保证同一 usage 每次输出一致。
func FormatToolUsage(toolUsage map[string]int) string {
	if len(toolUsage) == 0 {
		return "未调用工具"
	}
	names := make([]string, 0, len(toolUsage))
	for name := range toolUsage {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s×%d", name, toolUsage[name]))
	}
	return strings.Join(parts, "、")
}

// SubAgentTaskRunner 实际运行子代理并返回结论
type SubAgentTaskRunner func(ctx context.Context, task *SubAgentTask) SubAgentOutcome

// SubAgentFinishedCallback 任务自然结束（完成或失败）时的回调；被 stop 的任务不回调
type SubAgentFinishedCallback func(task *SubAgentTask)

// SubAgentTaskManager 登记并管理一个会话内的后台子代理任务
type SubAgentTaskManager struct {
	mu      sync.Mutex
	tasks   map[string]*SubAgentTask
	order   []string
	counter int
}

// NewSubAgentTaskManager 创建任务登记表
func NewSubAgentTaskManager() *SubAgentTaskManager {
	return &SubAgentTaskManager{tasks: map[string]*SubAgentTask{}}
}

// RunningCount 当前仍在运行的任务数量
func (m *SubAgentTaskManager) RunningCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, task := range m.tasks {
		if task.Status == SubAgentRunning {
			count++
		}
	}
	return count
}

// Get 按 id 取任务
func (m *SubAgentTaskManager) Get(taskID string) (*SubAgentTask, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[taskID]
	return task, ok
}

// ListTasks 按启动顺序返回全部已登记任务
func (m *SubAgentTaskManager) ListTasks() []*SubAgentTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*SubAgentTask, 0, len(m.order))
	for _, taskID := range m.order {
		if task, ok := m.tasks[taskID]; ok {
			out = append(out, task)
		}
	}
	return out
}

// AllocateTaskID 分配下一个任务 id；同步运行的子代理也用它在观察事件里标识自己
func (m *SubAgentTaskManager) AllocateTaskID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counter++
	return fmt.Sprintf("subagent-%d", m.counter)
}

// Start 登记一个任务并在后台开始运行
func (m *SubAgentTaskManager) Start(taskID, description string, grantedTools []string, runner SubAgentTaskRunner, onFinished SubAgentFinishedCallback) *SubAgentTask {
	ctx, cancel := context.WithCancel(context.Background())
	task := &SubAgentTask{
		TaskID:       taskID,
		Description:  description,
		GrantedTools: append([]string(nil), grantedTools...),
		StartedAt:    time.Now(),
		Status:       SubAgentRunning,
		ToolUsage:    map[string]int{},
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	m.mu.Lock()
	m.tasks[task.TaskID] = task
	m.order = append(m.order, task.TaskID)
	m.mu.Unlock()
	go m.run(ctx, task, runner, onFinished)
	m.dropOldFinishedTasks()
	return task
}

func (m *SubAgentTaskManager) run(ctx context.Context, task *SubAgentTask, runner SubAgentTaskRunner, onFinished SubAgentFinishedCallback) {
	defer task.cancel()
	defer close(task.done)
	var outcome SubAgentOutcome
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				zap.L().Error("后台子代理任务执行失败", zap.String("task_id", task.TaskID), zap.Any("panic", recovered))
				m.mu.Lock()
				task.Status = SubAgentFailed
				task.ErrorMessage = fmt.Sprintf("%T: %v", recovered, recovered)
				m.mu.Unlock()
			}
		}()
		outcome = runner(ctx, task)
	}()
	m.mu.Lock()
	if task.Status == SubAgentStopped {
		// 被 stop 的任务不覆盖状态、不回调
		m.mu.Unlock()
		return
	}
	task.Conclusion = outcome.Conclusion
	if outcome.ToolUsage != nil {
		task.ToolUsage = outcome.ToolUsage
	}
	if outcome.Conclusion == "" {
		task.Status = SubAgentFailed
		task.ErrorMessage = "子代理没有给出结论"
	} else {
		task.Status = SubAgentCompleted
	}
	finished := time.Now()
	task.FinishedAt = &finished
	m.mu.Unlock()
	if onFinished != nil {
		onFinished(task)
	}
}

// Stop 终止一个仍在运行的任务；任务不存在或已结束时返回 false
func (m *SubAgentTaskManager) Stop(taskID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[taskID]
	if !ok || task.Status != SubAgentRunning || task.cancel == nil {
		return false
	}
	task.cancel()
	task.Status = SubAgentStopped
	finished := time.Now()
	task.FinishedAt = &finished
	return true
}

// CancelAll 运行时停止时终止全部后台任务并等待退出（对齐 Python gather）
func (m *SubAgentTaskManager) CancelAll() {
	m.mu.Lock()
	targets := make([]*SubAgentTask, 0, len(m.tasks))
	for _, task := range m.tasks {
		if task.Status == SubAgentRunning && task.cancel != nil {
			targets = append(targets, task)
		}
	}
	dones := make([]<-chan struct{}, 0, len(targets))
	for _, task := range targets {
		task.cancel()
		task.Status = SubAgentStopped
		finished := time.Now()
		task.FinishedAt = &finished
		dones = append(dones, task.done)
	}
	m.mu.Unlock()
	for _, done := range dones {
		if done != nil {
			<-done
		}
	}
}

func (m *SubAgentTaskManager) dropOldFinishedTasks() {
	m.mu.Lock()
	defer m.mu.Unlock()
	var finished []string
	for _, taskID := range m.order {
		task, ok := m.tasks[taskID]
		if ok && task.Status != SubAgentRunning {
			finished = append(finished, taskID)
		}
	}
	drop := len(finished) - SubAgentFinishedTaskKeepCount
	for index := 0; index < drop; index++ {
		delete(m.tasks, finished[index])
	}
	if drop > 0 {
		remaining := m.order[:0]
		for _, taskID := range m.order {
			if _, ok := m.tasks[taskID]; ok {
				remaining = append(remaining, taskID)
			}
		}
		m.order = remaining
	}
}
