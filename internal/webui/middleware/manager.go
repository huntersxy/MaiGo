package auth

import (
	"sync/atomic"
)

// Manager 按 data/webui.json 的 access_token 校验请求令牌
//
// 对齐 Python src/webui/core/security.py 的 TokenManager 与
// src/webui/core/auth.py 的 is_token_valid：Cookie 直接持有访问口令本身，
// 每次请求与 webui.json 比对，没有服务端会话表，也没有独立管理密钥。
type Manager struct {
	validate     func(token string) bool
	secureCookie atomic.Bool
}

// NewManager 创建校验器；validate 返回该令牌是否等于当前 access_token
func NewManager(validate func(token string) bool) *Manager {
	return &Manager{validate: validate}
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

// Enabled 恒为 true：webui.json 在启动期必定创建，鉴权不存在关闭态
func (m *Manager) Enabled() bool {
	return m != nil && m.validate != nil
}

// ValidateToken 判定令牌是否为当前的 WebUI 访问口令
func (m *Manager) ValidateToken(token string) bool {
	if m == nil || m.validate == nil || token == "" {
		return false
	}
	return m.validate(token)
}
