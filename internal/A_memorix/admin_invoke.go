package memorix

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// AdminInvoker 是宿主暴露给 WebUI 的通用管理调用面
// 对齐 Python host_service.invoke(component, args, timeout_ms)
type AdminInvoker interface {
	InvokeAdmin(ctx context.Context, component string, args map[string]any, timeoutMs *int) (any, error)
}

// AdminUnsupportedError 表示当前后端没有对应的管理调用面
// 这不是服务故障，WebUI 应展示能力边界而不是 501
type AdminUnsupportedError struct {
	Backend   string
	Component string
	Action    string
	Reason    string
}

func (e *AdminUnsupportedError) Error() string {
	if e == nil {
		return "A_Memorix 管理调用不受支持"
	}
	backend := strings.TrimSpace(e.Backend)
	if backend == "" {
		backend = "unknown"
	}
	target := strings.TrimSpace(e.Component)
	if action := strings.TrimSpace(e.Action); action != "" {
		target += "/" + action
	}
	if target == "/" || target == "" {
		return fmt.Sprintf("A_Memorix 后端 %s 不支持管理调用", backend)
	}
	return fmt.Sprintf("A_Memorix 后端 %s 不支持管理调用: %s", backend, target)
}

// ToResponse 输出稳定的 unsupported 语义，供 WebUI 直接返回 200
func (e *AdminUnsupportedError) ToResponse() map[string]any {
	if e == nil {
		return map[string]any{
			"success": false, "unsupported": true,
			"reason": "admin_invoke_unsupported",
			"error":  "A_Memorix 管理调用不受支持",
		}
	}
	backend := strings.TrimSpace(e.Backend)
	if backend == "" {
		backend = "unknown"
	}
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "admin_invoke_unsupported"
	}
	out := map[string]any{
		"success":     false,
		"unsupported": true,
		"backend":     backend,
		"reason":      reason,
		"error":       e.Error(),
	}
	if component := strings.TrimSpace(e.Component); component != "" {
		out["component"] = component
	}
	if action := strings.TrimSpace(e.Action); action != "" {
		out["action"] = action
	}
	return out
}

// IsAdminUnsupported 判断错误是否为能力边界而非真实故障
func IsAdminUnsupported(err error) bool {
	var unsupported *AdminUnsupportedError
	return errors.As(err, &unsupported)
}

// NewAdminUnsupportedError 构造管理能力边界错误
func NewAdminUnsupportedError(backend, component, action, reason string) *AdminUnsupportedError {
	return &AdminUnsupportedError{
		Backend: backend, Component: component, Action: action, Reason: reason,
	}
}

// BackendName 返回宿主的真实后端名，用于能力边界诊断
func BackendName(host Host) string {
	switch typed := host.(type) {
	case nil:
		return "none"
	case DisabledHost:
		return "disabled"
	case *AMemorixHostService:
		return "native"
	case *RemoteHost:
		return "remote"
	default:
		_ = typed
		return "custom"
	}
}
