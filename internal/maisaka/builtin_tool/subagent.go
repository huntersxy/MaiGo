package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"maibot-go/internal/llm_models"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

const (
	subagentMaxRunningTasks   = 3
	subagentFinishedTaskKeep  = 20
	subagentTaskPreviewRunes  = 80
	subagentSynchronousRounds = 5
	subagentBackgroundRounds  = 20
	subagentSingleCallTimeout = 3 * time.Minute
	// subagentSystemPromptTemplate 对齐 Python subagent.py:69-77 的 SUBAGENT_SYSTEM_PROMPT
	subagentSystemPromptTemplate = "你是一个被派发任务的子代理。派发者看不到你的工作过程，只会收到你最后一条不带工具调用的文字。\n" +
		"工作方式：\n" +
		"1. 任务说明就是你知道的全部背景，不要假设任何没有写出来的信息。\n" +
		"2. 用提供的工具查清楚需要的事实；互不依赖的调用可以一次并行发出。\n" +
		"3. 工具返回无结果或失败时换一种查法，确实查不到就如实说明，不要编造。\n" +
		"4. 查清后直接输出结论：先给答案，再列出支撑它的关键事实；派发者后续可能用到的 id、链接、路径、版本号原样保留。\n" +
		"5. 你不能向任何人发言，也不能把任务再派发出去。结论控制在 800 字以内。"
)

// SubagentTaskStatus 与 MaiBot subagent_tasks.SubAgentTaskStatus 对齐
type SubagentTaskStatus string

const (
	SubagentStatusRunning   SubagentTaskStatus = "running"
	SubagentStatusCompleted SubagentTaskStatus = "completed"
	SubagentStatusFailed    SubagentTaskStatus = "failed"
	SubagentStatusStopped   SubagentTaskStatus = "stopped"
)

var (
	subagentStoreMu sync.Mutex
	subagentStores  = map[string]*SubagentTaskManager{}
)

// SubagentTask 一次子代理运行
type SubagentTask struct {
	TaskID        string
	Description   string
	GrantedTools  []string
	RejectedTools []string
	StartedAt     time.Time
	FinishedAt    time.Time
	Status        SubagentTaskStatus
	Conclusion    string
	ErrorMessage  string
	ToolUsage     map[string]int

	cancel context.CancelFunc
}

func (t *SubagentTask) elapsedSeconds() float64 {
	end := t.FinishedAt
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(t.StartedAt).Seconds()
}

// SubagentTaskManager 单会话内的子代理任务登记
type SubagentTaskManager struct {
	mu             sync.Mutex
	counter        int
	tasks          map[string]*SubagentTask
	running        int
	llmRouter      *llm.Router
	parentCtx      context.Context
	subagentRunner func(ctx context.Context, task string, grantedTools []string, maxRounds int) (string, map[string]int, error)
	advanced       bool
}

func subagentSessionKey(kind string, targetID int64) string {
	return fmt.Sprintf("%s:%d", kind, targetID)
}

func subagentManagerForContext(ctx context.Context) (*SubagentTaskManager, bool) {
	tc := GetToolContext(ctx)
	if tc == nil || tc.ConversationKind == "" {
		return nil, false
	}
	key := subagentSessionKey(tc.ConversationKind, tc.TargetID)
	subagentStoreMu.Lock()
	defer subagentStoreMu.Unlock()
	mgr, ok := subagentStores[key]
	if !ok {
		mgr = &SubagentTaskManager{
			tasks:     make(map[string]*SubagentTask),
			llmRouter: tc.LLMRouter,
			parentCtx: ctx,
		}
		subagentStores[key] = mgr
	} else {
		mgr.llmRouter = tc.LLMRouter
		mgr.parentCtx = ctx
	}
	mgr.subagentRunner = tc.SubagentRunner
	mgr.advanced = tc.AdvancedSubagent
	return mgr, true
}

func (m *SubagentTaskManager) runningCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func (m *SubagentTaskManager) allocateTaskID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counter++
	return fmt.Sprintf("subagent-%d", m.counter)
}

func (m *SubagentTaskManager) get(taskID string) *SubagentTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tasks[taskID]
}

func (m *SubagentTaskManager) listTasks() []*SubagentTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*SubagentTask, 0, len(m.tasks))
	for _, task := range m.tasks {
		out = append(out, task)
	}
	return out
}

func (m *SubagentTaskManager) dropOldFinishedLocked() {
	type finished struct {
		id         string
		finishedAt time.Time
	}
	var done []finished
	for id, task := range m.tasks {
		if task.Status == SubagentStatusRunning {
			continue
		}
		ft := task.FinishedAt
		if ft.IsZero() {
			ft = task.StartedAt
		}
		done = append(done, finished{id: id, finishedAt: ft})
	}
	if len(done) <= subagentFinishedTaskKeep {
		return
	}
	// 简单按完成时间删最旧的
	for i := 0; i < len(done)-1; i++ {
		for j := i + 1; j < len(done); j++ {
			if done[j].finishedAt.Before(done[i].finishedAt) {
				done[i], done[j] = done[j], done[i]
			}
		}
	}
	for _, row := range done[subagentFinishedTaskKeep:] {
		delete(m.tasks, row.id)
	}
}

func (m *SubagentTaskManager) start(taskID, description string, granted, rejected []string, runner func(ctx context.Context, task string, grantedTools []string, maxRounds int) (string, map[string]int, error), maxRounds int) (*SubagentTask, error) {
	m.mu.Lock()
	if m.running >= subagentMaxRunningTasks {
		m.mu.Unlock()
		return nil, fmt.Errorf("已有 %d 个子代理在运行，达到上限；请等待结束或用 subagent_stop 终止后再派发", subagentMaxRunningTasks)
	}
	parent := m.parentCtx
	if parent == nil {
		parent = context.Background()
	}
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), subagentSingleCallTimeout)
	task := &SubagentTask{
		TaskID:        taskID,
		Description:   description,
		GrantedTools:  append([]string(nil), granted...),
		RejectedTools: append([]string(nil), rejected...),
		StartedAt:     time.Now(),
		Status:        SubagentStatusRunning,
		cancel:        cancel,
	}
	m.tasks[taskID] = task
	m.running++
	router := m.llmRouter
	m.mu.Unlock()

	go m.run(runCtx, task, router, runner, maxRounds, cancel)
	return task, nil
}

func (m *SubagentTaskManager) run(runCtx context.Context, task *SubagentTask, router *llm.Router, runner func(ctx context.Context, task string, grantedTools []string, maxRounds int) (string, map[string]int, error), maxRounds int, cancel context.CancelFunc) {
	defer cancel()
	defer func() {
		m.mu.Lock()
		m.running--
		if m.running < 0 {
			m.running = 0
		}
		m.mu.Unlock()
	}()

	var conclusion string
	var err error
	var usage map[string]int
	if runner != nil {
		conclusion, usage, err = runner(runCtx, task.Description, task.GrantedTools, maxRounds)
	} else {
		conclusion, err = subagentSingleLLMCall(runCtx, router, task.Description, task.GrantedTools)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	task.FinishedAt = time.Now()
	task.ToolUsage = usage
	if task.Status == SubagentStatusStopped || errors.Is(err, context.Canceled) || runCtx.Err() == context.Canceled {
		if task.Status == SubagentStatusRunning {
			task.Status = SubagentStatusStopped
		}
		m.dropOldFinishedLocked()
		return
	}
	if err != nil {
		task.Status = SubagentStatusFailed
		task.ErrorMessage = err.Error()
		zap.L().Warn("子代理任务失败", zap.String("task_id", task.TaskID), zap.Error(err))
		m.dropOldFinishedLocked()
		return
	}
	conclusion = strings.TrimSpace(conclusion)
	task.Conclusion = conclusion
	if conclusion == "" {
		task.Status = SubagentStatusFailed
		task.ErrorMessage = "子代理没有给出结论"
	} else {
		task.Status = SubagentStatusCompleted
	}
	m.dropOldFinishedLocked()
}

func (m *SubagentTaskManager) stop(taskID string) bool {
	m.mu.Lock()
	task := m.tasks[taskID]
	if task == nil || task.Status != SubagentStatusRunning || task.cancel == nil {
		m.mu.Unlock()
		return false
	}
	task.Status = SubagentStatusStopped
	task.FinishedAt = time.Now()
	cancel := task.cancel
	m.mu.Unlock()
	cancel()
	return true
}

func subagentSingleLLMCall(ctx context.Context, router *llm.Router, taskDesc string, granted []string) (string, error) {
	client, err := subagentLLMClient(ctx, router)
	if err != nil {
		return "", err
	}
	userBody := strings.TrimSpace(taskDesc)
	if len(granted) > 0 {
		userBody += "\n\n[授权工具（当前骨架未执行工具调用，仅供你知道派发者意图）]\n" + strings.Join(granted, "、")
	}
	msg, err := client.Generate(ctx, []*schema.AgenticMessage{
		schema.SystemAgenticMessage(subagentSystemPromptTemplate),
		schema.UserAgenticMessage(userBody),
	})
	if err != nil {
		return "", err
	}
	return agenticMessagePlainText(msg), nil
}

// subagentLLMClient 对齐 MaiBot run_sub_agent 默认 planner；失败时回退 utils（均经 model_list failover）
func subagentLLMClient(ctx context.Context, router *llm.Router) (model.AgenticModel, error) {
	if tc := GetToolContext(ctx); tc != nil {
		if tc.SubagentLLM != nil {
			return tc.SubagentLLM, nil
		}
	}
	if router == nil {
		if tc := GetToolContext(ctx); tc != nil && tc.UtilsLLM != nil {
			return tc.UtilsLLM, nil
		}
		return nil, fmt.Errorf("LLM 路由器未接入，无法运行子代理")
	}
	_, client, err := router.NewClientForTaskWithFailover(ctx, llm.TaskPlanner)
	if err != nil {
		if tc := GetToolContext(ctx); tc != nil && tc.UtilsLLM != nil {
			return tc.UtilsLLM, nil
		}
		_, client, err = router.NewClientForTaskWithFailover(ctx, llm.TaskUtils)
		if err != nil {
			return nil, fmt.Errorf("子代理模型路由失败: %w", err)
		}
	}
	return client, nil
}

func agenticMessagePlainText(msg *schema.AgenticMessage) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	for _, block := range msg.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeUserInputText:
			if block.UserInputText != nil {
				b.WriteString(block.UserInputText.Text)
			}
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil {
				b.WriteString(block.AssistantGenText.Text)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func formatSubagentToolUsage(usage map[string]int) string {
	if len(usage) == 0 {
		return "无"
	}
	names := make([]string, 0, len(usage))
	for name := range usage {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s×%d", name, usage[name]))
	}
	return strings.Join(parts, "、")
}

func formatSubagentTaskSummary(task *SubagentTask) string {
	if task == nil {
		return ""
	}
	preview := strings.ReplaceAll(task.Description, "\n", " ")
	runes := []rune(preview)
	if len(runes) > subagentTaskPreviewRunes {
		preview = string(runes[:subagentTaskPreviewRunes]) + "…"
	}
	statusLabel := subagentStatusLabel(task.Status)
	return fmt.Sprintf("- %s｜%s｜已用 %d 秒｜任务：%s",
		task.TaskID, statusLabel, int(task.elapsedSeconds()), preview)
}

func subagentStatusLabel(status SubagentTaskStatus) string {
	switch status {
	case SubagentStatusRunning:
		return "运行中"
	case SubagentStatusCompleted:
		return "已完成"
	case SubagentStatusFailed:
		return "失败"
	case SubagentStatusStopped:
		return "已终止"
	default:
		return string(status)
	}
}

var subagentForbiddenTools = map[string]struct{}{
	"subagent":        {},
	"subagent_status": {},
	"subagent_stop":   {},
	"react_message":   {},
	"reply":           {},
	"send_emoji":      {},
	"show_emoji_list": {},
	"tool_search":     {},
	"wait":            {},
	"stop":            {},
}

func normalizeSubagentToolGrants(requested []string) (granted, rejected []string) {
	seen := map[string]struct{}{}
	for _, raw := range requested {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		if _, forbidden := subagentForbiddenTools[name]; forbidden {
			rejected = append(rejected, name)
			continue
		}
		granted = append(granted, name)
	}
	return granted, rejected
}

type subagentTaskSnapshot struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

type subagentToolOutput struct {
	Success       bool                   `json:"success"`
	Message       string                 `json:"message,omitempty"`
	TaskID        string                 `json:"task_id,omitempty"`
	Status        string                 `json:"status,omitempty"`
	GrantedTools  []string               `json:"granted_tools,omitempty"`
	RejectedTools []string               `json:"rejected_tools,omitempty"`
	ToolUsage     map[string]int         `json:"tool_usage,omitempty"`
	Tasks         []subagentTaskSnapshot `json:"tasks,omitempty"`
}

func subagentFunc(ctx context.Context, input *SubagentInput) (*subagentToolOutput, error) {
	if input == nil || strings.TrimSpace(input.Task) == "" {
		return &subagentToolOutput{Success: false, Message: "subagent 需要提供非空的 task"}, nil
	}
	taskDesc := strings.TrimSpace(input.Task)
	if len(input.Tools) == 0 {
		return &subagentToolOutput{Success: false, Message: "subagent 需要在 tools 里至少授权一个工具"}, nil
	}
	granted, rejected := normalizeSubagentToolGrants(input.Tools)
	if len(granted) == 0 {
		return &subagentToolOutput{
			Success: false,
			Message: fmt.Sprintf("没有可以授权给子代理的工具：%s 属于只能由 Planner 调用的工具或尚未授权", strings.Join(rejected, "、")),
		}, nil
	}
	tc := GetToolContext(ctx)
	if tc != nil && !tc.AdvancedSubagent && tc.SubagentRunner != nil {
		conclusion, usage, err := tc.SubagentRunner(ctx, taskDesc, granted, subagentSynchronousRounds)
		if err != nil {
			return &subagentToolOutput{Success: false, Message: fmt.Sprintf("子代理执行失败：%v", err)}, nil
		}
		conclusion = strings.TrimSpace(conclusion)
		usageText := formatSubagentToolUsage(usage)
		if conclusion == "" {
			return &subagentToolOutput{Success: false, Message: fmt.Sprintf("子代理没有给出结论（工具使用：%s）", usageText)}, nil
		}
		rejectedNote := ""
		if len(rejected) > 0 {
			rejectedNote = fmt.Sprintf("\n（未授权的工具：%s，它们不存在或只能由你自己调用）", strings.Join(rejected, "、"))
		}
		return &subagentToolOutput{
			Success:       true,
			Message:       fmt.Sprintf("[子代理结论｜工具使用：%s]\n%s%s", usageText, conclusion, rejectedNote),
			GrantedTools:  granted,
			RejectedTools: rejected,
			ToolUsage:     usage,
		}, nil
	}
	mgr, ok := subagentManagerForContext(ctx)
	if !ok {
		return &subagentToolOutput{Success: false, Message: "子代理任务管理器未接入（缺少会话 ToolContext）"}, nil
	}
	taskID := mgr.allocateTaskID()
	maxRounds := subagentSynchronousRounds
	if mgr.advanced {
		maxRounds = subagentBackgroundRounds
	}
	_, err := mgr.start(taskID, taskDesc, granted, rejected, mgr.subagentRunner, maxRounds)
	if err != nil {
		return &subagentToolOutput{Success: false, Message: err.Error()}, nil
	}
	rejectedNote := ""
	if len(rejected) > 0 {
		rejectedNote = fmt.Sprintf("\n（未授权的工具：%s）", strings.Join(rejected, "、"))
	}
	return &subagentToolOutput{
		Success: true,
		TaskID:  taskID,
		Message: fmt.Sprintf(
			"子代理已在后台开始工作，任务 id：%s。它结束时结论会作为一条 <subagent_result> 消息送回给你；"+
				"现在可以继续处理聊天，不需要为它等待。%s",
			taskID, rejectedNote,
		),
		GrantedTools:  granted,
		RejectedTools: rejected,
	}, nil
}

type SubagentStatusInput struct {
	TaskID string `json:"task_id,omitempty" jsonschema:"description=要查看的任务 id；省略则列出全部任务"`
}

func subagentStatusFunc(ctx context.Context, input *SubagentStatusInput) (*subagentToolOutput, error) {
	mgr, ok := subagentManagerForContext(ctx)
	if !ok {
		return &subagentToolOutput{Success: false, Message: "子代理任务管理器未接入（缺少会话 ToolContext）"}, nil
	}
	taskID := ""
	if input != nil {
		taskID = strings.TrimSpace(input.TaskID)
	}
	if taskID == "" {
		tasks := mgr.listTasks()
		if len(tasks) == 0 {
			return &subagentToolOutput{Success: true, Message: "当前没有后台子代理任务。"}, nil
		}
		lines := make([]string, 0, len(tasks))
		snapshots := make([]subagentTaskSnapshot, 0, len(tasks))
		for _, task := range tasks {
			lines = append(lines, formatSubagentTaskSummary(task))
			snapshots = append(snapshots, subagentTaskSnapshot{TaskID: task.TaskID, Status: string(task.Status)})
		}
		return &subagentToolOutput{Success: true, Message: strings.Join(lines, "\n"), Tasks: snapshots}, nil
	}
	task := mgr.get(taskID)
	if task == nil {
		return &subagentToolOutput{Success: false, Message: fmt.Sprintf("没有 id 为 %s 的子代理任务。", taskID)}, nil
	}
	lines := []string{formatSubagentTaskSummary(task)}
	if len(task.GrantedTools) > 0 {
		lines = append(lines, "授权工具："+strings.Join(task.GrantedTools, "、"))
	}
	if len(task.ToolUsage) > 0 {
		lines = append(lines, "工具使用："+formatSubagentToolUsage(task.ToolUsage))
	}
	switch task.Status {
	case SubagentStatusCompleted:
		lines = append(lines, "结论：\n"+task.Conclusion)
	case SubagentStatusFailed:
		lines = append(lines, "失败原因："+task.ErrorMessage)
	case SubagentStatusStopped:
		lines = append(lines, "任务已终止，不会再送回结论。")
	}
	return &subagentToolOutput{
		Success:       true,
		TaskID:        task.TaskID,
		Status:        string(task.Status),
		Message:       strings.Join(lines, "\n"),
		GrantedTools:  task.GrantedTools,
		RejectedTools: task.RejectedTools,
		ToolUsage:     task.ToolUsage,
	}, nil
}

func subagentStopFunc(ctx context.Context, input *SubagentStopInput) (*subagentToolOutput, error) {
	mgr, ok := subagentManagerForContext(ctx)
	if !ok {
		return &subagentToolOutput{Success: false, Message: "子代理任务管理器未接入（缺少会话 ToolContext）"}, nil
	}
	taskID := ""
	if input != nil {
		taskID = strings.TrimSpace(input.TaskID)
	}
	if taskID == "" {
		return &subagentToolOutput{Success: false, Message: "subagent_stop 需要提供 task_id"}, nil
	}
	task := mgr.get(taskID)
	if task == nil {
		return &subagentToolOutput{Success: false, Message: fmt.Sprintf("没有 id 为 %s 的子代理任务。", taskID)}, nil
	}
	if task.Status != SubagentStatusRunning {
		return &subagentToolOutput{
			Success: false,
			Message: fmt.Sprintf("任务 %s 已经结束（%s），不需要终止。", taskID, subagentStatusLabel(task.Status)),
		}, nil
	}
	if !mgr.stop(taskID) {
		return &subagentToolOutput{Success: false, Message: fmt.Sprintf("任务 %s 无法终止。", taskID)}, nil
	}
	return &subagentToolOutput{Success: true, TaskID: taskID, Message: fmt.Sprintf("已终止子代理任务 %s，它不会再送回结论。", taskID)}, nil
}
