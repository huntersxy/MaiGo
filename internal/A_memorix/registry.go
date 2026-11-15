package memorix

import "sync"

// Registry 进程内 Memorix Host 入口；默认 DisabledHost，
// 由启动装配在原生内核就绪后注入 AMemorixHostService。
type Registry struct {
	mu   sync.RWMutex
	host Host
}

var defaultRegistry = &Registry{host: DisabledHost{}}

// DefaultRegistry 返回全局 Registry
func DefaultRegistry() *Registry {
	return defaultRegistry
}

// Host 返回当前 Host 实现
func (r *Registry) Host() Host {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.host
}

// SetHost 注入 Host（nil 时恢复 DisabledHost）
func (r *Registry) SetHost(host Host) {
	if host == nil {
		host = DisabledHost{}
	}
	r.mu.Lock()
	r.host = host
	r.mu.Unlock()
}
