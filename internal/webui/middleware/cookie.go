package auth

import (
	"net/http"
	"time"
)

const WebUISessionCookieName = "maibot_session"

// SessionCookieMaxAgeSeconds 对齐 Python src/webui/core/auth.py 的 COOKIE_MAX_AGE=604800
const SessionCookieMaxAgeSeconds = 7 * 24 * 60 * 60

// SetSessionCookie 写入 maibot_session，对齐 Python set_auth_cookie
func (m *Manager) SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     WebUISessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.SecureCookie(),
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(SessionCookieMaxAgeSeconds * time.Second),
		MaxAge:   SessionCookieMaxAgeSeconds,
	})
}

// ClearSessionCookie 清除 maibot_session，对齐 Python delete_auth_cookie
func (m *Manager) ClearSessionCookie(w http.ResponseWriter) {
	sameSite := http.SameSiteLaxMode
	if m.SecureCookie() {
		sameSite = http.SameSiteStrictMode
	}
	http.SetCookie(w, &http.Cookie{
		Name:     WebUISessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   m.SecureCookie(),
		SameSite: sameSite,
		MaxAge:   -1,
	})
}
