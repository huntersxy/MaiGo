package capabilities

import (
	"context"
	"fmt"
	"strings"

	"maibot-go/internal/common/database"
	"maibot-go/internal/emoji_system"
	"maibot-go/internal/files"
)

// HostCapabilityDeps Host 能力实现依赖
//
// 除 Memory/Instance/Files/InternalChat 外的端口由主程序接线；端口为 nil 时注册的
// 能力会返回 success=false 的「端口未接线」错误，而不是伪造成功。
type HostCapabilityDeps struct {
	Memory       *memory.Manager
	Instance     BotConfigSource
	InternalChat PluginChatRelay
	Files        *filesvc.Service
	Emojis       *emojisystem.Manager

	Sender     PluginSendPort
	LLM        PluginLLMPort
	Database   PluginDatabasePort
	Sessions   PluginSessionPort
	Persons    PluginPersonPort
	EmojiAdmin PluginEmojiPort
	Frequency  PluginFrequencyPort
	Knowledge  PluginKnowledgePort
	Market     PluginMarketPort
	Web        PluginWebPort
	Statistics PluginStatisticsPort
	Render     PluginRenderPort
	Maisaka    PluginMaisakaPort

	Components PluginComponentAdminPort
	Uploads    *UploadStore
	Gateway    PluginMessageGatewayPort
	Usage      *PluginUsageTracker

	// PluginDataDir 解析插件数据目录，供 webui.claim_upload 移动文件；nil 时该能力报未接线
	PluginDataDir func(pluginID string) (string, error)
}

// PluginSendRequest 统一发送请求，字段对齐 core.py send.* 参数
type PluginSendRequest struct {
	Kind                 string
	PluginID             string
	StreamID             string
	Text                 string
	ProcessedPlainText   string
	Base64               string
	Command              string
	MessageType          string
	Content              any
	Data                 map[string]any
	ForwardNodes         []map[string]any
	HybridNodes          []map[string]any
	Typing               *bool
	SetReply             bool
	ReplyMessageID       string
	ReturnDetails        bool
	StorageMessage       *bool
	SyncToMaisakaHistory *bool
	SourceKind           string
}

// PluginSendResult 发送结果
type PluginSendResult struct {
	Sent      bool
	MessageID string
}

// PluginSendPort 由主程序发送服务实现（MaiBot: src.services.send_service）
type PluginSendPort interface {
	SendPluginMessage(ctx context.Context, req PluginSendRequest) (PluginSendResult, error)
}

// PluginLLMRequest 插件 LLM 请求，对齐 LLMServiceRequest 能力侧字段
type PluginLLMRequest struct {
	TaskName         string
	TaskNameProvided bool
	RequestType      string
	Prompt           string
	Model            string
	ModelName        string
	Temperature      *float64
	MaxTokens        *int
	Tools            []any
}

// PluginEmbedRequest 插件 Embedding 请求
type PluginEmbedRequest struct {
	Texts         []string
	MaxConcurrent int
}

// PluginLLMPort 由主程序 LLM 服务实现（MaiBot: src.services.llm_service）
type PluginLLMPort interface {
	Generate(ctx context.Context, pluginID string, req PluginLLMRequest) (map[string]any, error)
	Embed(ctx context.Context, pluginID string, req PluginEmbedRequest) (map[string]any, error)
	Transcribe(ctx context.Context, pluginID, audioBase64 string) (map[string]any, error)
	AvailableModels(ctx context.Context) ([]string, error)
}

// PluginDatabaseQuery database.get/database.query 的读取参数
type PluginDatabaseQuery struct {
	Filters      map[string]any
	Limit        int64
	OrderBy      string
	SingleResult bool
}

// PluginDatabasePort 插件数据库能力端口；模型名到表的映射由实现方维护
//
// MaiBot 用 SQLModel 反射 ORM 模型，Go 侧不提供通用反射，因此由主程序登记可访问模型
// 并按 Python database_service 的返回结构返回 {success, data/result, ...}
type PluginDatabasePort interface {
	Get(ctx context.Context, pluginID, modelName string, query PluginDatabaseQuery) (any, error)
	Save(ctx context.Context, pluginID, modelName string, data map[string]any, keyField, keyValue string) (any, error)
	Update(ctx context.Context, pluginID, modelName string, data, filters map[string]any) (any, error)
	Delete(ctx context.Context, pluginID, modelName string, filters map[string]any) (any, error)
	Count(ctx context.Context, pluginID, modelName string, filters map[string]any) (any, error)
}

// PluginSessionFilter chat.* 查询过滤条件
type PluginSessionFilter struct {
	Platform     string
	IsGroup      *bool
	AllPlatforms bool
}

// PluginOpenSession chat.open_session 参数
type PluginOpenSession struct {
	ChatType  string
	GroupID   string
	UserID    string
	Platform  string
	AccountID string
	Scope     string
}

// PluginAvatarRequest chat.get_avatar 参数
type PluginAvatarRequest struct {
	Platform     string
	TargetID     string
	TargetType   string
	AccountID    string
	Scope        string
	ForceRefresh bool
}

// PluginSessionPort 由主程序会话管理实现（MaiBot: chat_manager / avatar_service）
type PluginSessionPort interface {
	ListStreams(ctx context.Context, filter PluginSessionFilter) ([]map[string]any, error)
	OpenSession(ctx context.Context, req PluginOpenSession) (map[string]any, error)
	StreamByGroupID(ctx context.Context, platform, groupID, accountID, scope string) (map[string]any, error)
	StreamByUserID(ctx context.Context, platform, userID, accountID, scope string) (map[string]any, error)
	Avatar(ctx context.Context, req PluginAvatarRequest) (map[string]any, error)
}

// PluginPersonPort 由主程序人物管理实现（MaiBot: Person）
type PluginPersonPort interface {
	GetID(ctx context.Context, platform, userID string) (string, error)
	GetIDByName(ctx context.Context, personName string) (string, error)
	GetValue(ctx context.Context, personID, fieldName string) (any, bool, error)
}

// PluginEmojiRegisterRequest emoji.register 参数
type PluginEmojiRegisterRequest struct {
	ImagePNG    []byte
	Description string
	Emotions    []string
	Replace     bool
	SourceName  string
}

// PluginEmojiPort 由主程序表情管理实现（MaiBot: register_emoji_by_filename / delete）
type PluginEmojiPort interface {
	Register(ctx context.Context, pluginID string, req PluginEmojiRegisterRequest) (map[string]any, error)
	Delete(ctx context.Context, pluginID, emojiHash string, keepDescription bool) (map[string]any, error)
	MaxCount(ctx context.Context) int
}

// PluginFrequencyPort 由主程序聊天频率服务实现（MaiBot: heartflow_chat / ChatConfigUtils）
type PluginFrequencyPort interface {
	CurrentTalkValue(ctx context.Context, chatID string) (float64, error)
	GetAdjust(ctx context.Context, chatID string) (float64, error)
	SetAdjust(ctx context.Context, chatID string, value float64) error
}

// PluginKnowledgePort 由知识库检索实现
type PluginKnowledgePort interface {
	Search(ctx context.Context, pluginID string, args map[string]any) (any, error)
}

// PluginMarketPort 由插件市场实现（internal/pluginmarket）
type PluginMarketPort interface {
	Search(ctx context.Context, args map[string]any) (any, error)
	GetDetail(ctx context.Context, args map[string]any) (any, error)
	GetReadme(ctx context.Context, args map[string]any) (any, error)
}

// PluginWebPort 由联网服务实现（MaiBot: WebSearchService）
type PluginWebPort interface {
	Search(ctx context.Context, pluginID string, args map[string]any) (map[string]any, error)
	Fetch(ctx context.Context, pluginID string, args map[string]any) (map[string]any, error)
}

// PluginStatisticsPort 由本地统计实现（MaiBot: statistics.local.*）
type PluginStatisticsPort interface {
	Invoke(ctx context.Context, capability string, args map[string]any) (any, error)
}

// PluginRenderRequest render.html2png 参数
type PluginRenderRequest struct {
	HTML              string
	Selector          string
	ViewportWidth     int
	ViewportHeight    int
	DeviceScaleFactor float64
	FullPage          bool
	OmitBackground    bool
	WaitUntil         string
	WaitForSelector   string
	WaitForTimeoutMS  int
	TimeoutMS         int
	AllowNetwork      bool
}

// PluginRenderPort 由 HTML 渲染服务实现（MaiBot: html_render_service）
type PluginRenderPort interface {
	RenderHTMLToPNG(ctx context.Context, req PluginRenderRequest) (map[string]any, error)
}

// PluginMaisakaPort 由 Maisaka 回复链路实现
type PluginMaisakaPort interface {
	Invoke(ctx context.Context, capability string, pluginID string, args map[string]any) (any, error)
}

// PluginComponentAdminPort 由运行时管理器实现插件装载、重载与配置热更新
type PluginComponentAdminPort interface {
	LoadPlugin(ctx context.Context, pluginID, reason string) (bool, error)
	ReloadPlugins(ctx context.Context, pluginIDs []string, reason string) (bool, error)
	UpdatePluginConfig(ctx context.Context, pluginID, key string, value any) (map[string]any, error)
}

// PluginGatewayDriverSpec 插件消息网关在 Platform IO 的注册声明
type PluginGatewayDriverSpec struct {
	PluginID        string
	GatewayName     string
	ComponentName   string
	Platform        string
	AccountID       string
	Scope           string
	RouteType       string
	PluginType      string
	Protocol        string
	SupportsSend    bool
	SupportsReceive bool
	Priority        int
	TimeoutMS       int64
	Metadata        map[string]any
}

// PluginGatewayDriverPort 由 Platform IO 实现插件消息网关驱动注册与解绑
type PluginGatewayDriverPort interface {
	RegisterPluginGatewayDriver(ctx context.Context, spec PluginGatewayDriverSpec) error
	UnregisterPluginGatewayDriver(ctx context.Context, pluginID, gatewayName string) error
}

// PluginGatewayInboundRequest 插件消息网关注入入站消息时的路由归属
type PluginGatewayInboundRequest struct {
	PluginID          string
	GatewayName       string
	DriverID          string
	Platform          string
	AccountID         string
	Scope             string
	ExternalMessageID string
	DedupeKey         string
	RouteMetadata     map[string]any
}

// PluginGatewayInboundPort 消息网关入站注入端口
// 由 Platform IO 实现：先做适配器名单策略与去重，再交给主消息链
type PluginGatewayInboundPort interface {
	ReceiveGatewayMessage(ctx context.Context, request PluginGatewayInboundRequest, message map[string]any) (bool, map[string]any, error)
}

// PluginMessageGatewayPort 由 Platform IO 实现外部消息互通与插件网关注册
type PluginMessageGatewayPort interface {
	PluginGatewayDriverPort
	ReceiveExternalMessage(ctx context.Context, message map[string]any) error
	SendMessageToExternal(ctx context.Context, message map[string]any, saveToDB bool) (bool, error)
}

// PluginPortNotWired 端口未接线时返回统一的失败结构
//
// 导出供父包 host_*.go 使用：能力注册时若某个端口没有注入，
// 走这里返回明确文案，而不是 panic 或静默成功。
func PluginPortNotWired(capability string) map[string]any {
	return pluginPortNotWired(capability)
}

// pluginPortNotWired 端口未接线时的标准失败返回
func pluginPortNotWired(capability string) map[string]any {
	return capabilityFailure("%s", pluginPortErrorText(capability))
}

// pluginPortErrorText 端口未接线错误文案
func pluginPortErrorText(capability string) string {
	return capability + " 端口未接线：主程序尚未注入对应服务"
}

// CapabilityHandler Host 侧能力实现，对齐 Python capability_service.CapabilityImpl 签名
type CapabilityHandler func(ctx context.Context, pluginID, capability string, args map[string]any) (any, error)

// BotConfigSource 提供 bot_config 的只读快照
//
// 对齐 Python config.get 能力：按点分路径读取实例 TOML 的 bot_config 段。
// 定义为单方法接口而非具体类型，让能力层不依赖配置读取实现。
type BotConfigSource interface {
	BotConfigSlice() map[string]any
}

// PersonalityConfigPort 提供人格档案的读取与切换
//
// 对齐 Python personality 能力：profile 列表、当前档案、切换都通过该端口，
// 能力层不直接读配置文件。
type PersonalityConfigPort interface {
	PersonalityProfiles() []map[string]any
	CurrentPersonalityProfile() map[string]any
	SwitchPersonalityProfile(name string) (map[string]any, error)
}

// capabilityFailure 构造 success=false 的能力返回
//
// 对齐 MaiBot capability_service 的失败封装：能力失败走返回值而非 RPC 错误，
// 让插件侧能拿到结构化错误信息。
func capabilityFailure(format string, args ...any) map[string]any {
	return map[string]any{"success": false, "error": fmt.Sprintf(format, args...)}
}

// capabilitySuccess 构造 success=true 的能力返回
func capabilitySuccess(result any) map[string]any {
	return map[string]any{"success": true, "result": result}
}

// coalesceString 按候选键顺序取第一个非空字符串参数
func coalesceString(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := args[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}
