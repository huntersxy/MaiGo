package tools

import (
	"context"
	"strings"
	"sync"

	"MaiGo/internal/agent/files"
	"MaiGo/internal/chat/replyer"
	"MaiGo/internal/common/database"
	"MaiGo/internal/common/media"
	"MaiGo/internal/config"
	"MaiGo/internal/files"
	"MaiGo/internal/llm_models"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// SpeakCallback 发言回调函数类型
type SpeakCallback func(ctx context.Context, targetID int64, content string, replyTo int64, mentions []int64) error

// ReplySendSegment 单段可见回复的真实发送结果，对齐 Python reply._build_send_result
// 定义位于 chat/replyer，发送循环与工具层共用同一结构
type ReplySendSegment = replyer.SendSegmentResult

// ReplySendOutcome 一次可见回复发送的逐段结果
type ReplySendOutcome = replyer.SendOutcome

// ReplySendCallback 发送可见回复并回传逐段结果；未接入时回退 SpeakCallback
type ReplySendCallback func(ctx context.Context, targetID int64, content string, replyTo int64, mentions []int64) (ReplySendOutcome, error)

// MessageAuthor 目标消息作者，用于 reply 回执的目标称呼
type MessageAuthor struct {
	UserID   string
	Nickname string
	Cardname string
	IsSelf   bool
}

// ResolveMessageAuthorCallback 按 msg_id 解析目标消息作者
type ResolveMessageAuthorCallback func(msgID string) (MessageAuthor, bool)

// SendStickerCallback 发送表情包回调函数类型
type SendStickerCallback func(ctx context.Context, targetID int64, fileName string, description string) error
type SendImageURLCallback func(ctx context.Context, imageURL string) error
type GetNewMessagesCallback func(ctx context.Context) (any, error)

// EmojiAttachment 已解析的表情包附件；Path/Binary 至少一个可用
type EmojiAttachment struct {
	Hash    string
	Path    string
	Content string
	Binary  []byte
}

// ResolveEmojiAttachmentCallback 解析 attach_emoji 的语义描述或上下文引用
type ResolveEmojiAttachmentCallback func(ctx context.Context, reference any) (EmojiAttachment, error)

// SendEmojiAttachmentCallback 发送已解析的表情包附件
type SendEmojiAttachmentCallback func(ctx context.Context, targetID int64, attachment EmojiAttachment) error

// ReplyerCallbackResult Replyer 正文及监控信息
type ReplyerCallbackResult struct {
	Text          string
	MonitorDetail map[string]any
	PromptHTMLURI string
}

type ReplyerCallback func(ctx context.Context, replyReference string, msgID string, replyToolArgs map[string]any) (ReplyerCallbackResult, error)
type ResolveMessageSenderCallback func(msgID string) (int64, bool)
type SessionMessageCallback func(ctx context.Context, fromKind string, fromTargetID int64, toKind string, toTargetID int64, text string) (*memory.SessionMessage, error)
type InternalTaskCallback func(ctx context.Context, kind string, targetID int64, text, sourceKind, reason string, images []media.Ref) (map[string]any, error)

// ToolContext 工具执行上下文
type ToolContext struct {
	ConversationKind        string
	Platform                string
	AccountID               string // 聊天流归属的 Bot 账号
	SessionID               string // 真实 chat_sessions.session_id
	TargetID                int64
	MemoryMgr               *memory.Manager
	SnapshotLogID           uint
	RunID                   string
	VisibleTranscriptIDs    []uint
	InjectedArrivalSeq      uint64
	CompactionAttempts      int
	CurrentConversationOnly bool
	RequestTokenEstimate    int
	MemoryReadVersions      map[uint]uint64
	ReferenceSeenPending    map[string]string
	SpeakCallback           SpeakCallback       // 发言回调
	ReplySendCallback       ReplySendCallback   // 可见回复逐段发送结果回调；未接入时回退 SpeakCallback
	SendStickerCallback     SendStickerCallback // 发送表情包回调
	SendImageURLCallback    SendImageURLCallback
	GetNewMessagesCallback  GetNewMessagesCallback
	ReplyerCallback         ReplyerCallback
	ResolveMessageSender    ResolveMessageSenderCallback
	// ResolveMessageAuthor 解析目标消息作者，供 reply 回执使用
	ResolveMessageAuthor ResolveMessageAuthorCallback
	// ResolveBotAccount 解析发送身份，对齐 get_bot_account；会话未指定账号且平台多账号时返回空
	ResolveBotAccount func(ctx context.Context, platform, preferredAccountID string) (string, error)
	// ReplyBotUserID 本次 reply 请求确定的 Bot 发送账号，供引用规则使用
	ReplyBotUserID string
	// PreviousTargetReply 返回本会话最近一次回复同一目标消息的正文，用于重复回复提醒
	PreviousTargetReply func(msgID string) string
	// RecordReplyTarget 记录本次成功发送的回复目标与正文
	RecordReplyTarget func(msgID string, content string)
	EnableReplyAt     bool

	// MemoryQueryDefaultLimit 对齐 memory_query_default_limit，零值由 query_memory 兜底 5
	MemoryQueryDefaultLimit int
	// ResolvePicture 按 msg_id 或 media_index 与图片序号解析上下文图片，失败原因逐字沿用 Python 文案
	ResolvePicture func(msgID string, index int) (path string, description string, err error)
	// SourceImages 返回当前会话可作为传话引用的图片来源表，键为消息编号、短编号或图片哈希
	SourceImages func() map[string][]media.Ref
	// ImageMemorySearch 按消息与组件路径查询 A_Memorix 图片记忆
	ImageMemorySearch func(ctx context.Context, messageID string, componentPath string, limit int) (map[string]any, error)
	// ResolveEmojiAttachment 解析 reply.attach_emoji 的上下文引用或语义描述
	ResolveEmojiAttachment ResolveEmojiAttachmentCallback
	// SendEmojiAttachment 在文字回复后发送已解析的表情包附件
	SendEmojiAttachment    SendEmojiAttachmentCallback
	SessionMessageCallback SessionMessageCallback
	InternalTaskCallback   InternalTaskCallback
	ReadForwardCallback    func(ctx context.Context, id string, index int) (any, error)
	GroupInfoCallback      func(ctx context.Context) string
	StickerDir             string
	Media                  *media.Manager // 会话外按 URL 下载缓存（偷表情）
	Session                SessionContext
	ThinkStartArrivalSeq   uint64
	ObservedThroughSeq     uint64
	RecallCallback         func(messageID int64, arrivalSeq uint64)
	BeginWait              func(seconds int) error
	// BeginWaitCounted 进入 wait 状态并返回连续 wait 次数；对齐 Python _enter_counted_wait_state
	BeginWaitCounted func(seconds int) (int, error)
	// PlannerMessageSteering 对齐 chat.reply_timing.planner_message_steering，控制 wait 结果文案
	PlannerMessageSteering bool
	// BotName 当前实例 bot.nickname，用于 reply 回执文案
	BotName string
	// UseVectorExpression 对齐 expression.use_vector_expression，控制 reply 是否暴露 expression_intent
	UseVectorExpression bool
	// Effective回复目标 本次发言实际使用的引用目标消息 ID（0 表示未引用），由 SpeakCallback 回填
	EffectiveReplyTo *int64
	LLMRouter        *llm.Router        // 子代理等内置工具的单次模型调用
	SubagentLLM      model.AgenticModel // generation 快照：model_task_config.planner
	UtilsLLM         model.AgenticModel // generation 快照：model_task_config.utils
	// SubagentRunner 宿主提供的多轮子代理执行器；nil 时退化为单次模型调用
	SubagentRunner    func(ctx context.Context, task string, grantedTools []string, maxRounds int) (string, map[string]int, error)
	ToolCatalog       []ToolCatalogEntry
	DeferredToolInfos []*schema.ToolInfo  // deferred 工具的模型定义，用于发现后恢复
	DiscoveredTools   map[string]struct{} // deferred 工具发现状态（供后续轮次过滤）
	// AgentFilesHost 当前会话的 agent_* 工具宿主
	AgentFilesHost agentfiles.Host
	// FileToolHost 当前会话的 files_* 工具宿主
	FileToolHost filesvc.FileToolHost

	memoryMu     sync.Mutex
	referenceMu  sync.Mutex
	discoveredMu sync.Mutex
}

// StructuredToolResult 工具的结构化执行结果，对应 Python ToolExecutionResult
type StructuredToolResult struct {
	Success           bool
	Content           string
	ErrorMessage      string
	StructuredContent any
	Metadata          map[string]any
	// PauseTool 对齐 Python ToolExecutionResult.pause_tool，用于 stop/wait 等需要暂停循环的工具
	PauseTool string
	// StopAfterExecution 插件工具要求本批执行完后结束 Planner
	StopAfterExecution bool
}

// StructuredToolResultInvoker 由需要保留结构化结果的工具实现
type StructuredToolResultInvoker interface {
	InvokeStructuredToolResult(ctx context.Context, argumentsInJSON string) (StructuredToolResult, error)
}

// ctxKey 上下文键类型
type ctxKey string

const toolContextKey ctxKey = "tool_context"
const toolLogMaxLen = 1000

// WithToolContext 将工具上下文放入 context
func WithToolContext(ctx context.Context, tc *ToolContext) context.Context {
	return context.WithValue(ctx, toolContextKey, tc)
}

// GetToolContext 从 context 获取工具上下文
func GetToolContext(ctx context.Context) *ToolContext {
	if tc, ok := ctx.Value(toolContextKey).(*ToolContext); ok {
		return tc
	}
	return nil
}

func truncateToolLogString(raw string, max int) string {
	if max <= 0 {
		return ""
	}

	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	runes := []rune(trimmed)
	if len(runes) <= max {
		return trimmed
	}
	return string(runes[:max]) + "...(truncated)"
}

// MarkMemoryRead 记录本轮实际读取过的记忆版本，供维护工具做并发校验
func (tc *ToolContext) MarkMemoryRead(id uint, version uint64) {
	if tc == nil || id == 0 || version == 0 {
		return
	}
	tc.memoryMu.Lock()
	defer tc.memoryMu.Unlock()
	if tc.MemoryReadVersions == nil {
		tc.MemoryReadVersions = map[uint]uint64{}
	}
	tc.MemoryReadVersions[id] = version
}

// MarkReferenceSeen 记录本轮工具实际展示过的参考项；模型请求成功后才由会话落库。
func (tc *ToolContext) MarkReferenceSeen(key, version string) {
	if tc == nil || key == "" || version == "" {
		return
	}
	tc.referenceMu.Lock()
	defer tc.referenceMu.Unlock()
	if tc.ReferenceSeenPending == nil {
		tc.ReferenceSeenPending = map[string]string{}
	}
	tc.ReferenceSeenPending[key] = version
}

// ReferenceSeenSnapshot 返回本轮待提交的参考回执。
func (tc *ToolContext) ReferenceSeenSnapshot() map[string]string {
	if tc == nil {
		return nil
	}
	tc.referenceMu.Lock()
	defer tc.referenceMu.Unlock()
	return tc.ReferenceSeenPending
}

// ClearReferenceSeen 在 receipt 成功落库后清空。
func (tc *ToolContext) ClearReferenceSeen() {
	if tc == nil {
		return
	}
	tc.referenceMu.Lock()
	defer tc.referenceMu.Unlock()
	tc.ReferenceSeenPending = nil
}

// MemoryVersion 读取本轮已经读取过的记忆版本
func (tc *ToolContext) MemoryVersion(id uint) (uint64, bool) {
	if tc == nil || id == 0 {
		return 0, false
	}
	tc.memoryMu.Lock()
	defer tc.memoryMu.Unlock()
	version, ok := tc.MemoryReadVersions[id]
	return version, ok
}

// LogToolCall 记录工具调用
func LogToolCall(toolName string, inputJSON string, outputJSON string, err error) {
	cfg := config.Snapshot()
	if cfg != nil && cfg.Debug.ShowToolCalls {
		inputJSON = truncateToolLogString(inputJSON, toolLogMaxLen)
		outputJSON = truncateToolLogString(outputJSON, toolLogMaxLen)
		if err != nil {
			zap.L().Debug("工具调用", zap.String("tool", toolName), zap.String("input", inputJSON), zap.String("output", outputJSON), zap.Error(err))
		} else {
			zap.L().Debug("工具调用", zap.String("tool", toolName), zap.String("input", inputJSON), zap.String("output", outputJSON))
		}
	}
}

// ==================== 获取群成员详情工具 ====================

// GetGroupMemberDetailInput 获取群成员详情的输入参数
