package weixin_clawbot

import (
	"context"

	"MaiGo/internal/platform_io"
)

// NewFactory 返回 NativeRuntime 工厂；enabled=false 时仍可 Construct，但 Start 不 dial
func NewFactory(hooks *HostHooks) platformio.NativeAdapterFactory {
	return func(ctx context.Context, pluginID string, config map[string]any) (platformio.Adapter, error) {
		_ = ctx
		_ = pluginID
		settings, err := ParseSettings(config)
		if err != nil {
			return nil, err
		}
		return NewOrchestrator(settings, hooks), nil
	}
}

// NewFactoryStandalone 无 HostHooks 的工厂（仅登记/编译）
func NewFactoryStandalone() platformio.NativeAdapterFactory {
	return NewFactory(nil)
}
