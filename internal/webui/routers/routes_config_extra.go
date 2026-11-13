package webuiapi

import (
	"github.com/go-chi/chi/v5"
)

// mountConfigRoutes 注册 /api/webui/config 下的全部配置路由（对齐 routers/config.py）
// 独立成函数，便于 Lead 在 router.go 中用一行替换现有 /config 块
func mountConfigRoutes(cr chi.Router, d Deps) {
	cr.Get("/bot", d.handleConfigBotGet)
	cr.Get("/model", d.handleConfigModelGet)
	cr.Post("/bot", d.handleConfigBotUpdate)
	cr.Post("/model", d.handleConfigModelUpdate)

	cr.Get("/bot/raw", d.handleConfigBotRawGet)
	cr.Post("/bot/raw", d.handleConfigBotRawUpdate)
	cr.Post("/bot/section/{section_name}", d.handleConfigBotSectionUpdate)
	cr.Post("/model/section/{section_name}", d.handleConfigModelSectionUpdate)

	cr.Get("/model/versions", d.handleConfigModelVersionsList)
	cr.Post("/model/versions", d.handleConfigModelVersionCreate)
	cr.Patch("/model/versions/{version_id}", d.handleConfigModelVersionUpdate)
	cr.Delete("/model/versions/{version_id}", d.handleConfigModelVersionDelete)
	cr.Post("/model/versions/{version_id}/activate", d.handleConfigModelVersionActivate)

	cr.Get("/prompts", d.handleConfigPromptsList)
	cr.Get("/prompts/{language}/{filename}", d.handleConfigPromptGet)
	cr.Get("/prompts/{language}/{filename}/default", d.handleConfigPromptDefault)
	cr.Get("/prompts/{language}/{filename}/versions", d.handleConfigPromptVersions)
	cr.Get("/prompts/{language}/{filename}/versions/{version_id}", d.handleConfigPromptVersionGet)
	cr.Post("/prompts/{language}/{filename}/versions/{version_id}/activate", d.handleConfigPromptVersionActivate)
	cr.Delete("/prompts/{language}/{filename}/versions/{version_id}", d.handleConfigPromptVersionDelete)
	cr.Put("/prompts/{language}/{filename}", d.handleConfigPromptUpdate)
	cr.Delete("/prompts/{language}/{filename}", d.handleConfigPromptReset)
	cr.Get("/maisaka-prompt-preview", d.handleConfigMaisakaPromptPreview)

	cr.Get("/adapter-config/path", d.handleConfigAdapterPathGet)
	cr.Post("/adapter-config/path", d.handleConfigAdapterPathSave)
	cr.Get("/adapter-config", d.handleConfigAdapterGet)
	cr.Post("/adapter-config", d.handleConfigAdapterSave)

	cr.Get("/upgrade-status", d.handleConfigUpgradeStatus)

	cr.Get("/schema/bot", d.handleConfigSchemaBot)
	cr.Get("/schema/model", d.handleConfigSchemaModel)
	cr.Get("/schema/section/{section_name}", d.handleConfigSchemaSection)
}

// mountConfigCompatRoutes 注册旧版 /api/config 兼容路由
func mountConfigCompatRoutes(cr chi.Router, d Deps) {
	cr.Get("/schema", d.handleConfigCompatSchema)
	cr.Get("/schema/bot", d.handleConfigSchemaBot)
	cr.Get("/raw", d.handleConfigBotRawGet)
	cr.Post("/raw", d.handleConfigBotRawUpdate)
}
