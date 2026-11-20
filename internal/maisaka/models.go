package maisaka

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"MaiGo/internal/maisaka/turn_trigger"
)

// ContextRole 上下文消息角色，对齐 Python LLMContextMessage 的 role
type ContextRole string

const (
	RoleSystem    ContextRole = "system"
	RoleUser      ContextRole = "user"
	RoleAssistant ContextRole = "assistant"
	RoleTool      ContextRole = "tool"
	RoleReference ContextRole = "reference"
)

// 历史消息来源标记，对齐 runtime / reasoning_engine 使用的 source 取值
const (
	// SourceUser 普通用户消息
	SourceUser = "user"
	// SourceGuidedReply 麦麦自身发言
	SourceGuidedReply = "guided_reply"
	// SourceToolResult 工具结果
	SourceToolResult = "tool_result"
	// SourceReference 参考消息
	SourceReference = "reference"
)

// ReferenceContextRestoreType 启动上下文恢复提示的参考类型，对齐 Python ReferenceMessageType.CONTEXT_RESTORE
const ReferenceContextRestoreType = "context_restore"

// PendingImagePlaceholder 图片尚未完成识别时的占位文本，对齐 Python 常量
const PendingImagePlaceholder = "[图片，识别中.....]"

// ToolResultMediaKind 工具结果媒体消息的 metadata.kind 标记，对齐 Python ToolResultMediaMessage
const ToolResultMediaKind = "tool_result_media"

// DuplicateTargetReplyReminderTemplate 重复回复同一目标时的提醒模板
// 对齐 Python maisaka/builtin_tool/reply.py::_DUPLICATE_TARGET_REPLY_REMINDER_TEMPLATE
const DuplicateTargetReplyReminderTemplate = "你刚刚已经回复过这条消息，你刚刚的发言是：“%s”\n你现在想再次回复这条消息，进行补充，注意请不要和之前你的发言重复。"

// NormalizeDuplicateReplyText 归一化提醒里的历史发言
// 对齐 Python：空白折叠为单空格后去首尾空白
func NormalizeDuplicateReplyText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// ImageComponent 图片组件的最小投影，二进制由媒体层按需回填
type ImageComponent struct {
	// MediaRef 内容寻址引用或原始 base64
	MediaRef string
	// RecognitionHash 是转码前图片在视觉描述缓存中的 SHA-256。
	RecognitionHash string
	// Format 图片格式
	Format string
	// Description 识别描述，空表示尚未识别
	Description string
	// Placeholder 识别中使用的占位文本
	Placeholder string
	// HasBinary 组件是否携带本地图片字节，对齐 Python binary_data 真值
	HasBinary bool
}

// RecognitionRef 返回查询识图缓存时使用的引用。
func (c ImageComponent) RecognitionRef() string {
	if c.RecognitionHash != "" {
		return c.RecognitionHash
	}
	return c.MediaRef
}

// VisualRefreshPort 视觉占位刷新与工具图片识别端口
type VisualRefreshPort interface {
	// IsVLMTaskConfigured 是否配置了可用的视觉模型
	IsVLMTaskConfigured() bool
	// RefreshChatHistoryVisualPlaceholders 用已完成识图结果回填历史占位，返回刷新条数
	RefreshChatHistoryVisualPlaceholders(chatHistory []ContextMessage) int
	// HasPendingImageRecognition 是否仍有可等待的图片识别
	HasPendingImageRecognition(chatHistory []ContextMessage) bool
	// ImageRecognitionWaitSeconds 文本 planner 进入前等待识图的最长秒数
	ImageRecognitionWaitSeconds(waitForTextModel bool) float64
	// ScheduleToolResultMediaImageRecognition 为工具结果图片登记后台识别
	ScheduleToolResultMediaImageRecognition(images []ImageComponent, mediaIndex string)
	// ScheduleToolResultMediaEmojiRecognition 为工具结果表情登记后台识别
	ScheduleToolResultMediaEmojiRecognition(emojis []ImageComponent, mediaIndex string)
}

// ToolCall 模型输出的一次工具调用
type ToolCall struct {
	CallID    string         `json:"call_id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	// FromReasoning 表示工具调用来自 reasoning 内容而非正文
	FromReasoning bool `json:"from_reasoning"`
	// ExtraContent Provider 附加元数据，对齐 Python ToolCall.extra_content
	ExtraContent map[string]any `json:"extra_content,omitempty"`
}

// ToolExecutionResult 统一工具执行结果
type ToolExecutionResult struct {
	// ToolName 工具名，Focus 路由会改写为组级工具名
	ToolName string
	Success  bool
	Content  string
	Items    []ToolResultItem
	Message  string
	Metadata map[string]any
	// AppendPostHistory 工具追加的普通上下文消息
	AppendPostHistory []ContextMessage
	// PauseTool 要求暂停循环的工具名，如 wait / stop
	PauseTool string
	// StopAfterExecution 插件工具要求本批工具执行完后结束 Planner，对齐 Python result.stop_after_execution
	StopAfterExecution bool
}

// ToolResultItem 工具结果中的统一内容项
type ToolResultItem struct {
	Type     string
	Text     string
	Data     string
	MIMEType string
	Metadata map[string]any
}

// BuildHistoryText 对齐 Python ToolContentItem.build_history_text
func (i ToolResultItem) BuildHistoryText() string {
	contentType := strings.TrimSpace(i.Type)
	if contentType == "text" && strings.TrimSpace(i.Text) != "" {
		return strings.TrimSpace(i.Text)
	}
	mime := strings.TrimSpace(i.MIMEType)
	if mime == "" {
		mime = "unknown"
	}
	switch contentType {
	case "image":
		return "[图片内容 " + mime + "]"
	case "audio":
		return "[音频内容 " + mime + "]"
	case "resource_link":
		return "[资源链接] " + firstNonEmpty(strings.TrimSpace(toolResultMediaMetadataString(i.Metadata, "name")), strings.TrimSpace(toolResultMediaMetadataString(i.Metadata, "uri")), "资源链接")
	case "resource":
		if strings.TrimSpace(i.Text) != "" {
			return strings.TrimSpace(i.Text)
		}
		return "[嵌入资源] " + firstNonEmpty(strings.TrimSpace(toolResultMediaMetadataString(i.Metadata, "name")), strings.TrimSpace(toolResultMediaMetadataString(i.Metadata, "uri")), "嵌入资源")
	case "binary":
		return "[二进制内容 " + mime + "]"
	default:
		return "[" + contentType + " 内容]"
	}
}

// GetHistoryContent 对齐 Python ToolExecutionResult.get_history_content
func (r ToolExecutionResult) GetHistoryContent() string {
	if content := strings.TrimSpace(r.Content); content != "" {
		return content
	}
	if len(r.Items) > 0 {
		parts := make([]string, 0, len(r.Items))
		for _, item := range r.Items {
			if text := strings.TrimSpace(item.BuildHistoryText()); text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return strings.TrimSpace(strings.Join(parts, "\n"))
		}
	}
	if structured, ok := r.Metadata["structured_content"]; ok && structured != nil {
		if text, ok := structured.(string); ok {
			return strings.TrimSpace(text)
		}
		if encoded, err := json.Marshal(structured); err == nil {
			return string(encoded)
		}
		return strings.TrimSpace(fmt.Sprint(structured))
	}
	return strings.TrimSpace(r.Message)
}

// ContextMessage Maisaka 内部历史消息，对齐 Python LLMContextMessage
type ContextMessage struct {
	Role      ContextRole
	Content   string
	Reasoning string
	// VisibleText 对齐 Python ContentContextMessage.visible_text：
	// 面向用户/监控展示的文本（含说话人前缀），与送入模型的 Content 分开保存
	VisibleText    string
	ToolCalls      []ToolCall
	ToolCallID     string
	ToolName       string
	Source         string
	Timestamp      time.Time
	ReceivedAt     *time.Time
	CountInContext bool
	// IsCommand 是否为命令消息，启发式记忆召回时排除
	IsCommand bool
	// MessageID 原始平台消息编号，工具参数别名指向它
	MessageID string
	SessionID string
	// LogicalTurnID 一次完整模型—工具—模型循环的标识
	LogicalTurnID string
	Images        []ImageComponent
	// Emojis 表情组件，Description 为空表示尚未补全
	Emojis []ImageComponent
	// MediaIndex 工具结果拆分出的媒体索引，如 tool_call_id:0
	MediaIndex string
	// Platform 平台标识
	Platform string
	// UserID 发送者编号
	UserID string
	// Metadata 附加标记，如 is_at / is_mentioned / success
	Metadata map[string]any
	// TranscriptID 关联 conversation_transcript 行号；0 表示运行时生成，不落库、不进模型请求
	TranscriptID uint `json:"-"`
}

// MetadataBool 读取布尔型附加标记
func (m ContextMessage) MetadataBool(key string) bool {
	if m.Metadata == nil {
		return false
	}
	value, ok := m.Metadata[key].(bool)
	return ok && value
}

// MetadataString 读取字符串型附加标记
func (m ContextMessage) MetadataString(key string) string {
	if m.Metadata == nil {
		return ""
	}
	value, _ := m.Metadata[key].(string)
	return value
}

// MetadataBoolSet 写入布尔型附加标记
func (m *ContextMessage) MetadataBoolSet(key string, value bool) {
	if m.Metadata == nil {
		m.Metadata = map[string]any{}
	}
	m.Metadata[key] = value
}

// ToTurnPending 转换为触发门控使用的待处理消息快照
func (m ContextMessage) ToTurnPending() turn_trigger.PendingMessage {
	return turn_trigger.PendingMessage{
		MessageID:          m.MessageID,
		UserID:             "",
		ProcessedPlainText: m.Content,
		ReceivedAt:         m.ReceivedAt,
	}
}

// ContextItem 构造请求时的一条消息项，对齐 items.ContextItem
type ContextItem struct {
	Role       ContextRole
	Text       string
	ToolCalls  []ToolCall
	ToolCallID string
	ToolName   string
	Images     []ImageComponent
	Emojis     []ImageComponent
	// Visual 本次请求允许携带可内联的图片/表情，投影为多模态内容块
	Visual bool
}

// ChatResponse 一次模型请求的响应
type ChatResponse struct {
	Content   string
	Reasoning string
	ToolCalls []ToolCall
	// RawMessages 按 Item 粒度写回历史的 envelope
	RawMessages []ContextMessage
	Usage       ChatUsage
	// ProviderActivity 原生工具调用摘要
	NativeToolCalls []NativeToolCallSummary
	// PlannerActionJudge 正文动作补全的独立请求开销
	PlannerActionJudge map[string]any
	// FinishReason 结束原因
	FinishReason string
	// ModelName 实际调用的模型标识，供终端面板展示
	ModelName string
	// SelectedHistoryCount 本次请求选中的历史消息条数
	SelectedHistoryCount int
	// ToolCount 本次请求携带的工具定义数量
	ToolCount int
	// ContextSections 本次请求的提示词分段用量
	ContextSections []ContextSectionUsage
}

// ContextSectionUsage 单个提示词分段的字符占用，对齐 context.usage.ContextSectionUsage
type ContextSectionUsage struct {
	Key   string
	Chars int
	Count int
}

// ChatUsage token 用量
type ChatUsage struct {
	PromptTokens     int
	CompletionTokens int
	CacheHitTokens   int
	CacheMissTokens  int
}

// NativeToolCallSummary Provider 原生工具调用摘要，对齐 payload_content/native_tool.py
type NativeToolCallSummary struct {
	ToolType    string
	CallID      string
	Status      string
	ActionType  string
	Details     []string
	SourceCount int
}

// CycleEnd 内部循环结束原因，对齐 reasoning_engine.CycleEnd
type CycleEnd struct {
	Reason string
	Detail string
}

// 结束原因取值
const (
	CycleEndPlannerNoTool     = "planner_no_tool_end"
	CycleEndToolPause         = "tool_pause"
	CycleEndToolPauseWait     = "tool_pause:wait"
	CycleEndToolStopAfterExec = "tool_stop_after_execution"
	CycleEndMaxRounds         = "max_internal_rounds"
	CycleEndError             = "engine_error"
)

// CycleDetail 一轮思考循环的展示与监控信息
type CycleDetail struct {
	CycleID     int
	StartedAt   time.Time
	RoundText   string
	TriggerKind string
	LogicalTurn string
}

// CycleRuntimeState 一轮内部循环中逐步积累的运行产物
type CycleRuntimeState struct {
	CycleDetail         CycleDetail
	MaxRounds           int
	RoundsRun           int
	UsedTools           []string
	ToolCallCount       int
	PlannerExtraLines   []string
	ToolResultSummaries []string
	ToolMonitorResults  []map[string]any
	ReplySent           bool
	PausedTool          string
	LastThought         string
	LastResponse        ChatResponse
	Stage               string
	PlannerStartedAt    *time.Time
	PlannerEndedAt      *time.Time
	PlannerDurationMs   int64
	// ActionToolCount 本轮实际暴露给 Planner 的工具数量
	ActionToolCount    int
	ReplyTargetMessage *ContextMessage
	PlannerInterrupted bool
	CycleEnd           CycleEnd
	Response           ChatResponse
}

// PlannerRequestSnapshot Planner 请求快照，供回复补充编排复用前缀
type PlannerRequestSnapshot struct {
	RequestKind  string
	ModelTask    string
	SystemPrompt string
	Items        []ContextItem
	Tools        []ToolSpec
	ToolNames    []string
	SessionID    string
	IsGroup      bool
	// CoveredHistory 本次请求实际选中的历史消息，压缩时用于排除已复用前缀
	CoveredHistory []ContextMessage
}

// TurnTriggerSource 内部循环触发来源
type TurnTriggerSource string

const (
	// TriggerMessage 消息触发
	TriggerMessage TurnTriggerSource = "message"
	// TriggerTimeout wait 超时触发
	TriggerTimeout TurnTriggerSource = "timeout"
	// TriggerProactive 主动任务触发
	TriggerProactive TurnTriggerSource = "proactive"
)
