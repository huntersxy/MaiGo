package webuiapi

import (
	"MaiGo/internal/webui/authutil"
	"net/http"
	"strings"

	"MaiGo/internal/webui/middleware"
)

func (d Deps) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "healthy",
		"service": "MaiBot WebUI",
		"backend": "MaiGo",
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
	if authutil.HostManaged() {
		return true
	}
	cookie, err := r.Cookie(auth.WebUISessionCookieName)
	if err != nil {
		return false
	}
	return d.sessionTokenValid(cookie.Value)
}

// sessionTokenValid 判定 maibot_session 是否持有当前访问口令
//
// auth/check、ws-token 与 WebSocket 握手共用这一判定；任何一处口径不同都会造成
// 「页面认为已登录、WebSocket 认为未登录」的死循环。
func (d Deps) sessionTokenValid(token string) bool {
	if authutil.HostManaged() {
		return true
	}
	token = strings.TrimSpace(token)
	if token == "" || d.AdminAuth == nil {
		return false
	}
	return d.AdminAuth.ValidateToken(token)
}

func (d Deps) authTokenSource(_ *http.Request) string {
	if manager, err := d.webUITokenManager(); err == nil {
		return manager.resolveTokenSource()
	}
	// 实例目录不可用时无法确定令牌来源，按已配置处理
	return webuiTokenSourceConfigured
}
