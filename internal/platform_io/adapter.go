package platformio

import "context"

// InboundHandler 入站封装继续上层的回调；返回错误表示上层拒绝或处理失败
type InboundHandler func(ctx context.Context, envelope InboundMessageEnvelope) error

// Adapter 平台适配器最小契约，对齐 MaiBot PlatformIODriver
type Adapter interface {
	Descriptor() DriverDescriptor
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	// OnMessage 注册入站消息交回 Manager 的回调
	OnMessage(handler InboundHandler)
	Send(ctx context.Context, outbound OutboundMessageEnvelope, routeKey RouteKey) (DeliveryReceipt, error)
}

// BaseAdapter 提供 OnMessage 回调挂接的公共实现
type BaseAdapter struct {
	desc           DriverDescriptor
	inboundHandler InboundHandler
}

// NewBaseAdapter 使用驱动描述初始化
func NewBaseAdapter(desc DriverDescriptor) BaseAdapter {
	return BaseAdapter{desc: desc}
}

// Descriptor 返回驱动描述
func (b *BaseAdapter) Descriptor() DriverDescriptor {
	return b.desc
}

// OnMessage 注册入站回调
func (b *BaseAdapter) OnMessage(handler InboundHandler) {
	b.inboundHandler = handler
}

// ClearOnMessage 清除入站回调
func (b *BaseAdapter) ClearOnMessage() {
	b.inboundHandler = nil
}

// EmitInbound 将入站封装转交给 Manager
func (b *BaseAdapter) EmitInbound(ctx context.Context, envelope InboundMessageEnvelope) error {
	if b.inboundHandler == nil {
		return errNoInboundHandler
	}
	return b.inboundHandler(ctx, envelope)
}
