package host

// 统一 WebUI 的认证协议，与现有登录/设置页面兼容；对齐 src/host/webui_auth.py。

import (
	"encoding/json"
	"io"
	"net/http"
)

type hostAuth struct {
	sessions    *Sessions
	credentials *Credentials
	attempts    *LoginAttempts
	resolver    *ClientIPResolver
}

func newHostAuth(sessions *Sessions, credentials *Credentials, resolver *ClientIPResolver) *hostAuth {
	return &hostAuth{sessions: sessions, credentials: credentials, attempts: NewLoginAttempts(), resolver: resolver}
}

func (a *hostAuth) authenticate(r *http.Request) bool {
	if a == nil || a.sessions == nil {
		return false
	}
	cookie, err := r.Cookie(a.sessions.CookieName)
	return err == nil && a.sessions.Valid(cookie.Value)
}

func (a *hostAuth) mount(router interface {
	Get(string, http.HandlerFunc)
	Post(string, http.HandlerFunc)
}) {
	router.Get("/api/webui/auth/check", a.check)
	router.Post("/api/webui/auth/verify", a.verify)
	router.Post("/api/webui/auth/logout", a.logout)
	router.Post("/api/webui/auth/update", a.requireSession(a.update))
	router.Post("/api/webui/auth/regenerate", a.requireSession(a.regenerate))
	router.Get("/api/webui/setup/status", a.requireSession(a.setupStatus))
	router.Post("/api/webui/setup/complete", a.requireSession(a.completeSetup))
	router.Post("/api/webui/setup/reset", a.requireSession(a.resetSetup))
}

func (a *hostAuth) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if !a.authenticate(r) {
			writeJSONResponse(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
			return
		}
		next(rw, r)
	}
}

func (a *hostAuth) check(rw http.ResponseWriter, r *http.Request) {
	authenticated := a.authenticate(r)
	payload := map[string]any{"authenticated": authenticated}
	if authenticated {
		for key, value := range a.credentials.SetupStatus() {
			payload[key] = value
		}
	}
	writeJSONResponse(rw, http.StatusOK, payload)
}

func (a *hostAuth) verify(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := decodeLimitedJSON(r, &body, 4096); err != nil || len([]rune(body.Token)) > 4096 {
		writeJSONResponse(rw, http.StatusUnprocessableEntity, map[string]any{"detail": "请求体格式错误"})
		return
	}
	address := a.resolver.Resolve(r)
	if a.attempts.Blocked(address) {
		writeJSONResponse(rw, http.StatusTooManyRequests, map[string]any{"detail": "认证失败次数过多，请稍后再试"})
		return
	}
	valid := a.credentials.Verify(body.Token)
	if err := a.attempts.Record(address, valid); err != nil {
		writeJSONResponse(rw, http.StatusTooManyRequests, map[string]any{"detail": err.Error()})
		return
	}
	if !valid {
		writeJSONResponse(rw, http.StatusOK, map[string]any{"valid": false, "message": "Token 无效或已过期"})
		return
	}
	session, err := a.sessions.CreateSession(r.Header.Get("Origin"))
	if err != nil {
		writeJSONResponse(rw, http.StatusInternalServerError, map[string]any{"detail": "登录会话创建失败"})
		return
	}
	a.sessions.SetCookie(rw, session)
	payload := map[string]any{"valid": true, "message": "登录成功"}
	for key, value := range a.credentials.SetupStatus() {
		payload[key] = value
	}
	writeJSONResponse(rw, http.StatusOK, payload)
}

func (a *hostAuth) logout(rw http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(a.sessions.CookieName); err == nil {
		a.sessions.Revoke(cookie.Value)
	}
	clearSessionCookie(rw, a.sessions.CookieName, a.sessions.secure)
	writeJSONResponse(rw, http.StatusOK, map[string]any{"success": true, "message": "已登出"})
}

func (a *hostAuth) update(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		NewToken string `json:"new_token"`
	}
	if err := decodeLimitedJSON(r, &body, 8192); err != nil || len([]rune(body.NewToken)) < 10 || len([]rune(body.NewToken)) > 4096 {
		writeJSONResponse(rw, http.StatusOK, map[string]any{"success": false, "message": "Token 至少 10 位且不超过 4096 位"})
		return
	}
	cookie, _ := r.Cookie(a.sessions.CookieName)
	session, err := a.sessions.UpdatePassword(cookie.Value, body.NewToken)
	if err != nil {
		writeJSONResponse(rw, http.StatusOK, map[string]any{"success": false, "message": err.Error()})
		return
	}
	if session != "" {
		a.sessions.SetCookie(rw, session)
		writeJSONResponse(rw, http.StatusOK, map[string]any{"success": true, "message": "密码已设置"})
		return
	}
	clearSessionCookie(rw, a.sessions.CookieName, a.sessions.secure)
	writeJSONResponse(rw, http.StatusOK, map[string]any{"success": true, "message": "密码已更新，请重新登录"})
}

func (a *hostAuth) regenerate(rw http.ResponseWriter, r *http.Request) {
	token, err := a.credentials.Regenerate()
	if err != nil {
		writeJSONResponse(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	a.sessions.ClearAll()
	clearSessionCookie(rw, a.sessions.CookieName, a.sessions.secure)
	writeJSONResponse(rw, http.StatusOK, map[string]any{"success": true, "token": token, "message": "密码已重新生成"})
}

func (a *hostAuth) setupStatus(rw http.ResponseWriter, r *http.Request) {
	payload := map[string]any{"message": "统一 WebUI 配置状态"}
	for key, value := range a.credentials.SetupStatus() {
		payload[key] = value
	}
	writeJSONResponse(rw, http.StatusOK, payload)
}

func (a *hostAuth) completeSetup(rw http.ResponseWriter, r *http.Request) {
	if err := a.credentials.CompleteSetup(); err != nil {
		writeJSONResponse(rw, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	writeJSONResponse(rw, http.StatusOK, map[string]any{"success": true, "message": "配置完成"})
}

func (a *hostAuth) resetSetup(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		ResetPassword bool `json:"reset_password"`
	}
	if err := decodeLimitedJSON(r, &body, 4096); err != nil {
		writeJSONResponse(rw, http.StatusUnprocessableEntity, map[string]any{"detail": "请求体格式错误"})
		return
	}
	if err := a.credentials.ResetSetup(body.ResetPassword); err != nil {
		writeJSONResponse(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if body.ResetPassword {
		a.sessions.ClearAll()
		session, err := a.sessions.CreateSession(r.Header.Get("Origin"))
		if err != nil {
			writeJSONResponse(rw, http.StatusInternalServerError, map[string]any{"detail": "登录会话创建失败"})
			return
		}
		a.sessions.SetCookie(rw, session)
	}
	writeJSONResponse(rw, http.StatusOK, map[string]any{"success": true, "message": "配置状态已重置"})
}

func decodeLimitedJSON(r *http.Request, target any, limit int64) error {
	return json.NewDecoder(io.LimitReader(r.Body, limit)).Decode(target)
}

func clearSessionCookie(rw http.ResponseWriter, name string, secure bool) {
	http.SetCookie(rw, &http.Cookie{
		Name: name, Value: "", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Path: "/", Secure: secure,
	})
}

func writeJSONResponse(rw http.ResponseWriter, status int, payload any) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(payload)
}
