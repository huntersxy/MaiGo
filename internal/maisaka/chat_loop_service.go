package maisaka

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// 对齐 chat_loop_service.py 的常量
const (
	// ContextSelectionCacheStabilityRatio 上下文选择为缓存稳定放宽的倍数
	ContextSelectionCacheStabilityRatio = 2.0
	// MinRetainedChatMessages 上下文选择至少保留的消息条数
	MinRetainedChatMessages = 8
	// PLANNER_FINAL_USER_REMINDER 每轮 Planner 请求末尾的一次性提醒
	PlannerFinalUserReminder = "请分析后决定下一步：用工具收集或处理信息，用 reply 回复，用 wait 稍后再判断，或用 stop 静默结束本轮。"
	// FocusPlannerFinalUserReminderTemplate Focus 模式的末尾提醒模板
	FocusPlannerFinalUserReminderTemplate = "你需要输出对%s发言的分析，视情况输出文本内容的分析，思考是否进行工具调用"
)

// RequestTypeByRequestKind LLM 统计口径
var RequestTypeByRequestKind = map[string]string{
	"expression_selector":  "expression.selector",
	"planner":              "maisaka.planner",
	"sub_agent":            "maisaka.sub_agent",
	"reply_supplement":     "maisaka.reply_supplement",
	"tool_result_condense": "maisaka.tool_result_condense",
}

// PromptPreviewCategoryByRequestKind Prompt 预览落盘分类
var PromptPreviewCategoryByRequestKind = map[string]string{
	"planner":             "planner",
	"expression_selector": "expression_selector",
	"sub_agent":           "sub_agent",
	"reply_supplement":    "reply_supplement",
}

// ModelTaskNameByRequestKind 模型任务配置名
var ModelTaskNameByRequestKind = map[string]string{
	"expression_selector": "fast_model",
}

// TodoContextProvider 主人私聊每轮重建的待办上下文；由宿主注入
var TodoContextProvider func(sessionID string) string

// ExplorationReuseContextProvider 主人私聊的探索复用规则上下文；由宿主注入
var ExplorationReuseContextProvider func(sessionID string) string

// PassivePlannerFinalUserReminders 被动式 Planner 的末尾提醒
var PassivePlannerFinalUserReminders = map[string]string{
	"zh-CN": "需要信息或操作时调用工具；决定发言时直接输出指导正文，不附带工具调用；不发言用 stop，需要稍后判断用 wait。",
	"en-US": "Use tools for information or actions. To speak, output guidance without tool calls. Use stop to stay silent, or wait to reconsider later.",
	"ja-JP": "情報や操作が必要ならツールを使い、発言する場合はツール呼び出しを伴わない指針本文を出力してください。発言しない場合は stop、後で再判断する場合は wait を使ってください。",
}

// ChatRequest 一次模型请求
type ChatRequest struct {
	RequestKind   string
	ModelTaskName string
	SystemPrompt  string
	Items         []ContextItem
	Tools         []ToolSpec
	Temperature   *float32
	// ResponseFormat 结构化输出格式，nil/空字符串表示自然文本
	ResponseFormat any
	// InterruptFlag 可打断标记，关闭表示请求被打断
	InterruptFlag <-chan struct{}
	// Stream 是否走流式请求；planner 快速反应模式下需要保留打断前的部分输出
	Stream bool
	// SessionID 会话编号
	SessionID string
	// LogicalTurnID 本轮逻辑轮次
	LogicalTurnID string
	// MaxImageSide 图片最大边，0 表示使用默认 512
	MaxImageSide int
}

// ChatClient 模型请求端口，由宿主注入真实 LLM 调用
type ChatClient interface {
	Chat(ctx context.Context, request ChatRequest) (ChatResponse, error)
}

// HistoryCompactionInput 一次滚动摘要所需的完整输入
type HistoryCompactionInput struct {
	// Removed 被裁切出上下文的旧消息
	Removed []ContextMessage
	// Recent 裁切后保留的近期消息
	Recent []ContextMessage
	// Previous 上一份常驻上下文摘要
	Previous *ContextMessage
	// Snapshot Planner 最近一次成功请求快照，用于复用真实前缀
	Snapshot *PlannerRequestSnapshot
	// SessionID 会话标识
	SessionID string
	// Timestamp 摘要时间戳，取被裁切消息的最后一条
	Timestamp time.Time
	// Focus 是否使用 Focus 压缩提示词
	Focus bool
	// DisplayPrefix 摘要展示前缀
	DisplayPrefix string
}

// HistoryCompactor 历史压缩端口，对应 context.compaction
type HistoryCompactor interface {
	// Summarize 为被裁切的旧消息生成滚动摘要；返回 nil 表示本次不产生摘要
	Summarize(ctx context.Context, input HistoryCompactionInput) (*ContextMessage, error)
}

// ChatLoopService 负责 Maisaka 主对话循环、系统提示词与请求构造
type ChatLoopService struct {
	SessionID     string
	IsGroupChat   bool
	ModelTaskName string
	// CustomSystemPrompt 外部指定的系统提示词
	CustomSystemPrompt string
	// Prompts Prompt 模板来源
	Prompts PromptSource
	// Client 模型客户端
	Client ChatClient
	// JudgeClient 正文动作判断器使用的 fast_model 客户端；nil 时跳过判断
	JudgeClient ChatClient
	// LogicalTurnID 本轮逻辑轮次，供动作判断器生成工具调用
	LogicalTurnID string
	// ExtraTools 额外工具定义
	ExtraTools []ToolSpec
	// ToolRegistry 统一工具注册表
	ToolRegistry ToolRuntime
	// InterruptFlag 当前 planner 请求使用的中断标记
	InterruptFlag <-chan struct{}
	// Snapshot 最近一次 Planner 请求快照；通过 PlannerSnapshot 读取，避免调用方直接改共享切片
	Snapshot   *PlannerRequestSnapshot
	snapshotMu sync.Mutex
	// PassiveReplyerOverride 单轮内固定的被动回复实验模式
	PassiveReplyerOverride *bool
	// FocusMode 使用 Focus 共享组提示词模板
	FocusMode bool
	// AttentionDriftBlock 注意力漂移提示词块，由宿主按配置渲染
	AttentionDriftBlock string
	// Personality 人格文本，由宿主从实例配置注入
	Personality string
	// BehaviorStyle 行为风格文本，空时回退 prompt 模板
	BehaviorStyle string
	// BotAliasNotice 昵称别名说明，直接拼到名字后面
	BotAliasNotice string
	// Locale 当前提示词语言，用于生成平台昵称说明
	Locale string
	// GroupChatAttention 当前聊天场景的额外注意事项块
	GroupChatAttention string
	// CurrentChatPrompt 当前聊天单独配置的额外注意事项
	CurrentChatPrompt string
	// PlannerSubagent 是否向 Planner 注入 subagent 工具说明
	PlannerSubagent bool
	// EnableVisualMessage 是否允许请求携带图片
	EnableVisualMessage bool
	// MaxImageSide Planner 上下文图片最大边，0 表示按 normal 档 512
	MaxImageSide int
	// MaxImageNum 多模态图片上限，0 表示使用 visual.max_image_num 默认值 64
	MaxImageNum int
	// MaxContextSize 上下文窗口，0 表示使用调用方传入值
	MaxContextSize          int
	UseTokenLimit           bool
	MaxContextTokens        int
	lastPlannerPromptTokens int
	// IncludeDayBoundaryTimeMessages 是否在跨日历史消息前插入时间提示
	IncludeDayBoundaryTimeMessages bool
	// PlannerMessageSteering 是否启用快速反应模式：启用时 Planner 走流式请求，打断可保留已收到的片段
	PlannerMessageSteering bool
	// MeasureSections 测量提示词分段用量，由宿主注入 context.usage 实现；nil 表示不统计
	MeasureSections func(items []ContextItem, historyItemCount int, tools []ToolSpec) []ContextSectionUsage
	// LimitImages 限制请求内图片数量，由宿主注入 visual.message_limiter 实现；nil 表示不限制
	LimitImages func(items []ContextItem, maxImageNum int) []ContextItem
}

// NewChatLoopService 创建对话循环服务
func NewChatLoopService(sessionID string, isGroup bool, prompts PromptSource, client ChatClient) *ChatLoopService {
	return &ChatLoopService{
		SessionID:           sessionID,
		IsGroupChat:         isGroup,
		ModelTaskName:       "planner",
		Prompts:             prompts,
		Client:              client,
		EnableVisualMessage: true,
	}
}

// BehaviorStylePrompt 返回 Planner 使用的行为风格提示词
func (s *ChatLoopService) BehaviorStylePrompt() string {
	if strings.TrimSpace(s.BehaviorStyle) != "" {
		return strings.TrimSpace(s.BehaviorStyle)
	}
	return s.localizedText("maisaka_behavior_style")
}

// ResolveLLMRequestType 根据 Maisaka 请求类型解析 LLM 统计口径
func ResolveLLMRequestType(requestKind string) string {
	if value, ok := RequestTypeByRequestKind[requestKind]; ok {
		return value
	}
	return requestKind
}

// ResolvePromptPreviewCategory 根据请求类型决定 Prompt 预览落盘目录
func ResolvePromptPreviewCategory(requestKind string) string {
	if value, ok := PromptPreviewCategoryByRequestKind[requestKind]; ok {
		return value
	}
	return requestKind
}

// ResolveModelTaskName 根据请求类型解析模型任务配置名
func ResolveModelTaskName(requestKind, defaultTask string) string {
	if value, ok := ModelTaskNameByRequestKind[requestKind]; ok {
		return value
	}
	return defaultTask
}

// ChatPromptName 选择当前聊天使用的 Planner 模板
func (s *ChatLoopService) ChatPromptName() string {
	if s.FocusMode {
		return "planner_focus"
	}
	if !s.IsGroupChat {
		if s.PassiveReplyer() {
			return "planner_passive_private"
		}
		return "planner_default_private"
	}
	if s.PassiveReplyer() {
		return "planner_passive_group"
	}
	return "planner_default_group"
}

// PassiveReplyer 本轮是否使用被动回复器
func (s *ChatLoopService) PassiveReplyer() bool {
	if s.PassiveReplyerOverride != nil {
		return *s.PassiveReplyerOverride
	}
	return false
}

func (s *ChatLoopService) localizedText(name string) string {
	if s.Prompts == nil {
		return ""
	}
	text, err := s.Prompts.Get(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(text)
}

func (s *ChatLoopService) renderLocalized(name string, vars map[string]string) string {
	if s.Prompts == nil {
		return ""
	}
	if renderer, ok := s.Prompts.(PromptRenderer); ok {
		if text, err := renderer.Render(name, vars); err == nil && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	text := s.localizedText(name)
	for key, value := range vars {
		text = strings.ReplaceAll(text, "{"+key+"}", value)
	}
	return strings.TrimSpace(text)
}

// BuildPromptTemplateContext 构造 Maisaka prompt 模板的公共渲染参数
func (s *ChatLoopService) BuildPromptTemplateContext(toolsSection, botNicknameNotice string) map[string]string {
	attention := s.buildGroupChatAttentionBlock()
	return map[string]string{
		"bot_name":                     s.localizedText("bot_name"),
		"bot_alias_notice":             s.BotAliasNotice,
		"bot_nickname_notice":          prefixNicknameNotice(botNicknameNotice),
		"personality":                  s.Personality,
		"file_tools_section":           toolsSection,
		"tools_section":                toolsSection,
		"behavior_style":               s.BehaviorStylePrompt(),
		"group_chat_attention":         attention,
		"group_chat_attention_block":   attention,
		"private_chat_attention_block": attention,
		"query_memory_rule":            s.buildQueryMemoryRule(),
		"subagent_rule":                s.buildSubagentRule(),
		"planner_idle_focus_rule":      s.buildPlannerIdleFocusRule(),
		"idle_focus_rule":              s.buildPlannerIdleFocusRule(),
	}
}

// BuildSystemPrompt 基于当前配置实时构造主聊天系统提示词
func (s *ChatLoopService) BuildSystemPrompt(toolsSection, botNicknameNotice string) string {
	templateName := s.ChatPromptName()
	template := s.localizedText(templateName)
	if template == "" {
		return ""
	}
	// 普通聊天模板自己写明了名字，昵称说明进入占位符；Focus 模板没有占位符，整体前置
	inlineNotice := botNicknameNotice
	prefix := ""
	if !strings.Contains(template, "{bot_nickname_notice}") {
		inlineNotice = ""
		if notice := strings.TrimSpace(botNicknameNotice); notice != "" {
			prefix = notice + "\n\n"
		}
	}
	context := s.BuildPromptTemplateContext(toolsSection, inlineNotice)
	rendered := false
	if renderer, ok := s.Prompts.(PromptRenderer); ok && renderer != nil {
		if text, err := renderer.Render(templateName, context); err == nil && text != "" {
			template = text
			rendered = true
		}
	}
	if !rendered {
		for key, value := range context {
			template = strings.ReplaceAll(template, "{"+key+"}", value)
		}
	}
	if block := strings.TrimSpace(s.AttentionDriftBlock); block != "" {
		template = strings.TrimRight(template, "\n") + "\n\n" + block
	}
	return prefix + template
}

func prefixNicknameNotice(notice string) string {
	trimmed := strings.TrimSpace(notice)
	if trimmed == "" {
		return ""
	}
	return "\n" + trimmed
}

// BuildBotNicknameNotice 从已选上下文恢复机器人在当前平台的真实昵称
func (s *ChatLoopService) BuildBotNicknameNotice(history []ContextMessage) string {
	nickname := ""
	platform := ""
	for _, message := range history {
		if message.Metadata == nil {
			continue
		}
		value, ok := message.Metadata["bot_platform_nickname"].(string)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		nickname = strings.TrimSpace(value)
		platform = strings.TrimSpace(message.Platform)
	}
	if nickname == "" {
		return ""
	}
	botName := strings.TrimSpace(s.localizedText("bot_name"))
	if nickname == botName {
		return ""
	}
	if platform == "" {
		platform = "qq"
	}
	switch s.Locale {
	case "en-US":
		return fmt.Sprintf("Your nickname on %s is \"%s\".", platform, nickname)
	case "ja-JP":
		return fmt.Sprintf("%s でのあなたのニックネームは「%s」です。", platform, nickname)
	default:
		return fmt.Sprintf("你在 %s 平台的昵称是“%s”。", platform, nickname)
	}
}

// BuildPlannerFinalUserReminder 构造每轮 Planner 请求末尾的一次性 user 提醒
func (s *ChatLoopService) BuildPlannerFinalUserReminder() string {
	promptName := s.ChatPromptName()
	instruction := map[string]string{
		"planner_default_group":   "planner_turn_action",
		"planner_default_private": "planner_turn_action",
		"planner_passive_group":   "planner_turn_passive",
		"planner_passive_private": "planner_turn_passive",
		"planner_focus":           "planner_turn_focus",
	}[promptName]
	if text := s.renderLocalized(instruction, map[string]string{"bot_name": s.localizedText("bot_name")}); text != "" {
		return text
	}
	if promptName == "planner_focus" {
		return fmt.Sprintf(FocusPlannerFinalUserReminderTemplate, s.localizedText("bot_name"))
	}
	if s.PassiveReplyer() {
		if text := PassivePlannerFinalUserReminders[s.Locale]; text != "" {
			return text
		}
		return PassivePlannerFinalUserReminders["zh-CN"]
	}
	return PlannerFinalUserReminder
}

// BuildTimeUserMessage 构建统一格式的时间提示消息
func BuildTimeUserMessage(timestamp time.Time) string {
	return fmt.Sprintf("时间：%s", timestamp.Format("2006-01-02 15:04:05"))
}

// BuildCurrentTimeUserMessage 构建追加到请求末尾的当前时间消息
func BuildCurrentTimeUserMessage() string { return BuildTimeUserMessage(nowTime()) }

// AppendTimeUserMessage 向请求消息列表追加一条时间提示
func AppendTimeUserMessage(items []ContextItem, timestamp time.Time) []ContextItem {
	return append(items, ContextItem{Role: RoleUser, Text: BuildTimeUserMessage(timestamp)})
}

func (s *ChatLoopService) buildGroupChatAttentionBlock() string {
	return strings.TrimSpace(s.GroupChatAttention)
}

func (s *ChatLoopService) localizedMap(texts map[string]string) string {
	if text := texts[s.Locale]; text != "" {
		return text
	}
	return texts["zh-CN"]
}

func (s *ChatLoopService) buildPlannerIdleFocusRule() string {
	if text := s.renderLocalized("planner_turn_focus", map[string]string{"bot_name": s.localizedText("bot_name")}); text != "" {
		return text
	}
	return s.localizedMap(map[string]string{
		"en-US": "Consider all chats in the shared context. Every tool requires its target session_id. reply.after_reply controls the entire group: stop by default, continue for further actions, or wait with wait_seconds. To end the group turn silently, call stop on its own with a session_id from the group.",
		"ja-JP": "共有コンテキスト内の全チャットを判断し、各ツールに対象の session_id を指定してください。reply.after_reply はグループ全体に適用され、既定は stop、行動を続けるなら continue、待機するなら wait と wait_seconds を使います。発言せずにグループのターンを終えるには、グループ内の session_id を指定して stop を単独で呼び出してください。",
		"zh-CN": "结合共享上下文中的所有聊天判断，每个工具填写目标 session_id；reply.after_reply 作用于整个组，默认 stop，需继续操作选 continue，需等待选 wait 并填写 wait_seconds。需要静默结束整个组的本轮思考时，填写组内的 session_id 并单独调用 stop。",
	})
}

func (s *ChatLoopService) buildQueryMemoryRule() string {
	promptName := "planner_memory_private"
	if s.IsGroupChat {
		promptName = "planner_memory_group"
	}
	if text := s.localizedText(promptName); text != "" {
		return text
	}
	if s.IsGroupChat {
		return s.localizedMap(map[string]string{
			"en-US": "- query_memory(): Use it only when the reply clearly depends on past group conversation, shared experiences, public agreements, task progress, or recent clues. Do not retrieve memory for greetings, immediate emotional responses, light banter, or content that can be answered from recent messages alone. Do not bring private-chat or personal-privacy memories into a group chat.",
			"ja-JP": "- query_memory()：返信がグループ内の過去会話、共有した経験、公開された約束、タスクの進捗、最近の手がかりに明確に依存する場合だけ使ってください。挨拶、その場の感情への反応、軽いやり取り、最近のメッセージだけで答えられる内容では検索しないでください。個人チャットや私的な記憶をグループチャットに持ち込まないでください。",
			"zh-CN": "- query_memory()：只有回复明显依赖群内过去对话、共同经历、公开约定、任务进展或近期线索时使用；不要为了寒暄、即时情绪回应、轻松接话、只看最近消息就能回答的内容而检索。不要把私聊或个人隐私记忆带到群聊里。",
		})
	}
	return s.localizedMap(map[string]string{
		"en-US": "- query_memory(): Consider retrieval more actively when the other person mentions signals like \"before\", \"last time\", \"recently\", \"do you remember\", \"I like\", or \"I said\", or when the reply depends on long-term preferences, prior promises, shared experiences, or long-term information about a person.",
		"ja-JP": "- query_memory()：相手が「前に」「この前」「最近」「覚えてる？」「好き」「言った」などの合図を出した場合、または返信が長期的な好み、以前の約束、共有した経験、人物の長期的な情報に依存する場合は、より積極的に検索を検討できます。",
		"zh-CN": "- query_memory()：当对方提到“之前”“上次”“最近”“还记得吗”“我喜欢”“我说过”等信号，或回复依赖长期偏好、先前承诺、共同经历、人物长期信息时，可以更积极检索。",
	})
}

func (s *ChatLoopService) buildSubagentRule() string {
	if !s.PlannerSubagent {
		return ""
	}
	if text := s.localizedText("planner_subagent_policy"); text != "" {
		return "\n" + text
	}
	rule := s.localizedMap(map[string]string{
		"en-US": "- subagent(): When something needs looking up before you can answer or judge it (reading docs, searching the web, checking version or plugin information, finding images, verifying a claim, and so on), prefer handing it to a sub-agent: state clearly what to find out and grant it the tools it may need. It only hands you its conclusion. Call a tool directly only for things that take a single step.",
		"ja-JP": "- subagent()：答えたり判断したりする前に調べものが必要なこと（ドキュメントの確認、ウェブ検索、バージョンやプラグイン情報の確認、画像探し、ある主張の検証など）は、サブエージェントに任せることを優先してください。何を調べるかを明確に書き、使えそうなツールを許可します。サブエージェントは結論だけをあなたに渡します。一手で済むことだけ、該当するツールを直接呼び出してください。",
		"zh-CN": "- subagent()：遇到需要查资料才能回答或判断的事（查文档、搜网页、查版本或插件信息、找图、核实某个说法等），优先交给子代理：写清楚要查什么，把用得上的工具授权给它；它查完只把结论交给你。只有一步就能完成的事才直接调用对应工具。",
	})
	rule += s.localizedMap(map[string]string{
		"en-US": " The sub-agent runs in the background: after dispatching it you do not need to wait and can keep chatting; its conclusion will be sent back to you later.",
		"ja-JP": "サブエージェントはバックグラウンドで動作します。依頼したあとは待つ必要はなく、会話を続けてかまいません。結論は後であなたに返されます。",
		"zh-CN": "子代理在后台运行，派发后不用等它，可以继续聊天，结论稍后会送回给你。",
	})
	return "\n" + rule
}

// BuildCurrentChatAttentionTailMessage 构建追加到请求末尾的当前聊天专属注意事项
func (s *ChatLoopService) BuildCurrentChatAttentionTailMessage() string {
	if prompt := strings.TrimSpace(s.CurrentChatPrompt); prompt != "" {
		return "当前聊天额外注意事项：\n" + prompt
	}
	return ""
}

// BuildRequestMessages 构造发给大模型的消息列表，并返回系统提示词之后的历史条数
func (s *ChatLoopService) BuildRequestMessages(
	selectedHistory []ContextMessage,
	systemPrompt string,
	injectedUserMessages []string,
	tailUserMessages []string,
	finalUserMessage string,
) ([]ContextItem, int) {
	return s.buildRequestMessages(selectedHistory, s.EnableVisualMessage, systemPrompt, injectedUserMessages, tailUserMessages, finalUserMessage, "")
}

// buildRequestMessages 按本次请求解析出的视觉开关构造消息列表
func (s *ChatLoopService) buildRequestMessages(
	selectedHistory []ContextMessage,
	enableVisual bool,
	systemPrompt string,
	injectedUserMessages []string,
	tailUserMessages []string,
	finalUserMessage string,
	deferredToolsReminder string,
) ([]ContextItem, int) {
	items := make([]ContextItem, 0, len(selectedHistory)+4)
	resolvedSystemPrompt := systemPrompt
	if resolvedSystemPrompt == "" {
		resolvedSystemPrompt = s.CustomSystemPrompt
	}
	botNicknameNotice := s.BuildBotNicknameNotice(selectedHistory)
	if resolvedSystemPrompt == "" {
		resolvedSystemPrompt = s.BuildSystemPrompt("", botNicknameNotice)
	} else if notice := strings.TrimSpace(botNicknameNotice); notice != "" {
		resolvedSystemPrompt = notice + "\n\n" + resolvedSystemPrompt
	}
	if TodoContextProvider != nil {
		if todoContext := strings.TrimSpace(TodoContextProvider(s.SessionID)); todoContext != "" {
			resolvedSystemPrompt += "\n\n" + todoContext
		}
	}
	if ExplorationReuseContextProvider != nil {
		if explorationContext := strings.TrimSpace(ExplorationReuseContextProvider(s.SessionID)); explorationContext != "" {
			resolvedSystemPrompt += "\n\n" + explorationContext
		}
	}
	if reminder := strings.TrimSpace(deferredToolsReminder); reminder != "" {
		resolvedSystemPrompt += "\n\n" + reminder
	}
	items = append(items, ContextItem{Role: RoleSystem, Text: resolvedSystemPrompt})

	historyItems := make([]ContextItem, 0, len(selectedHistory))
	var previousTimestamp time.Time
	var deferredTimestamps []time.Time
	appendTimeItem := func(timestamp time.Time) {
		// 零值时间戳来自未填时间的条目，不能渲染成 0001-01-01
		if timestamp.IsZero() {
			return
		}
		items = append(items, ContextItem{Role: RoleUser, Text: BuildTimeUserMessage(timestamp)})
	}
	for _, message := range selectedHistory {
		contextItems := BuildContextItemsFromHistoryEntry(message, enableVisual)
		if len(contextItems) == 0 {
			continue
		}
		// assistant 工具调用与连续 tool 结果是协议原子段，跨日提示必须延后到整段之后
		isToolResultEntry := true
		for _, item := range contextItems {
			if item.Role != RoleTool {
				isToolResultEntry = false
				break
			}
		}
		if !isToolResultEntry && len(deferredTimestamps) > 0 {
			for _, timestamp := range deferredTimestamps {
				appendTimeItem(timestamp)
			}
			deferredTimestamps = deferredTimestamps[:0]
		}
		if s.IncludeDayBoundaryTimeMessages && !previousTimestamp.IsZero() &&
			previousTimestamp.Format("2006-01-02") != message.Timestamp.Format("2006-01-02") {
			if isToolResultEntry {
				deferredTimestamps = append(deferredTimestamps, message.Timestamp)
			} else {
				appendTimeItem(message.Timestamp)
			}
		}
		items = append(items, contextItems...)
		historyItems = append(historyItems, contextItems...)
		previousTimestamp = message.Timestamp
	}
	for _, timestamp := range deferredTimestamps {
		appendTimeItem(timestamp)
	}
	historyItemCount := len(items) - 1

	tail := make([]string, 0, len(injectedUserMessages)+3)
	tail = append(tail, injectedUserMessages...)
	tail = append(tail, BuildCurrentTimeUserMessage())
	tail = append(tail, tailUserMessages...)
	tail = append(tail, s.BuildCurrentChatAttentionTailMessage())
	for _, text := range tail {
		normalized := strings.TrimSpace(text)
		if normalized == "" {
			continue
		}
		items = append(items, ContextItem{Role: RoleUser, Text: normalized})
	}
	if normalizedFinal := strings.TrimSpace(finalUserMessage); normalizedFinal != "" {
		items = append(items, ContextItem{Role: RoleUser, Text: normalizedFinal})
	}
	// 上下文截断可能把工具调用与其锚点一起切断，先剔除缺锚点的前缀再校验
	historyItems = trimUngroundedLeadingToolCalls(historyItems)
	if err := ValidateFunctionCallContextAnchors(historyItems); err != nil {
		// 与 Python 一致：构造阶段直接暴露协议错误，不能静默发送坏请求
		panic(err)
	}
	return items, historyItemCount
}

// ValidateFunctionCallContextAnchors 禁止请求历史从缺少 user/function output 锚点的工具调用开始
func ValidateFunctionCallContextAnchors(items []ContextItem) error {
	hasAnchor := false
	for _, item := range items {
		switch item.Role {
		case RoleUser:
			hasAnchor = true
		case RoleTool:
			hasAnchor = true
		case RoleAssistant:
			if len(item.ToolCalls) > 0 && !hasAnchor {
				return fmt.Errorf("请求上下文中的 function call 缺少前置 user/function output 锚点: call_id=%s", item.ToolCalls[0].CallID)
			}
		}
	}
	return nil
}

// SelectLLMContextMessages 选择 LLM 上下文消息
func (s *ChatLoopService) SelectLLMContextMessages(chatHistory []ContextMessage, requestKind string, maxContextSize int) ([]ContextMessage, string) {
	filtered := FilterHistoryForRequestKind(chatHistory, requestKind)
	baseContextSize := maxContextSize
	if baseContextSize <= 0 {
		baseContextSize = s.MaxContextSize
	}
	if baseContextSize <= 0 {
		baseContextSize = 1
	}
	effectiveContextSize := int(float64(baseContextSize) * ContextSelectionCacheStabilityRatio)
	if effectiveContextSize < MinRetainedChatMessages {
		effectiveContextSize = MinRetainedChatMessages
	}
	useTokenLimit := s.UseTokenLimit && (requestKind == "planner" || requestKind == "replyer")
	if useTokenLimit {
		effectiveContextSize = 1
		for _, message := range filtered {
			if message.CountInContext {
				effectiveContextSize++
			}
		}
	}
	enableVisual := s.EnableVisualMessage && ResolveEnableVisualMessage(requestKind)

	alwaysSelected := CollectAlwaysSelectedReferenceIndices(filtered, enableVisual)
	selectedIndices := make([]int, 0, len(filtered))
	counted := 0
	for index := len(filtered) - 1; index >= 0; index-- {
		message := filtered[index]
		if len(BuildContextItemsFromHistoryEntry(message, enableVisual)) == 0 {
			continue
		}
		selectedIndices = append(selectedIndices, index)
		if message.CountInContext {
			counted++
			if counted >= effectiveContextSize {
				break
			}
		}
	}
	selectedIndices = append(selectedIndices, alwaysSelected...)
	sortUniqueInts(selectedIndices)
	if len(selectedIndices) == 0 {
		return nil, "实际发送 0 条上下文（tool 0 条，聊天消息 0 条）"
	}
	selected := ExpandSelectedToolTurnsByIndex(filtered, selectedIndices)
	if len(selected) == 0 {
		return nil, "实际发送 0 条上下文（tool 0 条，聊天消息 0 条）"
	}
	// 重启后 transcript 可能残留「工具未执行」的孤儿结果，首次装配就要清掉
	selected = dropOrphanToolResultPrefix(selected)
	selected = normalizeLoopToolResultOrder(selected)
	selectedCount := 0
	toolCount := 0
	for _, message := range selected {
		if message.CountInContext {
			selectedCount++
		}
		if message.Role == RoleTool {
			toolCount++
		}
	}
	overflow := selectedCount - effectiveContextSize
	if overflow < 0 {
		overflow = 0
	}
	stabilityText := fmt.Sprintf("|cache_window %d->%d", baseContextSize, effectiveContextSize)
	if useTokenLimit {
		stabilityText = "|token_limit"
	}
	overflowText := ""
	if overflow > 0 {
		overflowText = fmt.Sprintf("|tool_turn_overflow +%d", overflow)
	}
	reason := fmt.Sprintf("实际发送 %d 条上下文|聊天消息 %d 条|tool %d 条%s%s",
		len(selected), selectedCount, toolCount, stabilityText, overflowText)
	return selected, reason
}

// ExpandSelectedToolTurnsByIndex 预算命中工具循环任意条目时，按 logical_turn_id 补齐整个循环。
//
// 对齐 Python _expand_selected_tool_turns：选中集合以索引（等价于 Python 的 id(message)）
// 为准，只有工具轮次才按 logical_turn_id 扩散。普通用户消息没有 logical_turn_id，
// 必须依赖索引保留，否则整段聊天记录会被全部丢弃。
func ExpandSelectedToolTurnsByIndex(fullHistory []ContextMessage, selectedIndices []int) []ContextMessage {
	toolTurnIDs := map[string]struct{}{}
	for _, message := range fullHistory {
		if message.LogicalTurnID == "" {
			continue
		}
		if message.Role == RoleTool || len(message.ToolCalls) > 0 {
			toolTurnIDs[message.LogicalTurnID] = struct{}{}
		}
	}
	selectedTurnIDs := map[string]struct{}{}
	selected := make(map[int]struct{}, len(selectedIndices))
	for _, index := range selectedIndices {
		if index < 0 || index >= len(fullHistory) {
			continue
		}
		selected[index] = struct{}{}
		if turnID := fullHistory[index].LogicalTurnID; turnID != "" {
			if _, ok := toolTurnIDs[turnID]; ok {
				selectedTurnIDs[turnID] = struct{}{}
			}
		}
	}
	anchorIndexByTurnID := CollectToolTurnAnchorIndices(fullHistory, selectedTurnIDs)
	for _, anchorIndex := range anchorIndexByTurnID {
		selected[anchorIndex] = struct{}{}
	}
	out := make([]ContextMessage, 0, len(selected))
	for index, message := range fullHistory {
		if _, ok := selected[index]; ok {
			out = append(out, message)
			continue
		}
		if message.LogicalTurnID == "" {
			continue
		}
		if _, ok := selectedTurnIDs[message.LogicalTurnID]; ok {
			out = append(out, message)
		}
	}
	return dropUnauthorizedToolCallPrefix(out)
}

// dropOrphanToolResultPrefix 去掉开头缺少对应 assistant tool_call 的工具结果。
//
// 进程中断会在 transcript 留下「工具未执行」的结果条目，重启后首个请求若以它开头，
// 模型会看到一段没有来源的工具输出。
func dropOrphanToolResultPrefix(history []ContextMessage) []ContextMessage {
	available := make(map[string]struct{}, len(history))
	for _, message := range history {
		if message.Role != RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.CallID != "" {
				available[call.CallID] = struct{}{}
			}
		}
	}
	first := 0
	for first < len(history) && history[first].Role == RoleTool {
		if _, ok := available[history[first].ToolCallID]; ok {
			break
		}
		first++
	}
	return history[first:]
}

// dropUnauthorizedToolCallPrefix 去掉开头缺少 user/tool 锚点的工具调用。
//
// buildRequestMessages 会跳过产不出请求项的条目，选中集合若以工具调用开头，
// 请求历史就缺少协议要求的锚点。这里按真实可见性判断，避免把坏请求发给模型。
func dropUnauthorizedToolCallPrefix(history []ContextMessage) []ContextMessage {
	anchor := -1
	for index, message := range history {
		if len(BuildContextItemsFromHistoryEntry(message, false)) == 0 {
			continue
		}
		if message.Role == RoleUser || message.Role == RoleTool {
			anchor = index
			continue
		}
		if message.Role == RoleAssistant && len(message.ToolCalls) > 0 && anchor < 0 {
			return dropUnauthorizedToolCallPrefix(history[index+1:])
		}
	}
	return history
}

// CollectToolTurnAnchorIndices 收集每个工具轮次之前最近的真实 user 上下文位置
//
// 只接受能真正产出请求项的 user 消息：锚点若落到产不出 item 的条目上，
// 会被 buildRequestMessages 跳过，工具调用就变成请求首条，触发协议校验 panic。
func CollectToolTurnAnchorIndices(fullHistory []ContextMessage, turnIDs map[string]struct{}) map[string]int {
	anchors := map[string]int{}
	lastUserIndex := -1
	for index, message := range fullHistory {
		if message.Role == RoleUser && len(BuildContextItemsFromHistoryEntry(message, false)) > 0 {
			lastUserIndex = index
			continue
		}
		if message.LogicalTurnID == "" {
			continue
		}
		if _, ok := turnIDs[message.LogicalTurnID]; !ok {
			continue
		}
		if _, exists := anchors[message.LogicalTurnID]; exists {
			continue
		}
		if lastUserIndex >= 0 {
			anchors[message.LogicalTurnID] = lastUserIndex
		}
	}
	return anchors
}

// CollectAlwaysSelectedReferenceIndices 收集需要长期随请求发送的参考消息索引
//
// 对齐 Python _collect_always_selected_reference_indices：只有上下文摘要与启动恢复提示常驻，
// 其余参考消息（黑话、Focus 事件、工具媒体）随窗口自然淘汰。
func CollectAlwaysSelectedReferenceIndices(chatHistory []ContextMessage, enableVisual bool) []int {
	indices := make([]int, 0, 4)
	for index, message := range chatHistory {
		if !IsContextSummaryMessage(message) && message.MetadataString("reference_type") != ReferenceContextRestoreType {
			continue
		}
		if len(BuildContextItemsFromHistoryEntry(message, enableVisual)) == 0 {
			continue
		}
		indices = append(indices, index)
	}
	return indices
}

// FilterHistoryForRequestKind 按请求类型过滤不应暴露的历史工具链
func FilterHistoryForRequestKind(history []ContextMessage, requestKind string) []ContextMessage {
	switch requestKind {
	case "expression_selector":
		// 对齐 Python：只保留 SessionBackedMessage，即真实收发的 IM 消息
		out := make([]ContextMessage, 0, len(history))
		for _, message := range history {
			if message.CountInContext {
				out = append(out, message)
			}
		}
		return out
	case "planner":
		return history
	default:
		// 对齐 Python：非 planner 请求只剔除常驻上下文摘要
		out := make([]ContextMessage, 0, len(history))
		for _, message := range history {
			if IsContextSummaryMessage(message) {
				continue
			}
			out = append(out, message)
		}
		return out
	}
}

// ResolveEnableVisualMessage 按请求类型决定是否允许携带图片
func ResolveEnableVisualMessage(requestKind string) bool {
	switch requestKind {
	case "expression_selector":
		return false
	default:
		return true
	}
}

// requestRoleOf 返回请求侧角色。参考消息在 Python 里 role 即为 user，
// 内部仍保留 RoleReference 供历史选择与一次性消费使用。
func requestRoleOf(message ContextMessage) ContextRole {
	if message.Role == RoleReference {
		return RoleUser
	}
	return message.Role
}

// BuildContextItemsFromHistoryEntry 把一条历史消息转换为请求项
func BuildContextItemsFromHistoryEntry(message ContextMessage, enableVisual bool) []ContextItem {
	item := ContextItem{
		Role:       requestRoleOf(message),
		ToolCalls:  message.ToolCalls,
		ToolCallID: message.ToolCallID,
		ToolName:   message.ToolName,
		Visual:     enableVisual,
	}
	text := message.Content
	if text == "" && len(message.ToolCalls) == 0 && len(message.Images) == 0 && len(message.Emojis) == 0 {
		return nil
	}
	if len(message.Emojis) > 0 {
		item.Emojis = message.Emojis
		emojiText := emojiPlaceholderText(message.Emojis, enableVisual)
		if text == "" {
			text = emojiText
		} else if strings.Contains(text, "[表情包]") {
			text = strings.ReplaceAll(text, "[表情包]", emojiText)
		} else if emojiText != "" {
			text += "\n" + emojiText
		}
	}
	if len(message.Images) > 0 {
		if enableVisual {
			item.Images = message.Images
			if text == "" {
				text = imagePlaceholderText(message.Images)
			}
		} else {
			pictures := imagePlaceholderText(message.Images)
			if strings.TrimSpace(text) == "" {
				text = pictures
			} else {
				text += "\n" + pictures
			}
		}
	}
	item.Text = text
	return []ContextItem{item}
}

func emojiPlaceholderText(emojis []ImageComponent, enableVisual bool) string {
	parts := make([]string, 0, len(emojis))
	for _, emoji := range emojis {
		if enableVisual {
			if _, _, ok := ImageComponentPayload(emoji); ok {
				parts = append(parts, "[消息类型]表情包")
				continue
			}
		}
		if strings.TrimSpace(emoji.Description) != "" {
			parts = append(parts, emoji.Description)
			continue
		}
		if emoji.Placeholder != "" {
			parts = append(parts, emoji.Placeholder)
			continue
		}
		parts = append(parts, "[表情包]")
	}
	return strings.Join(parts, " ")
}

// ImagePlaceholderText 按 Python _append_image_component 生成图片占位文本：
// 已识别用描述，未识别统一落到「识别中」占位
func ImagePlaceholderText(images []ImageComponent) string {
	return imagePlaceholderText(images)
}

// imagePlaceholderText 按 Python _append_image_component 生成图片占位文本
func imagePlaceholderText(images []ImageComponent) string {
	parts := make([]string, 0, len(images))
	for _, image := range images {
		if image.Description != "" {
			parts = append(parts, "[图片："+image.Description+"]")
			continue
		}
		if image.Placeholder != "" {
			parts = append(parts, image.Placeholder)
			continue
		}
		parts = append(parts, PendingImagePlaceholder)
	}
	return strings.Join(parts, " ")
}

// SetInterruptFlag 绑定本轮 Planner 请求的中断标记，传 nil 解绑
func (s *ChatLoopService) SetInterruptFlag(flag <-chan struct{}) {
	s.InterruptFlag = flag
}

// ChatLoopStep 执行一轮 Maisaka 规划器请求
func (s *ChatLoopService) ChatLoopStep(ctx context.Context, chatHistory []ContextMessage, requestKind string, maxContextSize int, tools []ToolSpec, systemPrompt, finalUserMessage string, injected, tail []string, deferredToolsReminder ...string) (ChatResponse, error) {
	selected, reason := s.SelectLLMContextMessages(chatHistory, requestKind, maxContextSize)
	s.IncludeDayBoundaryTimeMessages = requestKind == "planner"
	enableVisual := s.EnableVisualMessage && ResolveEnableVisualMessage(requestKind)
	items, historyItemCount := s.buildRequestMessages(selected, enableVisual, systemPrompt, injected, tail, finalUserMessage, strings.Join(deferredToolsReminder, "\n"))
	if enableVisual && s.LimitImages != nil {
		items = s.LimitImages(items, s.MaxImageNum)
	}
	items, tools = emitPlannerBeforeRequest(ctx, s.SessionID, items, tools, len(selected), reason)
	// 选中 0 条历史却仍发起请求时，模型只能看到工具提示与时间，会误判「没有聊天内容」
	if len(selected) == 0 {
		zap.L().Warn("Planner 请求缺少聊天历史",
			zap.String("kind", requestKind),
			zap.Int("history", len(chatHistory)),
			zap.Int("items", len(items)),
			zap.String("reason", reason))
	}
	client := s.Client
	if client == nil {
		return ChatResponse{}, fmt.Errorf("Maisaka 对话循环缺少模型客户端")
	}
	request := ChatRequest{
		RequestKind:   requestKind,
		ModelTaskName: ResolveModelTaskName(requestKind, s.ModelTaskName),
		SystemPrompt:  systemPrompt,
		Items:         items,
		Tools:         tools,
		InterruptFlag: s.InterruptFlag,
		Stream:        requestKind == "planner" && s.PlannerMessageSteering,
		SessionID:     s.SessionID,
		MaxImageSide:  s.MaxImageSide,
	}
	response, err := client.Chat(ctx, request)
	if err != nil {
		return ChatResponse{}, err
	}
	if requestKind == "planner" {
		requestClient := response.RequestClient
		if requestClient == nil {
			requestClient = client
		}
		s.setPlannerSnapshot(&PlannerRequestSnapshot{
			RequestKind:    requestKind,
			ModelTask:      request.ModelTaskName,
			SystemPrompt:   systemPrompt,
			Items:          cloneContextItems(items),
			Tools:          cloneToolSpecs(tools),
			ToolNames:      toolNames(tools),
			SessionID:      s.SessionID,
			IsGroup:        s.IsGroupChat,
			Client:         requestClient,
			Stream:         request.Stream,
			MaxImageSide:   request.MaxImageSide,
			CoveredHistory: cloneContextMessages(selected),
		}, response.Usage.PromptTokens)
	}
	response.SelectedHistoryCount = len(selected)
	response.ToolCount = len(tools)
	if s.MeasureSections != nil {
		response.ContextSections = s.MeasureSections(items, historyItemCount, tools)
	}
	if response.RawMessages == nil {
		response.RawMessages = BuildRawMessagesFromResponse(response, s.SessionID, items)
	}
	emitPlannerAfterResponse(ctx, s.SessionID, &response, len(selected), reason)
	if requestKind == "planner" {
		judgment, judgeErr := s.CompleteMissingPlannerAction(ctx, response, tools, selected)
		if judgeErr != nil {
			return ChatResponse{}, judgeErr
		}
		if judgment != nil {
			response.ToolCalls = append(response.ToolCalls, judgment.ToolCall)
			response.RawMessages = BuildRawMessagesFromResponse(response, s.SessionID, items)
			response.PlannerActionJudge = judgment.Metadata
		}
	}
	_ = historyItemCount
	_ = reason
	return response, nil
}

// PlannerSnapshot 返回最近一次成功的 Planner 请求快照
func (s *ChatLoopService) PlannerSnapshot() *PlannerRequestSnapshot {
	snapshot, _ := s.plannerState()
	return snapshot
}

func (s *ChatLoopService) LastPlannerPromptTokens() int {
	_, tokens := s.plannerState()
	return tokens
}

func (s *ChatLoopService) plannerState() (*PlannerRequestSnapshot, int) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	return s.Snapshot, s.lastPlannerPromptTokens
}

// ClearPlannerSnapshot 提交压缩后清空旧前缀，避免同一批资料重复复用
func (s *ChatLoopService) ClearPlannerSnapshot() {
	s.setPlannerSnapshot(nil, 0)
}

func (s *ChatLoopService) setPlannerSnapshot(snapshot *PlannerRequestSnapshot, tokens int) {
	s.snapshotMu.Lock()
	s.Snapshot = snapshot
	s.lastPlannerPromptTokens = tokens
	s.snapshotMu.Unlock()
}

func cloneContextMessages(messages []ContextMessage) []ContextMessage {
	if len(messages) == 0 {
		return nil
	}
	out := make([]ContextMessage, len(messages))
	copy(out, messages)
	return out
}

func cloneContextItems(items []ContextItem) []ContextItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]ContextItem, len(items))
	for index, item := range items {
		out[index] = item
		if len(item.ToolCalls) > 0 {
			calls := make([]ToolCall, len(item.ToolCalls))
			for callIndex, call := range item.ToolCalls {
				if call.Arguments != nil {
					arguments := make(map[string]any, len(call.Arguments))
					for key, value := range call.Arguments {
						arguments[key] = value
					}
					call.Arguments = arguments
				}
				if call.ExtraContent != nil {
					extraContent := make(map[string]any, len(call.ExtraContent))
					for key, value := range call.ExtraContent {
						extraContent[key] = value
					}
					call.ExtraContent = extraContent
				}
				calls[callIndex] = call
			}
			out[index].ToolCalls = calls
		}
		if len(item.Images) > 0 {
			out[index].Images = append([]ImageComponent(nil), item.Images...)
		}
	}
	return out
}

func cloneToolSpecs(tools []ToolSpec) []ToolSpec {
	if len(tools) == 0 {
		return nil
	}
	out := make([]ToolSpec, len(tools))
	copy(out, tools)
	return out
}

// BuildRawMessagesFromResponse 把一次响应投影成可写入历史的消息
func BuildRawMessagesFromResponse(response ChatResponse, sessionID string, items []ContextItem) []ContextMessage {
	message := ContextMessage{
		Role:      RoleAssistant,
		Content:   response.Content,
		Reasoning: response.Reasoning,
		ToolCalls: response.ToolCalls,
		Timestamp: nowTime(),
		SessionID: sessionID,
	}
	return []ContextMessage{message}
}

func toolNames(tools []ToolSpec) []string {
	out := make([]string, 0, len(tools))
	for _, spec := range tools {
		out = append(out, spec.Name)
	}
	return out
}

func sortUniqueInts(values []int) {
	if len(values) < 2 {
		return
	}
	sortInts(values)
	unique := values[:1]
	for _, value := range values[1:] {
		if value == unique[len(unique)-1] {
			continue
		}
		unique = append(unique, value)
	}
	copy(values, unique)
}

// trimUngroundedLeadingToolCalls 移除开头缺少 user/tool 锚点的工具调用消息
//
// 上下文裁剪按条数截断时，可能只留下 assistant 的 tool_calls 而丢掉触发它的 user 消息；
// 这类前缀在协议上非法，直接丢弃，避免整轮请求 panic。
func trimUngroundedLeadingToolCalls(items []ContextItem) []ContextItem {
	index := 0
	for index < len(items) {
		item := items[index]
		if item.Role == RoleUser || item.Role == RoleTool {
			break
		}
		if item.Role == RoleAssistant && len(item.ToolCalls) > 0 {
			index++
			continue
		}
		break
	}
	if index == 0 {
		return items
	}
	return items[index:]
}

func normalizeLoopToolResultOrder(history []ContextMessage) []ContextMessage {
	if len(history) == 0 {
		return history
	}
	consumed := make(map[int]struct{})
	normalized := make([]ContextMessage, 0, len(history))
	for i := 0; i < len(history); i++ {
		if _, skip := consumed[i]; skip {
			continue
		}
		if history[i].Role != RoleAssistant {
			normalized = append(normalized, history[i])
			continue
		}
		var calls []ToolCall
		for j := i; j < len(history) && history[j].Role == RoleAssistant; j++ {
			if _, skip := consumed[j]; skip {
				continue
			}
			consumed[j] = struct{}{}
			normalized = append(normalized, history[j])
			calls = append(calls, history[j].ToolCalls...)
		}
		for _, call := range calls {
			if call.CallID == "" {
				continue
			}
			for k := i + 1; k < len(history); k++ {
				if _, skip := consumed[k]; skip {
					continue
				}
				if history[k].Role == RoleTool && history[k].ToolCallID == call.CallID {
					consumed[k] = struct{}{}
					normalized = append(normalized, history[k])
					break
				}
			}
		}
	}
	return normalized
}
