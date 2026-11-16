package webuiapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Mount 注册 MaiBot Dashboard 兼容 API；独立于 templ 管理后台会话，不修改 /admin 鉴权链
func Mount(r chi.Router, d Deps) {
	d.setupPluginProgress()
	// memory.py 的 /api/webui/memory 与 /api 兼容树是绝对路径，先于其余路由挂载
	mountMemoryAdminRoutes(r, d)

	r.Route("/api/webui", func(api chi.Router) {
		api.Get("/health", d.handleHealth)
		api.Get("/version-compatibility", d.handleVersionCompatibility)

		api.Get("/auth/check", d.handleAuthCheck)
		api.Post("/auth/verify", d.handleAuthVerify)
		api.Post("/auth/logout", d.handleAuthLogout)
		api.Post("/auth/update", d.handleAuthUpdateToken)
		api.Post("/auth/regenerate", d.handleAuthRegenerateToken)
		api.Get("/setup/status", d.handleSetupStatus)
		api.Post("/setup/complete", d.handleSetupComplete)
		api.Post("/setup/reset", d.handleSetupReset)
		api.Get("/ws-token", d.handleWSToken)
		api.Get("/ws/logs", d.handleWSLogs)
		api.Get("/ws", d.handleWSUnified)

		api.Route("/config", func(cr chi.Router) {
			cr.Use(d.requireAuthMiddleware)
			mountConfigRoutes(cr, d)
		})

		api.Route("/models", func(mr chi.Router) {
			mr.Use(d.requireAuthMiddleware)
			mountModelsRoutes(mr, d)
		})

		api.Route("/search", func(sr chi.Router) {
			sr.Use(d.requireAuthMiddleware)
			sr.Post("/ai/stream", d.handleSearchAIStream)
			sr.Get("/help/sessions", d.handleHelpSessions)
			sr.Get("/help/sessions/{session_id}", d.handleHelpSessionDetail)
			sr.Delete("/help/sessions/{session_id}", d.handleHelpSessionDelete)
		})

		api.Route("/system", func(sr chi.Router) {
			sr.Use(d.requireAuthMiddleware)
			mountSystemRoutes(sr, d)
			sr.Route("/self-update", func(sur chi.Router) {
				mountSelfUpdateRoutes(sur, d)
			})
		})

		api.Route("/data-transfer", func(dr chi.Router) {
			dr.Use(d.requireAuthMiddleware)
			mountDataTransferRoutes(dr, d)
		})

		api.Route("/reasoning-process", func(rr chi.Router) {
			rr.Use(d.requireAuthMiddleware)
			mountReasoningRoutes(rr, d)
		})

		api.Route("/avatar", func(ar chi.Router) {
			ar.Use(d.requireAuthMiddleware)
			mountAvatarRoutes(ar, d)
		})
		// Python 的 /avatar 无尾斜杠
		api.Get("/avatar", d.requireAuthHandler(d.handleAvatarGet))
		api.Route("/user-emojis", func(ur chi.Router) {
			d.mountUserEmojiRoutes(ur)
		})
		// Python 的 /user-emojis 无尾斜杠
		api.HandleFunc("/user-emojis", d.handleUserEmojisRoot)
		api.Group(func(gr chi.Router) {
			gr.Use(d.requireAuthMiddleware)
			mountBotAccountRoutes(gr, d)
		})
		api.Group(func(gr chi.Router) {
			gr.Use(d.requireAuthMiddleware)
			mountAgentRoutes(gr, d)
		})
		api.Route("/person", func(pr chi.Router) {
			pr.Use(d.requireAuthMiddleware)
			pr.Get("/list", d.handlePersonList)
			pr.Get("/stats/summary", d.handlePersonStats)
			pr.Post("/batch/delete", d.handlePersonBatchDelete)
			pr.Get("/{person_id}", d.handlePersonDetail)
			pr.Delete("/{person_id}", d.handlePersonDelete)
			pr.Patch("/{person_id}", d.handlePersonUpdate)
			pr.Post("/{person_id}", d.handlePersonUpdate)
		})
		api.Route("/jargon", func(jr chi.Router) {
			jr.Use(d.requireAuthMiddleware)
			jr.Get("/list", d.handleJargonList)
			jr.Get("/chats", d.handleJargonChats)
			jr.Get("/groups", d.handleJargonGroups)
			jr.Get("/stats/summary", d.handleJargonStats)
			jr.Post("/export", d.handleJargonExport)
			jr.Post("/import", d.handleJargonImport)
			jr.Post("/batch/delete", d.handleJargonBatchDelete)
			jr.Post("/batch/set-jargon", d.handleJargonBatchSetJargon)
			jr.Get("/{jargon_id}", d.handleJargonDetail)
			jr.Post("/", d.handleJargonCreate)
			jr.Patch("/{jargon_id}", d.handleJargonUpdate)
			jr.Delete("/{jargon_id}", d.handleJargonDelete)
		})
		// Python 的创建接口无尾斜杠
		api.Post("/jargon", d.requireAuthHandler(d.handleJargonCreate))
		api.Route("/expression", func(er chi.Router) {
			er.Use(d.requireAuthMiddleware)
			er.Get("/chats", d.handleExpressionChats)
			er.Get("/chat-targets", d.handleExpressionChatTargets)
			er.Get("/list", d.handleExpressionList)
			er.Post("/export", d.handleExpressionExport)
			er.Post("/import", d.handleExpressionImport)
			er.Post("/clear", d.handleExpressionClear)
			er.Get("/stats/summary", d.handleExpressionStats)
			er.Get("/review/stats", d.handleExpressionReviewStats)
			er.Get("/review/list", d.handleExpressionReviewList)
			er.Post("/review/batch", d.handleExpressionReviewBatch)
			er.Post("/batch/delete", d.handleExpressionBatchDelete)
			er.Get("/groups", d.handleExpressionGroups)
			er.Get("/vector-build-progress", d.handleExpressionVectorBuildProgress)
			er.Get("/clusters", d.handleExpressionClusters)
			er.Get("/clusters/{cluster_id}/members", d.handleExpressionClusterMembers)
			er.Get("/runtime/vectors/spaces", d.handleExpressionVectorSpaces)
			er.Delete("/runtime/vectors/spaces/{space_id}", d.handleExpressionVectorSpaceDelete)
			er.Get("/review/logs", d.handleExpressionReviewLogs)
			er.Post("/review/logs/{review_log_id}/approve", d.handleExpressionReviewLogApprove)
			er.Get("/{expression_id}", d.handleExpressionDetail)
			er.Post("/", d.handleExpressionCreate)
			er.Patch("/{expression_id}", d.handleExpressionUpdate)
			er.Patch("/{expression_id}/review-status", d.handleExpressionReviewStatus)
			er.Delete("/{expression_id}", d.handleExpressionDelete)
		})
		// Python 的创建接口无尾斜杠
		api.Post("/expression", d.requireAuthHandler(d.handleExpressionCreate))
		api.Route("/mcp", func(mr chi.Router) {
			mr.Get("/status", d.handleMCPStatus)
			mr.Post("/test", d.handleMCPTest)
		})
		api.Route("/plugins", func(pr chi.Router) {
			// Python 仅 version/git-status 公开，其余均要求登录令牌
			pr.Use(d.pluginAuthMiddleware)
			d.mountPluginMarketRoutesCompat(pr)
		})
		api.Route("/emoji", func(er chi.Router) {
			d.mountEmojiRoutes(er)
		})
		api.Route("/statistics", func(sr chi.Router) {
			sr.Use(d.requireAuthMiddleware)
			sr.Get("/models", d.handleStatisticsModels)
			sr.Get("/chats", d.handleStatisticsChats)
			sr.Get("/dashboard", d.handleStatisticsDashboard)
			sr.Get("/summary", d.handleStatisticsSummary)
			sr.Get("/detailed", d.handleStatisticsDetailed)
			sr.Get("/task-usage", d.handleStatisticsTaskUsage)
		})

		api.Route("/learning/text", func(lr chi.Router) {
			lr.Use(d.requireAuthMiddleware)
			lr.Post("/prepare", d.handleTextLearningPrepare)
			lr.Post("/extract", d.handleTextLearningExtract)
		})
		api.Route("/news", func(nr chi.Router) {
			nr.Use(d.requireAuthMiddleware)
			nr.Get("/", d.handleNewsGet)
			nr.Get("/status", d.handleNewsStatus)
		})
		// Python 的 /news 无尾斜杠
		api.Get("/news", d.requireAuthHandler(d.handleNewsGet))
		api.Route("/desktop-pet", func(pr chi.Router) {
			pr.Use(d.requireAuthMiddleware)
			pr.Get("/skins", d.handleDesktopPetSkins)
			pr.Get("/skins/{skin_id}", d.handleDesktopPetSkin)
			pr.Post("/skins", d.handleDesktopPetInstall)
		})

		api.Get("/html-render/status", d.handleHTMLRenderStatus)

		api.Route("/go", func(gr chi.Router) {
			gr.Use(d.requireAuthMiddleware)
			mountGoSettingsRoutes(gr, d)
		})

		api.NotFound(d.handleWebUINotFound)
	})

	r.Route("/api/chat", func(cr chi.Router) {
		d.mountChatRoutesCompat(cr)
	})

	r.Route("/api/config", func(cr chi.Router) {
		mountConfigCompatRoutes(cr, d)
		cr.MethodFunc(http.MethodPost, "/*", d.handleConfigCompatNotFound)
		cr.MethodFunc(http.MethodGet, "/*", d.handleConfigCompatNotFound)
	})

	// Go 平台专属开发者设置页；配置 API 统一走 /api/webui/go（受 requireAuthMiddleware 保护）。
	r.Get("/go", d.handleGoDeveloperPage)
	r.Get("/go/", d.handleGoDeveloperPage)
}

func (d Deps) handleWebUINotFound(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Not Found"})
}

func (d Deps) handleChatNotFound(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Not Found"})
}

func (d Deps) handleConfigCompatNotFound(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Not Found"})
}
