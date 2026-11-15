package host

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/plugin_runtime/hooks"
	"MaiGo/internal/plugin_runtime/protocol"
)

// EmitHook 触发命名 Hook，对齐 integration.invoke_hook
func (h *Host) EmitHook(ctx context.Context, hookName string, kwargs map[string]any) hooks.DispatchResult {
	if h == nil || h.hooks == nil {
		if kwargs == nil {
			kwargs = map[string]any{}
		}
		return hooks.DispatchResult{HookName: hookName, Kwargs: kwargs}
	}
	return h.hooks.EmitFiltered(ctx, h, hookName, kwargs, h.hookSubscriptionEnabled)
}

// hookSubscriptionEnabled 按统一组件索引过滤已被禁用的 HookHandler
func (h *Host) hookSubscriptionEnabled(sub hooks.Subscription) bool {
	if h == nil || h.components == nil {
		return true
	}
	fullName := hookHandlerFullName(sub)
	if fullName == "" {
		return true
	}
	enabled, found := h.components.ComponentEnabled(fullName, "")
	return !found || enabled
}

// HasActiveRunner 是否存在已握手的 Runner 连接
func (h *Host) HasActiveRunner() bool {
	return h.activeRPCServer() != nil
}

// InvokeHookHandler 经 plugin.invoke_hook 调用 Runner 内 HOOK_HANDLER
func (h *Host) InvokeHookHandler(ctx context.Context, sub hooks.Subscription, hookName string, kwargs map[string]any) (hooks.HandlerResult, error) {
	rpc := h.activeRPCServer()
	if rpc == nil {
		return hooks.HandlerResult{}, fmt.Errorf("Runner 未连接")
	}
	args := map[string]any{"hook_name": hookName}
	for k, v := range kwargs {
		args[k] = v
	}
	payload := map[string]any{
		"component_name": sub.HandlerName,
		"args":           args,
	}
	timeoutMS := h.resolveHookHandlerTimeoutMS(sub, hookName)
	resp, err := rpc.SendRequest(ctx, protocol.MethodPluginInvokeHook, sub.PluginID, payload, timeoutMS)
	if err != nil {
		if strings.Contains(err.Error(), "超时") {
			return hooks.HandlerResult{
				Success:      false,
				ErrorMessage: fmt.Sprintf("HookHandler %s 执行超时，已超过 %dms", hookHandlerFullName(sub), timeoutMS),
			}, nil
		}
		return hooks.HandlerResult{}, err
	}
	if resp.Error != nil {
		code, _ := resp.Error["message"].(string)
		if code == "" {
			code, _ = resp.Error["code"].(string)
		}
		if code == "" {
			code = "RPC 错误"
		}
		return hooks.HandlerResult{Success: false, ErrorMessage: code}, nil
	}
	return parseHookHandlerPayload(resp.Payload), nil
}

// resolveHookHandlerTimeoutMS 对齐 hook_dispatcher 的超时优先级：处理器 > Hook 默认 > 全局 60s
func (h *Host) resolveHookHandlerTimeoutMS(sub hooks.Subscription, hookName string) int64 {
	if sub.TimeoutMS > 0 {
		return sub.TimeoutMS
	}
	if spec, ok := h.hookSpecs.GetHookSpec(hookName); ok && spec.DefaultTimeoutMS > 0 {
		return int64(spec.DefaultTimeoutMS)
	}
	return h.HookBlockingTimeoutMS()
}

// hookHandlerFullName 返回日志与错误文案中的处理器全名
func hookHandlerFullName(sub hooks.Subscription) string {
	if sub.PluginID != "" && sub.HandlerName != "" {
		return sub.PluginID + "." + sub.HandlerName
	}
	if sub.HandlerName != "" {
		return sub.HandlerName
	}
	return sub.PluginID
}

func (h *Host) activeRPCServer() *RPCServer {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, state := range h.runners {
		if state != nil && state.rpc != nil && state.rpc.HasActiveConnection() {
			return state.rpc
		}
	}
	return nil
}

func parseHookHandlerPayload(payload map[string]any) hooks.HandlerResult {
	if payload == nil {
		return hooks.HandlerResult{Success: true, Action: "continue"}
	}
	success := true
	if v, ok := payload["success"].(bool); ok {
		success = v
	}
	action := "continue"
	if v, ok := payload["action"].(string); ok {
		action = strings.TrimSpace(v)
	}
	var modified map[string]any
	if raw, ok := payload["modified_kwargs"].(map[string]any); ok && raw != nil {
		modified = raw
	}
	errMsg, _ := payload["error_message"].(string)
	return hooks.HandlerResult{
		Success:        success,
		Action:         action,
		ModifiedKwargs: modified,
		CustomResult:   payload["custom_result"],
		ErrorMessage:   errMsg,
	}
}
