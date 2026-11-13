package chat

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// mountChatRoutes 注册 /api/chat 全部本地聊天室端点，路由表对齐 MaiBot chat/routes.py
func mountChatRoutes(cr chi.Router, d Deps) {
	cr.Get("/history", d.handleChatHistory)
	cr.Delete("/history", d.handleChatClearHistory)
	cr.Get("/platforms", d.handleChatPlatforms)
	cr.Get("/persons", d.handleChatPersons)
	cr.Get("/info", d.handleChatInfo)

	cr.Get("/sessions", d.handleChatSessions)
	cr.Post("/sessions/adapter-status", d.handleChatAdapterStatus)
	cr.Get("/resolve-target", d.handleChatResolveTarget)
	cr.Post("/resolve-targets", d.handleChatResolveTargets)
	cr.Get("/sessions/{session_id}", d.handleChatSessionDetail)
	cr.Delete("/sessions/{session_id}", d.handleChatDeleteSession)
	cr.Put("/sessions/{session_id}/talk-frequency", d.handleChatUpdateTalkFrequency)
	cr.Delete("/sessions/{session_id}/talk-frequency", d.handleChatDeleteTalkFrequency)
	cr.Put("/sessions/{session_id}/private-reply-pause", d.handleChatPrivateReplyPause)
	cr.Put("/sessions/{session_id}/learning/{kind}", d.handleChatUpdateLearning)
	cr.Put("/sessions/{session_id}/prompts", d.handleChatUpsertPrompt)
	cr.Delete("/sessions/{session_id}/prompts/{index}", d.handleChatDeletePrompt)
	cr.Put("/sessions/{session_id}/adapters/policy", d.handleChatSessionAdapterPolicy)
	cr.Get("/sessions/{session_id}/messages", d.handleChatSessionMessages)
	cr.Get("/adapters/policy/defaults", d.handleChatAdapterPolicyDefaultsGet)
	cr.Put("/adapters/policy/defaults", d.handleChatAdapterPolicyDefaultsUpdate)
	cr.Get("/adapters/plugins/{plugin_id}/policy", d.handleChatAdapterPluginPolicyGet)
	cr.Put("/adapters/plugins/{plugin_id}/policy", d.handleChatAdapterPluginPolicyUpdate)

	cr.MethodFunc(http.MethodGet, "/*", d.handleChatNotFound)
	cr.MethodFunc(http.MethodPost, "/*", d.handleChatNotFound)
}

// Mount 注册 /api/chat 全部本地聊天室端点
//
// 导出入口供父包在路由树里挂载；路由表与 Python chat/routes.py 对齐。
func Mount(cr chi.Router, d Deps) {
	mountChatRoutes(cr, d)
}
