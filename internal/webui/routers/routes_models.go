package webuiapi

import "github.com/go-chi/chi/v5"

// mountModelsRoutes 注册 /api/webui/models 下与 MaiBot model.py 对等的路由
func mountModelsRoutes(mr chi.Router, d Deps) {
	mr.Get("/client-types", d.handleModelsClientTypes)
	mr.Get("/list", d.handleModelsList)
	mr.Get("/list-by-url", d.handleModelsListByURL)
	mr.Get("/test-connection", d.handleModelsTestConnection)
	mr.Post("/test-connection-by-name", d.handleModelsTestConnectionByName)
	mr.Post("/test-model", d.handleModelsTestModel)
}
