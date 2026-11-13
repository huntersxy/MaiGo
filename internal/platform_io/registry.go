package platformio

// AdapterRegistry 集中保存已注册适配器
type AdapterRegistry struct {
	adapters map[string]Adapter
}

// NewAdapterRegistry 创建空注册表
func NewAdapterRegistry() *AdapterRegistry {
	return &AdapterRegistry{adapters: make(map[string]Adapter)}
}

// Register 注册适配器
func (r *AdapterRegistry) Register(adapter Adapter) error {
	desc := adapter.Descriptor()
	if desc.DriverID == "" {
		return errEmptyDriverID
	}
	if _, exists := r.adapters[desc.DriverID]; exists {
		return errDuplicateAdapter
	}
	r.adapters[desc.DriverID] = adapter
	return nil
}

// Unregister 注销适配器
func (r *AdapterRegistry) Unregister(driverID string) Adapter {
	adapter := r.adapters[driverID]
	delete(r.adapters, driverID)
	return adapter
}

// Get 按 ID 获取适配器
func (r *AdapterRegistry) Get(driverID string) Adapter {
	return r.adapters[driverID]
}

// List 列出适配器，可选按 kind / platform 过滤
func (r *AdapterRegistry) List(kind *DriverKind, platform string) []Adapter {
	out := make([]Adapter, 0, len(r.adapters))
	for _, adapter := range r.adapters {
		desc := adapter.Descriptor()
		if kind != nil && desc.Kind != *kind {
			continue
		}
		if platform != "" && desc.Platform != platform {
			continue
		}
		out = append(out, adapter)
	}
	return out
}

// Clear 清空注册表
func (r *AdapterRegistry) Clear() {
	r.adapters = make(map[string]Adapter)
}
