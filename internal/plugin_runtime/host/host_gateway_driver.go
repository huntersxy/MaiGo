package host

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"

	"maibot-go/internal/plugin_runtime/protocol"
)

// UpdateMessageGatewayStateContext 带上下文应用消息网关运行时状态
//
// ready 时先注册 Platform IO 驱动并绑定路由，注册失败不写入 ready 状态；
// not-ready 时先反注册驱动，再保存离线状态
func (h *Host) UpdateMessageGatewayStateContext(ctx context.Context, pluginID string, payload protocol.MessageGatewayStateUpdatePayload) (MessageGatewayRuntimeState, map[string]any, error) {
	if h == nil {
		return MessageGatewayRuntimeState{}, nil, fmt.Errorf("Host 未初始化")
	}
	h.gatewayOpsMu.Lock()
	defer h.gatewayOpsMu.Unlock()

	entry := h.resolveMessageGateway(pluginID, payload.GatewayName)
	if entry == nil {
		name := strings.TrimSpace(payload.GatewayName)
		if name == "" {
			name = "<auto>"
		}
		return MessageGatewayRuntimeState{}, nil, &GatewayRPCError{
			Code:    string(protocol.ErrorCodeMethodNotAllowed),
			Message: fmt.Sprintf("插件 %s 未声明消息网关 %s", strings.TrimSpace(pluginID), name),
		}
	}
	state := MessageGatewayRuntimeState{
		Metadata: cloneAnyMap(payload.Metadata),
	}
	if !payload.Ready {
		if err := h.unregisterMessageGatewayDriverLocked(ctx, pluginID, entry.Name); err != nil {
			return MessageGatewayRuntimeState{}, nil, err
		}
		state.Platform = firstNonEmpty(strings.TrimSpace(payload.Platform), entry.Platform)
		state.AccountID = firstNonEmpty(strings.TrimSpace(payload.AccountID), entry.AccountID)
		state.Scope = firstNonEmpty(strings.TrimSpace(payload.Scope), entry.Scope)
		h.storeMessageGatewayState(pluginID, entry.Name, state)
		return state, map[string]any{}, nil
	}
	platform := firstNonEmpty(strings.TrimSpace(payload.Platform), entry.Platform)
	if platform == "" {
		return MessageGatewayRuntimeState{}, nil, fmt.Errorf("消息网关 %s 未提供有效的平台名称", entry.FullName)
	}
	state.Ready = true
	state.Platform = platform
	state.AccountID = firstNonEmpty(strings.TrimSpace(payload.AccountID), entry.AccountID)
	state.Scope = firstNonEmpty(strings.TrimSpace(payload.Scope), entry.Scope)
	protocolName, err := resolveGatewayProtocol(entry, payload.Metadata)
	if err != nil {
		return MessageGatewayRuntimeState{}, nil, err
	}
	if err := h.recordGatewayBotAccount(ctx, pluginID, entry, state, BotAccountSourceReady); err != nil {
		return MessageGatewayRuntimeState{}, nil, fmt.Errorf("上报插件消息网关账号 %s 失败: %w", entry.FullName, err)
	}
	if err := h.registerMessageGatewayDriverLocked(ctx, pluginID, entry, state, protocolName); err != nil {
		return MessageGatewayRuntimeState{}, nil, err
	}
	h.storeMessageGatewayState(pluginID, entry.Name, state)
	return state, map[string]any{
		"platform":   state.Platform,
		"account_id": state.AccountID,
		"scope":      state.Scope,
	}, nil
}

func (h *Host) gatewayDriverPort() (PluginGatewayDriverPort, error) {
	port := h.MessageGatewayPort()
	if port == nil {
		return nil, fmt.Errorf("消息网关驱动端口未接线：主程序尚未注入 Platform IO")
	}
	driverPort, ok := port.(PluginGatewayDriverPort)
	if !ok || driverPort == nil {
		return nil, fmt.Errorf("消息网关驱动端口未接线：Platform IO 不支持插件消息网关注册")
	}
	return driverPort, nil
}

func (h *Host) pluginTypeForGateway(pluginID string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return strings.TrimSpace(h.plugins[pluginID].PluginType)
}

func (h *Host) registerMessageGatewayDriverLocked(ctx context.Context, pluginID string, entry *ComponentEntry, state MessageGatewayRuntimeState, protocolName string) error {
	driverPort, err := h.gatewayDriverPort()
	if err != nil {
		return err
	}
	routeType := strings.ToLower(strings.TrimSpace(entry.RouteType))
	spec := PluginGatewayDriverSpec{
		PluginID:        strings.TrimSpace(pluginID),
		GatewayName:     entry.Name,
		ComponentName:   entry.Name,
		Platform:        state.Platform,
		AccountID:       state.AccountID,
		Scope:           state.Scope,
		RouteType:       routeType,
		PluginType:      h.pluginTypeForGateway(pluginID),
		Protocol:        protocolName,
		SupportsSend:    routeType == "send" || routeType == "duplex",
		SupportsReceive: routeType == "receive" || routeType == "duplex",
		TimeoutMS:       entry.TimeoutMS,
		Metadata:        cloneAnyMap(entry.Metadata),
	}
	if err := driverPort.RegisterPluginGatewayDriver(ctx, spec); err != nil {
		return fmt.Errorf("注册插件消息网关 %s 失败: %w", entry.FullName, err)
	}
	return nil
}

// resolveGatewayProtocol 解析运行时 protocol，覆盖组件声明值
func resolveGatewayProtocol(entry *ComponentEntry, metadata map[string]any) (string, error) {
	protocolName := entry.Protocol
	if raw, exists := metadata["protocol"]; exists {
		text, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("消息网关运行时 protocol 必须是字符串")
		}
		protocolName = text
	}
	return protocolName, nil
}

func (h *Host) unregisterMessageGatewayDriverLocked(ctx context.Context, pluginID, gatewayName string) error {
	driverPort, err := h.gatewayDriverPort()
	if err != nil {
		return err
	}
	if err := driverPort.UnregisterPluginGatewayDriver(ctx, strings.TrimSpace(pluginID), strings.TrimSpace(gatewayName)); err != nil {
		return fmt.Errorf("注销插件消息网关 %s 失败: %w", gatewayName, err)
	}
	return nil
}

// UnregisterPluginGatewayDrivers 注销指定插件的全部消息网关驱动并清理状态
func (h *Host) UnregisterPluginGatewayDrivers(ctx context.Context, pluginID string) error {
	if h == nil {
		return nil
	}
	h.gatewayOpsMu.Lock()
	defer h.gatewayOpsMu.Unlock()
	return h.unregisterPluginGatewayDriversLocked(ctx, strings.TrimSpace(pluginID))
}

func (h *Host) unregisterPluginGatewayDriversLocked(ctx context.Context, pluginID string) error {
	if h == nil || pluginID == "" {
		return nil
	}
	h.gatewayStateMu.RLock()
	states := h.gatewayStates[pluginID]
	names := make([]string, 0, len(states))
	for name := range states {
		names = append(names, name)
	}
	h.gatewayStateMu.RUnlock()
	if len(names) == 0 {
		h.removeMessageGatewayStates(pluginID)
		return nil
	}
	sort.Strings(names)
	var errs []error
	for _, name := range names {
		if err := h.unregisterMessageGatewayDriverLocked(ctx, pluginID, name); err != nil {
			errs = append(errs, err)
		}
	}
	h.removeMessageGatewayStates(pluginID)
	return errors.Join(errs...)
}

// UnregisterAllPluginGatewayDrivers 注销全部插件的消息网关驱动，供 Host 关停使用
func (h *Host) UnregisterAllPluginGatewayDrivers(ctx context.Context) error {
	if h == nil {
		return nil
	}
	h.gatewayOpsMu.Lock()
	defer h.gatewayOpsMu.Unlock()
	h.gatewayStateMu.RLock()
	pluginIDs := make([]string, 0, len(h.gatewayStates))
	for pluginID := range h.gatewayStates {
		pluginIDs = append(pluginIDs, pluginID)
	}
	h.gatewayStateMu.RUnlock()
	sort.Strings(pluginIDs)
	var errs []error
	for _, pluginID := range pluginIDs {
		if err := h.unregisterPluginGatewayDriversLocked(ctx, pluginID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ResetPluginGatewayDrivers 在插件重载时清除旧驱动与状态
func (h *Host) ResetPluginGatewayDrivers(ctx context.Context, pluginID string) error {
	if err := h.UnregisterPluginGatewayDrivers(ctx, pluginID); err != nil {
		zap.L().Warn("插件重载前注销消息网关驱动失败",
			zap.String("plugin_id", pluginID), zap.Error(err))
		return err
	}
	return nil
}
