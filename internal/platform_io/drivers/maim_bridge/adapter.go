package maimbridge

import (
	"context"
	"fmt"

	"maibot-go/internal/platform_io"
)

// Hub 出站写入面，由 maimmsg/server 实现
type Hub interface {
	SendToPlatform(ctx context.Context, platform string, payload []byte) error
	HasPlatform(platform string) bool
}

// RouteHub 可选路由感知出站面；带账号/作用域的路由精确到具体连接
// 旧 Hub 实现可不提供，Adapter 会自动回退到平台级发送
type RouteHub interface {
	SendToRoute(ctx context.Context, route platformio.RouteKey, payload []byte) error
	HasRoute(route platformio.RouteKey) bool
}

// Adapter 旧版 maim_message WS 汇聚适配器
type Adapter struct {
	platformio.BaseAdapter
	hub Hub
}

// NewAdapter 创建适配器
func NewAdapter(hub Hub) *Adapter {
	return &Adapter{
		BaseAdapter: platformio.NewBaseAdapter(DriverDescriptor()),
		hub:         hub,
	}
}

// Start 无额外资源，连接由 WS 服务管理
func (a *Adapter) Start(ctx context.Context) error {
	_ = ctx
	if a.hub == nil {
		return fmt.Errorf("maimbridge: hub 未配置")
	}
	return nil
}

// Stop 占位
func (a *Adapter) Stop(ctx context.Context) error {
	_ = ctx
	return nil
}

// Send 按 platform 路由键将 MessageBase JSON 推送到已连接适配器
func (a *Adapter) Send(ctx context.Context, outbound platformio.OutboundMessageEnvelope, routeKey platformio.RouteKey) (platformio.DeliveryReceipt, error) {
	receipt := platformio.DeliveryReceipt{
		InternalMessageID: outbound.InternalMessageID,
		RouteKey:          routeKey,
		Status:            platformio.DeliveryStatusPending,
		DriverID:          DriverIDMaimMsg,
		DriverKind:        platformio.DriverKindLegacy,
	}
	platform := routeKey.Platform
	if platform == "" {
		platform = outbound.RouteKey.Platform
	}
	if platform == "" {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "缺少 platform 路由键"
		return receipt, fmt.Errorf("maimbridge: %s", receipt.Error)
	}
	if a.hub == nil {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "maim_message 出站通道未接线"
		return receipt, fmt.Errorf("maimbridge: %s", receipt.Error)
	}
	route := routeKey
	if route.Platform == "" {
		route = outbound.RouteKey
	}
	routeHub, hasRouteHub := a.hub.(RouteHub)
	routeSupported := hasRouteHub && routeHub.HasRoute(route)
	hasIdentity := route.AccountID != "" || route.Scope != ""
	if hasIdentity && !hasRouteHub {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "maim_message 出站通道不支持账号/作用域精确路由"
		return receipt, fmt.Errorf("maimbridge: %s", receipt.Error)
	}
	if hasRouteHub && hasIdentity && !routeSupported {
		// 旧版 maim_message 连接按平台建立；账号/作用域只是可选的精确路由。
		// 精确连接不存在时退回平台级投递，对齐 Python send_message 的平台语义。
		if !a.hub.HasPlatform(platform) {
			receipt.Status = platformio.DeliveryStatusFailed
			receipt.Error = fmt.Sprintf("该平台账号/作用域无已连接的 maim_message 适配器: platform=%s account=%s scope=%s", route.Platform, route.AccountID, route.Scope)
			return receipt, fmt.Errorf("maimbridge: %s", receipt.Error)
		}
		route = platformio.RouteKey{Platform: route.Platform}
		routeSupported = routeHub.HasRoute(route)
	}
	if !routeSupported && !a.hub.HasPlatform(platform) {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "该平台无已连接的 maim_message 适配器"
		return receipt, fmt.Errorf("maimbridge: %s", receipt.Error)
	}
	msg, err := OutboundToMessageBase(ctx, outbound, route)
	if err != nil {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = err.Error()
		return receipt, err
	}
	raw, err := msg.ToJSON()
	if err != nil {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = err.Error()
		return receipt, err
	}
	var sendErr error
	if routeSupported {
		sendErr = routeHub.SendToRoute(ctx, route, raw)
	} else {
		sendErr = a.hub.SendToPlatform(ctx, platform, raw)
	}
	if sendErr != nil {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = sendErr.Error()
		return receipt, sendErr
	}
	receipt.Status = platformio.DeliveryStatusSent
	return receipt, nil
}

var _ platformio.Adapter = (*Adapter)(nil)
