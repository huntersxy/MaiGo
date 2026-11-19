package host

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/platform_io/drivers/qq_official"
	"MaiGo/internal/platform_io/drivers/snowluma"
	"MaiGo/internal/platform_io/drivers/telegram"
	"MaiGo/internal/platform_io/drivers/weixin_clawbot"
	"MaiGo/internal/plugin_runtime/protocol"
)

// EnsureNativeMessageGateways 为进程内适配器登记 MESSAGE_GATEWAY 组件声明
// Python Runner 被 block 后不会再 register_components；原生侧必须本地声明，否则
// UpdateMessageGatewayState / RouteMessage 会因「未声明消息网关」失败。
func (h *Host) EnsureNativeMessageGateways() {
	if h == nil || h.components == nil {
		return
	}
	h.ensureNativeMessageGateway(snowluma.PluginID, snowluma.GatewayName, snowluma.PlatformQQ, snowluma.RouteTypeDuplex, "", "SnowLuma / NapCat 正向 WebSocket 双工消息网关")
	h.ensureNativeMessageGateway(telegram.PluginID, telegram.GatewayName, telegram.PlatformTelegram, telegram.RouteTypeDuplex, telegram.ProtocolBotAPI, "Telegram Bot API 双向消息网关")
	h.ensureNativeMessageGateway(qq_official.PluginID, qq_official.GatewayName, qq_official.PlatformQQ, qq_official.RouteTypeDuplex, qq_official.ProtocolOfficial, "QQ 官方机器人群聊与单聊消息网关")
	for _, name := range weixin_clawbot.AllGatewayNames() {
		h.ensureNativeMessageGateway(weixin_clawbot.PluginID, name, weixin_clawbot.PlatformWeixin, weixin_clawbot.RouteTypeDuplex, weixin_clawbot.ProtocolILink, "微信 ClawBot iLink 双向消息网关")
	}
}

func (h *Host) ensureNativeMessageGateway(pluginID, gatewayName, platform, routeType, protocolName, description string) {
	pluginID = strings.TrimSpace(pluginID)
	gatewayName = strings.TrimSpace(gatewayName)
	if pluginID == "" || gatewayName == "" || h.components == nil {
		return
	}
	if existing := h.components.MessageGateway(pluginID, gatewayName, false, ""); existing != nil {
		return
	}
	metadata := map[string]any{
		"route_type":  routeType,
		"platform":    platform,
		"protocol":    protocolName,
		"description": description,
		"enabled":     true,
		"source":      "native_message_gateway",
	}
	entry := &ComponentEntry{
		Name:            gatewayName,
		FullName:        pluginID + "." + gatewayName,
		ComponentType:   ComponentTypeMessageGateway,
		PluginID:        pluginID,
		Metadata:        metadata,
		Enabled:         true,
		RouteType:       routeType,
		Platform:        platform,
		Protocol:        protocolName,
		disabledSession: make(map[string]bool),
	}
	h.components.RegisterLocalComponent(entry)
}

// applyNativeMessageGatewayState 原生适配器专用状态更新：
// 不调用 registerMessageGatewayDriverLocked（那条路径会装 PluginPlatformDriver 盖掉原生 Adapter）。
// ready=false 时只清状态，不 Unregister 驱动——原生驱动由 RegisterNativeGatewayDriver / NativeTeardown 管理。
func (h *Host) applyNativeMessageGatewayState(ctx context.Context, pluginID string, payload protocol.MessageGatewayStateUpdatePayload) (MessageGatewayRuntimeState, map[string]any, error) {
	if h == nil {
		return MessageGatewayRuntimeState{}, nil, fmt.Errorf("Host 未初始化")
	}
	h.EnsureNativeMessageGateways()
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
	if err := h.recordGatewayBotAccount(ctx, pluginID, entry, state, BotAccountSourceReady); err != nil {
		return MessageGatewayRuntimeState{}, nil, fmt.Errorf("上报插件消息网关账号 %s 失败: %w", entry.FullName, err)
	}
	h.storeMessageGatewayState(pluginID, entry.Name, state)
	return state, map[string]any{
		"platform":   state.Platform,
		"account_id": state.AccountID,
		"scope":      state.Scope,
	}, nil
}
