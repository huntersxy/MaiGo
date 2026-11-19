package host

import (
	"context"
	"fmt"
	"path/filepath"

	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/qq_official"
	"MaiGo/internal/plugin_runtime/protocol"
)

func (h *Host) buildQQOfficialHostHooks() *qq_official.HostHooks {
	if h == nil {
		return nil
	}
	var inbound qq_official.InboundSink
	if port, ok := h.MessageGatewayPort().(PluginGatewayInboundPort); ok && port != nil {
		inbound = port
	}
	return &qq_official.HostHooks{
		Inbound: inbound,
		InvokeAPI: qqOfficialAPIInvoker{host: h},
		ReportGatewayState: func(ctx context.Context, state *qq_official.RuntimeState) error {
			return h.reportQQOfficialGatewayState(ctx, state)
		},
		RegisterDriver: func(ctx context.Context, adapter platformio.Adapter) error {
			if adapter == nil {
				return fmt.Errorf("qq official adapter 为空")
			}
			desc := adapter.Descriptor()
			return h.RegisterNativeGatewayDriver(ctx, NativeGatewayDriverSpec{
				PluginID:        qq_official.PluginID,
				GatewayName:     qq_official.GatewayName,
				ComponentName:   qq_official.GatewayName,
				Platform:        qq_official.PlatformQQ,
				AccountID:       desc.AccountID,
				Scope:           desc.Scope,
				RouteType:       qq_official.RouteTypeDuplex,
				Protocol:        qqOfficialProtocol(desc.Metadata),
				SupportsSend:    true,
				SupportsReceive: true,
				Metadata: map[string]any{
					"plugin_type": "adapter",
					"connection":  qq_official.ProtocolOfficial,
				},
				Adapter: adapter,
			})
		},
		DataDir: func() string {
			root := ResolveInstanceRoot()
			if root == "" {
				return filepath.Join("data", "plugins", qq_official.PluginID)
			}
			return filepath.Join(root, "data", "plugins", qq_official.PluginID)
		},
	}
}

type qqOfficialAPIInvoker struct {
	host *Host
}

func (i qqOfficialAPIInvoker) InvokeAPI(ctx context.Context, pluginID, componentName string, args map[string]any, timeoutMS int64) (map[string]any, error) {
	_ = timeoutMS
	if i.host == nil {
		return nil, fmt.Errorf("Host 未初始化")
	}
	// identity 只走 snowluma LocalAPI 短路；未就绪则跳过，绝不落到 Python Runner
	apis := i.host.APIRegistry()
	if apis == nil {
		return nil, fmt.Errorf("snowluma LocalAPI 未就绪")
	}
	entry := apis.GetAPI(pluginID, componentName, "", false)
	if entry == nil {
		return nil, fmt.Errorf("snowluma LocalAPI 未就绪")
	}
	handler := entry.LocalHandler()
	if handler == nil {
		return nil, fmt.Errorf("snowluma LocalAPI 未就绪")
	}
	if args == nil {
		args = map[string]any{}
	}
	payload, err := handler(ctx, pluginID, componentName, args)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		return map[string]any{}, nil
	}
	return payload, nil
}

func (h *Host) reportQQOfficialGatewayState(ctx context.Context, state *qq_official.RuntimeState) error {
	if h == nil || state == nil {
		return nil
	}
	ready, accountID, _, connected := state.SnapshotForReport()
	if !connected {
		_, _, err := h.UpdateMessageGatewayStateContext(ctx, qq_official.PluginID, protocol.MessageGatewayStateUpdatePayload{
			GatewayName: qq_official.GatewayName,
			Ready:       false,
			Platform:    qq_official.PlatformQQ,
		})
		return err
	}
	_, _, err := h.UpdateMessageGatewayStateContext(ctx, qq_official.PluginID, protocol.MessageGatewayStateUpdatePayload{
		GatewayName: qq_official.GatewayName,
		Ready:       ready,
		Platform:    qq_official.PlatformQQ,
		AccountID:   accountID,
		Metadata: map[string]any{
			"connection": qq_official.ProtocolOfficial,
			"protocol":   qq_official.ProtocolOfficial,
		},
	})
	return err
}

func (h *Host) ensureQQOfficialFactoryWithHooks() error {
	reg, ok := platformio.DefaultNativeRuntime().Get(qq_official.PluginID)
	if !ok {
		return fmt.Errorf("未登记的原生适配器: %s", qq_official.PluginID)
	}
	reg.Factory = qq_official.NewFactory(h.buildQQOfficialHostHooks())
	reg.DefaultConfig = qq_official.DefaultConfigMap()
	return platformio.DefaultNativeRuntime().Register(reg)
}

func (h *Host) startQQOfficialIfEnabled(ctx context.Context, config map[string]any) error {
	if err := h.ensureQQOfficialFactoryWithHooks(); err != nil {
		return err
	}
	adapter, err := platformio.DefaultNativeRuntime().StartRuntime(ctx, qq_official.PluginID, config)
	if err != nil {
		return err
	}
	if err := adapter.Start(ctx); err != nil {
		platformio.DefaultNativeRuntime().StopRuntime(qq_official.PluginID)
		return err
	}
	zap.L().Info("原生适配器运行时已启动", zap.String("plugin_id", qq_official.PluginID))
	return nil
}

func qqOfficialProtocol(metadata map[string]any) string {
	if v := asMetadataString(metadata, "protocol"); v != "" {
		return v
	}
	return qq_official.ProtocolOfficial
}
