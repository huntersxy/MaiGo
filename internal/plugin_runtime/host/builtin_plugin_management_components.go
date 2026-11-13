package host

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type managementComponentEntry struct {
	Name          string
	FullName      string
	PluginID      string
	ComponentType string
	Enabled       bool
	entry         *ComponentEntry
	event         *EventHandlerComponent
}

func managementComponentTypeLabel(componentType string) string {
	switch strings.ToUpper(strings.TrimSpace(componentType)) {
	case ComponentTypeTool, ComponentTypeAction:
		return "tool"
	case ComponentTypeCommand:
		return "command"
	case ComponentTypeEventHandler:
		return "event_handler"
	case ComponentTypeMessageGateway:
		return "message_gateway"
	case ComponentTypeHomeCard:
		return "home_card"
	case ComponentTypeReplyExtension:
		return "reply_extension"
	case ComponentTypeAPI:
		return "api"
	default:
		return strings.ToLower(strings.TrimSpace(componentType))
	}
}

func (h *Host) managementComponents(streamID string) []managementComponentEntry {
	if h == nil || h.components == nil {
		return nil
	}
	out := make([]managementComponentEntry, 0)
	appendEntry := func(entry *ComponentEntry) {
		if entry == nil {
			return
		}
		enabled := entry.Enabled
		if strings.TrimSpace(streamID) != "" {
			enabled = checkComponentEnabled(entry, ComponentFilter{SessionID: streamID})
		}
		out = append(out, managementComponentEntry{
			Name:          entry.Name,
			FullName:      entry.FullName,
			PluginID:      entry.PluginID,
			ComponentType: managementComponentTypeLabel(entry.ComponentType),
			Enabled:       enabled,
			entry:         entry,
		})
	}
	for _, componentType := range []string{
		ComponentTypeCommand,
		ComponentTypeTool,
		ComponentTypeMessageGateway,
		ComponentTypeHomeCard,
		ComponentTypeReplyExtension,
	} {
		entries, err := h.components.ComponentsByType(componentType, false, ComponentFilter{})
		if err != nil {
			continue
		}
		for _, entry := range entries {
			appendEntry(entry)
		}
	}
	for _, handler := range h.AllEventHandlers() {
		handlerCopy := handler
		enabled := handler.Enabled
		if strings.TrimSpace(streamID) != "" && enabled {
			enabled = h.EventHandlerLocalEnabled(handler.FullName, streamID)
		}
		out = append(out, managementComponentEntry{
			Name:          handler.Name,
			FullName:      handler.FullName,
			PluginID:      handler.PluginID,
			ComponentType: "event_handler",
			Enabled:       enabled,
			event:         &handlerCopy,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].FullName < out[j].FullName })
	return out
}

func formatManagementComponents(entries []managementComponentEntry) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		parts = append(parts, fmt.Sprintf("%s (%s)", entry.Name, entry.ComponentType))
	}
	return strings.Join(parts, ", ")
}

func (s *pluginManagementState) handleComponentList(ctx context.Context, host *Host, streamID, sub, scope, componentType string) {
	if sub == "" {
		entries := host.managementComponents("")
		if len(entries) == 0 {
			_ = s.sendText(ctx, host, streamID, "没有注册的组件")
			return
		}
		_ = s.sendText(ctx, host, streamID, "已注册的组件: "+formatManagementComponents(entries))
		return
	}
	if sub == "type" {
		componentType = strings.ToLower(strings.TrimSpace(componentType))
		if componentType != "tool" && componentType != "command" && componentType != "event_handler" {
			_ = s.sendText(ctx, host, streamID, "未知组件类型: "+componentType)
			return
		}
		entries := filterManagementComponents(host.managementComponents(""), "", componentType)
		if len(entries) == 0 {
			_ = s.sendText(ctx, host, streamID, "没有注册的 "+componentType+" 组件")
			return
		}
		_ = s.sendText(ctx, host, streamID, "注册的 "+componentType+" 组件: "+formatManagementComponents(entries))
		return
	}
	if sub != "enabled" && sub != "disabled" {
		_ = s.sendText(ctx, host, streamID, "插件管理命令不合法")
		return
	}
	if strings.TrimSpace(scope) == "" {
		scope = "global"
	}
	scopeLabel := "本聊天"
	querySession := streamID
	if scope == "global" {
		scopeLabel = "全局"
		querySession = ""
	}
	entries := filterManagementComponents(host.managementComponents(querySession), sub, "")
	label := "已启用"
	if sub == "disabled" {
		label = "已禁用"
	}
	if len(entries) == 0 {
		_ = s.sendText(ctx, host, streamID, fmt.Sprintf("没有满足条件的%s%s组件", label, scopeLabel))
		return
	}
	_ = s.sendText(ctx, host, streamID, fmt.Sprintf("满足条件的%s%s组件: %s", label, scopeLabel, formatManagementComponents(entries)))
}

func filterManagementComponents(entries []managementComponentEntry, mode, componentType string) []managementComponentEntry {
	out := make([]managementComponentEntry, 0, len(entries))
	for _, entry := range entries {
		if componentType != "" && entry.ComponentType != componentType {
			continue
		}
		if mode == "enabled" && !entry.Enabled {
			continue
		}
		if mode == "disabled" && entry.Enabled {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func (s *pluginManagementState) handleComponentToggle(ctx context.Context, host *Host, streamID, action, scope, componentName, componentType string) {
	if action != "enable" && action != "disable" {
		_ = s.sendText(ctx, host, streamID, "插件管理命令不合法")
		return
	}
	if scope != "global" && scope != "local" {
		_ = s.sendText(ctx, host, streamID, "插件管理命令不合法")
		return
	}
	componentType = strings.ToLower(strings.TrimSpace(componentType))
	if componentType != "tool" && componentType != "command" && componentType != "event_handler" {
		_ = s.sendText(ctx, host, streamID, "未知组件类型: "+componentType)
		return
	}
	enable := action == "enable"
	scopeLabel := "全局"
	sessionID := ""
	if scope == "local" {
		scopeLabel = "本地"
		sessionID = streamID
	}
	actionLabel := "启用"
	if !enable {
		actionLabel = "禁用"
	}
	failMessage := fmt.Sprintf("%s%s组件失败: %s", scopeLabel, actionLabel, componentName)

	if componentType == "event_handler" {
		fullName, ok := host.resolveManagementEventHandler(componentName)
		if !ok {
			_ = s.sendText(ctx, host, streamID, failMessage)
			return
		}
		changed := false
		if scope == "local" {
			changed = host.SetEventHandlerLocalEnabled(fullName, sessionID, enable)
		} else {
			changed = host.setEventHandlerEnabled(fullName, enable)
		}
		if !changed {
			_ = s.sendText(ctx, host, streamID, failMessage)
			return
		}
		_ = s.sendText(ctx, host, streamID, fmt.Sprintf("%s%s组件成功: %s", scopeLabel, actionLabel, componentName))
		return
	}

	fullName, ok := host.resolveManagementComponent(componentName, componentType)
	if !ok || !host.components.SetComponentEnabled(fullName, enable, sessionID) {
		_ = s.sendText(ctx, host, streamID, failMessage)
		return
	}
	_ = s.sendText(ctx, host, streamID, fmt.Sprintf("%s%s组件成功: %s", scopeLabel, actionLabel, componentName))
}

func (h *Host) resolveManagementComponent(name, componentType string) (string, bool) {
	name = strings.TrimSpace(name)
	normalized := ComponentTypeTool
	switch componentType {
	case "command":
		normalized = ComponentTypeCommand
	case "tool":
		normalized = ComponentTypeTool
	}
	if entry := h.components.GetComponent(name); entry != nil && entry.ComponentType == normalized {
		return entry.FullName, true
	}
	entries, err := h.components.ComponentsByType(normalized, false, ComponentFilter{})
	if err != nil {
		return "", false
	}
	matches := make([]*ComponentEntry, 0, 1)
	for _, entry := range entries {
		if entry.Name == name {
			matches = append(matches, entry)
		}
	}
	if len(matches) != 1 {
		return "", false
	}
	return matches[0].FullName, true
}

func (h *Host) resolveManagementEventHandler(name string) (string, bool) {
	name = strings.TrimSpace(name)
	handlers := h.AllEventHandlers()
	for _, handler := range handlers {
		if handler.FullName == name {
			return handler.FullName, true
		}
	}
	matches := make([]string, 0, 1)
	for _, handler := range handlers {
		if handler.Name == name {
			matches = append(matches, handler.FullName)
		}
	}
	if len(matches) != 1 {
		return "", false
	}
	return matches[0], true
}

func (h *Host) setEventHandlerEnabled(fullName string, enabled bool) bool {
	h.eventMu.Lock()
	defer h.eventMu.Unlock()
	for pluginID, group := range h.eventHandlers {
		for index := range group {
			if group[index].FullName == strings.TrimSpace(fullName) {
				group[index].Enabled = enabled
				h.eventHandlers[pluginID] = group
				return true
			}
		}
	}
	return false
}
