package internalchat

import "maibot-go/internal/core/localoperator"

// PluginRelayConfigKey 插件代为投递的消息标记，对齐 MaiBot PLUGIN_RELAY_CONFIG_KEY
const PluginRelayConfigKey = localoperator.PluginRelayConfigKey

// IsPluginRelayed 判断消息是否由插件代为投递
func IsPluginRelayed(additionalConfig map[string]any) bool {
	return localoperator.IsPluginRelayed(additionalConfig)
}
