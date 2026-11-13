package hooks

import (
	"fmt"
	"strings"
	"sync"
)

// SpecRegistry 命名 Hook 规格注册中心，对齐 host/hook_spec_registry.py
type SpecRegistry struct {
	mu    sync.RWMutex
	specs map[string]HookSpec
}

func NewSpecRegistry() *SpecRegistry {
	return &SpecRegistry{specs: make(map[string]HookSpec)}
}

func normalizeHookName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return "", fmt.Errorf("Hook 名称不能为空")
	}
	return n, nil
}

// RegisterHookSpecs 批量注册 Hook 规格
func (r *SpecRegistry) RegisterHookSpecs(specs ...HookSpec) []HookSpec {
	registered := make([]HookSpec, 0, len(specs))
	for _, spec := range specs {
		registered = append(registered, r.RegisterHookSpec(spec))
	}
	return registered
}

// RegisterHookSpec 注册单个 Hook 规格
func (r *SpecRegistry) RegisterHookSpec(spec HookSpec) HookSpec {
	name, err := normalizeHookName(spec.Name)
	if err != nil {
		panic(err)
	}
	spec.Name = name
	spec.Description = strings.TrimSpace(spec.Description)
	if spec.DefaultTimeoutMS < 0 {
		spec.DefaultTimeoutMS = 0
	}
	if spec.ParametersSchema == nil {
		spec.ParametersSchema = map[string]any{}
	}
	r.mu.Lock()
	r.specs[name] = spec
	r.mu.Unlock()
	return spec
}

// Clear 清空全部 Hook 规格，对齐 hook_spec_registry.clear
func (r *SpecRegistry) Clear() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.specs = make(map[string]HookSpec)
	r.mu.Unlock()
}

// UnregisterHookSpec 注销指定 Hook 规格，对齐 unregister_hook_spec
func (r *SpecRegistry) UnregisterHookSpec(name string) bool {
	if r == nil {
		return false
	}
	normalized, err := normalizeHookName(name)
	if err != nil {
		return false
	}
	r.mu.Lock()
	_, exists := r.specs[normalized]
	delete(r.specs, normalized)
	r.mu.Unlock()
	return exists
}

// GetHookSpec 按名称查询 Hook 规格
func (r *SpecRegistry) GetHookSpec(name string) (HookSpec, bool) {
	name, err := normalizeHookName(name)
	if err != nil {
		return HookSpec{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	spec, ok := r.specs[name]
	return spec, ok
}

// ListHookSpecs 返回已注册的全部 Hook 规格
func (r *SpecRegistry) ListHookSpecs() []HookSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]HookSpec, 0, len(r.specs))
	for _, spec := range r.specs {
		out = append(out, spec)
	}
	return out
}

// normalizeSubscription 补齐订阅默认值，对齐 HookHandlerEntry 的 mode/order 默认 blocking/normal
func normalizeSubscription(sub Subscription) (Subscription, bool) {
	name := strings.TrimSpace(sub.HookName)
	if name == "" {
		return Subscription{}, false
	}
	sub.HookName = name
	sub.PluginID = strings.TrimSpace(sub.PluginID)
	sub.HandlerName = strings.TrimSpace(sub.HandlerName)
	if sub.Mode == "" {
		sub.Mode = HandlerModeBlocking
	}
	if sub.Priority == "" {
		sub.Priority = PriorityNormal
	}
	sub.ErrorPolicy = NormalizeErrorPolicy(sub.ErrorPolicy)
	return sub, true
}

// NormalizeErrorPolicy 规范化异常处理策略，空值按 MaiBot 默认 skip 处理
func NormalizeErrorPolicy(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ErrorPolicyAbort:
		return ErrorPolicyAbort
	case ErrorPolicyLog:
		return ErrorPolicyLog
	default:
		return ErrorPolicySkip
	}
}
