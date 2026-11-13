// Host ↔ Runner 子进程环境变量名，对齐 src/plugin_runtime/__init__.py
package host

const (
	EnvIPCAddress               = "MAIBOT_IPC_ADDRESS"
	EnvSessionToken             = "MAIBOT_SESSION_TOKEN"
	EnvPluginDirs               = "MAIBOT_PLUGIN_DIRS"
	EnvPluginTypeFilter         = "MAIBOT_PLUGIN_TYPE_FILTER"
	EnvTrustedPluginDirs        = "MAIBOT_TRUSTED_PLUGIN_DIRS"
	EnvHostVersion              = "MAIBOT_HOST_VERSION"
	EnvHostPID                  = "MAIBOT_HOST_PID"
	EnvForcePluginCompatibility = "MAIBOT_FORCE_PLUGIN_COMPATIBILITY"
	EnvExternalPluginIDs        = "MAIBOT_EXTERNAL_PLUGIN_IDS"
	EnvBlockedPluginReasons     = "MAIBOT_BLOCKED_PLUGIN_REASONS"
	EnvRunnerGroup              = "MAIBOT_RUNNER_GROUP"
	EnvGlobalConfigSnapshot     = "MAIBOT_GLOBAL_CONFIG_SNAPSHOT"
	EnvLocalPluginSDKPath       = "MAIBOT_PLUGIN_SDK_PATH"

	// Go Host 侧可选：Runner IPC 传输（tcp / uds / pipe），空地址时的默认后端
	EnvPluginIPC = "MAIBOT_PLUGIN_IPC"

	// Go Host 侧可选：是否 spawn Python Runner 及解释器/入口
	EnvPluginSpawn        = "MAIBOT_PLUGIN_SPAWN"
	EnvPluginPython       = "MAIBOT_PLUGIN_PYTHON"
	EnvPluginRunnerScript = "MAIBOT_PLUGIN_RUNNER_SCRIPT"
	// Python 项目根（含 src/），未设时尝试工作目录 ../MaiBot
	EnvPluginProjectRoot = "MAIBOT_PLUGIN_PROJECT_ROOT"
)
