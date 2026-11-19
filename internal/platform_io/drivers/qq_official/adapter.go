package qq_official

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
)

// Adapter 进程内 QQ 官方适配器
type Adapter struct {
	platformio.BaseAdapter
	mu        sync.Mutex
	desc      platformio.DriverDescriptor
	settings  Settings
	runtime   *RuntimeState
	hostHooks *HostHooks
	client    *Client
	transport *Transport
	identity  *IdentityBridge
	started   bool
	dialed    bool
	cancel    context.CancelFunc
	sendMu    sync.Mutex
}

// NewAdapter 构造适配器；enabled=false 时 Start 不拨号
func NewAdapter(settings Settings, hooks *HostHooks) *Adapter {
	desc := platformio.BuildNativeAdapterDescriptor(
		PluginID,
		GatewayName,
		PlatformQQ,
		"",
		"",
		map[string]any{
			"route_type": RouteTypeDuplex,
			"protocol":   ProtocolOfficial,
			"connection": ProtocolOfficial,
		},
	)
	var invoker LocalAPIInvoker
	if hooks != nil {
		invoker = hooks.InvokeAPI
	}
	return &Adapter{
		BaseAdapter: platformio.NewBaseAdapter(desc),
		desc:        desc,
		settings:    settings,
		runtime:     NewRuntimeState(),
		hostHooks:   hooks,
		identity:    NewIdentityBridge(invoker),
	}
}

// Descriptor 返回可变驱动描述
func (a *Adapter) Descriptor() platformio.DriverDescriptor {
	if a == nil {
		return platformio.DriverDescriptor{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.desc
}

// Start 仅在 enabled=true 时启动 WS
func (a *Adapter) Start(ctx context.Context) error {
	if a == nil {
		return fmt.Errorf("qq official adapter 为空")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return nil
	}
	if !a.settings.Plugin.Enabled {
		return nil
	}
	if strings.TrimSpace(a.settings.QQOfficial.AppID) == "" || strings.TrimSpace(a.settings.QQOfficial.AppSecret) == "" {
		zap.L().Error("QQ 官方适配器缺少凭据，请在插件配置 [qq_official] 中填写 app_id 和 app_secret")
		return nil
	}
	client := NewClient(
		a.settings.QQOfficial.APIBaseURL,
		a.settings.QQOfficial.AppID,
		a.settings.QQOfficial.AppSecret,
	)
	runCtx, cancel := context.WithCancel(context.Background())
	transport := NewTransport(
		client,
		a.settings.Advanced.ReconnectDelaySec,
		a.onPayload,
		a.onReady,
		a.onUnready,
	)
	a.client = client
	a.transport = transport
	a.cancel = cancel
	a.started = true
	a.dialed = true
	if a.identity != nil {
		a.identity.Start(runCtx, a.settings, func() bool {
			return a.runtime != nil && a.runtime.Ready()
		})
	}
	return transport.Start(runCtx)
}

// Stop 停止 WS 与 identity 循环
func (a *Adapter) Stop(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.identity != nil {
		a.identity.Stop()
	}
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	if a.transport != nil {
		_ = a.transport.Stop(ctx)
		a.transport = nil
	}
	if a.client != nil {
		a.client.Close()
		a.client = nil
	}
	if a.runtime != nil {
		a.runtime.MarkNotReady()
	}
	if a.hostHooks != nil && a.hostHooks.ReportGatewayState != nil {
		_ = a.hostHooks.ReportGatewayState(ctx, a.runtime)
	}
	a.started = false
	return nil
}

// Send 独立出站，不走 PluginPlatformDriver
func (a *Adapter) Send(ctx context.Context, outbound platformio.OutboundMessageEnvelope, routeKey platformio.RouteKey) (platformio.DeliveryReceipt, error) {
	receipt := platformio.DeliveryReceipt{
		InternalMessageID: outbound.InternalMessageID,
		RouteKey:          routeKey,
		Status:            platformio.DeliveryStatusPending,
		DriverID:          a.Descriptor().DriverID,
		DriverKind:        platformio.DriverKindPlugin,
	}
	if a == nil {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "适配器未启动"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.mu.Lock()
	client := a.client
	ready := a.runtime != nil && a.runtime.Ready()
	routeAccount := ""
	if a.runtime != nil {
		routeAccount = a.runtime.RouteAccountID()
	}
	a.mu.Unlock()
	if client == nil || !ready {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "QQ 官方消息网关未就绪"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	if routeKey.Platform != "" && routeKey.Platform != PlatformQQ {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "出站消息的路由身份与 QQ 官方网关不一致"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	if routeKey.AccountID != "" && routeAccount != "" && routeKey.AccountID != routeAccount {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "出站消息的路由身份与 QQ 官方网关不一致"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	msg := outboundEnvelopeToMap(outbound)
	scene, target, text, err := EncodeOutboundText(msg)
	if err != nil {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = err.Error()
		return receipt, err
	}
	fields := map[string]any{}
	if outbound.ExternalReplyToMessageID != "" {
		fields["msg_id"] = outbound.ExternalReplyToMessageID
	}
	result, err := client.SendText(ctx, scene, target, text, fields)
	if err != nil {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = err.Error()
		return receipt, err
	}
	id := asString(result["id"])
	if id == "" {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "QQ 发送失败: 响应缺少消息 ID"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	receipt.Status = platformio.DeliveryStatusSent
	receipt.ExternalMessageID = id
	return receipt, nil
}

// Dialed 是否已进入拨号（enabled=false 时为 false）
func (a *Adapter) Dialed() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.transport != nil && a.transport.Dialed() {
		return true
	}
	return a.dialed && a.settings.Plugin.Enabled
}

func (a *Adapter) Runtime() *RuntimeState {
	if a == nil {
		return nil
	}
	return a.runtime
}

func (a *Adapter) Settings() Settings {
	if a == nil {
		return DefaultSettings()
	}
	return a.settings
}

func (a *Adapter) onReady(ctx context.Context, accountID, botName, sessionID string) error {
	_ = sessionID
	a.mu.Lock()
	unified := a.settings.QQOfficial.UnifiedAccountID
	if accountID == "" && a.runtime != nil {
		accountID = a.runtime.AccountID()
	}
	if botName == "" && a.runtime != nil {
		botName = a.runtime.BotName()
	}
	a.runtime.MarkReady(accountID, botName, unified)
	routeAccount := unified
	if routeAccount == "" {
		routeAccount = accountID
	}
	a.desc = platformio.BuildNativeAdapterDescriptor(
		PluginID, GatewayName, PlatformQQ, routeAccount, "",
		map[string]any{
			"route_type": RouteTypeDuplex,
			"protocol":   ProtocolOfficial,
			"connection": ProtocolOfficial,
		},
	)
	identity := a.identity
	settings := a.settings
	hooks := a.hostHooks
	adapter := a
	a.mu.Unlock()

	if settings.QQOfficial.SnowlumaIdentityOnly && identity != nil {
		if err := identity.Renew(ctx, settings, true, true); err != nil {
			zap.L().Warn("ready 前 SL 身份查询续期失败，降级开放路由", zap.Error(err))
		}
	}
	if hooks != nil && hooks.RegisterDriver != nil {
		if err := hooks.RegisterDriver(ctx, adapter); err != nil {
			return err
		}
	}
	if hooks != nil && hooks.ReportGatewayState != nil {
		return hooks.ReportGatewayState(ctx, adapter.runtime)
	}
	return nil
}

func (a *Adapter) onUnready(ctx context.Context) error {
	if a.runtime != nil {
		a.runtime.MarkNotReady()
	}
	if a.hostHooks != nil && a.hostHooks.ReportGatewayState != nil {
		return a.hostHooks.ReportGatewayState(ctx, a.runtime)
	}
	return nil
}

func (a *Adapter) onPayload(ctx context.Context, op int, event string, data map[string]any, seq *int) error {
	_ = op
	_ = seq
	if a == nil || !isSupportedEvent(event) || data == nil {
		return nil
	}
	if !a.runtime.InboundReady() {
		return nil
	}
	if a.settings.Filter.IgnoreOtherBots && IsBotAuthor(data) {
		return nil
	}
	accountID := a.runtime.AccountID()
	botName := a.runtime.BotName()
	unified := a.settings.QQOfficial.UnifiedAccountID
	// P0：附件下载未接，仅文本；有附件时以标签降级
	attachmentSegments := make([]map[string]any, 0)
	attachmentLabels := make([]string, 0)
	if attachments, ok := data["attachments"].([]any); ok && len(attachments) > 0 {
		attachmentSegments = append(attachmentSegments, map[string]any{"type": "text", "data": "[attachment]"})
		attachmentLabels = append(attachmentLabels, "[attachment]")
	}
	message, _, _, err := DecodeMessage(event, data, accountID, botName, unified, "", attachmentSegments, attachmentLabels)
	if err != nil {
		zap.L().Info("忽略 QQ 官方消息", zap.Error(err))
		return nil
	}
	var sink InboundSink
	if a.hostHooks != nil {
		sink = a.hostHooks.Inbound
	}
	return deliverInbound(ctx, sink, a.runtime.RouteAccountID(), message)
}

func outboundEnvelopeToMap(outbound platformio.OutboundMessageEnvelope) map[string]any {
	info := map[string]any{
		"user_info": map[string]any{
			"user_id":       outbound.MessageInfo.UserInfo.UserID,
			"user_nickname": outbound.MessageInfo.UserInfo.UserNickname,
		},
	}
	if outbound.MessageInfo.GroupInfo != nil {
		info["group_info"] = map[string]any{
			"group_id":   outbound.MessageInfo.GroupInfo.GroupID,
			"group_name": outbound.MessageInfo.GroupInfo.GroupName,
		}
	}
	if outbound.MessageInfo.AdditionalConfig != nil {
		info["additional_config"] = outbound.MessageInfo.AdditionalConfig
	} else {
		info["additional_config"] = map[string]any{}
	}
	raw := make([]any, 0, len(outbound.Segments))
	for _, seg := range outbound.Segments {
		switch seg.Kind {
		case platformio.SegmentText:
			raw = append(raw, map[string]any{"type": "text", "data": seg.Text})
		case platformio.SegmentAt:
			data := seg.Data
			if data == nil {
				data = map[string]any{
					"target_user_id":       seg.AtUserID,
					"target_user_nickname": seg.Text,
				}
			}
			raw = append(raw, map[string]any{"type": "at", "data": data})
		case platformio.SegmentImage, platformio.SegmentEmoji:
			raw = append(raw, map[string]any{"type": "image", "data": seg.Data})
		case platformio.SegmentVoice, platformio.SegmentRecord:
			raw = append(raw, map[string]any{"type": "voice", "data": seg.Data})
		case platformio.SegmentReply:
			raw = append(raw, map[string]any{"type": "reply", "data": seg.Data})
		default:
			if seg.Text != "" {
				raw = append(raw, map[string]any{"type": "text", "data": seg.Text})
			}
		}
	}
	if len(raw) == 0 && outbound.PlainText != "" {
		raw = append(raw, map[string]any{"type": "text", "data": outbound.PlainText})
	}
	return map[string]any{
		"message_info": info,
		"raw_message":  raw,
		"reply_to":     outbound.ExternalReplyToMessageID,
	}
}
