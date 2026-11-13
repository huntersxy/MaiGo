package platformio

// 适配器在线状态与账号描述查询，供 WebUI 与遥测接线使用

import "strings"

// AdapterDescriptors 返回当前已注册适配器的静态描述快照
func (m *Manager) AdapterDescriptors() []DriverDescriptor {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	adapters := m.registry.List(nil, "")
	out := make([]DriverDescriptor, 0, len(adapters))
	for _, adapter := range adapters {
		out = append(out, adapter.Descriptor())
	}
	return out
}

// GetDriver 按 driver_id 查找运行中的适配器；未注册时返回 nil
func (m *Manager) GetDriver(driverID string) Adapter {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.registry.Get(strings.TrimSpace(driverID))
}

// AdapterRouteStatus 适配器描述与其在指定路由键上的收发绑定状态
type AdapterRouteStatus struct {
	Descriptor   DriverDescriptor
	SendBound    bool
	ReceiveBound bool
}

// AdapterRouteStatuses 快照指定路由键下同平台适配器的收发绑定状态，对齐 Python adapter-status 数据源
func (m *Manager) AdapterRouteStatuses(routeKey RouteKey) []AdapterRouteStatus {
	if m == nil {
		return nil
	}
	normalized, err := routeKey.Normalize()
	if err != nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	adapters := m.registry.List(nil, normalized.Platform)
	out := make([]AdapterRouteStatus, 0, len(adapters))
	for _, adapter := range adapters {
		desc := adapter.Descriptor()
		out = append(out, AdapterRouteStatus{
			Descriptor:   desc,
			SendBound:    m.sendRouteTable.HasBindingForDriver(normalized, desc.DriverID),
			ReceiveBound: m.receiveRouteTable.HasBindingForDriver(normalized, desc.DriverID),
		})
	}
	return out
}

// AdapterOnline 按平台与账号查询适配器连接探针（IsConnected）。
// 账号为空时匹配该平台任一已连接适配器。
// WebUI bot-accounts / 自身消息门控请用 PluginAccountOnline：Python 以 PLUGIN 注册存在为在线。
func (m *Manager) AdapterOnline(platform, accountID string) bool {
	if m == nil {
		return false
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	accountID = strings.TrimSpace(accountID)
	if platform == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, adapter := range m.registry.List(nil, platform) {
		desc := adapter.Descriptor()
		if accountID != "" && strings.TrimSpace(desc.AccountID) != accountID {
			continue
		}
		if probe, ok := adapter.(interface{ IsConnected() bool }); ok && probe.IsConnected() {
			return true
		}
	}
	return false
}

// PluginAccountOnline 对齐 Python _online_account_pairs / get_online_bot_accounts：
// 仅 PLUGIN 驱动、account_id 非空，注册在册即在线。
func (m *Manager) PluginAccountOnline(platform, accountID string) bool {
	if m == nil {
		return false
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	accountID = strings.TrimSpace(accountID)
	if platform == "" || accountID == "" {
		return false
	}
	for _, descriptor := range m.AdapterDescriptors() {
		if descriptor.Kind != DriverKindPlugin {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(descriptor.Platform), platform) {
			continue
		}
		if strings.TrimSpace(descriptor.AccountID) == accountID {
			return true
		}
	}
	return false
}
