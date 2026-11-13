package host

import (
	"context"
	"fmt"
	"strings"

	"maibot-go/internal/plugin_runtime/protocol"
)

// Host→Runner 调用方法名，对齐 MaiBot supervisor.py 的 send_request 调用
const (
	methodPluginInvokeAPI            = "plugin.invoke_api"
	methodPluginInvokeReplyExtension = "plugin.invoke_reply_extension"
	methodPluginInvokeMessageGateway = "plugin.invoke_message_gateway"
	methodPluginInvokeLLMProvider    = "plugin.invoke_llm_provider"
	methodPluginConfigWidgets        = "plugin.config_widgets"
)

// ReloadPluginResult 对齐 protocol.envelope.ReloadPluginResultPayload
type ReloadPluginResult struct {
	Success                   bool              `json:"success"`
	RequestedPluginID         string            `json:"requested_plugin_id"`
	ReloadedPlugins           []string          `json:"reloaded_plugins"`
	UnloadedPlugins           []string          `json:"unloaded_plugins"`
	InactivePlugins           []string          `json:"inactive_plugins"`
	ExplicitlyDisabledPlugins []string          `json:"explicitly_disabled_plugins"`
	FailedPlugins             map[string]string `json:"failed_plugins"`
}

// InspectPluginConfigResult 对齐 protocol.envelope.InspectPluginConfigResultPayload
type InspectPluginConfigResult struct {
	Success          bool           `json:"success"`
	ConfigError      string         `json:"config_error"`
	DefaultConfig    map[string]any `json:"default_config"`
	ConfigSchema     map[string]any `json:"config_schema"`
	NormalizedConfig map[string]any `json:"normalized_config"`
	Changed          bool           `json:"changed"`
	Enabled          bool           `json:"enabled"`
}

// RunnerHealth 对齐 protocol.envelope.HealthPayload
type RunnerHealth struct {
	Healthy       bool     `json:"healthy"`
	LoadedPlugins []string `json:"loaded_plugins"`
	UptimeMS      int64    `json:"uptime_ms"`
}

// InvokePlugin 调用 Runner 内的插件组件（plugin.invoke_api / invoke_message_gateway 等）
func (h *Host) InvokePlugin(ctx context.Context, method, pluginID, componentName string, args map[string]any, timeoutMS int64) (protocol.Envelope, error) {
	rpc := h.activeRPCServer()
	if rpc == nil {
		return protocol.Envelope{}, fmt.Errorf("Runner 未连接")
	}
	if args == nil {
		args = map[string]any{}
	}
	return rpc.SendRequest(ctx, method, strings.TrimSpace(pluginID), map[string]any{
		"component_name": strings.TrimSpace(componentName),
		"args":           args,
	}, timeoutMS)
}

// InvokeAPI 调用插件声明的 API 方法
func (h *Host) InvokeAPI(ctx context.Context, pluginID, componentName string, args map[string]any, timeoutMS int64) (protocol.Envelope, error) {
	return h.InvokePlugin(ctx, methodPluginInvokeAPI, pluginID, componentName, args, timeoutMS)
}

// InvokeMessageGateway 调用插件声明的消息网关方法
func (h *Host) InvokeMessageGateway(ctx context.Context, pluginID, componentName string, args map[string]any, timeoutMS int64) (protocol.Envelope, error) {
	return h.InvokePlugin(ctx, methodPluginInvokeMessageGateway, pluginID, componentName, args, timeoutMS)
}

// InvokeLLMProvider 调用插件声明的 LLM Provider
func (h *Host) InvokeLLMProvider(ctx context.Context, pluginID, clientType, operation string, request map[string]any, timeoutMS int64) (protocol.Envelope, error) {
	rpc := h.activeRPCServer()
	if rpc == nil {
		return protocol.Envelope{}, fmt.Errorf("Runner 未连接")
	}
	if request == nil {
		request = map[string]any{}
	}
	return rpc.SendRequest(ctx, methodPluginInvokeLLMProvider, strings.TrimSpace(pluginID), map[string]any{
		"client_type": strings.TrimSpace(clientType),
		"operation":   strings.TrimSpace(operation),
		"request":     request,
	}, timeoutMS)
}

// ReloadPlugin 按插件 ID 触发精确重载，对齐 supervisor.reload_plugin
func (h *Host) ReloadPlugin(ctx context.Context, pluginID, reason string, externalAvailablePlugins map[string]string) (ReloadPluginResult, error) {
	rpc := h.activeRPCServer()
	if rpc == nil {
		return ReloadPluginResult{}, fmt.Errorf("Runner 未连接")
	}
	if strings.TrimSpace(reason) == "" {
		reason = "manual"
	}
	external := map[string]any{}
	for key, value := range externalAvailablePlugins {
		external[key] = value
	}
	resp, err := rpc.SendRequest(ctx, protocol.MethodPluginReload, strings.TrimSpace(pluginID), map[string]any{
		"plugin_id":                  strings.TrimSpace(pluginID),
		"reason":                     reason,
		"external_available_plugins": external,
	}, 30000)
	if err != nil {
		return ReloadPluginResult{}, err
	}
	if resp.Error != nil {
		return ReloadPluginResult{}, fmt.Errorf("%s", envelopeErrorMessage(resp.Error, "插件重载失败"))
	}
	result := ReloadPluginResult{
		Success:                   payloadBool(resp.Payload, "success", false),
		RequestedPluginID:         payloadText(resp.Payload, "requested_plugin_id"),
		ReloadedPlugins:           payloadTexts(resp.Payload, "reloaded_plugins"),
		UnloadedPlugins:           payloadTexts(resp.Payload, "unloaded_plugins"),
		InactivePlugins:           payloadTexts(resp.Payload, "inactive_plugins"),
		ExplicitlyDisabledPlugins: payloadTexts(resp.Payload, "explicitly_disabled_plugins"),
		FailedPlugins:             payloadTextMap(resp.Payload, "failed_plugins"),
	}
	return result, nil
}

// NotifyPluginConfigUpdated 向 Runner 推送插件配置更新，对齐 supervisor.notify_plugin_config_updated
func (h *Host) NotifyPluginConfigUpdated(ctx context.Context, pluginID string, configData map[string]any, configVersion, configScope string) (bool, error) {
	rpc := h.activeRPCServer()
	if rpc == nil {
		return false, fmt.Errorf("Runner 未连接")
	}
	scope := protocol.ConfigReloadScope(strings.TrimSpace(configScope))
	if scope == "" {
		scope = protocol.ConfigReloadScopeSelf
	}
	switch scope {
	case protocol.ConfigReloadScopeSelf, protocol.ConfigReloadScopeBot, protocol.ConfigReloadScopeModel:
	default:
		return false, fmt.Errorf("非法的 config_scope=%s", configScope)
	}
	if configData == nil {
		configData = map[string]any{}
	}
	resp, err := rpc.SendRequest(ctx, protocol.MethodPluginConfigUpdated, strings.TrimSpace(pluginID), map[string]any{
		"plugin_id":      strings.TrimSpace(pluginID),
		"config_scope":   string(scope),
		"config_version": configVersion,
		"config_data":    configData,
	}, 10000)
	if err != nil {
		return false, err
	}
	if resp.Error != nil {
		return false, fmt.Errorf("%s", envelopeErrorMessage(resp.Error, "插件配置更新通知失败"))
	}
	return payloadBool(resp.Payload, "acknowledged", false), nil
}

// ValidatePluginConfig 请求 Runner 使用插件自身配置模型校验配置
func (h *Host) ValidatePluginConfig(ctx context.Context, pluginID string, configData map[string]any) (map[string]any, error) {
	rpc := h.activeRPCServer()
	if rpc == nil {
		return nil, fmt.Errorf("Runner 未连接")
	}
	if configData == nil {
		configData = map[string]any{}
	}
	resp, err := rpc.SendRequest(ctx, protocol.MethodPluginValidateConfig, strings.TrimSpace(pluginID), map[string]any{
		"config_data": configData,
	}, 10000)
	if err != nil {
		return nil, fmt.Errorf("插件配置校验请求失败: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("%s", envelopeErrorMessage(resp.Error, "插件配置校验失败"))
	}
	if !payloadBool(resp.Payload, "success", false) {
		return nil, fmt.Errorf("插件配置校验失败")
	}
	normalized, _ := resp.Payload["normalized_config"].(map[string]any)
	if normalized == nil {
		normalized = map[string]any{}
	}
	return normalized, nil
}

// ConfigWidgets 读取配置页控件或执行声明的按钮，不写入配置
func (h *Host) ConfigWidgets(ctx context.Context, pluginID, actionID string) (map[string]any, error) {
	rpc := h.activeRPCServer()
	if rpc == nil {
		return nil, fmt.Errorf("Runner 未连接")
	}
	payload := map[string]any{"action_id": nil}
	if strings.TrimSpace(actionID) != "" {
		payload["action_id"] = strings.TrimSpace(actionID)
	}
	resp, err := rpc.SendRequest(ctx, methodPluginConfigWidgets, strings.TrimSpace(pluginID), payload, 10000)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("%s", envelopeErrorMessage(resp.Error, "配置页运行时请求失败"))
	}
	if resp.Payload == nil {
		return map[string]any{}, nil
	}
	return resp.Payload, nil
}

// InspectPluginConfig 请求 Runner 解析插件配置元数据
func (h *Host) InspectPluginConfig(ctx context.Context, pluginID string, configData map[string]any, useProvidedConfig bool) (InspectPluginConfigResult, error) {
	rpc := h.activeRPCServer()
	if rpc == nil {
		return InspectPluginConfigResult{}, fmt.Errorf("Runner 未连接")
	}
	if configData == nil {
		configData = map[string]any{}
	}
	resp, err := rpc.SendRequest(ctx, protocol.MethodPluginInspectConfig, strings.TrimSpace(pluginID), map[string]any{
		"config_data":         configData,
		"use_provided_config": useProvidedConfig,
	}, 10000)
	if err != nil {
		return InspectPluginConfigResult{}, fmt.Errorf("插件配置解析请求失败: %w", err)
	}
	if resp.Error != nil {
		return InspectPluginConfigResult{}, fmt.Errorf("%s", envelopeErrorMessage(resp.Error, "插件配置解析失败"))
	}
	result := InspectPluginConfigResult{
		Success:          payloadBool(resp.Payload, "success", false),
		ConfigError:      payloadText(resp.Payload, "config_error"),
		DefaultConfig:    payloadMap(resp.Payload, "default_config"),
		ConfigSchema:     payloadMap(resp.Payload, "config_schema"),
		NormalizedConfig: payloadMap(resp.Payload, "normalized_config"),
		Changed:          payloadBool(resp.Payload, "changed", false),
		Enabled:          payloadBool(resp.Payload, "enabled", true),
	}
	if !result.Success {
		return result, fmt.Errorf("插件配置解析失败")
	}
	return result, nil
}

// RequestRunnerHealth 请求 Runner 健康状态，对齐 supervisor._request_runner_health
func (h *Host) RequestRunnerHealth(ctx context.Context, timeoutMS int64) (RunnerHealth, error) {
	rpc := h.activeRPCServer()
	if rpc == nil {
		return RunnerHealth{}, fmt.Errorf("Runner 未连接")
	}
	if timeoutMS <= 0 {
		timeoutMS = 30000
	}
	resp, err := rpc.SendRequest(ctx, protocol.MethodPluginHealth, "", nil, timeoutMS)
	if err != nil {
		return RunnerHealth{}, err
	}
	if resp.Error != nil {
		return RunnerHealth{}, fmt.Errorf("%s", envelopeErrorMessage(resp.Error, "Runner 健康检查失败"))
	}
	return RunnerHealth{
		Healthy:       payloadBool(resp.Payload, "healthy", false),
		LoadedPlugins: payloadTexts(resp.Payload, "loaded_plugins"),
		UptimeMS:      payloadInt64(resp.Payload, "uptime_ms"),
	}, nil
}

func envelopeErrorMessage(errPayload map[string]any, fallback string) string {
	if message, ok := errPayload["message"].(string); ok && strings.TrimSpace(message) != "" {
		return message
	}
	return fallback
}

func payloadBool(payload map[string]any, key string, fallback bool) bool {
	if value, ok := payload[key].(bool); ok {
		return value
	}
	return fallback
}

func payloadText(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func payloadTexts(payload map[string]any, key string) []string {
	switch value := payload[key].(type) {
	case []string:
		return append([]string(nil), value...)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func payloadTextMap(payload map[string]any, key string) map[string]string {
	out := map[string]string{}
	if raw, ok := payload[key].(map[string]any); ok {
		for k, v := range raw {
			if text, ok := v.(string); ok {
				out[k] = text
			}
		}
	}
	return out
}

func payloadMap(payload map[string]any, key string) map[string]any {
	if raw, ok := payload[key].(map[string]any); ok && raw != nil {
		return raw
	}
	return map[string]any{}
}

func payloadInt64(payload map[string]any, key string) int64 {
	switch value := payload[key].(type) {
	case int:
		return int64(value)
	case int8:
		return int64(value)
	case int16:
		return int64(value)
	case int32:
		return int64(value)
	case int64:
		return value
	case uint:
		return int64(value)
	case uint8:
		return int64(value)
	case uint16:
		return int64(value)
	case uint32:
		return int64(value)
	case uint64:
		return int64(value)
	case float32:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}
