package platformio

import (
	"context"
	"fmt"
	"strings"
)

// PreparedMessageSender 旧链发送通道；返回是否已成功交给平台
type PreparedMessageSender func(ctx context.Context, outbound OutboundMessageEnvelope, routeKey RouteKey) (bool, error)

// LegacyDriverOptions 旧链平台驱动配置
type LegacyDriverOptions struct {
	DriverID  string
	Platform  string
	AccountID string
	Scope     string
	Sender    PreparedMessageSender
}

// LegacyPlatformDriver 旧链平台驱动，对齐 MaiBot LegacyPlatformDriver
type LegacyPlatformDriver struct {
	BaseAdapter
	driverID string
	sender   PreparedMessageSender
}

// NewLegacyPlatformDriver 创建旧链驱动；driverID 为空时使用 legacy.send.<platform>
func NewLegacyPlatformDriver(opts LegacyDriverOptions) *LegacyPlatformDriver {
	driverID := strings.TrimSpace(opts.DriverID)
	if driverID == "" {
		driverID = LegacySendDriverID(opts.Platform)
	}
	desc := DriverDescriptor{
		DriverID:  driverID,
		Kind:      DriverKindLegacy,
		Platform:  strings.TrimSpace(opts.Platform),
		AccountID: strings.TrimSpace(opts.AccountID),
		Scope:     strings.TrimSpace(opts.Scope),
		Metadata: map[string]any{
			"source": "legacy_send",
		},
	}
	return &LegacyPlatformDriver{
		BaseAdapter: NewBaseAdapter(desc),
		driverID:    driverID,
		sender:      opts.Sender,
	}
}

// LegacySendDriverID 计算旧链 fallback 驱动 ID
func LegacySendDriverID(platform string) string {
	return "legacy.send." + strings.TrimSpace(platform)
}

// SenderConfigured 是否已注入旧链发送通道
func (d *LegacyPlatformDriver) SenderConfigured() bool { return d.sender != nil }

// SetSender 注入/替换旧链发送通道
func (d *LegacyPlatformDriver) SetSender(sender PreparedMessageSender) {
	d.sender = sender
}

// Start 无额外资源，发送通道可在运行期注入
func (d *LegacyPlatformDriver) Start(ctx context.Context) error {
	_ = ctx
	return nil
}

// Stop 占位
func (d *LegacyPlatformDriver) Stop(ctx context.Context) error {
	_ = ctx
	return nil
}

// Send 通过旧链通道发送消息
func (d *LegacyPlatformDriver) Send(ctx context.Context, outbound OutboundMessageEnvelope, routeKey RouteKey) (DeliveryReceipt, error) {
	receipt := DeliveryReceipt{
		InternalMessageID: outbound.InternalMessageID,
		RouteKey:          routeKey,
		Status:            DeliveryStatusPending,
		DriverID:          d.driverID,
		DriverKind:        DriverKindLegacy,
	}
	if d.sender == nil {
		receipt.Status = DeliveryStatusFailed
		receipt.Error = "旧链发送通道未配置"
		return receipt, fmt.Errorf("legacy: %s", receipt.Error)
	}
	sent, err := d.sender(ctx, outbound, routeKey)
	if err != nil {
		receipt.Status = DeliveryStatusFailed
		receipt.Error = err.Error()
		return receipt, err
	}
	if !sent {
		receipt.Status = DeliveryStatusFailed
		receipt.Error = "旧链发送失败"
		return receipt, fmt.Errorf("legacy: %s", receipt.Error)
	}
	receipt.Status = DeliveryStatusSent
	return receipt, nil
}

var _ Adapter = (*LegacyPlatformDriver)(nil)
