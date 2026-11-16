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

// mountAgentRoutes 注册 /api/webui/agent 路由
func mountAgentRoutes(api chi.Router, d Deps) {
	api.Get("/agent", d.handleAgentStatus)
	api.Post("/agent/control", d.handleAgentControl)
	api.Get("/agent/events", d.handleAgentEvents)
	api.Post("/agent/message", d.handleAgentMessage)
}
