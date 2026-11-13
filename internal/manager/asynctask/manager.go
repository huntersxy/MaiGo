// Package asynctask 提供进程级异步任务调度，对齐 MaiBot src/manager/async_task_manager.py。
//
// Python 用全局单例 async_task_manager 统一管理周期任务，各模块通过
// add_task 注册、stop_and_wait_all_tasks 优雅退出；本包按同样语义提供
// 注册、替换、状态查询与等待能力。
package asynctask

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// defaultStopTimeout 对齐 Python stop_and_wait_all_tasks 的 10s 上限
const defaultStopTimeout = 10 * time.Second

// replaceWaitTimeout 对齐 Python 替换同名任务时等待旧任务的 5s 上限
const replaceWaitTimeout = 5 * time.Second

// Task 异步任务，对齐 Python AsyncTask
//
// 子类实现 Run；WaitBeforeStart 与 RunInterval 语义同 Python：
// WaitBeforeStart 为首次运行前等待秒数，RunInterval 为两次运行间隔，
// 为 0 表示只运行一次。
type Task struct {
	// TaskName 任务名，空时用类型名
	TaskName string
	// WaitBeforeStart 首次运行前等待时长，0 表示不等待
	WaitBeforeStart time.Duration
	// RunInterval 重复运行间隔，0 表示只运行一次
	RunInterval time.Duration
}

// Name 返回任务名
func (t *Task) Name() string {
	if t == nil {
		return ""
	}
	if t.TaskName != "" {
		return t.TaskName
	}
	return "task"
}

// Spec 返回任务自身的调度参数
//
// 嵌入 Task 的类型自动获得该方法，无需再写 TaskSpec。
func (t *Task) Spec() *Task { return t }

// Runner 可被调度的任务
//
// 对齐 Python AsyncTask.start_task：先等 WaitBeforeStart，
// 然后循环 Run 直到中止标志置位，RunInterval 为 0 时只跑一次。
type Runner interface {
	// Spec 返回调度参数
	Spec() *Task
	// Run 执行一次任务
	Run(ctx context.Context) error
}

// taskEntry 一个已启动任务的运行期状态
type taskEntry struct {
	name     string
	cancel   context.CancelFunc
	done     chan struct{}
	errOnce  sync.Once
	lastErr  error
	finished bool
}

// Manager 异步任务管理器，对齐 Python AsyncTaskManager
type Manager struct {
	mu     sync.Mutex
	tasks  map[string]*taskEntry
	closed bool
}

// New 构造任务管理器
func New() *Manager {
	return &Manager{tasks: make(map[string]*taskEntry)}
}

var (
	defaultManager = New()
	defaultMu      sync.RWMutex
)

// Default 返回全局管理器，对齐 Python 的 async_task_manager 单例
func Default() *Manager {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultManager
}

// SetDefault 替换全局管理器，供测试注入
func SetDefault(manager *Manager) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if manager == nil {
		manager = New()
	}
	defaultManager = manager
}

// Add 添加并启动任务，对齐 Python add_task
//
// 同名任务已存在时先取消旧任务并等待其退出（最多 5s），再启动新任务。
func (m *Manager) Add(ctx context.Context, runner Runner) error {
	if m == nil {
		return errors.New("任务管理器为空")
	}
	if runner == nil {
		return errors.New("任务为空")
	}
	spec := runner.Spec()
	if spec == nil {
		return errors.New("任务缺少调度参数")
	}
	name := spec.Name()
	if name == "" {
		return errors.New("任务名不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("任务管理器已停止")
	}
	old := m.tasks[name]
	m.mu.Unlock()

	if old != nil {
		// 对齐 Python：同名任务取消并等待，超时只告警
		zap.L().Warn("已存在同名任务，取消并替换", zap.String("task", name))
		old.cancel()
		select {
		case <-old.done:
		case <-time.After(replaceWaitTimeout):
			zap.L().Warn("等待旧任务退出超时", zap.String("task", name))
		}
	}

	taskCtx, cancel := context.WithCancel(ctx)
	entry := &taskEntry{name: name, cancel: cancel, done: make(chan struct{})}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		return errors.New("任务管理器已停止")
	}
	m.tasks[name] = entry
	m.mu.Unlock()

	go m.runTask(taskCtx, entry, spec, runner)
	return nil
}

// runTask 任务主循环，对齐 Python start_task
func (m *Manager) runTask(ctx context.Context, entry *taskEntry, spec *Task, runner Runner) {
	defer close(entry.done)

	if spec.WaitBeforeStart > 0 {
		timer := time.NewTimer(spec.WaitBeforeStart)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			m.finish(entry, ctx.Err())
			return
		}
	}

	// 对齐 Python start_task：while not abort_flag.is_set(): await run()
	// 首次进入先执行，再判断中止，避免刚启动就被 Stop 打断
	for {
		err := runner.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			zap.L().Error("任务执行异常", zap.String("task", entry.name), zap.Error(err))
		}
		if spec.RunInterval <= 0 {
			m.finish(entry, err)
			return
		}
		if ctx.Err() != nil {
			m.finish(entry, ctx.Err())
			return
		}
		timer := time.NewTimer(spec.RunInterval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			m.finish(entry, ctx.Err())
			return
		}
	}
}

// finish 记录结束状态并从任务表移除，对齐 Python _remove_task_call_back
func (m *Manager) finish(entry *taskEntry, err error) {
	entry.errOnce.Do(func() {
		entry.lastErr = err
		entry.finished = true
	})
	m.mu.Lock()
	// 只有表里仍是自己时才删除，避免误删同名新任务
	if current, ok := m.tasks[entry.name]; ok && current == entry {
		delete(m.tasks, entry.name)
	}
	m.mu.Unlock()
}

// Status 返回所有任务状态，对齐 Python get_tasks_status
//
// 键为任务名，值为 "running" 或 "done"。
func (m *Manager) Status() map[string]string {
	if m == nil {
		return map[string]string{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.tasks))
	for name, entry := range m.tasks {
		select {
		case <-entry.done:
			out[name] = "done"
		default:
			out[name] = "running"
		}
	}
	return out
}

// StopAll 终止所有任务并等待退出，对齐 Python stop_and_wait_all_tasks
//
// 超时返回错误；无论成功与否都清空任务表并解除 closed 状态。
func (m *Manager) StopAll(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), defaultStopTimeout)
		defer cancel()
	}

	m.mu.Lock()
	m.closed = true
	entries := make([]*taskEntry, 0, len(m.tasks))
	for _, entry := range m.tasks {
		entries = append(entries, entry)
	}
	m.mu.Unlock()

	for _, entry := range entries {
		entry.cancel()
	}

	var firstErr error
	for _, entry := range entries {
		select {
		case <-entry.done:
		case <-ctx.Done():
			if firstErr == nil {
				firstErr = fmt.Errorf("等待任务 %s 退出超时: %w", entry.name, ctx.Err())
			}
		}
	}

	m.mu.Lock()
	m.tasks = make(map[string]*taskEntry)
	m.closed = false
	m.mu.Unlock()

	if firstErr == nil {
		zap.L().Info("所有异步任务已停止")
	}
	return firstErr
}

// DebugStatus 打印任务状态，对齐 Python debug_task_status
func (m *Manager) DebugStatus() {
	if m == nil {
		return
	}
	m.mu.Lock()
	count := len(m.tasks)
	names := make([]string, 0, count)
	states := make(map[string]string, count)
	for name, entry := range m.tasks {
		names = append(names, name)
		select {
		case <-entry.done:
			states[name] = "done"
		default:
			states[name] = "running"
		}
	}
	m.mu.Unlock()

	zap.L().Info("异步任务状态",
		zap.Int("count", count))
	for _, name := range names {
		zap.L().Info("任务状态", zap.String("task", name), zap.String("status", states[name]))
	}
}
