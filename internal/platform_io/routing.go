package platformio

import "sort"

// RouteTable 维护单张路由绑定表
type RouteTable struct {
	bindings map[string]map[string]RouteBinding
}

// NewRouteTable 创建空路由表
func NewRouteTable() *RouteTable {
	return &RouteTable{bindings: make(map[string]map[string]RouteBinding)}
}

// Bind 注册或更新绑定
func (t *RouteTable) Bind(binding RouteBinding) {
	key := binding.RouteKey.tableKey()
	if t.bindings[key] == nil {
		t.bindings[key] = make(map[string]RouteBinding)
	}
	t.bindings[key][binding.DriverID] = binding
}

// Unbind 移除绑定；driverID 为空时移除该路由键下全部绑定
func (t *RouteTable) Unbind(routeKey RouteKey, driverID string) []RouteBinding {
	key := routeKey.tableKey()
	bindingMap, ok := t.bindings[key]
	if !ok {
		return nil
	}
	if driverID == "" {
		removed := sortBindings(flattenBindings(bindingMap))
		delete(t.bindings, key)
		return removed
	}
	removedBinding, ok := bindingMap[driverID]
	if !ok {
		return nil
	}
	delete(bindingMap, driverID)
	if len(bindingMap) == 0 {
		delete(t.bindings, key)
	}
	return []RouteBinding{removedBinding}
}

// RemoveBindingsByDriver 移除某驱动在整张表上的全部绑定
func (t *RouteTable) RemoveBindingsByDriver(driverID string) []RouteBinding {
	var removed []RouteBinding
	var emptyKeys []string
	for key, bindingMap := range t.bindings {
		if b, ok := bindingMap[driverID]; ok {
			removed = append(removed, b)
			delete(bindingMap, driverID)
		}
		if len(bindingMap) == 0 {
			emptyKeys = append(emptyKeys, key)
		}
	}
	for _, key := range emptyKeys {
		delete(t.bindings, key)
	}
	return sortBindings(removed)
}

// ListBindings 列出绑定；routeKey 为零值时返回全部
func (t *RouteTable) ListBindings(routeKey RouteKey) []RouteBinding {
	if routeKey.Platform == "" {
		var all []RouteBinding
		for _, bindingMap := range t.bindings {
			all = append(all, flattenBindings(bindingMap)...)
		}
		return sortBindings(all)
	}
	bindingMap := t.bindings[routeKey.tableKey()]
	return sortBindings(flattenBindings(bindingMap))
}

// ResolveBindings 按从具体到宽泛的顺序解析候选绑定
func (t *RouteTable) ResolveBindings(routeKey RouteKey) []RouteBinding {
	var resolved []RouteBinding
	seen := make(map[string]struct{})
	for _, candidate := range routeKey.ResolutionOrder() {
		for _, binding := range t.ListBindings(candidate) {
			if _, ok := seen[binding.DriverID]; ok {
				continue
			}
			seen[binding.DriverID] = struct{}{}
			resolved = append(resolved, binding)
		}
	}
	return resolved
}

// HasBindingForDriver 判断驱动是否在当前路由键解析结果中
func (t *RouteTable) HasBindingForDriver(routeKey RouteKey, driverID string) bool {
	for _, binding := range t.ResolveBindings(routeKey) {
		if binding.DriverID == driverID {
			return true
		}
	}
	return false
}

func flattenBindings(m map[string]RouteBinding) []RouteBinding {
	if len(m) == 0 {
		return nil
	}
	out := make([]RouteBinding, 0, len(m))
	for _, b := range m {
		out = append(out, b)
	}
	return out
}

func sortBindings(bindings []RouteBinding) []RouteBinding {
	sort.Slice(bindings, func(i, j int) bool {
		return bindings[i].Priority > bindings[j].Priority
	})
	return bindings
}
