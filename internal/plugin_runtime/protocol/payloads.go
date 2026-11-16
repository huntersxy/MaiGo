package protocol

// 常用 RPC 载荷子集，完整模型见 MaiBot src/plugin_runtime/protocol/envelope.py

// HelloPayload runner.hello 握手请求
type HelloPayload struct {
	RunnerID     string `msgpack:"runner_id" json:"runner_id"`
	SDKVersion   string `msgpack:"sdk_version" json:"sdk_version"`
	SessionToken string `msgpack:"session_token" json:"session_token"`
}

// HelloResponsePayload runner.hello 握手响应
type HelloResponsePayload struct {
	Accepted    bool   `msgpack:"accepted" json:"accepted"`
	HostVersion string `msgpack:"host_version" json:"host_version"`
	Reason      string `msgpack:"reason" json:"reason"`
}

// CapabilityRequestPayload cap.* 请求（插件 → Host）
type CapabilityRequestPayload struct {
	Capability string         `msgpack:"capability" json:"capability"`
	Args       map[string]any `msgpack:"args" json:"args"`
}

// CapabilityResponsePayload cap.* 响应
type CapabilityResponsePayload struct {
	Success bool `msgpack:"success" json:"success"`
	Result  any  `msgpack:"result" json:"result"`
}

// InvokePayload plugin.invoke.* 请求
type InvokePayload struct {
	ComponentName string         `msgpack:"component_name" json:"component_name"`
	Args          map[string]any `msgpack:"args" json:"args"`
}

// InvokeResultPayload plugin.invoke.* 响应
type InvokeResultPayload struct {
	Success bool `msgpack:"success" json:"success"`
	Result  any  `msgpack:"result" json:"result"`
}

// LLMProviderDeclaration 插件声明的 LLM Provider，对齐 protocol.envelope.LLMProviderDeclaration
type LLMProviderDeclaration struct {
	ClientType  string         `msgpack:"client_type" json:"client_type"`
	Name        string         `msgpack:"name" json:"name"`
	Description string         `msgpack:"description" json:"description"`
	Version     string         `msgpack:"version" json:"version"`
	Metadata    map[string]any `msgpack:"metadata" json:"metadata"`
}

// BootstrapPluginPayload plugin.bootstrap 请求，对齐 protocol.envelope.BootstrapPluginPayload
type BootstrapPluginPayload struct {
	PluginID             string   `msgpack:"plugin_id" json:"plugin_id"`
	PluginVersion        string   `msgpack:"plugin_version" json:"plugin_version"`
	PluginType           string   `msgpack:"plugin_type" json:"plugin_type"`
	CapabilitiesRequired []string `msgpack:"capabilities_required" json:"capabilities_required"`
}

// ComponentDeclaration 单个组件声明，对齐 protocol.envelope.ComponentDeclaration
type ComponentDeclaration struct {
	Name           string         `msgpack:"name" json:"name"`
	ComponentType  string         `msgpack:"component_type" json:"component_type"`
	PluginID       string         `msgpack:"plugin_id" json:"plugin_id"`
	ChatScope      string         `msgpack:"chat_scope" json:"chat_scope"`
	Platforms      []string       `msgpack:"platforms" json:"platforms"`
	AllowedSession []string       `msgpack:"allowed_session" json:"allowed_session"`
	Metadata       map[string]any `msgpack:"metadata" json:"metadata"`
}

// RegisterPluginPayload plugin.register_components / plugin.register_plugin
type RegisterPluginPayload struct {
	PluginID                  string                   `msgpack:"plugin_id" json:"plugin_id"`
	DisplayName               string                   `msgpack:"display_name" json:"display_name"`
	PluginVersion             string                   `msgpack:"plugin_version" json:"plugin_version"`
	PluginType                string                   `msgpack:"plugin_type" json:"plugin_type"`
	Components                []ComponentDeclaration   `msgpack:"components" json:"components"`
	LLMProviders              []LLMProviderDeclaration `msgpack:"llm_providers" json:"llm_providers"`
	CapabilitiesRequired      []string                 `msgpack:"capabilities_required" json:"capabilities_required"`
	Dependencies              []string                 `msgpack:"dependencies" json:"dependencies"`
	ConfigReloadSubscriptions []string                 `msgpack:"config_reload_subscriptions" json:"config_reload_subscriptions"`
	DefaultConfig             map[string]any           `msgpack:"default_config" json:"default_config"`
	ConfigSchema              map[string]any           `msgpack:"config_schema" json:"config_schema"`
	ConfigError               string                   `msgpack:"config_error" json:"config_error"`
	DesktopPetButtons         []DesktopPetButton       `msgpack:"desktop_pet_buttons" json:"desktop_pet_buttons"`
	WebUI                     map[string]any           `msgpack:"webui" json:"webui"`
}

// MessageGatewayStateUpdatePayload 消息网关运行时状态更新，对齐 protocol.envelope.MessageGatewayStateUpdatePayload
type MessageGatewayStateUpdatePayload struct {
	GatewayName string         `msgpack:"gateway_name" json:"gateway_name"`
	Ready       bool           `msgpack:"ready" json:"ready"`
	Platform    string         `msgpack:"platform" json:"platform"`
	AccountID   string         `msgpack:"account_id" json:"account_id"`
	Scope       string         `msgpack:"scope" json:"scope"`
	Metadata    map[string]any `msgpack:"metadata" json:"metadata"`
}

// RouteMessagePayload 消息网关入站消息，对齐 protocol.envelope.RouteMessagePayload
type RouteMessagePayload struct {
	GatewayName       string         `msgpack:"gateway_name" json:"gateway_name"`
	Message           map[string]any `msgpack:"message" json:"message"`
	RouteMetadata     map[string]any `msgpack:"route_metadata" json:"route_metadata"`
	ExternalMessageID string         `msgpack:"external_message_id" json:"external_message_id"`
	DedupeKey         string         `msgpack:"dedupe_key" json:"dedupe_key"`
}

// DesktopPetButton 桌宠按钮声明，对齐 plugin_runtime.desktop_pet_buttons.DesktopPetButton
type DesktopPetButton struct {
	ID    string `msgpack:"id" json:"id"`
	Label string `msgpack:"label" json:"label"`
	Icon  string `msgpack:"icon" json:"icon"`
	Order int    `msgpack:"order" json:"order"`
}

// DesktopPetButtonState 单个按钮的运行态，对齐 DesktopPetButtonState
type DesktopPetButtonState struct {
	Visible  bool   `msgpack:"visible" json:"visible"`
	Disabled bool   `msgpack:"disabled" json:"disabled"`
	Text     string `msgpack:"text" json:"text"`
}

// DesktopPetButtonSnapshot 桌宠按钮快照，对齐 DesktopPetButtonSnapshot
type DesktopPetButtonSnapshot struct {
	Buttons []DesktopPetButton               `msgpack:"buttons" json:"buttons"`
	States  map[string]DesktopPetButtonState `msgpack:"states" json:"states"`
}

// RunnerReadyPayload runner.ready
type RunnerReadyPayload struct {
	LoadedPlugins             []string          `msgpack:"loaded_plugins" json:"loaded_plugins"`
	FailedPlugins             []string          `msgpack:"failed_plugins" json:"failed_plugins"`
	FailedPluginReasons       map[string]string `msgpack:"failed_plugin_reasons" json:"failed_plugin_reasons"`
	InactivePlugins           []string          `msgpack:"inactive_plugins" json:"inactive_plugins"`
	ExplicitlyDisabledPlugins []string          `msgpack:"explicitly_disabled_plugins" json:"explicitly_disabled_plugins"`
}

// RunnerStartupProgressPayload runner.startup_progress 广播：本轮已完成结果和仍在等待的插件
type RunnerStartupProgressPayload struct {
	LoadedPlugins             []string          `msgpack:"loaded_plugins" json:"loaded_plugins"`
	FailedPlugins             []string          `msgpack:"failed_plugins" json:"failed_plugins"`
	FailedPluginReasons       map[string]string `msgpack:"failed_plugin_reasons" json:"failed_plugin_reasons"`
	InactivePlugins           []string          `msgpack:"inactive_plugins" json:"inactive_plugins"`
	ExplicitlyDisabledPlugins []string          `msgpack:"explicitly_disabled_plugins" json:"explicitly_disabled_plugins"`
	LoadingPlugins            []string          `msgpack:"loading_plugins" json:"loading_plugins"`
}

// ReadyPayload 转换成本轮完成结果
func (p RunnerStartupProgressPayload) ReadyPayload() RunnerReadyPayload {
	return RunnerReadyPayload{
		LoadedPlugins:             p.LoadedPlugins,
		FailedPlugins:             p.FailedPlugins,
		FailedPluginReasons:       p.FailedPluginReasons,
		InactivePlugins:           p.InactivePlugins,
		ExplicitlyDisabledPlugins: p.ExplicitlyDisabledPlugins,
	}
}

// UnregisterPluginPayload plugin.unregister 请求，对齐 protocol.envelope.UnregisterPluginPayload
type UnregisterPluginPayload struct {
	PluginID string `msgpack:"plugin_id" json:"plugin_id"`
	Reason   string `msgpack:"reason" json:"reason"`
}

// RunnerLogEntry Runner 日志条目，对齐 Python protocol.envelope.LogEntry
// 字段与 Python wire 完全一致：timestamp_ms/level 为整数，异常文本单独承载
type RunnerLogEntry struct {
	TimestampMS   int64   `msgpack:"timestamp_ms" json:"timestamp_ms"`
	Level         int     `msgpack:"level" json:"level"`
	LoggerName    string  `msgpack:"logger_name" json:"logger_name"`
	Message       string  `msgpack:"message" json:"message"`
	ExceptionText string  `msgpack:"exception_text" json:"exception_text"`
	LogColorInHex *string `msgpack:"log_color_in_hex" json:"log_color_in_hex"`
}

// LogBatchPayload runner.log_batch 广播负载
type LogBatchPayload struct {
	Entries []RunnerLogEntry `msgpack:"entries" json:"entries"`
}
