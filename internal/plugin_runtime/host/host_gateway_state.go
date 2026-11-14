package host

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"maibot-go/internal/plugin_runtime/protocol"
)

// MessageGatewayRuntimeState 消息网关运行时连接状态，对齐 _MessageGatewayRuntimeState
type MessageGatewayRuntimeState struct {
	Ready     bool
	Platform  string
	AccountID string
	Scope     string
	Metadata  map[string]any
}

// GatewayRPCError 带 RPC 错误码的网关错误
type GatewayRPCError struct {
	Code    string
	Message string
}

func (e *GatewayRPCError) Error() string { return e.Message }

// MessageGatewayState 读取消息网关运行时状态
func (h *Host) MessageGatewayState(pluginID, gatewayName string) (MessageGatewayRuntimeState, bool) {
	if h == nil {
		return MessageGatewayRuntimeState{}, false
	}
	h.gatewayStateMu.RLock()
	defer h.gatewayStateMu.RUnlock()
	state, ok := h.gatewayStates[strings.TrimSpace(pluginID)][strings.TrimSpace(gatewayName)]
	return state, ok
}

// UpdateMessageGatewayState 应用消息网关运行时状态，对齐 _handle_update_message_gateway_state
func (h *Host) UpdateMessageGatewayState(pluginID string, payload protocol.MessageGatewayStateUpdatePayload) (MessageGatewayRuntimeState, map[string]any, error) {
	return h.UpdateMessageGatewayStateContext(context.Background(), pluginID, payload)
}

// RouteMessage 把消息网关入站消息注入 Host，对齐 _handle_route_message
// 返回 accepted 与本次使用的路由键；适配器名单拒绝或重复消息时 accepted 为 false
func (h *Host) RouteMessage(ctx context.Context, pluginID string, payload protocol.RouteMessagePayload) (bool, map[string]any, error) {
	pluginID = strings.TrimSpace(pluginID)
	entry := h.resolveMessageGateway(pluginID, payload.GatewayName)
	if entry == nil || !messageGatewaySupportsReceive(entry) {
		return false, nil, &GatewayRPCError{
			Code:    string(protocol.ErrorCodeMethodNotAllowed),
			Message: fmt.Sprintf("插件 %s 未声明可接收的消息网关 %s", pluginID, payload.GatewayName),
		}
	}
	state, ok := h.MessageGatewayState(pluginID, entry.Name)
	if !ok || !state.Ready {
		return false, nil, &GatewayRPCError{
			Code:    string(protocol.ErrorCodeMethodNotAllowed),
			Message: fmt.Sprintf("消息网关 %s 尚未就绪，不能注入外部消息", entry.FullName),
		}
	}
	if err := h.recordGatewayBotAccount(ctx, pluginID, entry, state, BotAccountSourceInbound); err != nil {
		zap.L().Warn("消息网关入站账号上报失败",
			zap.String("plugin_id", pluginID),
			zap.String("gateway", entry.Name),
			zap.Error(err))
	}
	message := cloneAnyMap(payload.Message)
	if message == nil {
		message = map[string]any{}
	}
	if state.Platform != "" {
		message["platform"] = state.Platform
	}
	if state.AccountID != "" {
		message["account_id"] = state.AccountID
	}
	if state.Scope != "" {
		message["scope"] = state.Scope
	}
	if payload.ExternalMessageID != "" {
		if _, exists := message["message_id"]; !exists {
			message["message_id"] = payload.ExternalMessageID
		}
	}
	if payload.DedupeKey != "" {
		if _, exists := message["dedupe_key"]; !exists {
			message["dedupe_key"] = payload.DedupeKey
		}
	}
	routeKey := map[string]any{
		"platform":   state.Platform,
		"account_id": state.AccountID,
		"scope":      state.Scope,
	}
	if port, ok := h.MessageGatewayPort().(PluginGatewayInboundPort); ok && port != nil {
		return port.ReceiveGatewayMessage(ctx, PluginGatewayInboundRequest{
			PluginID:          pluginID,
			GatewayName:       entry.Name,
			DriverID:          pluginGatewayAdapterID(pluginID, entry.Name),
			Platform:          state.Platform,
			AccountID:         state.AccountID,
			Scope:             state.Scope,
			ExternalMessageID: payload.ExternalMessageID,
			DedupeKey:         payload.DedupeKey,
			RouteMetadata:     payload.RouteMetadata,
		}, message)
	}
	if err := h.ReceiveExternalMessage(ctx, message); err != nil {
		return false, nil, err
	}
	return true, routeKey, nil
}

func (h *Host) resolveMessageGateway(pluginID, gatewayName string) *ComponentEntry {
	if h == nil || h.components == nil {
		return nil
	}
	name := strings.TrimSpace(gatewayName)
	if name != "" {
		return h.components.MessageGateway(strings.TrimSpace(pluginID), name, false, "")
	}
	gateways := h.components.MessageGateways(strings.TrimSpace(pluginID), "", "", false, "")
	if len(gateways) == 1 {
		return gateways[0]
	}
	return nil
}

func (h *Host) storeMessageGatewayState(pluginID, gatewayName string, state MessageGatewayRuntimeState) {
	h.gatewayStateMu.Lock()
	defer h.gatewayStateMu.Unlock()
	if h.gatewayStates == nil {
		h.gatewayStates = make(map[string]map[string]MessageGatewayRuntimeState)
	}
	pluginID = strings.TrimSpace(pluginID)
	states := h.gatewayStates[pluginID]
	if states == nil {
		states = make(map[string]MessageGatewayRuntimeState)
		h.gatewayStates[pluginID] = states
	}
	states[strings.TrimSpace(gatewayName)] = state
}

func (h *Host) removeMessageGatewayStates(pluginID string) {
	h.gatewayStateMu.Lock()
	delete(h.gatewayStates, strings.TrimSpace(pluginID))
	h.gatewayStateMu.Unlock()
}

func messageGatewaySupportsReceive(entry *ComponentEntry) bool {
	if entry == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(entry.RouteType)) {
	case "receive", "duplex":
		return true
	default:
		return false
	}
}
