package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const SessionCookieName = "maibot_admin_session"

// SessionTTL 对齐 Python src/webui/core/auth.py:16 的 COOKIE_MAX_AGE
// Python 会话令牌没有服务端过期，Go 内存会话保留 7 天
const SessionTTL = 7 * 24 * time.Hour

// SessionCookieMaxAgeSeconds 对齐 Python src/webui/core/auth.py:16 COOKIE_MAX_AGE=604800
const SessionCookieMaxAgeSeconds = 7 * 24 * 60 * 60

type Manager struct {
	adminKey string
	ttl      time.Duration

	secureCookie atomic.Bool

	mu       sync.RWMutex
	sessions map[string]time.Time
}

func NewManager(adminKey string, ttl time.Duration) *Manager {
	if ttl <= 0 {
		ttl = SessionTTL
	}
	return &Manager{
		adminKey: adminKey,
		ttl:      ttl,
		sessions: make(map[string]time.Time),
	}
}

func (m *Manager) SetSecureCookie(enabled bool) {
	if m == nil {
		return
	}
	m.secureCookie.Store(enabled)
}

func (m *Manager) SecureCookie() bool {
	return m != nil && m.secureCookie.Load()
}

func (m *Manager) Enabled() bool {
	return m != nil && m.adminKey != ""
}

func (m *Manager) CheckKey(input string) bool {
	if !m.Enabled() {
		return false
	}
	if len(input) != len(m.adminKey) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(input), []byte(m.adminKey)) == 1
}

func (m *Manager) CreateSession() (string, time.Time, error) {
	if !m.Enabled() {
		return "", time.Time{}, errors.New("admin auth disabled")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}

	token := base64.RawURLEncoding.EncodeToString(raw)
	expiresAt := time.Now().Add(m.ttl)

	m.mu.Lock()
	// 顺手清理其他已过期的会话，避免长期运行时无访问的过期令牌堆积
	now := time.Now()
	for token, expires := range m.sessions {
		if now.After(expires) {
			delete(m.sessions, token)
		}
	}
	m.sessions[token] = expiresAt
	m.mu.Unlock()

	return token, expiresAt, nil
}

func (m *Manager) IsAuthenticated(token string) bool {
	if token == "" {
		return false
	}

	m.mu.RLock()
	expiresAt, ok := m.sessions[token]
	m.mu.RUnlock()
	if !ok {
		return false
	}

	if time.Now().After(expiresAt) {
		m.DeleteSession(token)
		return false
	}

	return true
}

func (m *Manager) DeleteSession(token string) {
	if token == "" {
		return
	}

	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}
