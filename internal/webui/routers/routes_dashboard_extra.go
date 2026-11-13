package webuiapi

import "github.com/go-chi/chi/v5"

// mountAvatarRoutes 注册 /api/webui/avatar 路由
func mountAvatarRoutes(ar chi.Router, d Deps) {
	ar.Put("/webui-user", d.handleAvatarUploadWebUIUser)
	ar.Get("/", d.handleAvatarGet)
}

// mountBotAccountRoutes 注册 /api/webui/bot-accounts 路由
func mountBotAccountRoutes(api chi.Router, d Deps) {
	api.Get("/bot-accounts", d.handleBotAccountList)
	api.Post("/bot-accounts/{account_id}/disable", d.handleBotAccountDisable)
	api.Post("/bot-accounts/{account_id}/restore", d.handleBotAccountRestore)
	api.Delete("/bot-accounts/{account_id}", d.handleBotAccountDelete)
}

// mountComputerRoutes 注册 /api/webui/computer 路由（对齐 routers/computer.py）
func mountComputerRoutes(api chi.Router, d Deps) {
	api.Get("/computer", d.handleComputerStatus)
	api.Post("/computer/control", d.handleComputerControl)
	api.Get("/computer/frame", d.handleComputerFrame)
	api.Post("/computer/input", d.handleComputerInput)
	api.Get("/computer/operations/{operation_id}", d.handleComputerOperation)
}

// mountAgentRoutes 注册 /api/webui/agent 路由
func mountAgentRoutes(api chi.Router, d Deps) {
	api.Get("/agent", d.handleAgentStatus)
	api.Post("/agent/control", d.handleAgentControl)
	api.Get("/agent/events", d.handleAgentEvents)
	api.Post("/agent/message", d.handleAgentMessage)
}
