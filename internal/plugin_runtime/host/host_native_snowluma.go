package host

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/snowluma"
	"MaiGo/internal/plugin_runtime/protocol"
)

func (h *Host) buildSnowlumaHostHooks() *snowluma.HostHooks {
	if h == nil {
		return nil
	}
	var inbound snowluma.InboundSink
	if port, ok := h.MessageGatewayPort().(PluginGatewayInboundPort); ok && port != nil {
		inbound = port
	}
	return &snowluma.HostHooks{
		Inbound: inbound,
		APIs:    h.APIRegistry(),
		ReportGatewayState: func(ctx context.Context, state *snowluma.RuntimeState) error {
			return h.reportSnowlumaGatewayState(ctx, state)
		},
		RegisterDriver: func(ctx context.Context, adapter platformio.Adapter) error {
			if adapter == nil {
				return fmt.Errorf("snowluma adapter 为空")
			}
			desc := adapter.Descriptor()
			return h.RegisterNativeGatewayDriver(ctx, NativeGatewayDriverSpec{
				PluginID:        snowluma.PluginID,
				GatewayName:     snowluma.GatewayName,
				ComponentName:   snowluma.GatewayName,
				Platform:        snowluma.PlatformQQ,
				AccountID:       desc.AccountID,
				Scope:           desc.Scope,
				RouteType:       snowluma.RouteTypeDuplex,
				Protocol:        asMetadataString(desc.Metadata, "protocol"),
				SupportsSend:    true,
				SupportsReceive: true,
				Metadata:        map[string]any{"plugin_type": "adapter"},
				Adapter:         adapter,
			})
		},
	}
}

func (h *Host) reportSnowlumaGatewayState(ctx context.Context, state *snowluma.RuntimeState) error {
	if h == nil || state == nil {
		return nil
	}
	ready, accountID, scope, detectedProtocol, connected := state.SnapshotForReport()
	if !connected {
		_, _, err := h.UpdateMessageGatewayStateContext(ctx, snowluma.PluginID, protocol.MessageGatewayStateUpdatePayload{
			GatewayName: snowluma.GatewayName,
			Ready:       false,
			Platform:    snowluma.PlatformQQ,
		})
		return err
	}
	metadata := map[string]any{}
	if detectedProtocol != "" {
		metadata["protocol"] = detectedProtocol
	}
	_, _, err := h.UpdateMessageGatewayStateContext(ctx, snowluma.PluginID, protocol.MessageGatewayStateUpdatePayload{
		GatewayName: snowluma.GatewayName,
		Ready:       ready,
		Platform:    snowluma.PlatformQQ,
		AccountID:   accountID,
		Scope:       scope,
		Metadata:    metadata,
	})
	return err
}

func asMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if value, ok := metadata[key].(string); ok {
		return value
	}
	return ""
}

func (h *Host) ensureSnowlumaFactoryWithHooks() error {
	reg, ok := platformio.DefaultNativeRuntime().Get(snowluma.PluginID)
	if !ok {
		return fmt.Errorf("未登记的原生适配器: %s", snowluma.PluginID)
	}
	reg.Factory = snowluma.NewFactory(h.buildSnowlumaHostHooks())
	reg.DefaultConfig = snowluma.DefaultConfigMap()
	return platformio.DefaultNativeRuntime().Register(reg)
}

// EnsureSnowlumaLocalAPIs 即使 enabled=false 也注册 LocalAPI，避免 Invoke 打到 Python Runner
func (h *Host) EnsureSnowlumaLocalAPIs() {
	if h == nil || h.APIRegistry() == nil {
		return
	}
	snowluma.RegisterLocalAPIs(h.APIRegistry(), nil)
}

func (h *Host) startSnowlumaIfEnabled(ctx context.Context, config map[string]any) error {
	if err := h.ensureSnowlumaFactoryWithHooks(); err != nil {
		return err
	}
	adapter, err := platformio.DefaultNativeRuntime().StartRuntime(ctx, snowluma.PluginID, config)
	if err != nil {
		return err
	}
	if err := adapter.Start(ctx); err != nil {
		platformio.DefaultNativeRuntime().StopRuntime(snowluma.PluginID)
		return err
	}
	zap.L().Info("原生适配器运行时已启动", zap.String("plugin_id", snowluma.PluginID))
	return nil
}
