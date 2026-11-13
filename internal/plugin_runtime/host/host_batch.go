package host

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"maibot-go/internal/plugin_runtime/protocol"
)

// pluginBatchTimeoutMS 批量插件操作超时；MaiBot 使用 max(runner_spawn_timeout*1000, 10000)
const pluginBatchTimeoutMS int64 = 30000

// ReloadPluginsResult 对齐 protocol.envelope.ReloadPluginsResultPayload
type ReloadPluginsResult struct {
	Success                   bool              `json:"success"`
	RequestedPluginIDs        []string          `json:"requested_plugin_ids"`
	ReloadedPlugins           []string          `json:"reloaded_plugins"`
	UnloadedPlugins           []string          `json:"unloaded_plugins"`
	InactivePlugins           []string          `json:"inactive_plugins"`
	ExplicitlyDisabledPlugins []string          `json:"explicitly_disabled_plugins"`
	FailedPlugins             map[string]string `json:"failed_plugins"`
}

// UnloadPluginsResult 对齐 protocol.envelope.UnloadPluginsResultPayload
type UnloadPluginsResult struct {
	Success            bool              `json:"success"`
	RequestedPluginIDs []string          `json:"requested_plugin_ids"`
	UnloadedPlugins    []string          `json:"unloaded_plugins"`
	FailedPlugins      map[string]string `json:"failed_plugins"`
}

// ReloadPlugins 批量重载插件，对齐 supervisor.reload_plugins：
// 空列表表示当前已注册的全部插件；单插件直接走 plugin.reload。
func (h *Host) ReloadPlugins(ctx context.Context, pluginIDs []string, reason string, externalAvailablePlugins map[string]string) (ReloadPluginsResult, error) {
	ids := normalizePluginIDs(pluginIDs)
	if len(ids) == 0 {
		for _, rec := range h.ListPlugins() {
			ids = append(ids, rec.PluginID)
		}
		sort.Strings(ids)
	}
	if len(ids) == 0 {
		return ReloadPluginsResult{Success: true, RequestedPluginIDs: []string{}, FailedPlugins: map[string]string{}}, nil
	}
	if len(ids) == 1 {
		result, err := h.ReloadPlugin(ctx, ids[0], reason, externalAvailablePlugins)
		if err != nil {
			return ReloadPluginsResult{}, err
		}
		reloaded := ReloadPluginsResult{
			Success:                   result.Success,
			RequestedPluginIDs:        ids,
			ReloadedPlugins:           result.ReloadedPlugins,
			UnloadedPlugins:           result.UnloadedPlugins,
			InactivePlugins:           result.InactivePlugins,
			ExplicitlyDisabledPlugins: result.ExplicitlyDisabledPlugins,
			FailedPlugins:             result.FailedPlugins,
		}
		h.applyPluginRecoveryResult(reloaded)
		return reloaded, nil
	}

	resp, err := h.sendRunnerPayloadRequest(ctx, protocol.MethodPluginReloadBatch, ids, reason, externalAvailablePlugins, pluginBatchTimeoutMS)
	if err != nil {
		return ReloadPluginsResult{}, err
	}
	if resp.Error != nil {
		return ReloadPluginsResult{}, fmt.Errorf("%s", envelopeErrorMessage(resp.Error, "插件批量重载失败"))
	}
	reloaded := decodeReloadPluginsResult(resp.Payload, ids)
	// 对齐 Python supervisor.py:424：显式禁用与已重载插件不再属于恢复目标
	h.applyPluginRecoveryResult(reloaded)
	return reloaded, nil
}

// RecoverPlugins 请求 Runner 恢复指定插件，复刻 supervisor._recover_missing_plugins 的 plugin.recover 调用。
func (h *Host) RecoverPlugins(ctx context.Context, pluginIDs []string, reason string, externalAvailablePlugins map[string]string) (ReloadPluginsResult, error) {
	ids := normalizePluginIDs(pluginIDs)
	if len(ids) == 0 {
		return ReloadPluginsResult{Success: true, RequestedPluginIDs: []string{}, FailedPlugins: map[string]string{}}, nil
	}
	if strings.TrimSpace(reason) == "" {
		reason = "automatic_recovery"
	}
	resp, err := h.sendRunnerPayloadRequest(ctx, protocol.MethodPluginRecover, ids, reason, externalAvailablePlugins, pluginBatchTimeoutMS)
	if err != nil {
		return ReloadPluginsResult{}, err
	}
	if resp.Error != nil {
		return ReloadPluginsResult{}, &RunnerRPCError{
			Code:    payloadText(resp.Error, "code"),
			Message: envelopeErrorMessage(resp.Error, "插件恢复失败"),
		}
	}
	return decodeReloadPluginsResult(resp.Payload, ids), nil
}

// UnloadPlugins 批量卸载插件，对齐 supervisor.unload_plugins。
func (h *Host) UnloadPlugins(ctx context.Context, pluginIDs []string, reason string) (UnloadPluginsResult, error) {
	ids := normalizePluginIDs(pluginIDs)
	if strings.TrimSpace(reason) == "" {
		reason = "manual"
	}
	rpc := h.activeRPCServer()
	if rpc == nil {
		return UnloadPluginsResult{}, fmt.Errorf("Runner 未连接")
	}
	resp, err := rpc.SendRequest(ctx, protocol.MethodPluginUnloadBatch, "", map[string]any{
		"plugin_ids": ids,
		"reason":     reason,
	}, pluginBatchTimeoutMS)
	if err != nil {
		return UnloadPluginsResult{}, err
	}
	if resp.Error != nil {
		return UnloadPluginsResult{}, fmt.Errorf("%s", envelopeErrorMessage(resp.Error, "插件批量卸载失败"))
	}
	requested := payloadTexts(resp.Payload, "requested_plugin_ids")
	if len(requested) == 0 {
		requested = ids
	}
	unloaded := payloadTexts(resp.Payload, "unloaded_plugins")
	// 对齐 Python supervisor.py:443：已卸载插件不再属于恢复目标
	for _, pluginID := range unloaded {
		h.dropPluginRecoveryTarget(pluginID)
	}
	return UnloadPluginsResult{
		Success:            payloadBool(resp.Payload, "success", false),
		RequestedPluginIDs: requested,
		UnloadedPlugins:    unloaded,
		FailedPlugins:      payloadTextMap(resp.Payload, "failed_plugins"),
	}, nil
}

func (h *Host) sendRunnerPayloadRequest(ctx context.Context, method string, pluginIDs []string, reason string, externalAvailablePlugins map[string]string, timeoutMS int64) (protocol.Envelope, error) {
	rpc := h.activeRPCServer()
	if rpc == nil {
		return protocol.Envelope{}, fmt.Errorf("Runner 未连接")
	}
	if strings.TrimSpace(reason) == "" {
		reason = "manual"
	}
	external := map[string]any{}
	for key, value := range externalAvailablePlugins {
		external[key] = value
	}
	return rpc.SendRequest(ctx, method, "", map[string]any{
		"plugin_ids":                 append([]string(nil), pluginIDs...),
		"reason":                     reason,
		"external_available_plugins": external,
	}, timeoutMS)
}

func decodeReloadPluginsResult(payload map[string]any, fallbackIDs []string) ReloadPluginsResult {
	requested := payloadTexts(payload, "requested_plugin_ids")
	if len(requested) == 0 {
		requested = fallbackIDs
	}
	failed := payloadTextMap(payload, "failed_plugins")
	if failed == nil {
		failed = map[string]string{}
	}
	return ReloadPluginsResult{
		Success:                   payloadBool(payload, "success", false),
		RequestedPluginIDs:        requested,
		ReloadedPlugins:           payloadTexts(payload, "reloaded_plugins"),
		UnloadedPlugins:           payloadTexts(payload, "unloaded_plugins"),
		InactivePlugins:           payloadTexts(payload, "inactive_plugins"),
		ExplicitlyDisabledPlugins: payloadTexts(payload, "explicitly_disabled_plugins"),
		FailedPlugins:             failed,
	}
}

func normalizePluginIDs(pluginIDs []string) []string {
	seen := make(map[string]struct{}, len(pluginIDs))
	out := make([]string, 0, len(pluginIDs))
	for _, id := range pluginIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
