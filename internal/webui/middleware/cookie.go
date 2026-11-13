package auth

import (
	"net/http"
	"time"
)

const WebUISessionCookieName = "maibot_session"

// SetSessionCookie 写入 maibot_admin_session，与 templ /login 一致
func (m *Manager) SetSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	setNamedSessionCookie(w, SessionCookieName, token, expiresAt, m.SecureCookie())
}

// SetWebUISessionCookie 写入 Dashboard 使用的 maibot_session
func (m *Manager) SetWebUISessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	setNamedSessionCookie(w, WebUISessionCookieName, token, expiresAt, m.SecureCookie())
}

// ClearSessionCookie 清除 maibot_admin_session
func (m *Manager) ClearSessionCookie(w http.ResponseWriter) {
	clearNamedSessionCookie(w, SessionCookieName, m.SecureCookie())
}

// ClearWebUISessionCookie 清除 maibot_session
func (m *Manager) ClearWebUISessionCookie(w http.ResponseWriter) {
	clearNamedSessionCookie(w, WebUISessionCookieName, m.SecureCookie())
}

func setNamedSessionCookie(w http.ResponseWriter, name, token string, expiresAt time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
		MaxAge:   SessionCookieMaxAgeSeconds,
	})
}

func clearNamedSessionCookie(w http.ResponseWriter, name string, secure bool) {
	sameSite := http.SameSiteLaxMode
	if secure {
		sameSite = http.SameSiteStrictMode
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: sameSite,
		MaxAge:   -1,
	})
}
