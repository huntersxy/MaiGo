package webuiapi

import "github.com/go-chi/chi/v5"

// mountGoSettingsRoutes 注册 Go 平台开发者设置 API 路由
func mountGoSettingsRoutes(r chi.Router, d Deps) {
	r.Get("/settings", d.handleGoSettingsGet)
	r.Post("/settings", d.handleGoSettingsUpdate)
	r.Post("/toggle/{feature_key}", d.handleGoSettingsToggle)
	r.Post("/reset", d.handleGoSettingsReset)
	r.Post("/mode", d.handleGoSettingsMode)
	r.Post("/restart", d.handleGoSettingsRestart)
}
