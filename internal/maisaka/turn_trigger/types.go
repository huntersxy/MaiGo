package turn_trigger

import "time"

// AgentState Maisaka 内部循环状态，对齐 runtime.py 的 _STATE_* 常量
type AgentState string

const (
	StateIdle    AgentState = "idle"
	StateRunning AgentState = "running"
	StateWait    AgentState = "wait"
	StateStop    AgentState = "stop"
)

// TurnGateDecision 单个触发门控的判定结果
type TurnGateDecision string

const (
	// DecisionTrigger 放行进入 Planner
	DecisionTrigger TurnGateDecision = "trigger"
	// DecisionWait 继续等待更多消息
	DecisionWait TurnGateDecision = "wait"
	// DecisionDelay 延迟后重新检查
	DecisionDelay TurnGateDecision = "delay"
)

// TurnGateResult 对齐 gates.TurnGateResult
type TurnGateResult struct {
	Decision     TurnGateDecision
	Detail       string
	DelaySeconds float64
}

// ShouldTrigger 是否放行进入 Planner
func (r TurnGateResult) ShouldTrigger() bool { return r.Decision == DecisionTrigger }

// Delay 返回延迟时长
func (r TurnGateResult) Delay() time.Duration {
	return time.Duration(r.DelaySeconds * float64(time.Second))
}

// PendingMessage 待处理外部消息的最小快照，对齐 SessionMessage 中被门控读取的字段
type PendingMessage struct {
	MessageID          string
	Platform           string
	UserID             string
	ProcessedPlainText string
	// IsAt 表示消息 @ 了麦麦
	IsAt bool
	// IsMentioned 表示消息以任意方式提及麦麦
	IsMentioned bool
	// ReceivedAt 到达时刻，nil 表示未知
	ReceivedAt *time.Time
}

// HistoryMessage 内部聊天历史的最小快照
type HistoryMessage struct {
	// Source 为 guided_reply 时表示麦麦自身发言
	Source string
	// Timestamp 为本机发送时间，仅自身发言使用
	Timestamp time.Time
	// ReceivedAt 为外部消息到达时刻
	ReceivedAt *time.Time
	// CountInContext 为 false 的历史不计入活跃度统计
	CountInContext bool
}

// SelfMessageSource 自身发言的 source 取值
const SelfMessageSource = "guided_reply"

// RuntimeView 触发调度所需的 runtime 只读视图与少量动作端口
type RuntimeView interface {
	// LogPrefix 日志前缀
	LogPrefix() string
	// AgentState 当前内部循环状态
	AgentState() AgentState
	// IsGroupSession 是否群聊会话
	IsGroupSession() bool
	// MessageTurnScheduled 是否已排入 message turn
	MessageTurnScheduled() bool
	// PendingMessageCount 待处理消息数
	PendingMessageCount() int
	// PendingMessages 尚未处理的待处理消息，对齐 message_cache[_last_processed_index:]
	PendingMessages() []PendingMessage
	// MessageQuietRemainingSeconds 消息防抖剩余秒数
	MessageQuietRemainingSeconds() float64
	// EffectiveReplyFrequency 生效回复频率
	EffectiveReplyFrequency() float64
	// IsReplyFrequencySilent 回复频率是否为静默消费
	IsReplyFrequencySilent() bool
	// HasForcedTurnTrigger 是否存在 @ 强制触发
	HasForcedTurnTrigger() bool
	// FocusModeActive 当前聊天流是否处于 Focus 模式
	FocusModeActive() bool
	// ChatHistory 内部聊天历史，按到达顺序
	ChatHistory() []HistoryMessage
	// ReplyTiming 空闲退避参数
	ReplyTiming() IdleBackoffConfig
	// PlannerMessageSteering 是否启用快速反应模式（wait 期间新消息直接续接）
	PlannerMessageSteering() bool
	// OnlineBotAccountIDs 平台在线机器人账号 ID 集合，用于过滤自身消息
	OnlineBotAccountIDs(platform string) map[string]struct{}
	// CancelWaitTimeout 取消挂起的 wait 超时任务
	CancelWaitTimeout()
	// UpdateStageStatus 更新阶段看板状态
	UpdateStageStatus(title, detail string)
	// DeferMessageTurnCheck 延迟若干秒后重新检查
	DeferMessageTurnCheck(delaySeconds float64)
	// EnqueueMessageTurn 投递 message turn
	EnqueueMessageTurn()
	// EnterStopState 进入 stop 状态
	EnterStopState()
	// EnterRunningState 进入 running 状态
	EnterRunningState()
}
