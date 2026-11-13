package host

import (
	"strings"

	"github.com/bytedance/sonic"
)

// SetExternalAvailablePlugins 设置本次 Runner 启动时视为已满足依赖的外部插件版本
//
// 对齐 supervisor.set_external_available_plugins：只保留非空插件 ID 与版本
func (h *Host) SetExternalAvailablePlugins(pluginVersions map[string]string) {
	if h == nil {
		return
	}
	next := make(map[string]string, len(pluginVersions))
	for pluginID, version := range pluginVersions {
		pluginID = strings.TrimSpace(pluginID)
		version = strings.TrimSpace(version)
		if pluginID != "" && version != "" {
			next[pluginID] = version
		}
	}
	h.mu.Lock()
	h.externalAvailablePlugins = next
	h.mu.Unlock()
}

// ExternalAvailablePlugins 返回外部可用插件版本映射副本
func (h *Host) ExternalAvailablePlugins() map[string]string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]string, len(h.externalAvailablePlugins))
	for pluginID, version := range h.externalAvailablePlugins {
		out[pluginID] = version
	}
	return out
}

// SetForcePluginCompatibility 设置强制插件兼容开关
func (h *Host) SetForcePluginCompatibility(enabled bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.forcePluginCompatibility = enabled
	h.mu.Unlock()
}

// ForcePluginCompatibility 返回强制插件兼容开关
func (h *Host) ForcePluginCompatibility() bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.forcePluginCompatibility
}

// runnerHostEnv 生成 Runner 启动环境中的 Host 状态快照
//
// 对齐 supervisor._build_runner_environment；overrides 最后写入，允许调用方覆盖
func (h *Host) runnerHostEnv(overrides map[string]string) map[string]string {
	env := make(map[string]string, len(overrides)+3)
	if h != nil {
		h.mu.RLock()
		reasons := make(map[string]string, len(h.blockedReasons))
		for pluginID, reason := range h.blockedReasons {
			reasons[pluginID] = reason
		}
		external := make(map[string]string, len(h.externalAvailablePlugins))
		for pluginID, version := range h.externalAvailablePlugins {
			external[pluginID] = version
		}
		force := h.forcePluginCompatibility
		h.mu.RUnlock()
		env[EnvBlockedPluginReasons] = marshalRunnerEnvMap(reasons)
		env[EnvExternalPluginIDs] = marshalRunnerEnvMap(external)
		if force {
			env[EnvForcePluginCompatibility] = "1"
		} else {
			env[EnvForcePluginCompatibility] = "0"
		}
	}
	for key, value := range overrides {
		if key = strings.TrimSpace(key); key != "" {
			env[key] = value
		}
	}
	return env
}

func marshalRunnerEnvMap(value map[string]string) string {
	raw, err := sonic.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func parseForcePluginCompatibilityEnv(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
