package pluginruntime

import "strings"

// ForcePluginCompatibilityEnvKey Runner 环境变量名，对齐 compat_policy 的常量
const ForcePluginCompatibilityEnvKey = "MAIBOT_FORCE_PLUGIN_COMPATIBILITY"

// compatEnvEnabledValues 对齐 compat_policy._ENV_ENABLED_VALUES
var compatEnvEnabledValues = map[string]struct{}{
	"1": {}, "true": {}, "yes": {}, "on": {},
}

// BuildForcePluginCompatibilityEnv 对齐 build_force_plugin_compatibility_env
func BuildForcePluginCompatibilityEnv(enabled bool) string {
	if enabled {
		return "1"
	}
	return "0"
}

// ParseForcePluginCompatibilityEnv 对齐 parse_force_plugin_compatibility_env
func ParseForcePluginCompatibilityEnv(rawValue string) bool {
	_, ok := compatEnvEnabledValues[strings.ToLower(strings.TrimSpace(rawValue))]
	return ok
}
