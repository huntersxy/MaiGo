package weixin_clawbot

import (
	"context"

	"MaiGo/internal/platform_io"
	"MaiGo/internal/plugin_runtime/capabilities"
)

// InboundSink 入站投递面：只允许 ReceiveGatewayMessage
type InboundSink interface {
	ReceiveGatewayMessage(ctx context.Context, request capabilities.PluginGatewayInboundRequest, message map[string]any) (bool, map[string]any, error)
}

// LocalAPIBinder Host API 本地短路注册面
type LocalAPIBinder interface {
	SetAPILocalHandler(name, pluginID string, metadata map[string]any, handler func(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error)) bool
	RemoveAPIsByPlugin(pluginID string) int
}

// HostHooks 与 snowluma/telegram 同形
type HostHooks struct {
	Inbound            InboundSink
	APIs               LocalAPIBinder
	ReportGatewayState func(ctx context.Context, gatewayName string, ready bool, accountID, remark string) error
	RegisterDriver     func(ctx context.Context, gatewayName string, adapter platformio.Adapter) error
	DataDir            func() string
}

func deliverInbound(ctx context.Context, sink InboundSink, gatewayName, accountID string, message map[string]any) error {
	if sink == nil {
		return nil
	}
	request := capabilities.PluginGatewayInboundRequest{
		PluginID:          PluginID,
		GatewayName:       gatewayName,
		DriverID:          driverID(gatewayName),
		Platform:          PlatformWeixin,
		AccountID:         accountID,
		ExternalMessageID: asString(message["message_id"]),
		DedupeKey:         asString(message["message_id"]),
	}
	_, _, err := sink.ReceiveGatewayMessage(ctx, request, message)
	return err
}
