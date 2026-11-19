package telegram

import (
	"context"

	"MaiGo/internal/platform_io"
)

// NewFactory 返回 NativeRuntime 工厂；enabled=false 时仍可 Construct，但 Adapter.Start 不轮询
func NewFactory(hooks *HostHooks) platformio.NativeAdapterFactory {
	return func(ctx context.Context, pluginID string, config map[string]any) (platformio.Adapter, error) {
		_ = ctx
		_ = pluginID
		settings, err := ParseSettings(config)
		if err != nil {
			return nil, err
		}
		return NewAdapter(settings, hooks), nil
	}
}

// NewFactoryStandalone 无 HostHooks 的工厂（仅登记/编译用）；实际启动由 Host 注入 hooks
func NewFactoryStandalone() platformio.NativeAdapterFactory {
	return NewFactory(nil)
}
