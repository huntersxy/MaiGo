package host

import "strings"

// MessageGateways 查询插件声明的消息网关，对齐 ComponentRegistry.get_message_gateways
func (h *Host) MessageGateways(pluginID, platform, routeType string, enabledOnly bool, sessionID string) []*ComponentEntry {
	if h == nil || h.components == nil {
		return nil
	}
	return h.components.MessageGateways(pluginID, platform, routeType, enabledOnly, sessionID)
}

// MessageGatewayCount 返回插件声明的全部消息网关数量
func (h *Host) MessageGatewayCount(pluginID string) int {
	return len(h.MessageGateways(strings.TrimSpace(pluginID), "", "", false, ""))
}

// HomeCards 查询全部 HOME_CARD 组件
func (h *Host) HomeCards(enabledOnly bool) ([]*ComponentEntry, error) {
	if h == nil || h.components == nil {
		return nil, nil
	}
	return h.components.ComponentsByType(ComponentTypeHomeCard, enabledOnly, ComponentFilter{})
}

// ComponentTypeCount 返回指定类型组件数量，TOOL 组件计数
func (h *Host) ComponentTypeCount(componentType string, enabledOnly bool, filter ComponentFilter) int {
	if h == nil || h.components == nil {
		return 0
	}
	entries, err := h.components.ComponentsByType(componentType, enabledOnly, filter)
	if err != nil {
		return 0
	}
	return len(entries)
}
