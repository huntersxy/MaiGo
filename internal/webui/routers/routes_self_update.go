package webuiapi

import "github.com/go-chi/chi/v5"

// mountSelfUpdateRoutes 对齐 ../MaiBot/src/webui/routers/self_update.py 的 /system/self-update 端点
func mountSelfUpdateRoutes(r chi.Router, d Deps) {
	r.Get("/status", d.handleSelfUpdateStatus)
	r.Post("/check", d.handleSelfUpdateCheck)
	r.Post("/check-stream", d.handleSelfUpdateCheckStream)
	r.Post("/apply", d.handleSelfUpdateApply)
}
