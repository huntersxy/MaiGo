// Package authutil 提供 WebUI 的会话令牌与一次性 WebSocket 令牌工具。
//
// 拆分 webui/routers 后，这些函数被父包（/api/webui/ws-token 路由）与
// websocket 子包（/api/ws 握手）同时需要；放在任一侧都会造成循环引用，
// 因此抽到独立包。对齐 Python src/webui/routers/websocket/auth.py 与
// src/webui/dependencies.py 里共用的 token 校验。
package authutil

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"

	auth "maibot-go/internal/webui/middleware"
)

// TokenExpireSeconds 一次性 WS 令牌有效期
const TokenExpireSeconds = 60

type tokenRecord struct {
	expiresAt    time.Time
	sessionToken string
}

var (
	mu     sync.Mutex
	tokens = map[string]tokenRecord{}
)

func cleanupExpired() {
	now := time.Now()
	for token, rec := range tokens {
		if now.After(rec.expiresAt) {
			delete(tokens, token)
		}
	}
}

// Generate 生成一次性 WS 令牌，返回令牌与有效期（秒）
func Generate(sessionToken string) (string, int) {
	mu.Lock()
	defer mu.Unlock()
	cleanupExpired()
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	tokens[token] = tokenRecord{
		expiresAt:    time.Now().Add(TokenExpireSeconds * time.Second),
		sessionToken: sessionToken,
	}
	return token, TokenExpireSeconds
}

// Consume 校验并消费一次性 WS 令牌；返回其绑定的会话令牌
func Consume(sessionToken string) (string, bool) {
	sessionToken = strings.TrimSpace(sessionToken)
	if sessionToken == "" {
		return "", false
	}
	mu.Lock()
	rec, ok := tokens[sessionToken]
	if ok {
		delete(tokens, sessionToken)
	}
	mu.Unlock()
	if !ok || time.Now().After(rec.expiresAt) {
		return "", false
	}
	return rec.sessionToken, true
}

// SessionToken 从请求 Cookie 中取 WebUI 会话令牌
func SessionToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	cookie, err := r.Cookie(auth.WebUISessionCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

// ValidSessionToken 校验会话令牌是否有效
func ValidSessionToken(manager *auth.Manager, token string) bool {
	return manager.ValidateToken(strings.TrimSpace(token))
}

// AuthorizeWebSocket 支持 query token（一次性 WS 令牌或有效会话）与 Cookie 会话
func AuthorizeWebSocket(manager *auth.Manager, r *http.Request) bool {
	if manager == nil || !manager.Enabled() || r == nil {
		return false
	}
	if queryToken := strings.TrimSpace(r.URL.Query().Get("token")); queryToken != "" {
		if bound, ok := Consume(queryToken); ok && ValidSessionToken(manager, bound) {
			return true
		}
		if ValidSessionToken(manager, queryToken) {
			return true
		}
	}
	return ValidSessionToken(manager, SessionToken(r))
}

// HandshakeSession 返回本次 WS 握手依据的会话令牌，供连接建立后复核
//
// 一次性 WS 令牌只在握手期有效，必须换成它绑定的会话令牌做后续校验。
// validate 为空时退回内置判定（仅内存会话 + admin key）。
func HandshakeSession(manager *auth.Manager, r *http.Request, validate ...func(string) bool) string {
	if manager == nil || !manager.Enabled() || r == nil {
		return ""
	}
	check := func(token string) bool { return ValidSessionToken(manager, token) }
	if len(validate) > 0 && validate[0] != nil {
		check = validate[0]
	}
	if queryToken := strings.TrimSpace(r.URL.Query().Get("token")); queryToken != "" {
		if bound, ok := Consume(queryToken); ok && check(bound) {
			return bound
		}
		if check(queryToken) {
			return queryToken
		}
	}
	sessionToken := SessionToken(r)
	if check(sessionToken) {
		return sessionToken
	}
	return ""
}
