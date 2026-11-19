package host

import (
	"context"
	"fmt"
	"path/filepath"

	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/telegram"
	"MaiGo/internal/plugin_runtime/protocol"
)

func (h *Host) buildTelegramHostHooks() *telegram.HostHooks {
	if h == nil {
		return nil
	}
	var inbound telegram.InboundSink
	if port, ok := h.MessageGatewayPort().(PluginGatewayInboundPort); ok && port != nil {
		inbound = port
	}
	return &telegram.HostHooks{
		Inbound: inbound,
		APIs:    h.APIRegistry(),
		ReportGatewayState: func(ctx context.Context, state *telegram.RuntimeState) error {
			return h.reportTelegramGatewayState(ctx, state)
		},
		RegisterDriver: func(ctx context.Context, adapter platformio.Adapter) error {
			if adapter == nil {
				return fmt.Errorf("telegram adapter 为空")
			}
			desc := adapter.Descriptor()
			return h.RegisterNativeGatewayDriver(ctx, NativeGatewayDriverSpec{
				PluginID:        telegram.PluginID,
				GatewayName:     telegram.GatewayName,
				ComponentName:   telegram.GatewayName,
				Platform:        telegram.PlatformTelegram,
				AccountID:       desc.AccountID,
				Scope:           desc.Scope,
				RouteType:       telegram.RouteTypeDuplex,
				Protocol:        telegramProtocol(desc.Metadata),
				SupportsSend:    true,
				SupportsReceive: true,
				Metadata: map[string]any{
					"plugin_type": "adapter",
					"connection":  telegram.ProtocolBotAPI,
				},
				Adapter: adapter,
			})
		},
		DataDir: func() string {
			root := ResolveInstanceRoot()
			if root == "" {
				return filepath.Join("data", "plugins", telegram.PluginID)
			}
			return filepath.Join(root, "data", "plugins", telegram.PluginID)
		},
	}
}

func (h *Host) reportTelegramGatewayState(ctx context.Context, state *telegram.RuntimeState) error {
	if h == nil || state == nil {
		return nil
	}
	ready, accountID, _, connected := state.SnapshotForReport()
	if !connected {
		_, _, err := h.UpdateMessageGatewayStateContext(ctx, telegram.PluginID, protocol.MessageGatewayStateUpdatePayload{
			GatewayName: telegram.GatewayName,
			Ready:       false,
			Platform:    telegram.PlatformTelegram,
		})
		return err
	}
	_, _, err := h.UpdateMessageGatewayStateContext(ctx, telegram.PluginID, protocol.MessageGatewayStateUpdatePayload{
		GatewayName: telegram.GatewayName,
		Ready:       ready,
		Platform:    telegram.PlatformTelegram,
		AccountID:   accountID,
		Metadata: map[string]any{
			"connection": telegram.ProtocolBotAPI,
		},
	})
	return err
}

func (h *Host) ensureTelegramFactoryWithHooks() error {
	reg, ok := platformio.DefaultNativeRuntime().Get(telegram.PluginID)
	if !ok {
		return fmt.Errorf("未登记的原生适配器: %s", telegram.PluginID)
	}
	reg.Factory = telegram.NewFactory(h.buildTelegramHostHooks())
	reg.DefaultConfig = telegram.DefaultConfigMap()
	return platformio.DefaultNativeRuntime().Register(reg)
}

func (h *Host) startTelegramIfEnabled(ctx context.Context, config map[string]any) error {
	if err := h.ensureTelegramFactoryWithHooks(); err != nil {
		return err
	}
	adapter, err := platformio.DefaultNativeRuntime().StartRuntime(ctx, telegram.PluginID, config)
	if err != nil {
		return err
	}
	if err := adapter.Start(ctx); err != nil {
		platformio.DefaultNativeRuntime().StopRuntime(telegram.PluginID)
		return err
	}
	zap.L().Info("原生适配器运行时已启动", zap.String("plugin_id", telegram.PluginID))
	return nil
}

func telegramProtocol(metadata map[string]any) string {
	if v := asMetadataString(metadata, "protocol"); v != "" {
		return v
	}
	return telegram.ProtocolBotAPI
}
