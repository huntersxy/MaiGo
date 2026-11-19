package snowluma

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
	"MaiGo/internal/plugin_runtime/capabilities"
	"MaiGo/internal/plugin_runtime/protocol"
)

// HostHooks 适配器与 Host 的交互面
type HostHooks struct {
	Inbound            InboundSink
	APIs               LocalAPIBinder
	ReportGatewayState func(ctx context.Context, state *RuntimeState) error
	RegisterDriver     func(ctx context.Context, adapter platformio.Adapter) error
}

// Adapter 进程内 SnowLuma/OneBot 适配器
type Adapter struct {
	platformio.BaseAdapter
	mu         sync.Mutex
	desc       platformio.DriverDescriptor
	settings   Settings
	runtime    *RuntimeState
	profile    *ProfileState
	transport  *Transport
	router     *Router
	hostHooks  *HostHooks
	started    bool
	cancel     context.CancelFunc
}

// NewAdapter 构造适配器；enabled=false 时 Start 不拨号
func NewAdapter(settings Settings, hooks *HostHooks) *Adapter {
	scope := settings.Client.ConnectionID
	desc := platformio.BuildNativeAdapterDescriptor(
		PluginID,
		GatewayName,
		PlatformQQ,
		"",
		scope,
		map[string]any{
			"route_type": RouteTypeDuplex,
			"protocol":   settings.Client.ClientType,
		},
	)
	runtime := NewRuntimeState()
	profile := NewProfileState(InitialProfileForConfig(settings.Client.ClientType))
	adapter := &Adapter{
		BaseAdapter: platformio.NewBaseAdapter(desc),
		desc:        desc,
		settings:    settings,
		runtime:     runtime,
		profile:     profile,
		hostHooks:   hooks,
	}
	var sink InboundSink
	if hooks != nil {
		sink = hooks.Inbound
	}
	adapter.router = NewRouter(settings, runtime, profile, sink)
	adapter.transport = NewTransport(adapter.onConnectionOpened, adapter.onConnectionClosed, adapter.onPayload)
	adapter.transport.Configure(settings.Client, profile)
	return adapter
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

// Start 仅在 enabled=true 时拨号
func (a *Adapter) Start(ctx context.Context) error {
	if a == nil {
		return fmt.Errorf("snowluma adapter 为空")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return nil
	}
	if !a.settings.Plugin.Enabled {
		return nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.started = true
	setActiveAdapter(a)
	if a.hostHooks != nil && a.hostHooks.APIs != nil {
		RegisterLocalAPIs(a.hostHooks.APIs, a)
	}
	return a.transport.Start(runCtx)
}

// Stop 停止连接并清 identity
func (a *Adapter) Stop(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	if a.transport != nil {
		_ = a.transport.Stop(ctx)
	}
	if a.router != nil {
		a.router.ClearIdentity()
	}
	if a.runtime != nil {
		a.runtime.MarkDisconnected()
	}
	if a.hostHooks != nil && a.hostHooks.APIs != nil {
		a.hostHooks.APIs.RemoveAPIsByPlugin(PluginID)
	}
	clearActiveAdapter(a)
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
	if a == nil || a.transport == nil {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "适配器未启动"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	if a.runtime != nil && !a.runtime.RoutingEnabled() {
		receipt.Status = platformio.DeliveryStatusDropped
		receipt.Error = "identity_only 模式已暂停聊天路由"
		return receipt, nil
	}
	profile := a.profile.Profile()
	segments := EncodeOutboundSegments(outbound, profile)
	if len(segments) == 0 {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "消息内容为空"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	action, params := ResolveSendAction(profile, outbound.ChatType, outbound.TargetID, segments)
	resp, err := a.transport.CallAction(ctx, action, params)
	if err != nil {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = err.Error()
		return receipt, err
	}
	if status := asString(resp["status"]); status != "" && status != "ok" {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = asString(resp["message"])
		if receipt.Error == "" {
			receipt.Error = "对端返回失败"
		}
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	if data := asMap(resp["data"]); data != nil {
		receipt.ExternalMessageID = asString(data["message_id"])
	}
	receipt.Status = platformio.DeliveryStatusSent
	return receipt, nil
}

func (a *Adapter) onConnectionOpened(ctx context.Context) error {
	if a == nil {
		return nil
	}
	resp, err := a.transport.CallAction(ctx, "get_login_info", map[string]any{})
	if err != nil {
		return err
	}
	data := asMap(resp["data"])
	accountID := ""
	if data != nil {
		accountID = asString(data["user_id"])
	}
	if accountID == "" {
		return fmt.Errorf("get_login_info 缺少 user_id")
	}
	if err := a.probeAndApplyProfile(ctx); err != nil {
		zap.L().Warn("SnowLuma 客户端画像探测失败，沿用配置画像", zap.Error(err))
		if a.runtime.DetectedProtocol() == "" {
			a.runtime.SetDetectedProtocol(a.profile.Profile().ClientType)
		}
	}
	scope := a.settings.Client.ConnectionID
	a.runtime.MarkConnected(accountID, scope, a.runtime.DetectedProtocol())
	a.mu.Lock()
	a.desc = platformio.BuildNativeAdapterDescriptor(
		PluginID, GatewayName, PlatformQQ, accountID, scope,
		map[string]any{"route_type": RouteTypeDuplex, "protocol": a.runtime.DetectedProtocol()},
	)
	a.mu.Unlock()
	if a.hostHooks != nil && a.hostHooks.RegisterDriver != nil {
		if err := a.hostHooks.RegisterDriver(ctx, a); err != nil {
			return err
		}
	}
	if a.hostHooks != nil && a.hostHooks.ReportGatewayState != nil {
		if err := a.hostHooks.ReportGatewayState(ctx, a.runtime); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) onConnectionClosed(ctx context.Context) error {
	_ = ctx
	if a == nil {
		return nil
	}
	if a.router != nil {
		a.router.ClearIdentity()
	}
	if a.runtime != nil {
		a.runtime.MarkDisconnected()
	}
	if a.hostHooks != nil && a.hostHooks.ReportGatewayState != nil {
		_ = a.hostHooks.ReportGatewayState(context.Background(), a.runtime)
	}
	return nil
}

func (a *Adapter) onPayload(ctx context.Context, payload map[string]any) error {
	if a == nil || a.router == nil {
		return nil
	}
	return a.router.HandlePayload(ctx, payload)
}

func (a *Adapter) probeAndApplyProfile(ctx context.Context) error {
	resp, err := a.transport.CallAction(ctx, "get_version_info", map[string]any{})
	if err != nil {
		return err
	}
	data := asMap(resp["data"])
	if data == nil {
		return fmt.Errorf("get_version_info 缺少 data")
	}
	appName := firstNonEmpty(asString(data["app_name"]), asString(data["app"]))
	detected := DetectClientTypeFromAppName(appName)
	declared := a.settings.Client.ClientType
	if declared != "auto" {
		if detected != "" && detected != declared {
			zap.L().Warn("配置 client_type 与对端不一致",
				zap.String("declared", declared), zap.String("detected", detected), zap.String("app_name", appName))
		}
		if profile, ok := ProfileByType(declared); ok {
			a.profile.Update(profile)
			a.runtime.SetDetectedProtocol(profile.ClientType)
			return nil
		}
	}
	if detected == "" {
		a.profile.Update(OneBotV11Profile)
		a.runtime.SetDetectedProtocol(OneBotV11Profile.ClientType)
		return nil
	}
	if profile, ok := ProfileByType(detected); ok {
		a.profile.Update(profile)
		a.runtime.SetDetectedProtocol(profile.ClientType)
	}
	return nil
}

func (a *Adapter) Runtime() *RuntimeState { return a.runtime }

func (a *Adapter) Settings() Settings { return a.settings }

func (a *Adapter) Dialed() bool {
	if a == nil || a.transport == nil {
		return false
	}
	return a.transport.Dialed()
}

var _ = protocol.MessageGatewayStateUpdatePayload{}
var _ = capabilities.PluginGatewayInboundRequest{}
