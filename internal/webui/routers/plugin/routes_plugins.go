package plugin

import (
	"github.com/go-chi/chi/v5"
)

// mountPluginMarketRoutes 注册插件市场路由与 Python 的 /plugins 兼容别名
// 独立成函数便于测试直接校验路由表，不依赖 Mount 中其它子系统的注册
func mountPluginMarketRoutes(pr chi.Router, d Deps) {
	pr.Get("/marketplace", d.handlePluginMarketplaceList)
	pr.Get("/marketplace/source", d.handlePluginMarketplaceSourceGet)
	pr.Put("/marketplace/source", d.handlePluginMarketplaceSourceUpdate)
	pr.Get("/marketplace/user-states", d.handlePluginMarketplaceUserStates)
	pr.Post("/marketplace/like", d.handlePluginMarketplaceLike)
	pr.Post("/marketplace/dislike", d.handlePluginMarketplaceDislike)
	pr.Post("/marketplace/rate", d.handlePluginMarketplaceRate)
	pr.Post("/marketplace/download", d.handlePluginMarketplaceDownload)
	pr.Post("/marketplace/install", d.handlePluginMarketplaceInstall)
	pr.Post("/marketplace/uninstall", d.handlePluginMarketplaceUninstall)
	pr.Post("/marketplace/update", d.handlePluginMarketplaceUpdate)
	pr.Get("/marketplace/{plugin_id}", d.handlePluginMarketplaceDetail)
	pr.Get("/marketplace/{plugin_id}/stats", d.handlePluginMarketplaceStats)
	pr.Get("/marketplace/{plugin_id}/readme", d.handlePluginMarketplaceReadme)
	pr.Get("/marketplace/{plugin_id}/changelog", d.handlePluginMarketplaceChangelog)
	pr.Get("/marketplace/{plugin_id}/feedback", d.handlePluginMarketplaceFeedback)
	pr.Get("/marketplace/{plugin_id}/user-state", d.handlePluginMarketplaceUserState)
	// 插件配置页（对齐 routers/plugin/config_routes.py）
	pr.Get("/config/{plugin_id}/bundle", d.handlePluginConfigBundle)
	pr.Get("/config/{plugin_id}/schema", d.handlePluginConfigSchema)
	pr.Get("/config/{plugin_id}/raw", d.handlePluginConfigRawGet)
	pr.Put("/config/{plugin_id}/raw", d.handlePluginConfigRawUpdate)
	pr.Post("/config/{plugin_id}/reset", d.handlePluginConfigReset)
	pr.Post("/config/{plugin_id}/toggle", d.handlePluginConfigToggle)
	pr.Get("/config/{plugin_id}/widgets", d.handlePluginConfigWidgets)
	pr.Post("/config/{plugin_id}/widgets/{action_id}", d.handlePluginConfigWidgetAction)
	pr.Get("/config/{plugin_id}", d.handlePluginConfigGet)
	pr.Put("/config/{plugin_id}", d.handlePluginConfigUpdate)
	// 插件运行时组件/命令/首页卡片/Hook（对齐 routers/plugin/runtime_routes.py）
	pr.Get("/runtime/plugins/{plugin_id}/components", d.handleRuntimePluginComponents)
	pr.Get("/runtime/commands", d.handleRuntimeCommands)
	pr.Get("/runtime/home-cards", d.handleRuntimeHomeCards)
	pr.Get("/runtime/hooks", d.handleRuntimeHookSpecs)
	// 已安装插件与评论身份（对齐 routers/plugin/management.py）
	pr.Get("/installed", d.handlePluginInstalled)
	// 版本、Git 与图标（对齐 routers/plugin/repository.py 与 icon_routes.py）
	pr.Get("/version", d.handlePluginVersion)
	pr.Get("/git-status", d.handlePluginGitStatus)
	pr.Post("/clone", d.handlePluginClone)
	pr.Get("/icon/{plugin_id}", d.handlePluginLocalIcon)
	pr.Get("/market-icon", d.handlePluginMarketIcon)
	// 插件声明式 WebUI（对齐 routers/plugin/webui_extensions.py）
	pr.Get("/runtime/webui", d.handleRuntimeWebUIList)
	pr.Post("/runtime/webui/{plugin_id}/reload", d.handleRuntimeWebUIReload)
	pr.Get("/runtime/webui/{plugin_id}/{page_id}/document", d.handleRuntimeWebUIDocument)
	pr.Post("/runtime/webui/{plugin_id}/{page_id}/uploads/{name}", d.handleRuntimeWebUIUpload)
	pr.Post("/runtime/webui/{plugin_id}/{page_id}/{kind}/{name}", d.handleRuntimeWebUIInvoke)
	pr.Get("/usage", d.handlePluginUsage)
	pr.Get("/marketplace/review-identity", d.handlePluginMarketplaceReviewIdentity)

	// Python marketplace 的兼容裸路径（历史主题路径），与新前缀行为一致
	pr.Get("/source", d.handlePluginMarketplaceSourceGet)
	pr.Put("/source", d.handlePluginMarketplaceSourceUpdate)
	pr.Get("/review-identity", d.handlePluginMarketplaceReviewIdentity)
	pr.Get("/user-states", d.handlePluginMarketplaceUserStates)
	pr.Post("/like", d.handlePluginMarketplaceLike)
	pr.Post("/dislike", d.handlePluginMarketplaceDislike)
	pr.Post("/rate", d.handlePluginMarketplaceRate)
	pr.Post("/download", d.handlePluginMarketplaceDownload)
	pr.Get("/{plugin_id}", d.handlePluginMarketplaceDetail)
	pr.Get("/{plugin_id}/stats", d.handlePluginMarketplaceStats)
	pr.Get("/{plugin_id}/readme", d.handlePluginMarketplaceReadme)
	pr.Get("/{plugin_id}/changelog", d.handlePluginMarketplaceChangelog)
	pr.Get("/{plugin_id}/feedback", d.handlePluginMarketplaceFeedback)
	pr.Get("/{plugin_id}/user-state", d.handlePluginMarketplaceUserState)

	// 兼容 Python 的 /plugins 前缀：安装、卸载、更新、ZIP 安装
	pr.Post("/install", d.handlePluginMarketplaceInstall)
	pr.Post("/uninstall", d.handlePluginMarketplaceUninstall)
	pr.Post("/update", d.handlePluginMarketplaceUpdate)
	pr.Post("/install-zip", d.handlePluginInstallZip)
}

// MountMarket 注册插件市场路由与 Python 的 /plugins 兼容别名
func MountMarket(pr chi.Router, d Deps) {
	mountPluginMarketRoutes(pr, d)
}
