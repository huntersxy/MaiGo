package telegram

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"MaiGo/internal/platform_io"
	"MaiGo/internal/plugin_runtime/capabilities"
)

// InboundSink 入站投递面：只允许 ReceiveGatewayMessage
type InboundSink interface {
	ReceiveGatewayMessage(ctx context.Context, request capabilities.PluginGatewayInboundRequest, message map[string]any) (bool, map[string]any, error)
}

// LocalAPIBinder Host API 本地短路注册面（与 snowluma 同形；Telegram P0 可不注册）
type LocalAPIBinder interface {
	SetAPILocalHandler(name, pluginID string, metadata map[string]any, handler func(ctx context.Context, pluginID, componentName string, args map[string]any) (map[string]any, error)) bool
	RemoveAPIsByPlugin(pluginID string) int
}

// HostHooks 适配器与 Host 的交互面（与 snowluma 同形）
type HostHooks struct {
	Inbound            InboundSink
	APIs               LocalAPIBinder
	ReportGatewayState func(ctx context.Context, state *RuntimeState) error
	RegisterDriver     func(ctx context.Context, adapter platformio.Adapter) error
	DataDir            func() string
}

func driverID() string {
	return "gateway:" + PluginID + ":" + GatewayName
}

func resolveDataDir(hooks *HostHooks) string {
	if hooks != nil && hooks.DataDir != nil {
		if dir := strings.TrimSpace(hooks.DataDir()); dir != "" {
			return dir
		}
	}
	return filepath.Join("data", "plugins", PluginID)
}

func deliverInbound(ctx context.Context, sink InboundSink, accountID string, message map[string]any) error {
	if sink == nil {
		return fmt.Errorf("入站端口未接线")
	}
	request := capabilities.PluginGatewayInboundRequest{
		PluginID:          PluginID,
		GatewayName:       GatewayName,
		DriverID:          driverID(),
		Platform:          PlatformTelegram,
		AccountID:         accountID,
		Scope:             "",
		ExternalMessageID: asString(message["message_id"]),
		DedupeKey:         asString(message["message_id"]),
	}
	_, _, err := sink.ReceiveGatewayMessage(ctx, request, message)
	return err
}
