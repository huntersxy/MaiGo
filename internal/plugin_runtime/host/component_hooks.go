package host

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"maibot-go/internal/plugin_runtime/hooks"
	"maibot-go/internal/plugin_runtime/protocol"
)

// MaiBot 组件类型，对齐 host/component_registry.py ComponentTypes
const (
	ComponentTypeAction         = "ACTION"
	ComponentTypeCommand        = "COMMAND"
	ComponentTypeTool           = "TOOL"
	ComponentTypeEventHandler   = "EVENT_HANDLER"
	ComponentTypeHookHandler    = "HOOK_HANDLER"
	ComponentTypeMessageGateway = "MESSAGE_GATEWAY"
	ComponentTypeHomeCard       = "HOME_CARD"
	ComponentTypeReplyExtension = "REPLY_EXTENSION"
	ComponentTypeAPI            = "API"
)

// knownComponentTypes 全部合法组件类型
var knownComponentTypes = map[string]struct{}{
	ComponentTypeAction:         {},
	ComponentTypeCommand:        {},
	ComponentTypeTool:           {},
	ComponentTypeEventHandler:   {},
	ComponentTypeHookHandler:    {},
	ComponentTypeMessageGateway: {},
	ComponentTypeHomeCard:       {},
	ComponentTypeReplyExtension: {},
	ComponentTypeAPI:            {},
}

// normalizeComponentType 规范化组件类型，对齐 ComponentRegistry._normalize_component_type
func normalizeComponentType(raw string) (string, error) {
	componentType := strings.ToUpper(strings.TrimSpace(raw))
	if _, ok := knownComponentTypes[componentType]; !ok {
		return "", fmt.Errorf("组件类型 %s 不存在", raw)
	}
	return componentType, nil
}

// ApplyPluginComponents 应用 Runner 上报的组件声明，返回写入的 Hook 订阅数量
//
// 语义对齐 component_registry.register_plugin_components：先校验全部声明，任一不合法则整体失败且不改动
// 既有订阅；全部通过后整体替换该插件的 Hook 订阅、事件处理器与通用组件索引（重复注册不会残留旧条目）。
func (h *Host) ApplyPluginComponents(rec PluginRecord, comps []protocol.ComponentDeclaration) (int, error) {
	if h == nil || h.hooks == nil || h.components == nil {
		return 0, fmt.Errorf("Host 未初始化")
	}
	pluginID := strings.TrimSpace(rec.PluginID)
	if pluginID == "" {
		return 0, fmt.Errorf("plugin_id 不能为空")
	}
	if h.llmProviders != nil {
		if err := h.llmProviders.ValidatePluginProviders(pluginID, rec.LLMProviders); err != nil {
			return 0, err
		}
	}
	// 对齐 register_plugin_components：chat_scope / allowed_session 可来自组件元数据，
	// 规范化后不再残留在 metadata 中；API 注册仍使用原始声明
	normalized := normalizeComponentDeclarations(comps)
	subs, err := h.buildHookSubscriptions(rec, normalized)
	if err != nil {
		return 0, err
	}
	events, err := h.buildEventHandlers(rec, normalized)
	if err != nil {
		return 0, err
	}
	entries, err := buildAuxiliaryComponents(rec, normalized)
	if err != nil {
		return 0, err
	}
	if err := h.components.ValidatePluginComponents(pluginID, entries); err != nil {
		return 0, err
	}
	h.replaceEventHandlers(pluginID, events)
	h.components.ReplacePluginComponents(pluginID, entries)
	if h.apis != nil {
		h.apis.RemoveAPIsByPlugin(pluginID)
		h.apis.RegisterPluginAPIs(pluginID, comps)
	}
	h.authorization.RegisterPlugin(pluginID, rec.Capabilities)
	if h.llmProviders != nil {
		if err := h.llmProviders.ReplacePluginProviders(pluginID, rec.LLMProviders); err != nil {
			return 0, err
		}
	}
	return h.hooks.ReplacePlugin(pluginID, subs), nil
}

// RemovePluginComponents 移除某个插件的全部组件，返回移除数量，对齐 remove_components_by_plugin
//
// 统一索引已包含 EVENT_HANDLER / HOOK_HANDLER，移除数量以索引条目为准；执行层订阅单独清理，避免重复计数。
func (h *Host) RemovePluginComponents(pluginID string) int {
	if h == nil {
		return 0
	}
	if h.hooks != nil {
		h.hooks.RemovePlugin(pluginID)
	}
	h.removeEventHandlers(pluginID)
	removed := 0
	if h.components != nil {
		removed = h.components.RemoveComponentsByPlugin(pluginID)
	}
	// API 组件有独立注册表，与 Python remove_components_by_plugin 一致不计入组件移除数
	if h.apis != nil {
		h.apis.RemoveAPIsByPlugin(pluginID)
	}
	return removed
}

// UnregisterPlugin 注销插件：移除组件订阅与注册记录
//
// 返回被移除的 Hook 订阅数量与是否存在注册记录，对齐 supervisor._handle_unregister_plugin。
func (h *Host) UnregisterPlugin(pluginID string) (int, bool) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return 0, false
	}
	removed := h.RemovePluginComponents(pluginID)
	if h.llmProviders != nil {
		h.llmProviders.RemovePluginProviders(pluginID)
	}
	if err := h.UnregisterPluginGatewayDrivers(context.Background(), pluginID); err != nil {
		zap.L().Warn("注销插件消息网关驱动失败", zap.String("plugin_id", pluginID), zap.Error(err))
	}
	h.dropPluginRecoveryTarget(pluginID)
	if h.authorization != nil {
		h.authorization.Revoke(pluginID)
	}
	h.mu.Lock()
	_, existed := h.plugins[pluginID]
	delete(h.plugins, pluginID)
	h.mu.Unlock()
	h.notifyStatusChanges()
	return removed, existed
}

// normalizeComponentDeclarations 归一化组件声明的 chat_scope / allowed_session
//
// 对齐 ComponentRegistry.register_plugin_components：两个字段优先取声明顶层值，
// 缺失时回退 metadata 同名字段，并从 metadata 中移除。
func normalizeComponentDeclarations(comps []protocol.ComponentDeclaration) []protocol.ComponentDeclaration {
	if len(comps) == 0 {
		return comps
	}
	out := make([]protocol.ComponentDeclaration, len(comps))
	for index, comp := range comps {
		metadata := cloneComponentMetadata(comp.Metadata)
		if comp.ChatScope == "" {
			comp.ChatScope = metadataText(metadata, "chat_scope")
		}
		if comp.AllowedSession == nil {
			comp.AllowedSession = metadataStringSlice(metadata["allowed_session"])
		}
		delete(metadata, "chat_scope")
		delete(metadata, "allowed_session")
		comp.Metadata = metadata
		out[index] = comp
	}
	return out
}

func metadataStringSlice(raw any) []string {
	var items []any
	switch typed := raw.(type) {
	case []any:
		items = typed
	case []string:
		items = make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
	default:
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		var text string
		if item == nil {
			text = "None"
		} else {
			text = strings.TrimSpace(fmt.Sprint(item))
		}
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

// buildHookSubscriptions 校验并转换全部组件声明，只保留 HOOK_HANDLER 对应的 Hook 订阅
func (h *Host) buildHookSubscriptions(rec PluginRecord, comps []protocol.ComponentDeclaration) ([]hooks.Subscription, error) {
	sourceRank := 1
	if rec.RunnerGroup == RunnerGroupBuiltin {
		sourceRank = 0
	}
	subs := make([]hooks.Subscription, 0, len(comps))
	for _, comp := range comps {
		componentType, err := normalizeComponentType(comp.ComponentType)
		if err != nil {
			return nil, err
		}
		if componentType != ComponentTypeHookHandler {
			// 其余组件由 buildAuxiliaryComponents / buildEventHandlers 分别处理
			continue
		}
		sub, enabled, err := h.buildHookSubmission(rec, comp, sourceRank)
		if err != nil {
			return nil, err
		}
		if !enabled {
			continue
		}
		subs = append(subs, sub)
	}
	return subs, nil
}

// buildHookSubmission 解析并校验单个 HOOK_HANDLER 组件，enabled=false 表示组件被元数据禁用
func (h *Host) buildHookSubmission(rec PluginRecord, comp protocol.ComponentDeclaration, sourceRank int) (hooks.Subscription, bool, error) {
	pluginID := strings.TrimSpace(rec.PluginID)
	handlerName := strings.TrimSpace(comp.Name)
	fullName := pluginID + "." + handlerName
	metadata := comp.Metadata

	hookName := metadataText(metadata, "hook")
	if hookName == "" {
		return hooks.Subscription{}, false, fmt.Errorf("HookHandler 的 hook 名称不能为空")
	}
	mode, err := normalizeHookHandlerMode(metadata)
	if err != nil {
		return hooks.Subscription{}, false, err
	}
	order, err := normalizeHookHandlerOrder(metadata)
	if err != nil {
		return hooks.Subscription{}, false, err
	}
	errorPolicy, err := normalizeHookHandlerErrorPolicy(metadata)
	if err != nil {
		return hooks.Subscription{}, false, err
	}
	timeoutMS, err := normalizeHookHandlerTimeoutMS(metadata["timeout_ms"])
	if err != nil {
		return hooks.Subscription{}, false, err
	}

	spec, ok := h.hookSpecs.GetHookSpec(hookName)
	if !ok {
		return hooks.Subscription{}, false, fmt.Errorf("HookHandler %s 声明了未注册的 Hook: %s", fullName, hookName)
	}
	if mode == hooks.HandlerModeBlocking && !spec.AllowBlocking() {
		return hooks.Subscription{}, false,
			fmt.Errorf("HookHandler %s 不能注册为 blocking：Hook %s 不允许 blocking 处理器", fullName, hookName)
	}
	if mode == hooks.HandlerModeObserve && !spec.AllowObserve() {
		return hooks.Subscription{}, false,
			fmt.Errorf("HookHandler %s 不能注册为 observe：Hook %s 不允许 observe 处理器", fullName, hookName)
	}
	if errorPolicy == hooks.ErrorPolicyAbort && !spec.AllowAbort() {
		return hooks.Subscription{}, false,
			fmt.Errorf("HookHandler %s 不能使用 error_policy=abort：Hook %s 不允许 abort", fullName, hookName)
	}

	return hooks.Subscription{
		HookName:    hookName,
		PluginID:    pluginID,
		HandlerName: handlerName,
		Mode:        mode,
		Priority:    order,
		SourceRank:  sourceRank,
		ErrorPolicy: errorPolicy,
		TimeoutMS:   timeoutMS,
	}, metadataEnabled(metadata), nil
}

// normalizeHookHandlerTimeoutMS 解析处理器的 timeout_ms，文案对齐 MaiBot
func normalizeHookHandlerTimeoutMS(raw any) (int64, error) {
	timeoutMS, err := normalizeComponentTimeoutMS(raw)
	if err != nil {
		if strings.Contains(err.Error(), "negative") {
			return 0, fmt.Errorf("HookHandler 超时配置不能为负数: %v", raw)
		}
		return 0, fmt.Errorf("HookHandler 超时配置不合法: %v", raw)
	}
	return timeoutMS, nil
}

// normalizeHookHandlerMode 规范化 Hook 处理模式，空值按 MaiBot 默认 blocking 处理
func normalizeHookHandlerMode(metadata map[string]any) (hooks.HandlerMode, error) {
	text := strings.ToLower(metadataText(metadata, "mode"))
	if text == "" {
		return hooks.HandlerModeBlocking, nil
	}
	switch text {
	case string(hooks.HandlerModeBlocking):
		return hooks.HandlerModeBlocking, nil
	case string(hooks.HandlerModeObserve):
		return hooks.HandlerModeObserve, nil
	default:
		return "", fmt.Errorf("HookHandler 模式不合法: %v", metadata["mode"])
	}
}

// normalizeHookHandlerOrder 规范化 Hook 顺序槽位，空值按 MaiBot 默认 normal 处理
func normalizeHookHandlerOrder(metadata map[string]any) (hooks.HandlerPriority, error) {
	text := strings.ToLower(metadataText(metadata, "order"))
	if text == "" {
		return hooks.PriorityNormal, nil
	}
	switch text {
	case string(hooks.PriorityEarly):
		return hooks.PriorityEarly, nil
	case string(hooks.PriorityNormal):
		return hooks.PriorityNormal, nil
	case string(hooks.PriorityLate):
		return hooks.PriorityLate, nil
	default:
		return "", fmt.Errorf("HookHandler 顺序槽位不合法: %v", metadata["order"])
	}
}

// normalizeHookHandlerErrorPolicy 规范化异常处理策略，空值按 MaiBot 默认 skip 处理
func normalizeHookHandlerErrorPolicy(metadata map[string]any) (string, error) {
	text := strings.ToLower(metadataText(metadata, "error_policy"))
	if text == "" {
		return hooks.ErrorPolicySkip, nil
	}
	switch text {
	case hooks.ErrorPolicyAbort, hooks.ErrorPolicySkip, hooks.ErrorPolicyLog:
		return text, nil
	default:
		return "", fmt.Errorf("HookHandler 异常处理策略不合法: %v", metadata["error_policy"])
	}
}

// metadataText 读取元数据字符串字段并去空白，非字符串值按 fmt.Sprint 归一
func metadataText(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return ""
	}
	if text, ok := raw.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(raw))
}

// metadataEnabled 组件是否启用，对齐 ComponentEntry.enabled = metadata.get("enabled", True)
//
// 键缺失时默认启用；显式 null/0/空串/空集合按 Python 真值语义视为禁用。
func metadataEnabled(metadata map[string]any) bool {
	if metadata == nil {
		return true
	}
	if _, ok := metadata["enabled"]; !ok {
		return true
	}
	return metadataTruthy(metadata, "enabled")
}
