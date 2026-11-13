package utils

// A_memorix 插件 ID 匹配策略，对齐 Python core/utils/plugin_id_policy.py

import "strings"

// PluginIDPolicyCanonicalID 规范插件 ID
const PluginIDPolicyCanonicalID = "a_memorix"

// PluginIDPolicy 集中管理插件 ID 的归一化与匹配规则
type PluginIDPolicy struct{}

// Normalize 对齐 normalize：仅接受字符串，去空白并转小写
func (PluginIDPolicy) Normalize(pluginID any) string {
	text, ok := pluginID.(string)
	if !ok {
		return ""
	}
	return strings.ToLower(pyStrip(text))
}

// IsTargetPluginID 对齐 is_target_plugin_id
func (PluginIDPolicy) IsTargetPluginID(pluginID any) bool {
	normalized := (PluginIDPolicy{}).Normalize(pluginID)
	if normalized == "" {
		return false
	}
	if normalized == PluginIDPolicyCanonicalID {
		return true
	}
	parts := strings.Split(normalized, ".")
	return parts[len(parts)-1] == PluginIDPolicyCanonicalID
}
