package host

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type pluginManagementCommandGroup struct {
	PluginID    string
	DisplayName string
	Commands    []*ComponentEntry
}

// ListPluginIDsForManagement 返回插件管理命令可见的插件 ID（含本地内置兜底插件）
func (h *Host) ListPluginIDsForManagement() []string {
	if h == nil {
		return nil
	}
	seen := map[string]struct{}{}
	for _, rec := range h.ListPlugins() {
		seen[rec.PluginID] = struct{}{}
	}
	if h.components != nil && len(h.components.ComponentsByPlugin(builtinPluginManagementID, false, "")) > 0 {
		seen[builtinPluginManagementID] = struct{}{}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// LoadedPluginIDsForManagement 返回已加载插件；本地兜底命令可用时内置插件视为已加载
func (h *Host) LoadedPluginIDsForManagement() []string {
	ids := h.ListPluginIDsForManagement()
	loaded := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == builtinPluginManagementID {
			loaded = append(loaded, id)
			continue
		}
		if _, ok := pluginRecordByID(h, id); ok {
			loaded = append(loaded, id)
		}
	}
	return loaded
}

func (h *Host) pluginManagementDisplayName(pluginID string) string {
	if pluginID == builtinPluginManagementID {
		return builtinPluginManagementDisplayName
	}
	if rec, ok := pluginRecordByID(h, pluginID); ok {
		if strings.TrimSpace(rec.DisplayName) != "" {
			return rec.DisplayName
		}
	}
	return pluginID
}

func (h *Host) collectManagementCommandGroups(pluginID string) []pluginManagementCommandGroup {
	if h == nil || h.components == nil {
		return nil
	}
	ids := h.ListPluginIDsForManagement()
	groups := make([]pluginManagementCommandGroup, 0, len(ids))
	for _, id := range ids {
		if pluginID != "" && id != pluginID {
			continue
		}
		commands := make([]*ComponentEntry, 0)
		for _, entry := range h.components.ComponentsByPlugin(id, false, "") {
			if entry.ComponentType == ComponentTypeCommand {
				commands = append(commands, entry)
			}
		}
		if len(commands) == 0 {
			continue
		}
		sort.SliceStable(commands, func(i, j int) bool { return commands[i].Name < commands[j].Name })
		groups = append(groups, pluginManagementCommandGroup{
			PluginID:    id,
			DisplayName: h.pluginManagementDisplayName(id),
			Commands:    commands,
		})
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].PluginID < groups[j].PluginID })
	return groups
}

func (h *Host) resolveManagementPluginID(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	for _, id := range h.ListPluginIDsForManagement() {
		if id == raw {
			return id, ""
		}
	}
	matches := make([]string, 0)
	for _, id := range h.ListPluginIDsForManagement() {
		if strings.TrimSpace(id[strings.LastIndex(id, ".")+1:]) == raw {
			matches = append(matches, id)
		}
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		return "", "未找到已注册插件: " + raw
	}
	if len(matches) > 1 {
		return "", "插件短名称 " + raw + " 对应多个插件，请使用完整ID:\n" + strings.Join(matches, "\n")
	}
	return matches[0], ""
}

func formatManagementCommandCatalog(groups []pluginManagementCommandGroup) string {
	lines := []string{"插件指令列表（含已禁用指令）"}
	for _, group := range groups {
		lines = append(lines, "", fmt.Sprintf("【%s】（%d 条）", group.DisplayName, len(group.Commands)))
		for _, command := range group.Commands {
			status := ""
			if !command.Enabled {
				status = " [已禁用]"
			}
			lines = append(lines, "  "+command.Name+status)
			lines = append(lines, "    说明："+firstNonEmptyText(command.Description, "未提供说明"))
			if len(command.Aliases) > 0 {
				lines = append(lines, "    别名："+strings.Join(command.Aliases, "、"))
			}
			if strings.TrimSpace(command.Permission) != "" {
				lines = append(lines, "    权限："+command.Permission)
			}
		}
	}
	return strings.Join(lines, "\n")
}

func (s *pluginManagementState) handleCommandCatalog(ctx context.Context, host *Host, streamID string, args []string, compact bool) bool {
	args = append([]string(nil), args...)
	if compact {
		if len(args) == 0 || (len(args) == 1 && args[0] == "pic") {
			args = append([]string{"list"}, args...)
		} else if !(len(args) == 1 && args[0] == "help") {
			if len(args) == 1 || (len(args) == 2 && args[1] == "pic") {
				args = append([]string{"plugin"}, args...)
			} else {
				_ = s.sendText(ctx, host, streamID, pluginManagementHelpCommand)
				return false
			}
		}
	}
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "plugin")) {
		_ = s.sendText(ctx, host, streamID, pluginManagementHelpCommand)
		return true
	}

	pluginID := ""
	picture := false
	switch {
	case len(args) == 1 && args[0] == "list":
	case len(args) == 2 && args[0] == "list" && args[1] == "pic":
		picture = true
	case len(args) == 2 && args[0] == "plugin":
		pluginID = args[1]
	case len(args) == 3 && args[0] == "plugin" && args[2] == "pic":
		pluginID = args[1]
		picture = true
	default:
		_ = s.sendText(ctx, host, streamID, pluginManagementHelpCommand)
		return false
	}

	if pluginID != "" {
		resolved, message := host.resolveManagementPluginID(pluginID)
		if message != "" {
			_ = s.sendText(ctx, host, streamID, message)
			return false
		}
		pluginID = resolved
	}
	groups := host.collectManagementCommandGroups(pluginID)
	if len(groups) == 0 {
		if pluginID != "" {
			_ = s.sendText(ctx, host, streamID, "插件 "+pluginID+" 没有注册指令")
		} else {
			_ = s.sendText(ctx, host, streamID, "没有注册的指令")
		}
		return true
	}
	if picture {
		return s.sendCommandCatalogPicture(ctx, host, streamID, groups)
	}
	if err := s.sendText(ctx, host, streamID, formatManagementCommandCatalog(groups)); err != nil {
		return false
	}
	return true
}

// CommandDetails 对齐 plugins/built_in/plugin_management/command_catalog.command_details：
// 返回「说明/别名/权限」三组可读标签，别名以「、」连接，权限为空时省略。
func CommandDetails(description string, aliases []string, permission string) [][2]string {
	details := [][2]string{{"说明", firstNonEmptyText(description, "未提供说明")}}
	if len(aliases) > 0 {
		details = append(details, [2]string{"别名", strings.Join(aliases, "、")})
	}
	if strings.TrimSpace(permission) != "" {
		details = append(details, [2]string{"权限", permission})
	}
	return details
}
