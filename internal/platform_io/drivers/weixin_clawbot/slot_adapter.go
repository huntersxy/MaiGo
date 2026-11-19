package weixin_clawbot

import (
	"context"
	"fmt"
	"sync"

	"MaiGo/internal/platform_io"
)

// SlotAdapter 单账号 Adapter；Descriptor/DriverID 绑定到对应 gateway_name
type SlotAdapter struct {
	platformio.BaseAdapter
	mu         sync.Mutex
	desc       platformio.DriverDescriptor
	slot       int
	gateway    string
	connection *AccountConnection
	overall    *Orchestrator
}

func NewSlotAdapter(orch *Orchestrator, slot int, conn *AccountConnection) *SlotAdapter {
	gateway := GatewayName(slot)
	accountID := ""
	if conn != nil {
		accountID = conn.Store.State().AccountID
	}
	desc := platformio.BuildNativeAdapterDescriptor(
		PluginID,
		gateway,
		PlatformWeixin,
		accountID,
		"",
		map[string]any{
			"route_type": RouteTypeDuplex,
			"protocol":   ProtocolILink,
		},
	)
	return &SlotAdapter{
		BaseAdapter: platformio.NewBaseAdapter(desc),
		desc:        desc,
		slot:        slot,
		gateway:     gateway,
		connection:  conn,
		overall:     orch,
	}
}

func (s *SlotAdapter) Descriptor() platformio.DriverDescriptor {
	if s == nil {
		return platformio.DriverDescriptor{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.desc
}

func (s *SlotAdapter) refreshDescriptor(accountID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.desc = platformio.BuildNativeAdapterDescriptor(
		PluginID, s.gateway, PlatformWeixin, accountID, "",
		map[string]any{"route_type": RouteTypeDuplex, "protocol": ProtocolILink},
	)
}

// Start 槽位 worker 由 Orchestrator 管理；RegisterNativeGatewayDriver 触发时 no-op
func (s *SlotAdapter) Start(ctx context.Context) error {
	_ = ctx
	return nil
}

func (s *SlotAdapter) Stop(ctx context.Context) error {
	_ = ctx
	return nil
}

func (s *SlotAdapter) Send(ctx context.Context, outbound platformio.OutboundMessageEnvelope, routeKey platformio.RouteKey) (platformio.DeliveryReceipt, error) {
	receipt := platformio.DeliveryReceipt{
		InternalMessageID: outbound.InternalMessageID,
		RouteKey:          routeKey,
		Status:            platformio.DeliveryStatusPending,
		DriverID:          s.Descriptor().DriverID,
		DriverKind:        platformio.DriverKindPlugin,
	}
	if s == nil || s.connection == nil {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "微信账号槽位未就绪"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	return s.connection.Send(ctx, outbound, routeKey, receipt)
}
