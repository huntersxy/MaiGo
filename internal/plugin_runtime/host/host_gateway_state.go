package host

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"MaiGo/internal/plugin_runtime/protocol"
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
	// 对齐 Python _build_inbound_route_key：消息只能选择所属网关已声明的归属，
	// 不允许通过入站消息创建新账号或改写平台/账号/作用域
	if err := validateGatewayInboundOwnership(message, state); err != nil {
		return false, nil, &GatewayRPCError{
			Code:    string(protocol.ErrorCodeBadPayload),
			Message: err.Error(),
		}
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

// validateGatewayInboundOwnership 校验入站消息归属与网关声明一致
//
// 对齐 Python _build_inbound_route_key：消息可以省略归属字段（由网关补齐），
// 但一旦声明就必须与网关的 platform/account_id/scope 完全相同。
func validateGatewayInboundOwnership(message map[string]any, state MessageGatewayRuntimeState) error {
	if message == nil {
		return nil
	}
	declared := func(value string) string { return strings.TrimSpace(value) }
	platform := declared(asText(message["platform"]))
	accountID := declared(asText(message["account_id"]))
	scope := declared(asText(message["scope"]))
	if info, ok := message["message_info"].(map[string]any); ok {
		if platform == "" {
			platform = declared(asText(info["platform"]))
		}
	}
	if config, ok := message["message_info"].(map[string]any); ok {
		if additional, ok := config["additional_config"].(map[string]any); ok {
			if accountID == "" {
				accountID = declared(firstGatewayText(additional, "platform_io_account_id", "account_id", "self_id", "bot_account"))
			}
			if scope == "" {
				scope = declared(firstGatewayText(additional, "platform_io_scope", "route_scope", "adapter_scope", "connection_id"))
			}
		}
	}
	if platform != "" && declared(state.Platform) != "" && !strings.EqualFold(platform, declared(state.Platform)) {
		return fmt.Errorf("消息归属未由网关声明: platform=%s declared=%s", platform, state.Platform)
	}
	if accountID != "" && declared(state.AccountID) != "" && accountID != declared(state.AccountID) {
		return fmt.Errorf("消息归属未由网关声明: account_id=%s declared=%s", accountID, state.AccountID)
	}
	if scope != "" && declared(state.Scope) != "" && scope != declared(state.Scope) {
		return fmt.Errorf("消息归属未由网关声明: scope=%s declared=%s", scope, state.Scope)
	}
	return nil
}

// asText 把任意值转成去空白的字符串
func asText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

// firstGatewayText 按优先级取第一个非空文本值
func firstGatewayText(mapping map[string]any, keys ...string) string {
	for _, key := range keys {
		if text := strings.TrimSpace(asText(mapping[key])); text != "" {
			return text
		}
	}
	return ""
}
