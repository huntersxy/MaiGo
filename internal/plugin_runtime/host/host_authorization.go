package host

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	capnames "maibot-go/internal/plugin_runtime/capabilities"
)

// CapabilityAuthorization 插件能力令牌表，对齐 host/authorization.py
//
// Runner 注册插件时签发令牌，仅令牌内的能力允许通过 cap.call 调用；
// api.replace_dynamic 永远放行，保证插件仍可自行登记动态 API 后收敛权限。
type CapabilityAuthorization struct {
	mu     sync.RWMutex
	tokens map[string]map[string]struct{}
}

// NewCapabilityAuthorization 构造空的授权表
func NewCapabilityAuthorization() *CapabilityAuthorization {
	return &CapabilityAuthorization{tokens: make(map[string]map[string]struct{})}
}

func normalizeCapabilityNames(names []string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		out[trimmed] = struct{}{}
	}
	return out
}

// RegisterPlugin 为插件签发能力令牌；重复注册以最新声明覆盖
func (a *CapabilityAuthorization) RegisterPlugin(pluginID string, capabilities []string) {
	if a == nil {
		return
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return
	}
	a.mu.Lock()
	a.tokens[pluginID] = normalizeCapabilityNames(capabilities)
	a.mu.Unlock()
}

// Revoke 撤销单个插件的令牌，返回是否存在
func (a *CapabilityAuthorization) Revoke(pluginID string) bool {
	if a == nil {
		return false
	}
	pluginID = strings.TrimSpace(pluginID)
	a.mu.Lock()
	_, existed := a.tokens[pluginID]
	delete(a.tokens, pluginID)
	a.mu.Unlock()
	return existed
}

// Clear 清空全部令牌
func (a *CapabilityAuthorization) Clear() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.tokens = make(map[string]map[string]struct{})
	a.mu.Unlock()
}

// Check 校验插件是否获授权，返回 false 时同时给出对齐 Python 的拒绝原因
func (a *CapabilityAuthorization) Check(pluginID, capability string) (bool, string) {
	capability = strings.TrimSpace(capability)
	if capnames.IsAlwaysAllowed(capability) {
		return true, ""
	}
	if a == nil {
		return false, fmt.Sprintf("插件 %s 未注册能力令牌", pluginID)
	}
	a.mu.RLock()
	allowed, registered := a.tokens[strings.TrimSpace(pluginID)]
	if registered {
		_, ok := allowed[capability]
		a.mu.RUnlock()
		if !ok {
			return false, fmt.Sprintf("插件 %s 未获授权能力: %s", pluginID, capability)
		}
		return true, ""
	}
	a.mu.RUnlock()
	return false, fmt.Sprintf("插件 %s 未注册能力令牌", pluginID)
}

// Get 返回插件令牌中的能力列表（排序后的副本）
func (a *CapabilityAuthorization) Get(pluginID string) ([]string, bool) {
	if a == nil {
		return nil, false
	}
	a.mu.RLock()
	allowed, ok := a.tokens[strings.TrimSpace(pluginID)]
	a.mu.RUnlock()
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(allowed))
	for name := range allowed {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, true
}

// ListPlugins 返回已签发令牌的插件 ID
func (a *CapabilityAuthorization) ListPlugins() []string {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	out := make([]string, 0, len(a.tokens))
	for pluginID := range a.tokens {
		out = append(out, pluginID)
	}
	a.mu.RUnlock()
	sort.Strings(out)
	return out
}

// CapabilityError 能力执行错误，携带协议错误码，供 cap.call 还原 RPCError
type CapabilityError struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *CapabilityError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// NewCapabilityError 构造带错误码的能力错误
func NewCapabilityError(code, message string) *CapabilityError {
	return &CapabilityError{Code: code, Message: message}
}

// capabilityFailure 构造 success=false 的能力返回，对齐 Python 能力层参数校验失败语义
func capabilityFailure(format string, args ...any) map[string]any {
	return map[string]any{"success": false, "error": fmt.Sprintf(format, args...)}
}

// capabilitySuccess 构造 success=true 的能力返回
func capabilitySuccess(result any) map[string]any {
	return map[string]any{"success": true, "result": result}
}
