package replyer

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// LoadPromptFunc 对应 Python load_prompt；由调用方注入 prompt.Manager 或其他模板源
type LoadPromptFunc func(name string, vars map[string]string) (string, error)

// MessagePartKind 聊天消息组件类型，仅覆盖 replyer 提示词需要渲染的部分
type MessagePartKind string

const (
	PartText  MessagePartKind = "text"
	PartAt    MessagePartKind = "at"
	PartImage MessagePartKind = "image"
	PartEmoji MessagePartKind = "emoji"
	PartVoice MessagePartKind = "voice"
	PartReply MessagePartKind = "reply"
)

// MessagePart 对齐 MaiBot MessageSequence 组件
type MessagePart struct {
	Kind MessagePartKind

	Text string

	// At 组件
	TargetUserID       string
	TargetUserNickname string
	TargetUserCardname string

	// Reply 组件
	TargetMessageID string
	TargetName      string
	TargetContent   string

	// 图片/表情包/语音解析后的描述
	Description string

	// 图片/表情包的原始二进制（base64），视觉编码用；格式为空时按内容识别
	ImageFormat string
	ImageBase64 string
}

// QuoteReference 引用消息在上下文中的渲染信息
type QuoteReference struct {
	MessageID string
	Sender    string
	Content   string
}

// ChatMessage replyer 视角的一条历史消息
type ChatMessage struct {
	Role       string // system | user | assistant
	MessageID  string
	SenderName string
	SenderID   string
	Platform   string
	Timestamp  time.Time

	Content    string
	Parts      []MessagePart
	Images     []MessageImage
	Quotes     []QuoteReference
	IsNotify   bool
	IsBotSelf  bool
	SourceKind string // user | guided_reply | model_output | ...

	// VisibleText 对齐 Python ContentContextMessage.visible_text：
	// 带说话人前缀的展示文本，replyer 从中解析真实正文；为空时回退 Content
	VisibleText string

	// VisibleReply 仅 model_output 消息使用；为空时回退 Content
	VisibleReply string
}

// ReplyTarget 本次回复的目标消息
type ReplyTarget struct {
	MessageID  string
	SenderName string
	SenderID   string
	Platform   string
	Content    string
	Parts      []MessagePart
	Images     []MessageImage
	Quotes     []QuoteReference
	IsBotSelf  bool
}

// MessageImage 历史消息附带的图片；Base64 为原始内容，视觉编码时按需缩放/转码
type MessageImage struct {
	Format string
	Base64 string
}

// AttachmentImage reply 工具要求额外发送的图片
type AttachmentImage struct {
	MediaIndex string
	MessageID  string
	Index      int
	SenderName string
	Content    string
}

// KeywordRule 关键词反应规则
type KeywordRule struct {
	Keywords []string
	Reaction string
}

// RegexRule 正则反应规则；Reaction 中可用 [name] 引用命名捕获组
type RegexRule struct {
	Patterns []string
	Reaction string
}

// GeneratorConfig replyer prompt 生成所需配置，对应 global_config 的相关字段
type GeneratorConfig struct {
	BotName     string
	BotAliases  []string
	Personality string
	ReplyStyle  string

	MultipleReplyStyles []string
	MultipleProbability float64

	GroupChatPrompt   string
	PrivateChatPrompt string
	ChatPrompt        string
	IsGroupChat       *bool

	KeywordRules []KeywordRule
	RegexRules   []RegexRule

	// PromptLocale 用于输出格式说明；为空时默认 zh-CN
	PromptLocale string

	// ReplyerRetroPrompt 对齐 debug.replyer_retro_prompt
	ReplyerRetroPrompt bool

	// SaveReasoning 对齐 debug.save_maisaka_reasoning：持久化 Replyer 请求与输出预览
	SaveReasoning bool
	// KeepPreviewBase64 对齐 debug.keep_prompt_preview_json_base64
	KeepPreviewBase64 bool

	// AttentionDriftBlock 对齐 build_attention_drift_prompt_block；普通模板追加到 system 末尾
	AttentionDriftBlock string
}

// ReplyRequest 一次 replyer 请求的输入
type ReplyRequest struct {
	History           []ChatMessage
	Target            *ReplyTarget
	ReplyReason       string
	ExpressionHabits  string
	ReplyRequirements string
	SessionID         string

	ReplyReference string
	ReplyStyle     string
	ReplyToolArgs  map[string]any

	// SelectedExpressionIDs/SelectedExpressions 为调用方已选择的表达方式；非空时优先于自动选择
	SelectedExpressionIDs []int
	SelectedExpressions   []map[string]any

	// SubAgentRunner 供表达方式选择器在需要时调用子代理；由调用方注入
	SubAgentRunner func(context.Context, string) (string, error)

	RetryConstraints []string

	// EnableVisualMessage 对齐 model_info.visual：开启后历史图片按多模态内容发送
	EnableVisualMessage bool
	// EnableVisualPlanner planner 是否按视觉模式运行；开启时文本模型不再等待识图
	EnableVisualPlanner bool
	// ImageRecognitionRefresher 回复生成前刷新历史图片识别结果，返回刷新条数
	ImageRecognitionRefresher func(ctx context.Context, history []ChatMessage) int
	// MaxImageSide 普通图片最长边；<=0 时按 ImageQuality 取 512/1024
	MaxImageSide int
	// MaxImageNum 多模态最大图片数；<=0 时默认 64（与 MaiBot visual.max_image_num 默认一致）
	MaxImageNum int
	// ImageQuality 对齐 visual.image_quality：normal/high
	ImageQuality string
}

// RequestPart 请求消息内的有序内容块；图片块与文本块按原顺序发送
type RequestPart struct {
	Kind  string
	Text  string
	Image *RequestImage
}

// RequestImage 视觉编码后的图片内容
type RequestImage struct {
	Format string
	Base64 string
}

// RequestMessage 发送给 LLM 的一条消息
type RequestMessage struct {
	Role    string
	Content string
	Parts   []RequestPart
}

const duplicateTargetReplyReminderArg = "_duplicate_target_reply_reminder"

const retroTargetContentLimit = 300

// PromptBuilder replyer 提示词装配器；PromptLoader 与配置解耦，便于测试
type PromptBuilder struct {
	PromptLoader LoadPromptFunc
	Config       GeneratorConfig
	Now          func() time.Time
	Rand         *rand.Rand
}

// DocumentBuilder 对应 DocumentReplyGenerator 的提示词部分
type DocumentBuilder struct {
	PromptBuilder

	// DocumentModel 对齐 DocumentReplyGenerator.document_model_name
	DocumentModel string
}

// ReplyerManager 对齐 replyer_manager.ReplyerManager 的实例缓存
type ReplyerManager struct {
	newBuilder func(requestType string) *PromptBuilder

	mu      sync.RWMutex
	byCache map[string]*PromptBuilder
}

// RequestMessages 按配置选择普通或 retro 提示词模板
func (b *PromptBuilder) RequestMessages(ctx context.Context, req ReplyRequest) ([]RequestMessage, error) {
	if b.Config.ReplyerRetroPrompt {
		return b.BuildRetroRequestMessages(ctx, req)
	}
	return b.BuildRequestMessages(ctx, req)
}

// LoadPrompt 调用注入的模板加载器
func (b *PromptBuilder) LoadPrompt(name string, vars map[string]string) (string, error) {
	if b.PromptLoader == nil {
		return "", errPromptLoaderMissing
	}
	return b.PromptLoader(name, vars)
}
