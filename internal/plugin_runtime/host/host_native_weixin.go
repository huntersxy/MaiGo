package host

import (
	"context"
	"fmt"
	"path/filepath"

	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/weixin_clawbot"
	"MaiGo/internal/plugin_runtime/protocol"
)

func (h *Host) buildWeixinHostHooks() *weixin_clawbot.HostHooks {
	if h == nil {
		return nil
	}
	var inbound weixin_clawbot.InboundSink
	if port, ok := h.MessageGatewayPort().(PluginGatewayInboundPort); ok && port != nil {
		inbound = port
	}
	return &weixin_clawbot.HostHooks{
		Inbound: inbound,
		APIs:    h.APIRegistry(),
		ReportGatewayState: func(ctx context.Context, gatewayName string, ready bool, accountID, remark string) error {
			return h.reportWeixinGatewayState(ctx, gatewayName, ready, accountID, remark)
		},
		RegisterDriver: func(ctx context.Context, gatewayName string, adapter platformio.Adapter) error {
			if adapter == nil {
				return fmt.Errorf("weixin slot adapter 为空")
			}
			desc := adapter.Descriptor()
			return h.RegisterNativeGatewayDriver(ctx, NativeGatewayDriverSpec{
				PluginID:        weixin_clawbot.PluginID,
				GatewayName:     gatewayName,
				ComponentName:   gatewayName,
				Platform:        weixin_clawbot.PlatformWeixin,
				AccountID:       desc.AccountID,
				Scope:           desc.Scope,
				RouteType:       weixin_clawbot.RouteTypeDuplex,
				Protocol:        weixin_clawbot.ProtocolILink,
				SupportsSend:    true,
				SupportsReceive: true,
				Metadata: map[string]any{
					"plugin_type":  "adapter",
					"account_name": remarkFromDesc(desc.Metadata),
				},
				Adapter: adapter,
			})
		},
		DataDir: func() string {
			root := ResolveInstanceRoot()
			if root == "" {
				return filepath.Join("data", "plugins", weixin_clawbot.PluginID)
			}
			return filepath.Join(root, "data", "plugins", weixin_clawbot.PluginID)
		},
	}
}

func remarkFromDesc(metadata map[string]any) string {
	return asMetadataString(metadata, "account_name")
}

func (h *Host) reportWeixinGatewayState(ctx context.Context, gatewayName string, ready bool, accountID, remark string) error {
	if h == nil {
		return nil
	}
	metadata := map[string]any{"protocol": weixin_clawbot.ProtocolILink}
	if remark != "" {
		metadata["account_name"] = remark
	}
	_, _, err := h.UpdateMessageGatewayStateContext(ctx, weixin_clawbot.PluginID, protocol.MessageGatewayStateUpdatePayload{
		GatewayName: gatewayName,
		Ready:       ready,
		Platform:    weixin_clawbot.PlatformWeixin,
		AccountID:   accountID,
		Metadata:    metadata,
	})
	return err
}

func (h *Host) ensureWeixinFactoryWithHooks() error {
	reg, ok := platformio.DefaultNativeRuntime().Get(weixin_clawbot.PluginID)
	if !ok {
		return fmt.Errorf("未登记的原生适配器: %s", weixin_clawbot.PluginID)
	}
	reg.Factory = weixin_clawbot.NewFactory(h.buildWeixinHostHooks())
	reg.DefaultConfig = weixin_clawbot.DefaultConfigMap()
	reg.GatewayNames = weixin_clawbot.AllGatewayNames()
	return platformio.DefaultNativeRuntime().Register(reg)
}

func (h *Host) startWeixinIfEnabled(ctx context.Context, config map[string]any) error {
	if err := h.ensureWeixinFactoryWithHooks(); err != nil {
		return err
	}
	adapter, err := platformio.DefaultNativeRuntime().StartRuntime(ctx, weixin_clawbot.PluginID, config)
	if err != nil {
		return err
	}
	if err := adapter.Start(ctx); err != nil {
		platformio.DefaultNativeRuntime().StopRuntime(weixin_clawbot.PluginID)
		return err
	}
	zap.L().Info("原生适配器运行时已启动", zap.String("plugin_id", weixin_clawbot.PluginID))
	return nil
}
