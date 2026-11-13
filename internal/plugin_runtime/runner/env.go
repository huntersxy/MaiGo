package runner

// Runner 子进程相关的环境变量名，对齐父包 env.go 的同名常量。
//
// 这些名字是 Runner 与前端的协议契约（Python runner_main 读的是同一批键），
// 放在本包是为了让进程启动逻辑自足；父包保留同名常量，两处取值必须一致，
// 由 TestRunnerEnvConstantsMatchParent 兜底校验。
const (
	// EnvIPCAddress Runner 的 IPC 地址
	EnvIPCAddress = "MAIBOT_IPC_ADDRESS"
	// EnvSessionToken 会话令牌
	EnvSessionToken = "MAIBOT_SESSION_TOKEN"
	// EnvPluginDirs 插件目录列表
	EnvPluginDirs = "MAIBOT_PLUGIN_DIRS"
	// EnvPluginTypeFilter 插件类型过滤
	EnvPluginTypeFilter = "MAIBOT_PLUGIN_TYPE_FILTER"
	// EnvTrustedPluginDirs 受信任插件目录
	EnvTrustedPluginDirs = "MAIBOT_TRUSTED_PLUGIN_DIRS"
	// EnvHostVersion 宿主版本
	EnvHostVersion = "MAIBOT_HOST_VERSION"
	// EnvHostPID 宿主进程号
	EnvHostPID = "MAIBOT_HOST_PID"
	// EnvRunnerGroup Runner 分组
	EnvRunnerGroup = "MAIBOT_RUNNER_GROUP"
	// EnvLocalPluginSDKPath 本地插件 SDK 路径
	EnvLocalPluginSDKPath = "MAIBOT_PLUGIN_SDK_PATH"
	// EnvPluginPython 指定插件使用的 Python 解释器
	EnvPluginPython = "MAIBOT_PLUGIN_PYTHON"
	// EnvForcePluginCompatibility 强制插件兼容性放行
	EnvForcePluginCompatibility = "MAIBOT_FORCE_PLUGIN_COMPATIBILITY"
	// EnvExternalPluginIDs 外部插件 ID 列表
	EnvExternalPluginIDs = "MAIBOT_EXTERNAL_PLUGIN_IDS"
	// EnvBlockedPluginReasons 被拒插件的拒绝原因
	EnvBlockedPluginReasons = "MAIBOT_BLOCKED_PLUGIN_REASONS"
	// EnvGlobalConfigSnapshot Runner 启动时注入的全局配置快照（JSON 对象）
	EnvGlobalConfigSnapshot = "MAIBOT_GLOBAL_CONFIG_SNAPSHOT"
)
