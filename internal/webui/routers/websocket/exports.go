package websocket

import (
	"net/http"
)

// Handler 统一 WebSocket 处理器
//
// 父包在路由注册时构造一次并复用；RequireAuth 由父包注入，
// 避免把 cookie/token 校验逻辑复制到本包。
type Handler struct {
	deps        Deps
	requireAuth func(w http.ResponseWriter, r *http.Request) bool
}

// NewHandler 构造 WebSocket 处理器
func NewHandler(deps Deps, requireAuth func(http.ResponseWriter, *http.Request) bool) *Handler {
	return &Handler{deps: deps, requireAuth: requireAuth}
}

// HandleLogs 处理 /api/webui/ws/logs
func (h *Handler) HandleLogs(w http.ResponseWriter, r *http.Request) {
	h.deps.HandleWSLogs(w, r)
}

// HandleUnified 处理 /api/webui/ws
func (h *Handler) HandleUnified(w http.ResponseWriter, r *http.Request) {
	h.deps.HandleWSUnified(w, r)
}

// HandleWSLogs 处理 /api/webui/ws/logs
func (d Deps) HandleWSLogs(w http.ResponseWriter, r *http.Request) {
	d.handleWSLogs(w, r)
}

// HandleWSUnified 处理 /api/webui/ws
func (d Deps) HandleWSUnified(w http.ResponseWriter, r *http.Request) {
	d.handleWSUnified(w, r)
}
