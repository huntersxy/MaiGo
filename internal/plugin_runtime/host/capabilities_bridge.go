package host

// 能力层的依赖契约与组件转发。
//
// 实现对等到 internal/plugin_runtime/capabilities（对齐 Python
// src/plugin_runtime/capabilities/ 与 host/capability_service.py 的分层）：
// 能力名注册表、具体 handler、依赖端口、上传存储与使用时长记录器都在子包，
// 父包只保留 Host 侧的编排（host_capabilities.go 负责把 deps 注入 handler）。
//
// 这里用类型别名把子包的契约暴露回包内，让 host_*.go 的调用点无需改动。

import (
	"maibot-go/internal/plugin_runtime/capabilities"
)

type (
	// HostCapabilityDeps 能力实现的依赖集合
	HostCapabilityDeps = capabilities.HostCapabilityDeps
	// UploadStore 插件上传文件的临时存储
	UploadStore = capabilities.UploadStore
	// PluginUsageTracker 按运行区间累计插件使用时长
	PluginUsageTracker = capabilities.PluginUsageTracker
)

// 能力端口别名
type (
	// PluginSessionPort 会话查询端口
	PluginSessionPort = capabilities.PluginSessionPort
	// PluginPersonPort 人物信息端口
	PluginPersonPort = capabilities.PluginPersonPort
	// PluginEmojiPort 表情管理端口
	PluginEmojiPort = capabilities.PluginEmojiPort
	// PluginFrequencyPort 词频统计端口
	PluginFrequencyPort = capabilities.PluginFrequencyPort
	// PluginKnowledgePort 知识检索端口
	PluginKnowledgePort = capabilities.PluginKnowledgePort
	// PluginMarketPort 插件市场端口
	PluginMarketPort = capabilities.PluginMarketPort
	// PluginWebPort 网页抓取端口
	PluginWebPort = capabilities.PluginWebPort
	// PluginStatisticsPort 统计端口
	PluginStatisticsPort = capabilities.PluginStatisticsPort
	// PluginRenderPort 渲染端口
	PluginRenderPort = capabilities.PluginRenderPort
	// PluginMaisakaPort Maisaka 智能体端口
	PluginMaisakaPort = capabilities.PluginMaisakaPort
	// PluginMessageGatewayPort 消息网关端口
	PluginMessageGatewayPort = capabilities.PluginMessageGatewayPort
	// PluginComponentAdminPort 组件管理端口
	PluginComponentAdminPort = capabilities.PluginComponentAdminPort
	// PluginGatewayDriverPort 插件网关注册端口
	PluginGatewayDriverPort = capabilities.PluginGatewayDriverPort
	// PluginGatewayDriverSpec 插件网关驱动注册参数
	PluginGatewayDriverSpec = capabilities.PluginGatewayDriverSpec
	// PluginGatewayInboundRequest 插件网关入站归属
	PluginGatewayInboundRequest = capabilities.PluginGatewayInboundRequest
	// PluginGatewayInboundPort 插件网关入站注入端口
	PluginGatewayInboundPort = capabilities.PluginGatewayInboundPort
	// PluginRenderRequest render.html2png 参数
	PluginRenderRequest = capabilities.PluginRenderRequest
	// PluginSendRequest 消息发送请求
	PluginSendRequest = capabilities.PluginSendRequest
	// PluginSendResult 消息发送结果
	PluginSendResult = capabilities.PluginSendResult
	// PluginLLMRequest 模型调用请求
	PluginLLMRequest = capabilities.PluginLLMRequest
	// PluginEmbedRequest 文本嵌入请求
	PluginEmbedRequest = capabilities.PluginEmbedRequest
	// PluginEmojiRegisterRequest 表情注册请求
	PluginEmojiRegisterRequest = capabilities.PluginEmojiRegisterRequest
	// PluginSessionFilter 会话过滤条件
	PluginSessionFilter = capabilities.PluginSessionFilter
	// PluginDatabaseQuery 数据库查询条件
	PluginDatabaseQuery = capabilities.PluginDatabaseQuery
	// PluginOpenSession 打开会话结果
	PluginOpenSession = capabilities.PluginOpenSession
	// PluginAvatarRequest 头像请求
	PluginAvatarRequest = capabilities.PluginAvatarRequest
	// UploadCapacityError 上传容量超限错误
	UploadCapacityError = capabilities.UploadCapacityError
	// PluginSendPort 消息发送端口
	PluginSendPort = capabilities.PluginSendPort
	// PluginLLMPort 模型调用端口
	PluginLLMPort = capabilities.PluginLLMPort
	// PluginDatabasePort 数据库访问端口
	PluginDatabasePort = capabilities.PluginDatabasePort
	// PluginChatRelay 内部聊天转发端口
	PluginChatRelay = capabilities.PluginChatRelay
)

const (
	// DefaultPluginUsageCheckpointInterval 使用时长周期保存间隔（60 秒）
	DefaultPluginUsageCheckpointInterval = capabilities.DefaultPluginUsageCheckpointInterval
	// MaxUploadBytes 单次上传上限
	MaxUploadBytes = capabilities.MaxUploadBytes
)

var (
	// NewUploadStore 构造上传存储
	NewUploadStore = capabilities.NewUploadStore
	// ValidateUploadImage 校验并识别上传图片格式
	ValidateUploadImage = capabilities.ValidateUploadImage
	// NewPluginUsageTracker 构造使用时长记录器
	NewPluginUsageTracker = capabilities.NewPluginUsageTracker
	// DefaultPluginUsagePath 默认使用时长文件路径
	DefaultPluginUsagePath = capabilities.DefaultPluginUsagePath
	// PluginPortNotWired 端口未接线时的统一失败结构
	PluginPortNotWired = capabilities.PluginPortNotWired
	// ValueAsStringKeyedMap 把任意结构转成字符串键 map
	ValueAsStringKeyedMap = capabilities.ValueAsStringKeyedMap
)

// pluginPortNotWired 保持父包内原有调用名
func pluginPortNotWired(capability string) map[string]any {
	return capabilities.PluginPortNotWired(capability)
}

// valueAsStringKeyedMap 保持父包内原有调用名
func valueAsStringKeyedMap(source any) (map[string]any, error) {
	return capabilities.ValueAsStringKeyedMap(source)
}

// lookupConfigValue 按点分路径读取配置值，保持父包内原有调用名
func lookupConfigValue(instance BotConfigSource, key string, defaultValue any) (any, error) {
	return capabilities.LookupConfigValue(instance, key, defaultValue)
}
