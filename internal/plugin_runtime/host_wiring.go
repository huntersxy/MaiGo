package pluginruntime

// Host 与集成层之间的接线。
//
// 对齐 MaiBot 的分层：src/plugin_runtime/integration.py（集成层，包根目录）
// 负责依赖流水线、manifest 校验、插件目录与冲突扫描；
// src/plugin_runtime/host/ 只负责进程编排、RPC、组件注册。
//
// Go 的包边界不允许 host 反向引用集成层，因此这两类能力以端口形式
// 声明在 host 包，由本文件提供实现并在构造时注入。

import (
	"context"
	"sort"

	"go.uber.org/zap"

	"MaiGo/internal/plugin_runtime/capabilities"
	"MaiGo/internal/plugin_runtime/host"
)

// Host 插件运行时宿主，定义在 host 子包
//
// 用类型别名暴露，使包外（bootstrap / plugin_market / webui）的
// pluginruntime.Host 引用与全部方法保持不变。
type Host = host.Host

// RunnerState Runner 组运行状态
type RunnerState = host.RunnerState

// NewHost 构造插件运行时宿主
//
// 构造后立即注入集成层能力：依赖流水线与 manifest 冲突扫描器。
// 未注入时 Host 会跳过对应步骤而不是报错，保证单测与最小化部署可用。
func NewHost() *host.Host {
	instance := host.NewHost()
	instance.SetDependencyRunner(NewPluginDependencyPipeline(""))
	instance.SetManifestConflictScanner(manifestConflictScanner{})
	return instance
}

// IsNativeAdapter 判断是否进程内 Go 适配器
func IsNativeAdapter(pluginID string) bool {
	return host.IsNativeAdapter(pluginID)
}

// manifestConflictScanner 实现 host.ManifestConflictScanner
//
// 对齐 Python integration.py:639-663：先扫重复插件 ID，再扫同一 client_type
// 的 LLM Provider 声明冲突，最后隔离已并入主程序的桌宠插件。
type manifestConflictScanner struct{}

// ScanManifestConflicts 返回 pluginID -> 阻止原因
func (manifestConflictScanner) ScanManifestConflicts(pluginDirs []string, hostVersion string, forceCompatibility bool) map[string]string {
	validator := NewManifestValidator("")
	validator.HostVersion = hostVersion
	validator.ForcePluginCompatibility = forceCompatibility
	// 依赖检查由流水线负责，冲突扫描只看 manifest 声明，避免重复跑 pip 探测
	validator.SkipPythonPackageDependencies = true

	reasons := validator.BuildDuplicatePluginBlockReasons(pluginDirs)

	excluded := make(map[string]struct{}, len(reasons))
	for pluginID := range reasons {
		excluded[pluginID] = struct{}{}
	}
	// 对齐 Python integration.py:646-651：桌宠已并入主程序，旧插件不能再启动第二个窗口
	if _, exists := validator.BuildPluginDependencyMap(pluginDirs, false, false)[desktopPetPluginID]; exists {
		reasons[desktopPetPluginID] = desktopPetBlockedReason
	}
	for pluginID, reason := range validator.DiscoverLLMProviderConflicts(pluginDirs, excluded) {
		if existing := reasons[pluginID]; existing != "" {
			reasons[pluginID] = existing + "；" + reason
		} else {
			reasons[pluginID] = reason
		}
	}
	return reasons
}

const (
	// desktopPetPluginID 已并入主程序的桌宠插件 ID
	desktopPetPluginID = "sengokucola.desktop-pet"
	// desktopPetBlockedReason 对齐 Python integration.py:649-651 的隔离原因
	desktopPetBlockedReason = "桌宠已合并到主程序，请在主程序配置中启用 desktop_pet.enabled；旧待办和窗口位置继续沿用。"
)

// SyncBlockedReasons 实现 host.DependencyRunner
//
// 把流水线的完整结果投影成 Host 关心的三项：阻止原因、环境是否变化、
// 安装失败原因。InstallRequirements 只用于流水线内部日志，不外传。
func (p *PluginDependencyPipeline) SyncBlockedReasons(
	ctx context.Context,
	pluginDirs []string,
	initialBlocked map[string]string,
) host.DependencySyncResult {
	if p == nil {
		return host.DependencySyncResult{BlockedPluginReasons: initialBlocked}
	}
	result := p.Execute(ctx, pluginDirs, initialBlocked)
	return host.DependencySyncResult{
		BlockedPluginReasons: result.BlockedPluginReasons,
		EnvironmentChanged:   result.EnvironmentChanged,
		InstallError:         result.InstallError,
	}
}

// logBlockedPluginDetails 把阻止原因明细打到日志，便于运维定位
func logBlockedPluginDetails(reasons map[string]string) {
	if len(reasons) == 0 {
		return
	}
	details := make([]string, 0, len(reasons))
	for pluginID, reason := range reasons {
		details = append(details, pluginID+": "+reason)
	}
	sort.Strings(details)
	zap.L().Error("插件被阻止加载",
		zap.Strings("details", details))
}

// 以下为迁入 host 子包后仍需在包内可见的符号。
//
// 这些是集成层（依赖流水线、manifest 校验、人格配置）实际要用的
// 路径解析与环境变量名，定义在 host 子包，这里转出以保持调用点不变。

// InstanceBotConfig 实例 bot_config 的只读视图
type InstanceBotConfig = host.InstanceBotConfig

const (
	// EnvLocalPluginSDKPath 本地插件 SDK 路径环境变量
	EnvLocalPluginSDKPath = host.EnvLocalPluginSDKPath
	// EnvPluginPython 插件使用的 Python 解释器环境变量
	EnvPluginPython = host.EnvPluginPython
)

// ResolveInstanceRoot 解析实例根目录
func ResolveInstanceRoot() string {
	return host.ResolveInstanceRoot()
}

// defaultPythonBin 返回默认的 Python 解释器；projectRoot 有 .venv 时优先使用
func defaultPythonBin(projectRoot string) string {
	return host.DefaultPythonBin(projectRoot)
}

// 以下别名把 host 子包的类型与常量转出给包外使用。
//
// hostports 与 webui 等上层以 pluginruntime.XXX 的形式引用这些类型，
// 迁包后用别名保持调用点不变。

type (
	// DesktopPetButtonContribution 桌宠按钮贡献
	DesktopPetButtonContribution = host.DesktopPetButtonContribution
	// StartRunnerOptions 启动 Runner 子进程的参数
	StartRunnerOptions = host.StartRunnerOptions
	// PluginMessageGatewayPort 消息网关端口
	PluginMessageGatewayPort = host.PluginMessageGatewayPort
	// PluginGatewayDriverSpec 插件网关驱动注册参数
	PluginGatewayDriverSpec = host.PluginGatewayDriverSpec
	// NativeGatewayDriverSpec 进程内原生网关驱动注册参数
	NativeGatewayDriverSpec = host.NativeGatewayDriverSpec
	// PluginGatewayInboundRequest 插件网关入站归属
	PluginGatewayInboundRequest = host.PluginGatewayInboundRequest
	// PluginSendRequest 消息发送请求
	PluginSendRequest = host.PluginSendRequest
	// PluginSendResult 消息发送结果
	PluginSendResult = host.PluginSendResult
	// PluginLLMRequest 模型调用请求
	PluginLLMRequest = host.PluginLLMRequest
	// PluginEmbedRequest 文本嵌入请求
	PluginEmbedRequest = host.PluginEmbedRequest
	// PluginEmojiRegisterRequest 表情注册请求
	PluginEmojiRegisterRequest = host.PluginEmojiRegisterRequest
	// PluginRenderRequest render.html2png 参数
	PluginRenderRequest = host.PluginRenderRequest
)

const (
	// EnvPluginProjectRoot 插件项目根目录环境变量
	EnvPluginProjectRoot = host.EnvPluginProjectRoot
)

// LoadSpawnConfigFromEnv 从环境变量加载 Runner 启动配置
func LoadSpawnConfigFromEnv() host.SpawnConfig {
	return host.LoadSpawnConfigFromEnv()
}

type (
	// PluginSessionFilter 会话过滤条件
	PluginSessionFilter = host.PluginSessionFilter
	// PluginOpenSession 打开会话结果
	PluginOpenSession = host.PluginOpenSession
	// PluginAvatarRequest 头像请求
	PluginAvatarRequest = host.PluginAvatarRequest
)

var (
	// DefaultHost 返回进程级插件宿主
	DefaultHost = host.DefaultHost
	// ResolveComponentRPCTimeoutMS 解析组件 RPC 超时（毫秒）
	ResolveComponentRPCTimeoutMS = host.ResolveComponentRPCTimeoutMS
	// NormalizeComponentTimeoutMS 归一组件超时（毫秒）
	NormalizeComponentTimeoutMS = host.NormalizeComponentTimeoutMS
)

const (
	// ComponentTypeCommand 命令组件类型
	ComponentTypeCommand = host.ComponentTypeCommand
)

type (
	// APIEntry 插件 API 注册项
	APIEntry = host.APIEntry
	// AdapterTransitionResult 适配器运行时切换结果
	AdapterTransitionResult = host.AdapterTransitionResult
	// CommandExecutionContext 命令执行上下文
	CommandExecutionContext = host.CommandExecutionContext
	// ComponentEntry 组件注册项
	ComponentEntry = host.ComponentEntry
	// ComponentFilter 组件过滤条件
	ComponentFilter = host.ComponentFilter
	// HostCapabilityDeps 能力实现的依赖集合
	HostCapabilityDeps = host.HostCapabilityDeps
	// PluginRecord Host 已注册插件快照
	PluginRecord = host.PluginRecord
	// PluginBotAccountIdentity 插件可见的机器人账号身份
	PluginBotAccountIdentity = host.PluginBotAccountIdentity
	// PluginDatabaseQuery 数据库查询条件
	PluginDatabaseQuery = host.PluginDatabaseQuery
	// PluginRenderPort 渲染端口
	PluginRenderPort = host.PluginRenderPort
	// PluginSendPort 消息发送端口
	PluginSendPort = host.PluginSendPort
	// PluginSessionPort 会话端口
	PluginSessionPort = host.PluginSessionPort
	// PluginWebPort 网页抓取端口
	PluginWebPort = host.PluginWebPort
	// RuntimeSettings 运行时超时与重试配置
	RuntimeSettings = host.RuntimeSettings
	// ToolExecutionContext 工具执行上下文
	ToolExecutionContext = host.ToolExecutionContext
	// UploadCapacityError 上传容量超限错误
	UploadCapacityError = host.UploadCapacityError
	// UploadBudget 上传即时配额（单插件/全局并发处理数）
	UploadBudget = capabilities.UploadBudget
)

const (
	// ComponentTypeHomeCard 主页卡片组件类型
	ComponentTypeHomeCard = host.ComponentTypeHomeCard
	// ComponentTypeMessageGateway 消息网关组件类型
	ComponentTypeMessageGateway = host.ComponentTypeMessageGateway
	// ComponentTypeTool 工具组件类型
	ComponentTypeTool = host.ComponentTypeTool
	// EventOnStart 插件启动事件
	EventOnStart = host.EventOnStart
	// EventOnStop 插件停止事件
	EventOnStop = host.EventOnStop
	// MaxUploadBytes 单次上传上限
	MaxUploadBytes = host.MaxUploadBytes
	// MaxWebUIArgumentBytes WebUI 参数单值上限
	MaxWebUIArgumentBytes = host.MaxWebUIArgumentBytes
	// MaxWebUIArgumentDepth WebUI 参数嵌套上限
	MaxWebUIArgumentDepth = host.MaxWebUIArgumentDepth
	// RunnerGroupThirdParty 第三方插件分组
	RunnerGroupThirdParty = host.RunnerGroupThirdParty
)

var (
	// NewUploadStore 构造上传存储
	NewUploadStore = host.NewUploadStore
	// WebUIRequiredCapabilities WebUI 扩展所需能力
	WebUIRequiredCapabilities = host.WebUIRequiredCapabilities
	// DefaultRunnerPluginDirs 按分组返回缺省插件目录与受信目录
	DefaultRunnerPluginDirs = host.DefaultRunnerPluginDirs
	// ResolvePluginConfigPath 解析插件配置文件位置
	ResolvePluginConfigPath = host.ResolvePluginConfigPath
)

// DefaultRuntimeSettings 返回默认运行时配置
func DefaultRuntimeSettings() RuntimeSettings { return host.DefaultRuntimeSettings() }

// LogSpawnSkipped 记录跳过启动 Runner 的原因
func LogSpawnSkipped(cfg host.SpawnConfig, reason error) { host.LogSpawnSkipped(cfg, reason) }

// RegisterHostCapabilities 注册全部 Host 能力实现
func RegisterHostCapabilities(h *host.Host, deps HostCapabilityDeps) ([]string, error) {
	return host.RegisterHostCapabilities(h, deps)
}

// SetAdapterPolicyBlockedCloser 设置适配器策略被阻止时的清理回调
func SetAdapterPolicyBlockedCloser(closer func(ctx context.Context) error) {
	host.SetAdapterPolicyBlockedCloser(closer)
}

// StartRunnerOptionsFromSpawnConfig 从 spawn 配置构造启动参数
func StartRunnerOptionsFromSpawnConfig(cfg host.SpawnConfig, base StartRunnerOptions) StartRunnerOptions {
	return host.StartRunnerOptionsFromSpawnConfig(cfg, base)
}

// RuntimeSettingsFromConfig 从配置构造运行时设置
func RuntimeSettingsFromConfig(healthCheckIntervalSec float64, maxRestartAttempts int, runnerSpawnTimeoutSec float64, hookBlockingTimeoutSec float64, ipcSocketPath string) RuntimeSettings {
	return host.RuntimeSettingsFromConfig(healthCheckIntervalSec, maxRestartAttempts, runnerSpawnTimeoutSec, hookBlockingTimeoutSec, ipcSocketPath)
}

// ValidateWebUIJSON 校验 WebUI 扩展 JSON 的体积与深度
func ValidateWebUIJSON(value any, maxBytes, maxDepth int) error {
	return host.ValidateWebUIJSON(value, maxBytes, maxDepth)
}

type (
	// UploadStore 插件上传文件的临时存储
	UploadStore = host.UploadStore
	// PluginDatabasePort 数据库访问端口
	PluginDatabasePort = host.PluginDatabasePort
	// PluginPersonPort 人物信息端口
	PluginPersonPort = host.PluginPersonPort
	// PluginFrequencyPort 词频统计端口
	PluginFrequencyPort = host.PluginFrequencyPort
	// PluginKnowledgePort 知识检索端口
	PluginKnowledgePort = host.PluginKnowledgePort
	// PluginMarketPort 插件市场端口
	PluginMarketPort = host.PluginMarketPort
	// PluginStatisticsPort 统计端口
	PluginStatisticsPort = host.PluginStatisticsPort
)

const (
	// EnvPluginDirs 插件目录环境变量
	EnvPluginDirs = host.EnvPluginDirs
)
