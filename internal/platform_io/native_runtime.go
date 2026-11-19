package platformio

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// NativeAdapterFactory 在 StartRuntime 时按配置构造 Adapter；Register 只登记工厂
type NativeAdapterFactory func(ctx context.Context, pluginID string, config map[string]any) (Adapter, error)

// NativeRegistration 进程内 Go 适配器登记项
type NativeRegistration struct {
	PluginID      string
	DirectoryName string
	ConfigRoot    string
	GatewayNames  []string
	DefaultConfig map[string]any
	ConfigSchema  map[string]any
	Factory       NativeAdapterFactory
}

// NativeRuntimeState 已启动实例的运行态
type NativeRuntimeState struct {
	PluginID string
	Adapters map[string]Adapter
	Config   map[string]any
	Started  bool
}

// NativeRuntime 进程内适配器注册表：Register 不 Construct；StartRuntime 才接线
type NativeRuntime struct {
	mu           sync.RWMutex
	registrations map[string]*NativeRegistration
	states        map[string]*NativeRuntimeState
}

var defaultNativeRuntime = NewNativeRuntime()

// DefaultNativeRuntime 返回进程级原生适配器注册表
func DefaultNativeRuntime() *NativeRuntime {
	return defaultNativeRuntime
}

// NewNativeRuntime 构造空注册表
func NewNativeRuntime() *NativeRuntime {
	return &NativeRuntime{
		registrations: make(map[string]*NativeRegistration),
		states:        make(map[string]*NativeRuntimeState),
	}
}

// Register 登记工厂与配置根；enabled=false 时不得 Construct/AddDriver
func (r *NativeRuntime) Register(reg NativeRegistration) error {
	if r == nil {
		return fmt.Errorf("NativeRuntime 未初始化")
	}
	reg.PluginID = strings.TrimSpace(reg.PluginID)
	if reg.PluginID == "" {
		return fmt.Errorf("plugin_id 不能为空")
	}
	if reg.Factory == nil {
		return fmt.Errorf("原生适配器 %s 未提供 Factory", reg.PluginID)
	}
	reg.ConfigRoot = strings.TrimSpace(reg.ConfigRoot)
	reg.DirectoryName = strings.TrimSpace(reg.DirectoryName)
	names := make([]string, 0, len(reg.GatewayNames))
	seen := map[string]struct{}{}
	for _, name := range reg.GatewayNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	reg.GatewayNames = names
	r.mu.Lock()
	defer r.mu.Unlock()
	cloned := reg
	r.registrations[reg.PluginID] = &cloned
	return nil
}

// Unregister 移除登记（不负责 teardown 驱动）
func (r *NativeRuntime) Unregister(pluginID string) {
	if r == nil {
		return
	}
	pluginID = strings.TrimSpace(pluginID)
	r.mu.Lock()
	delete(r.registrations, pluginID)
	delete(r.states, pluginID)
	r.mu.Unlock()
}

// IsRegistered 判断是否已登记为进程内原生适配器
func (r *NativeRuntime) IsRegistered(pluginID string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.registrations[strings.TrimSpace(pluginID)]
	return ok
}

// Get 返回登记副本
func (r *NativeRuntime) Get(pluginID string) (NativeRegistration, bool) {
	if r == nil {
		return NativeRegistration{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.registrations[strings.TrimSpace(pluginID)]
	if !ok || reg == nil {
		return NativeRegistration{}, false
	}
	return *reg, true
}

// ListRegistered 返回已登记插件 ID（排序）
func (r *NativeRuntime) ListRegistered() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.registrations))
	for pluginID := range r.registrations {
		out = append(out, pluginID)
	}
	sort.Strings(out)
	return out
}

// IsStarted 判断原生运行时是否已启动
func (r *NativeRuntime) IsStarted(pluginID string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	state := r.states[strings.TrimSpace(pluginID)]
	return state != nil && state.Started
}

// StartedAdapter 返回已启动的主 Adapter（空槽位）
func (r *NativeRuntime) StartedAdapter(pluginID string) (Adapter, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	state := r.states[strings.TrimSpace(pluginID)]
	if state == nil || !state.Started || state.Adapters == nil {
		return nil, false
	}
	adapter, ok := state.Adapters[""]
	return adapter, ok && adapter != nil
}

// StartRuntime 构造 Adapter 并记入运行态；调用方负责 RegisterNativeGatewayDriver / 拨号
func (r *NativeRuntime) StartRuntime(ctx context.Context, pluginID string, config map[string]any) (Adapter, error) {
	if r == nil {
		return nil, fmt.Errorf("NativeRuntime 未初始化")
	}
	pluginID = strings.TrimSpace(pluginID)
	r.mu.RLock()
	reg := r.registrations[pluginID]
	r.mu.RUnlock()
	if reg == nil {
		return nil, fmt.Errorf("未登记的原生适配器: %s", pluginID)
	}
	if config == nil {
		config = map[string]any{}
	}
	adapter, err := reg.Factory(ctx, pluginID, config)
	if err != nil {
		return nil, err
	}
	if adapter == nil {
		return nil, fmt.Errorf("原生适配器 %s Factory 返回空", pluginID)
	}
	r.mu.Lock()
	r.states[pluginID] = &NativeRuntimeState{
		PluginID: pluginID,
		Adapters: map[string]Adapter{"": adapter},
		Config:   cloneStringAnyMap(config),
		Started:  true,
	}
	r.mu.Unlock()
	return adapter, nil
}

// StopRuntime 清除运行态；驱动卸载由 Host NativeTeardown 负责
func (r *NativeRuntime) StopRuntime(pluginID string) {
	if r == nil {
		return
	}
	pluginID = strings.TrimSpace(pluginID)
	r.mu.Lock()
	delete(r.states, pluginID)
	r.mu.Unlock()
}

// GatewayNames 返回登记的网关名
func (r *NativeRuntime) GatewayNames(pluginID string) []string {
	reg, ok := r.Get(pluginID)
	if !ok {
		return nil
	}
	out := make([]string, len(reg.GatewayNames))
	copy(out, reg.GatewayNames)
	return out
}
