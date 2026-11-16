package maisaka

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"MaiGo/internal/maisaka/turn_trigger"
)

// 对齐 runtime.py 的常量
const (
	// MaxInternalRounds 单轮内部思考最多执行的模型—工具循环轮数
	MaxInternalRounds = 16
	// MaxRetainedMessageCacheSize 已消费消息缓存保留上限
	MaxRetainedMessageCacheSize = 200
	// ContextRestoreFillRatio 启动上下文回灌与主动回合回填占窗口比例
	ContextRestoreFillRatio = 0.5
	// ContextRestoreShortRestartSeconds 短重启阈值
	ContextRestoreShortRestartSeconds = 5 * 60
	// ContextRestoreShortOfflineSeconds 短暂离线阈值
	ContextRestoreShortOfflineSeconds = 30 * 60
	// ContextRestoreLongSleepSeconds 长睡眠阈值
	ContextRestoreLongSleepSeconds = 6 * 60 * 60
	// ContextRestoreDaySeconds 一天阈值
	ContextRestoreDaySeconds = 24 * 60 * 60
)

// ToolSpec 统一工具声明
type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any
	// SessionID 工具所属会话，Focus 组内用于路由
	SessionID string
	// Deferred 是否只在 tool_search 发现后暴露
	Deferred bool
	// Provider 工具来源，maisaka_builtin 表示内置工具
	Provider string
	// ProviderType 工具来源类型，如 builtin / mcp / plugin
	ProviderType string
	// Title 工具展示标题
	Title string
	// Enabled 工具当前是否可用
	Enabled bool
	// Metadata 工具声明元数据，含 builtin_stage / visibility 等
	Metadata map[string]any
}

// ToLLMDefinition 转成模型可见的工具声明
func (s ToolSpec) ToLLMDefinition() map[string]any {
	parameters := s.Parameters
	if parameters == nil {
		parameters = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        s.Name,
			"description": s.Description,
			"parameters":  parameters,
		},
	}
}

// MonitorEmitter 监控事件出口，对应 monitor.events
type MonitorEmitter interface {
	SessionStart(runID, sessionID, sessionName string, isGroup bool)
	MessageIngested(sessionID string, message ContextMessage)
	MessageSent(sessionID, speakerName, sourceKind string, message ContextMessage)
	MessageUpdated(sessionID string, refreshKey [2]string, message ContextMessage)
	FlowStep(sessionID, step, title, detail string, cycleID int)
	PlannerProgress(sessionID string, payload map[string]any)
	PlannerFinalized(sessionID string, payload map[string]any)
}

// LearnerPort 裁切历史学习端口
type LearnerPort interface {
	TriggerTrimmedHistoryLearning(ctx context.Context, sessionID string, messages []ContextMessage, onProgress func(map[string]any)) error
	CancelTrimmedHistoryLearning()
}

// DebounceObserve 一次消息防抖观测输入
type DebounceObserve struct {
	Author      string
	IsBareImage bool
	IsPureEmoji bool
	Mode        string
	AtBot       bool
	ReceivedAt  time.Time
	WallTime    time.Time
	Now         time.Time
}

// Debouncer 消息防抖端口，实现见 internal/chat/debounce
type Debouncer interface {
	Remaining(now time.Time) time.Duration
	SyncMode(mode string)
	Consume()
	// Observe 更新截止并返回规则说明；Deadline 供 INFO 日志取剩余等待
	Observe(in DebounceObserve) string
	Deadline() (time.Time, bool)
}

// MentionDetector 判定消息是否 @ 或提及机器人
type MentionDetector func(message ContextMessage) (mentioned, at bool, replyProbabilityBoost float64)

// QuoteContextProvider 回复引用快照端口
type QuoteContextProvider interface {
	GetReplyQuoteContext(target ContextMessage) (any, bool)
}

// RuntimeOptions 运行时依赖，由宿主注入
type RuntimeOptions struct {
	SessionID   string
	SessionName string
	// IsGroupSession 是否群聊会话
	IsGroupSession bool
	// ReplyTiming 读取当前生效的回复时序参数
	ReplyTiming func() turn_trigger.IdleBackoffConfig
	// PlannerSteering 是否启用快速反应模式（wait 期间新消息直接续接）
	PlannerSteering bool
	// ReplyFrequency 读取当前生效的回复频率
	ReplyFrequency func() float64
	// MaxContextSize 当前上下文条数上限，对齐 Python chat.max_context_size / max_private_context_size
	MaxContextSize func() int
	// ToolResultCondense 读取当前生效的工具结果整理开关与字符阈值，阈值 <=0 表示关闭
	ToolResultCondense func() (enabled bool, threshold int)
	// OnlineBotAccountIDs 平台在线机器人账号，用于过滤自身消息
	OnlineBotAccountIDs func(platform string) map[string]struct{}
	// MentionDetector 提及检测
	MentionDetector MentionDetector
	// Debouncer 消息防抖，nil 表示不防抖
	Debouncer Debouncer
	// DebounceMode 读取群聊/私聊防抖档位
	DebounceMode func(isGroup bool) string
	// InevitableAtReply @ 必回开关
	InevitableAtReply func() bool
	// MentionedBotReply 提及必回开关
	MentionedBotReply func() bool
	// Monitor 监控事件出口
	Monitor MonitorEmitter
	// Learners 学习端口
	Learners LearnerPort
	// EnableExpressionLearning 表达学习开关
	EnableExpressionLearning func() bool
	// EnableJargonLearning 黑话学习开关
	EnableJargonLearning func() bool
	// FocusActive 当前会话是否处于 Focus 模式
	FocusActive func() bool
	// VisualPlannerEnabled 当前配置下 Planner 是否具备视觉能力，对应 resolve_enable_visual_planner
	VisualPlannerEnabled func() bool
	// ProactiveContextRestore 主动回合回填真实用户消息的端口，nil 表示不回填
	ProactiveContextRestore func(ctx context.Context, limit int) ([]ContextMessage, error)
	// FocusProactiveRegistrar 把主动任务登记到当前 Focus 共享组，返回 true 表示已由 Focus 接管
	FocusProactiveRegistrar func(trigger ContextMessage) bool
	// FocusSentRecorder 把已发送消息登记到当前 Focus 共享组，nil 表示未接入 Focus
	FocusSentRecorder func(message ContextMessage)
	// Engine 推理引擎，nil 时使用默认实现
	Engine *ReasoningEngine
	// SubAgent 临时子代理端口，供工具结果整理与子代理任务使用
	SubAgent SubAgentChatRunner
	// ChatLoop 主对话循环端口，nil 时使用包内默认实现
	ChatLoop ChatLoopRunner
	// Tools 工具执行端口
	Tools ToolRuntime
	// ToolRecords 工具执行记录落库端口，nil 表示不落库
	ToolRecords ToolRecordStore
	// ToolPostExecution 工具记录落库成功后的非落库副作用端口，nil 表示不处理
	ToolPostExecution ToolPostExecutionHandler
	// Prompts Prompt 模板来源
	Prompts PromptSource
	// ToolSearchTailLoading 读取 deferred 工具定义末尾加载开关
	ToolSearchTailLoading func() bool
	// PlannerExtraInjections 本轮 Planner 一次性注入的额外内部参考（启发式记忆、图片记忆等）
	PlannerExtraInjections func(ctx context.Context, trigger *ContextMessage, source []ContextMessage) []string
	// Compactor 历史压缩端口
	Compactor HistoryCompactor
	// HistoryPostProcessor 每轮结束后的历史后处理端口，由 Agent 注入 context 实现
	HistoryPostProcessor HistoryPostProcessor
	// Reply Planner 正文直接触发回复的端口
	Reply ReplyExecutor
	// PassiveReplyer 启用后 Planner 正文直接进入回复执行端口，对应被动回复实验模式
	PassiveReplyer bool
	// VisualRefresh 视觉占位刷新与工具结果图片识别端口
	VisualRefresh VisualRefreshPort
	// ShowThinking 是否在终端展示 Planner 思考过程，nil 表示关闭
	ShowThinking func() bool
	// TerminalOutput 终端面板输出；nil 表示标准输出
	TerminalOutput io.Writer
}

// PlannerInterruptController 维护当前可打断的 Planner 请求；同一次请求只打断一次
type PlannerInterruptController struct {
	mu        sync.Mutex
	flag      chan struct{}
	requested bool
}

// NewPlannerInterruptController 创建打断控制器
func NewPlannerInterruptController() *PlannerInterruptController {
	return &PlannerInterruptController{}
}

// Bind 绑定当前可打断请求使用的中断标记
func (c *PlannerInterruptController) Bind(flag chan struct{}) {
	c.mu.Lock()
	c.flag = flag
	c.requested = false
	c.mu.Unlock()
}

// Unbind 解绑当前可打断请求使用的中断标记
func (c *PlannerInterruptController) Unbind(flag chan struct{}) {
	c.mu.Lock()
	if c.flag == flag {
		c.flag = nil
	}
	c.requested = false
	c.mu.Unlock()
}

// Request 尝试发起打断，返回 request / duplicate / idle
func (c *PlannerInterruptController) Request() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.flag == nil {
		return "idle"
	}
	if c.requested {
		return "duplicate"
	}
	c.requested = true
	close(c.flag)
	return "request"
}

// Runtime 会话级别的 Maisaka 运行时，对齐 MaisakaHeartFlowChatting
type Runtime struct {
	sessionID   string
	sessionName string
	logPrefix   string
	opts        RuntimeOptions

	mu sync.Mutex

	// chatHistory 内部聊天历史
	chatHistory []ContextMessage
	// historyRevision 历史版本号，每次整体或追加写入递增，供后处理检测并发覆盖
	historyRevision uint64
	// transcriptRecording/turnTranscript 记录本轮推理新增的历史，供宿主写回 transcript
	transcriptRecording bool
	turnTranscript      []ContextMessage
	// messageCache 原始消息缓存，用于成批处理与后续学习
	messageCache        []ContextMessage
	lastProcessedIndex  int
	internalTurnQueue   chan TurnTriggerSource
	turnQueueMu         sync.Mutex
	pendingTurnTriggers []TurnTriggerSource

	proactiveTriggerMessage *ContextMessage
	proactiveLogicalTurnID  string

	subagentTasks *SubAgentTaskManager

	currentCycleDetail *CycleDetail
	running            bool
	cycleCounter       int
	monitorRunID       string

	messageTurnScheduled     bool
	deferredTurnCancel       context.CancelFunc
	deferredTurnGen          int
	lastMessageReceivedAt    time.Time
	talkFrequencyAdjust      float64
	waitTimeoutCancel        context.CancelFunc
	maxInternalRounds        int
	agentState               turn_trigger.AgentState
	pendingWaitToolCallID    string
	pendingWaitLogicalTurnID string
	pendingWaitStartedAt     *time.Time
	pendingWaitSeconds       *float64
	pendingWaitAfterReply    bool
	consecutiveWaitCount     int
	consecutiveIdleCount     int

	currentActionToolNames map[string]struct{}
	currentActionToolSpecs map[string]ToolSpec
	discoveredToolNames    map[string]struct{}
	deferredToolSpecs      map[string]ToolSpec
	toolSearchTailLoading  bool
	lightweightPrivate     bool

	minExtractionInterval        float64
	lastExpressionExtractionTime float64
	forcedTurnEnabled            bool
	forcedTurnMessageID          string
	forcedTurnReason             string
	plannerContinuationActive    bool
	plannerInterrupt             *PlannerInterruptController
	messageDebounceWasEnabled    bool
	stage                        string
	stageDetail                  string
	stageStartedAt               float64
	toolResultCondenseThreshold  int
	builtinConfig                BuiltinToolConfig
	builtinConfigSet             bool

	engine    *ReasoningEngine
	scheduler *turn_trigger.MessageTurnScheduler

	ctx    context.Context
	cancel context.CancelFunc
}

// NewRuntime 创建会话级运行时
func NewRuntime(opts RuntimeOptions) *Runtime {
	if opts.ReplyTiming == nil {
		opts.ReplyTiming = func() turn_trigger.IdleBackoffConfig { return turn_trigger.IdleBackoffConfig{} }
	}
	if opts.ReplyFrequency == nil {
		opts.ReplyFrequency = func() float64 { return 1 }
	}
	if opts.DebounceMode == nil {
		opts.DebounceMode = func(bool) string { return "off" }
	}
	if opts.MentionDetector == nil {
		opts.MentionDetector = func(ContextMessage) (bool, bool, float64) { return false, false, 0 }
	}
	if opts.OnlineBotAccountIDs == nil {
		opts.OnlineBotAccountIDs = func(string) map[string]struct{} { return nil }
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &Runtime{
		sessionID:              opts.SessionID,
		sessionName:            firstNonEmpty(opts.SessionName, opts.SessionID),
		opts:                   opts,
		internalTurnQueue:      make(chan TurnTriggerSource, 16),
		subagentTasks:          NewSubAgentTaskManager(),
		maxInternalRounds:      MaxInternalRounds,
		agentState:             turn_trigger.StateStop,
		talkFrequencyAdjust:    1,
		monitorRunID:           newRunID(),
		currentActionToolNames: map[string]struct{}{},
		currentActionToolSpecs: map[string]ToolSpec{},
		discoveredToolNames:    map[string]struct{}{},
		deferredToolSpecs:      map[string]ToolSpec{},
		plannerInterrupt:       NewPlannerInterruptController(),
		engine:                 opts.Engine,
		ctx:                    ctx,
		cancel:                 cancel,
	}
	runtime.logPrefix = "[" + runtime.sessionName + "]"
	// 默认关闭，由宿主的 ToolResultCondense 提供者或 SetToolResultCondenseThreshold 注入
	runtime.toolResultCondenseThreshold = 0
	if runtime.engine == nil {
		runtime.engine = NewReasoningEngine(runtime)
	}
	runtime.engine.runtime = runtime
	runtime.scheduler = turn_trigger.NewMessageTurnScheduler(runtime)
	runtime.emitMonitorSessionStart()
	return runtime
}

// SessionID 会话编号
func (r *Runtime) SessionID() string { return r.sessionID }

// SessionName 会话展示名
func (r *Runtime) SessionName() string { return r.sessionName }

// LogPrefix 日志前缀
func (r *Runtime) LogPrefix() string { return r.logPrefix }

// IsGroupSession 是否群聊会话
func (r *Runtime) IsGroupSession() bool { return r.opts.IsGroupSession }

// Engine 返回推理引擎
func (r *Runtime) Engine() *ReasoningEngine { return r.engine }

// Scheduler 返回触发调度器
func (r *Runtime) Scheduler() *turn_trigger.MessageTurnScheduler { return r.scheduler }

// PlannerSnapshotProvider 可选：主对话循环暴露最近一次成功请求的快照
type PlannerSnapshotProvider interface {
	PlannerSnapshot() *PlannerRequestSnapshot
	ClearPlannerSnapshot()
}

// SetChatLoop 替换主对话循环实现，宿主可在每轮注入真实模型客户端
func (r *Runtime) SetChatLoop(loop ChatLoopRunner) {
	r.mu.Lock()
	r.opts.ChatLoop = loop
	r.mu.Unlock()
}

// PlannerRequestSnapshot 返回主对话循环最近一次成功的 Planner 请求快照
func (r *Runtime) PlannerRequestSnapshot() *PlannerRequestSnapshot {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	loop := r.opts.ChatLoop
	r.mu.Unlock()
	provider, ok := loop.(PlannerSnapshotProvider)
	if !ok {
		return nil
	}
	return provider.PlannerSnapshot()
}

// ClearPlannerRequestSnapshot 提交历史压缩后清空旧 Planner 前缀
func (r *Runtime) ClearPlannerRequestSnapshot() {
	if r == nil {
		return
	}
	r.mu.Lock()
	loop := r.opts.ChatLoop
	r.mu.Unlock()
	if provider, ok := loop.(PlannerSnapshotProvider); ok {
		provider.ClearPlannerSnapshot()
	}
}

// PlannerInterrupt 返回打断控制器
func (r *Runtime) PlannerInterrupt() *PlannerInterruptController { return r.plannerInterrupt }

// SubAgentTasks 返回后台子代理登记表
func (r *Runtime) SubAgentTasks() *SubAgentTaskManager { return r.subagentTasks }

// MaxContextSize 返回当前会话实时生效的上下文窗口大小
func (r *Runtime) MaxContextSize() int {
	if r.opts.MaxContextSize != nil {
		return r.opts.MaxContextSize()
	}
	return 0
}

// RefreshVisualPlaceholders 用已完成识图结果刷新历史占位；waitForTextModel 时按配置等待识别完成
func (r *Runtime) RefreshVisualPlaceholders(ctx context.Context, waitForTextModel bool) int {
	if r == nil || r.opts.VisualRefresh == nil {
		return 0
	}
	port := r.opts.VisualRefresh
	history := r.History()
	refreshed := port.RefreshChatHistoryVisualPlaceholders(history)
	waitSeconds := port.ImageRecognitionWaitSeconds(waitForTextModel)
	if waitSeconds > 0 && port.HasPendingImageRecognition(history) {
		deadline := time.Now().Add(time.Duration(waitSeconds * float64(time.Second)))
		for time.Now().Before(deadline) {
			remaining := time.Until(deadline)
			if remaining > 200*time.Millisecond {
				remaining = 200 * time.Millisecond
			}
			timer := time.NewTimer(remaining)
			select {
			case <-ctx.Done():
				timer.Stop()
				if refreshed > 0 {
					r.SetHistory(history)
				}
				return refreshed
			case <-timer.C:
			}
			if changed := port.RefreshChatHistoryVisualPlaceholders(history); changed > 0 {
				refreshed += changed
			}
			if !port.HasPendingImageRecognition(history) {
				break
			}
		}
		if changed := port.RefreshChatHistoryVisualPlaceholders(history); changed > 0 {
			refreshed += changed
		}
	}
	if refreshed > 0 {
		r.SetHistory(history)
	}
	return refreshed
}

// ScheduleToolResultMediaEmojiRecognition 为工具结果媒体消息登记后台表情识别
func (r *Runtime) ScheduleToolResultMediaEmojiRecognition(emojis []ImageComponent, mediaIndex string) {
	if r == nil || r.opts.VisualRefresh == nil || len(emojis) == 0 {
		return
	}
	r.opts.VisualRefresh.ScheduleToolResultMediaEmojiRecognition(emojis, mediaIndex)
}

// ScheduleToolResultMediaImageRecognition 为工具结果媒体消息登记后台图片识别
func (r *Runtime) ScheduleToolResultMediaImageRecognition(images []ImageComponent, mediaIndex string) {
	if r == nil || r.opts.VisualRefresh == nil || len(images) == 0 {
		return
	}
	r.opts.VisualRefresh.ScheduleToolResultMediaImageRecognition(images, mediaIndex)
}

// RecordFocusSentMessage 把已发送消息登记到 Focus 共享组
func (r *Runtime) RecordFocusSentMessage(message ContextMessage) {
	if r == nil || r.opts.FocusSentRecorder == nil {
		return
	}
	r.opts.FocusSentRecorder(message)
}

// ScheduleSentImageRecognition 为已发送并写入历史的图片登记后台识图
// 对齐 Python _schedule_sent_image_recognition：不阻塞发送链路，识别结果稍后回填占位
func (r *Runtime) ScheduleSentImageRecognition(images []ImageComponent) {
	if r == nil || r.opts.VisualRefresh == nil || len(images) == 0 {
		return
	}
	readable := make([]ImageComponent, 0, len(images))
	for _, image := range images {
		if strings.TrimSpace(image.MediaRef) != "" {
			readable = append(readable, image)
		}
	}
	if len(readable) == 0 {
		return
	}
	r.opts.VisualRefresh.ScheduleToolResultMediaImageRecognition(readable, "")
}

// History 返回内部聊天历史副本
func (r *Runtime) History() []ContextMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ContextMessage, len(r.chatHistory))
	copy(out, r.chatHistory)
	return out
}

// BuildPlannerExtraInjections 调用宿主注册的一轮性 Planner 注入（启发式记忆、画像、图片记忆）
func (r *Runtime) BuildPlannerExtraInjections(ctx context.Context, trigger *ContextMessage, source []ContextMessage) []string {
	if r == nil || r.opts.PlannerExtraInjections == nil {
		return nil
	}
	return r.opts.PlannerExtraInjections(ctx, trigger, source)
}

// HasPlannerExtraInjections 报告宿主是否注册了一轮性 Planner 注入
func (r *Runtime) HasPlannerExtraInjections() bool {
	return r != nil && r.opts.PlannerExtraInjections != nil
}

// ChatHistory 实现 turn_trigger.RuntimeView，返回门控需要的活跃度快照
func (r *Runtime) ChatHistory() []turn_trigger.HistoryMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]turn_trigger.HistoryMessage, 0, len(r.chatHistory))
	for _, message := range r.chatHistory {
		received := message.ReceivedAt
		out = append(out, turn_trigger.HistoryMessage{
			Source:         message.Source,
			Timestamp:      message.Timestamp,
			ReceivedAt:     received,
			CountInContext: message.CountInContext,
		})
	}
	return out
}

// HistoryRevision 返回当前历史版本号
func (r *Runtime) HistoryRevision() uint64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.historyRevision
}

// AppendHistory 追加一条内部历史消息，返回写入位置
// 不强制改写 CountInContext：真实聊天消息由构造点显式标记，模型输出、工具结果与引用按各自语义保留
func (r *Runtime) AppendHistory(message ContextMessage) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if message.ReceivedAt == nil && message.Role != RoleAssistant {
		received := message.Timestamp
		message.ReceivedAt = &received
	}
	r.chatHistory = append(r.chatHistory, message)
	r.historyRevision++
	if r.transcriptRecording {
		r.turnTranscript = append(r.turnTranscript, message)
	}
	return len(r.chatHistory) - 1
}

// BeginTurnTranscript 开始记录本轮推理写入的内部历史
func (r *Runtime) BeginTurnTranscript() {
	r.mu.Lock()
	r.transcriptRecording = true
	r.turnTranscript = nil
	r.mu.Unlock()
}

// DrainTurnTranscript 结束并取走本轮推理新增的内部历史
func (r *Runtime) DrainTurnTranscript() []ContextMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transcriptRecording = false
	out := r.turnTranscript
	r.turnTranscript = nil
	return out
}

// SetHistory 整体替换内部历史，供 Focus 组与恢复流程使用
func (r *Runtime) SetHistory(messages []ContextMessage) {
	r.mu.Lock()
	r.chatHistory = messages
	r.historyRevision++
	r.mu.Unlock()
}

// SetAgentState 供 Focus 共享组同步成员状态
func (r *Runtime) SetAgentState(state string) {
	switch state {
	case "stop":
		r.EnterStopState()
	case "running":
		r.EnterRunningState()
	}
}

// TrimHistory 只保留最近 keep 条历史，供 Focus 成员裁剪上下文
func (r *Runtime) TrimHistory(keep int) {
	if keep < 0 {
		keep = 0
	}
	r.mu.Lock()
	if len(r.chatHistory) > keep {
		r.chatHistory = append([]ContextMessage(nil), r.chatHistory[len(r.chatHistory)-keep:]...)
		r.historyRevision++
	}
	r.mu.Unlock()
}

// ListTools 暴露当前可用工具声明
func (r *Runtime) ListTools(ctx context.Context) ([]ToolSpec, error) {
	if r == nil || r.opts.Tools == nil {
		return nil, fmt.Errorf("工具运行时未初始化")
	}
	return r.opts.Tools.AvailableTools(ctx)
}

// GetToolSpec 按名称返回工具声明
func (r *Runtime) GetToolSpec(ctx context.Context, name string) (*ToolSpec, bool, error) {
	specs, err := r.ListTools(ctx)
	if err != nil {
		return nil, false, err
	}
	for i := range specs {
		if specs[i].Name == name {
			return &specs[i], true, nil
		}
	}
	return nil, false, nil
}

// InvokeTool 执行一次工具调用并把错误折叠为失败结果
func (r *Runtime) InvokeTool(ctx context.Context, invocation ToolInvocation) ToolExecutionResult {
	if r == nil || r.opts.Tools == nil {
		return ToolExecutionResult{Success: false, Message: "工具运行时未初始化"}
	}
	invocation.SessionID = firstNonEmpty(invocation.SessionID, r.sessionID)
	result, err := r.opts.Tools.Invoke(ctx, invocation)
	if err != nil {
		return ToolExecutionResult{Success: false, Message: err.Error()}
	}
	return result
}

// ExpandMessageIDAliases 把工具参数中的展示别名展开成真实消息编号
func (r *Runtime) ExpandMessageIDAliases(arguments map[string]any) map[string]any {
	if arguments == nil {
		return nil
	}
	history := r.History()
	ids := make([]string, 0, len(history))
	for _, message := range history {
		if message.MessageID != "" {
			ids = append(ids, message.MessageID)
		}
	}
	expanded := ExpandMessageIDAliases(arguments, BuildAliasMap(ids))
	if value, ok := expanded.(map[string]any); ok {
		return value
	}
	return arguments
}

// DiscoveredToolNames 返回本次运行已经发现的延迟工具名
func (r *Runtime) DiscoveredToolNames() map[string]struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]struct{}, len(r.discoveredToolNames))
	for name := range r.discoveredToolNames {
		out[name] = struct{}{}
	}
	return out
}

// FindSourceMessageByID 从内部历史中查找指定消息编号对应的原始消息
func (r *Runtime) FindSourceMessageByID(messageID string) (ContextMessage, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := len(r.chatHistory) - 1; index >= 0; index-- {
		if r.chatHistory[index].MessageID == messageID {
			return r.chatHistory[index], true
		}
	}
	return ContextMessage{}, false
}

// AgentState 当前内部循环状态
func (r *Runtime) AgentState() turn_trigger.AgentState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.agentState
}

// ReplyTiming 实现 turn_trigger.RuntimeView
func (r *Runtime) ReplyTiming() turn_trigger.IdleBackoffConfig { return r.opts.ReplyTiming() }

// PlannerMessageSteering 是否启用快速反应模式
func (r *Runtime) PlannerMessageSteering() bool { return r.opts.PlannerSteering }

// OnlineBotAccountIDs 实现 turn_trigger.RuntimeView
func (r *Runtime) OnlineBotAccountIDs(platform string) map[string]struct{} {
	return r.opts.OnlineBotAccountIDs(platform)
}

// FocusModeActive 当前聊天流是否处于 Focus 模式
func (r *Runtime) FocusModeActive() bool {
	if r.opts.FocusActive == nil {
		return false
	}
	return r.opts.FocusActive()
}

// VisualPlannerActive 当前配置下 Planner 是否启用视觉能力
func (r *Runtime) VisualPlannerActive() bool {
	if r == nil || r.opts.VisualPlannerEnabled == nil {
		return false
	}
	return r.opts.VisualPlannerEnabled()
}

// PassiveReplyerActive 本轮是否走被动回复实验模式
// 轮次进行中取引擎冻结值；尚未进入轮次时回退到启动配置，两者在轮次开始时一致
func (r *Runtime) PassiveReplyerActive() bool {
	if r == nil {
		return false
	}
	if r.engine == nil {
		return r.opts.PassiveReplyer
	}
	if r.engine.turnPassiveReplyer == nil {
		return r.opts.PassiveReplyer || r.LightweightPrivate()
	}
	return r.engine.PassiveReplyerEnabled()
}

// EffectiveReplyFrequency 返回当前会话生效的回复频率
func (r *Runtime) EffectiveReplyFrequency() float64 {
	r.mu.Lock()
	adjust := r.talkFrequencyAdjust
	r.mu.Unlock()
	base := r.baseReplyFrequency()
	return clamp01(base * adjust)
}

func (r *Runtime) baseReplyFrequency() float64 { return clamp01(r.opts.ReplyFrequency()) }

// IsReplyFrequencySilent 判断当前会话是否因频率为 0 而静默接收
func (r *Runtime) IsReplyFrequencySilent() bool { return r.EffectiveReplyFrequency() <= 0 }

// AdjustTalkFrequency 调整当前会话的回复频率倍率
func (r *Runtime) AdjustTalkFrequency(frequency float64) {
	r.mu.Lock()
	r.talkFrequencyAdjust = frequency
	r.mu.Unlock()
}

// HasForcedTurnTrigger 是否存在 @/提及必回触发
func (r *Runtime) HasForcedTurnTrigger() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.forcedTurnEnabled
}

// ConsumeForcedTurnReason 消费一次性强制触发状态并返回原因描述
func (r *Runtime) ConsumeForcedTurnReason() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.forcedTurnEnabled {
		return ""
	}
	reason := r.forcedTurnReason
	messageID := firstNonEmpty(r.forcedTurnMessageID, "unknown")
	if reason == "" {
		reason = "@/提及消息"
	}
	r.forcedTurnEnabled = false
	r.forcedTurnMessageID = ""
	r.forcedTurnReason = ""
	return fmt.Sprintf("检测到新的%s（消息编号=%s），本轮直接进入 Planner。", reason, messageID)
}

// ClearForcedTurnState 清理一次性强制触发状态，不产生伪门控提示
func (r *Runtime) ClearForcedTurnState() {
	r.mu.Lock()
	r.forcedTurnEnabled = false
	r.forcedTurnMessageID = ""
	r.forcedTurnReason = ""
	r.mu.Unlock()
}

// StartPlannerContinuation 标记已进入连续 Planner 状态
func (r *Runtime) StartPlannerContinuation() {
	r.mu.Lock()
	r.plannerContinuationActive = true
	r.mu.Unlock()
}

// EndPlannerContinuation 结束连续 Planner 状态
func (r *Runtime) EndPlannerContinuation() {
	r.mu.Lock()
	r.plannerContinuationActive = false
	r.mu.Unlock()
}

// IsPlannerContinuationActive 判断当前是否保持连续 Planner 状态
func (r *Runtime) IsPlannerContinuationActive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.plannerContinuationActive
}
