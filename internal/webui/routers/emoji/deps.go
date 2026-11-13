// Package emoji 提供 WebUI 的表情包路由（/api/webui/emoji 系列）。
//
// 对齐 MaiBot src/webui/routers/emoji/。
package emoji

import (
	"net/http"

	"maibot-go/internal/webui/authutil"
	"maibot-go/internal/webui/httputil"
	auth "maibot-go/internal/webui/middleware"
)

// Deps 表情包路由依赖
type Deps struct {
	InstanceDir string

	// RequireAuth 认证校验；由父包注入
	RequireAuth func(w http.ResponseWriter, r *http.Request) bool
	// AdminAuth 认证管理器，供「是否已登录」分支使用
	AdminAuth *auth.Manager
}

// requireAuth 认证校验的 nil 安全包装
//
// 未接线时一律拒绝（不伪造通过），避免 nil 调用 panic 把 401 变成 500。
func (d Deps) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if d.RequireAuth == nil {
		// 未接线时必须写出 401，否则调用方直接 return 会留下 200 空响应
		httputil.WriteJSON(w, http.StatusUnauthorized, map[string]any{"detail": "Token 无效或已过期"})
		return false
	}
	return d.RequireAuth(w, r)
}

// validSessionToken 校验会话令牌
func (d Deps) validSessionToken(token string) bool {
	return authutil.ValidSessionToken(d.AdminAuth, token)
}
