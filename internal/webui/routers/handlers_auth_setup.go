package webuiapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	"maibot-go/internal/webui/middleware"
)

func (d Deps) handleAuthUpdateToken(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	if !validateRequestBodyForRoute(w, r) {
		return
	}
	var payload struct {
		NewToken string `json:"new_token"`
	}
	body := readBody(r)
	if len(body) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "new_token 为必填字段"})
		return
	}
	if err := sonic.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "请求格式错误"})
		return
	}
	payload.NewToken = strings.TrimSpace(payload.NewToken)
	if len([]rune(payload.NewToken)) < 10 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "new_token 长度至少为 10 位"})
		return
	}
	manager, err := d.webUITokenManager()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "Token 更新失败"})
		return
	}
	success, message := manager.updateToken(payload.NewToken)
	if success {
		d.clearWebUICookies(w, r)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": success, "message": message})
}

func (d Deps) handleAuthRegenerateToken(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	manager, err := d.webUITokenManager()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "Token 重新生成失败"})
		return
	}
	newToken, err := manager.regenerateToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "Token 重新生成失败"})
		return
	}
	d.clearWebUICookies(w, r)
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"token":   newToken,
		"message": "Token 已重新生成",
	})
}

func (d Deps) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	manager, err := d.webUITokenManager()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "获取配置状态失败"})
		return
	}
	source := manager.resolveTokenSource()
	requiresCustom := source == webuiTokenSourceTemporary
	isFirst := manager.isFirstSetup()
	message := "已完成配置"
	if requiresCustom {
		message = "需要设置自定义 Token"
	} else if isFirst {
		message = "首次配置"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"is_first_setup":        isFirst,
		"token_source":          source,
		"requires_custom_token": requiresCustom,
		"message":               message,
	})
}

func (d Deps) handleSetupComplete(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	manager, err := d.webUITokenManager()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "标记配置完成失败"})
		return
	}
	success := manager.markSetupCompleted()
	message := "标记失败"
	if success {
		message = "配置已完成"
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": success, "message": message})
}

func (d Deps) handleSetupReset(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	if !validateRequestBodyForRoute(w, r) {
		return
	}
	resetPassword := false
	if body := readBody(r); len(body) > 0 {
		var payload struct {
			ResetPassword bool `json:"reset_password"`
		}
		if err := sonic.Unmarshal(body, &payload); err == nil {
			resetPassword = payload.ResetPassword
		}
	}
	manager, err := d.webUITokenManager()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "重置配置状态失败"})
		return
	}
	success := manager.resetSetupStatus(resetPassword)
	if success && resetPassword {
		// 旧凭证立即失效：/admin 会话重建，maibot_session 续上新的访问口令（对齐 Python set_auth_cookie）
		if d.AdminAuth != nil && d.AdminAuth.Enabled() {
			if sessionToken, expiresAt, err := d.AdminAuth.CreateSession(); err == nil {
				d.AdminAuth.SetSessionCookie(w, sessionToken, expiresAt)
				d.AdminAuth.SetWebUISessionCookie(w, EncodeCookieToken(manager.token()), expiresAt)
			} else {
				d.clearWebUICookies(w, r)
			}
		} else if token := manager.token(); token != "" {
			// 鉴权未启用时仍写入 Python 兼容的令牌 Cookie
			d.AdminAuth.SetWebUISessionCookie(w, EncodeCookieToken(token), time.Now().Add(auth.SessionTTL))
		}
		w.Header().Set("Cache-Control", "no-store")
	}
	message := "重置失败"
	if success {
		message = "配置状态已重置"
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": success, "message": message})
}

func (d Deps) clearWebUICookies(w http.ResponseWriter, _ *http.Request) {
	d.AdminAuth.ClearSessionCookie(w)
	d.AdminAuth.ClearWebUISessionCookie(w)
}
