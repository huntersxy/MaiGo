package pluginruntime

import (
	"path/filepath"

	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/qq_official"
	"MaiGo/internal/platform_io/drivers/snowluma"
	"MaiGo/internal/platform_io/drivers/telegram"
	"MaiGo/internal/platform_io/drivers/weixin_clawbot"
	"MaiGo/internal/plugin_runtime/host"
)

// RegisterOfficialNativeAdapterPlaceholders 登记四官方/OneBot 原生适配器
// snowluma / telegram / weixin / qq_official 均使用真实 Factory；实际启动由 Host 注入 hooks
func RegisterOfficialNativeAdapterPlaceholders(instanceRoot, projectRoot string) error {
	instanceRoot = filepath.Clean(instanceRoot)
	_ = projectRoot
	thirdParty := host.DefaultThirdPartyPluginDir(instanceRoot)
	if thirdParty == "" {
		thirdParty = filepath.Join(instanceRoot, "plugins")
	}
	builtinRoot := func(dir string) string {
		return filepath.Join(instanceRoot, "config", "builtin_plugins", dir)
	}
regs := []platformio.NativeRegistration{
			{
				PluginID:      snowluma.PluginID,
				DirectoryName: snowluma.DirectoryName,
				ConfigRoot:    filepath.Join(thirdParty, snowluma.DirectoryName),
				GatewayNames:  []string{snowluma.GatewayName},
				DefaultConfig: snowluma.DefaultConfigMap(),
				ConfigSchema:  snowluma.ConfigSchemaMap(),
				Factory:       snowluma.NewFactoryStandalone(),
			},
			{
				PluginID:      telegram.PluginID,
				DirectoryName: telegram.DirectoryName,
				ConfigRoot:    builtinRoot(telegram.DirectoryName),
				GatewayNames:  []string{telegram.GatewayName},
				DefaultConfig: telegram.DefaultConfigMap(),
				ConfigSchema:  telegram.ConfigSchemaMap(),
				Factory:       telegram.NewFactoryStandalone(),
			},
			{
				PluginID:      weixin_clawbot.PluginID,
				DirectoryName: weixin_clawbot.DirectoryName,
				ConfigRoot:    builtinRoot(weixin_clawbot.DirectoryName),
				GatewayNames:  weixin_clawbot.AllGatewayNames(),
				DefaultConfig: weixin_clawbot.DefaultConfigMap(),
				ConfigSchema:  weixin_clawbot.ConfigSchemaMap(),
				Factory:       weixin_clawbot.NewFactoryStandalone(),
			},
			{
				PluginID:      qq_official.PluginID,
				DirectoryName: qq_official.DirectoryName,
				ConfigRoot:    builtinRoot(qq_official.DirectoryName),
				GatewayNames:  []string{qq_official.GatewayName},
				DefaultConfig: qq_official.DefaultConfigMap(),
				ConfigSchema:  qq_official.ConfigSchemaMap(),
				Factory:       qq_official.NewFactoryStandalone(),
			},
		}
	for _, reg := range regs {
		if err := platformio.DefaultNativeRuntime().Register(reg); err != nil {
			return err
		}
	}
	return nil
}
