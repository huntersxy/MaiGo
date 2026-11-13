package webuiapi

import (
	"maibot-go/internal/webui/authutil"
	"net/http"
	"strings"

	"maibot-go/internal/webui/middleware"
)

func (d Deps) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "healthy",
		"service": "MaiBot WebUI",
		"backend": "maibot-go",
	})
}

func (d Deps) handleAuthCheck(w http.ResponseWriter, r *http.Request) {
	if !d.authenticated(r) {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	source := d.authTokenSource(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated":         true,
		"token_source":          source,
		"requires_custom_token": source == "temporary",
	})
}

func (d Deps) handleWSToken(w http.ResponseWriter, r *http.Request) {
	sessionToken := d.webUISessionToken(r)
	if sessionToken == "" {
		// 登录页本身也会请求，缺 Cookie 是正常情况，保持 200 避免误跳转
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    false,
			"message":    "未提供认证信息，请先登录",
			"token":      nil,
			"expires_in": 0,
		})
		return
	}
	if !d.sessionTokenValid(sessionToken) {
		// 对齐 Python：认证过期同样返回 200 + success=false，
		// 避免前端在登录页被 401 反复刷新；跳转由 WebSocket 的 4001 关闭码负责
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    false,
			"message":    "认证已过期，请重新登录",
			"token":      nil,
			"expires_in": 0,
		})
		return
	}
	token, expiresIn := authutil.Generate(sessionToken)
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"token":      token,
		"expires_in": expiresIn,
	})
}

func (d Deps) authenticated(r *http.Request) bool {
	for _, name := range []string{auth.SessionCookieName, auth.WebUISessionCookieName} {
		if cookie, err := r.Cookie(name); err == nil && d.sessionTokenValid(cookie.Value) {
			return true
		}
	}
	return false
}

// sessionTokenValid 统一判定 WebUI 会话令牌
//
// 必须同时认三种令牌：内存会话令牌、MAIBOT_WEB_ADMIN_KEY、data/webui.json 自定义令牌。
// auth/check、ws-token 与 WebSocket 握手共用这一判定；任何一处口径不同都会造成
// 「页面认为已登录、WebSocket 认为未登录」的死循环。
func (d Deps) sessionTokenValid(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" || d.AdminAuth == nil || !d.AdminAuth.Enabled() {
		return false
	}
	if d.AdminAuth.IsAuthenticated(token) || d.AdminAuth.CheckKey(token) {
		return true
	}
	if manager, err := d.webUITokenManager(); err == nil && manager.verifyCookieToken(token) {
		return true
	}
	return false
}

func (d Deps) authTokenSource(_ *http.Request) string {
	if manager, err := d.webUITokenManager(); err == nil {
		return manager.resolveTokenSource()
	}
	// 实例目录不可用时回退为 Go 桥接的 MAIBOT_WEB_ADMIN_KEY 语义
	return webuiTokenSourceConfigured
}
