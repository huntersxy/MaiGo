package webuiapi

import (
	"context"
	"path/filepath"

	"MaiGo/internal/common/self_update"
	"MaiGo/internal/plugin_market"
)

// MarketplaceCatalog 是插件市场目录的只读视图，便于启动公告与 WebUI 更新检查复用
type MarketplaceCatalog interface {
	List(ctx context.Context, compatibleOnly bool, forceRefresh bool) (pluginmarket.CatalogResponse, error)
}

// NewMarketplaceLoader 把插件市场目录转换为更新兼容性检查所需的 loader
// 对齐 ../MaiBot/src/plugin_runtime/update_compatibility_notice.py:203-219 的市场目录加载
func NewMarketplaceLoader(catalog MarketplaceCatalog) selfupdate.MarketplaceLoader {
	if catalog == nil {
		return nil
	}
	return func(ctx context.Context) ([]selfupdate.MarketplacePlugin, error) {
		catalogResponse, err := catalog.List(ctx, false, false)
		if err != nil {
			return nil, err
		}
		plugins := make([]selfupdate.MarketplacePlugin, 0, len(catalogResponse.Plugins))
		for _, item := range catalogResponse.Plugins {
			manifest := item.Manifest
			if manifest == nil {
				manifest = map[string]any{
					"id": item.ID, "name": item.Name, "version": item.Version,
					"host_application": item.HostApplication,
				}
			}
			id := item.MarketplaceID
			if id == "" {
				id = item.ID
			}
			plugins = append(plugins, selfupdate.MarketplacePlugin{ID: id, Manifest: manifest})
		}
		return plugins, nil
	}
}

func (d Deps) updateIncompatiblePlugins(ctx context.Context, fromVersion, currentVersion string) []selfupdate.IncompatiblePluginNotice {
	pluginsDir, err := pluginmarket.ResolvePluginsDir(d.InstanceDir)
	if err != nil || pluginsDir == "" {
		pluginsDir = filepath.Join(d.InstanceDir, "plugins")
	}
	notices, err := selfupdate.CollectUpdateIncompatiblePlugins(
		ctx, fromVersion, currentVersion, pluginsDir, NewMarketplaceLoader(d.PluginMarket),
	)
	if err != nil {
		return []selfupdate.IncompatiblePluginNotice{}
	}
	return notices
}
