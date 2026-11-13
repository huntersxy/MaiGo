package host

import (
	"context"
	"fmt"
	"strings"
)

// installPluginManagementCommand 注册 Go 宿主内置 /pm 命令
//
// MaiBot 的 /pm 由 plugins/built_in/plugin_management 提供；Go 宿主在 Runner 未加载该插件时
// 用同一组件模型提供本地兜底，保证插件管理命令可用。Runner 注册同名内置插件后会通过
// ReplacePluginComponents 覆盖此本地组件。
func (h *Host) installPluginManagementCommand() {
	if h == nil || h.components == nil {
		return
	}
	state := newPluginManagementState()
	h.mu.Lock()
	h.pluginManagement = state
	h.mu.Unlock()
	pattern, err := compileCommandPattern("(?P<manage_command>^/pm(?:\\s+.+)?\\s*$)")
	if err != nil {
		return
	}
	entry := &ComponentEntry{
		Name:                  "management",
		FullName:              builtinPluginManagementID + ".management",
		ComponentType:         ComponentTypeCommand,
		PluginID:              builtinPluginManagementID,
		Metadata:              map[string]any{"description": "管理插件和组件的生命周期", "permission": "operator", "aliases": []string{"/插件管理"}},
		Enabled:               true,
		ChatScope:             ComponentChatScopeAll,
		Description:           "管理插件和组件的生命周期",
		Aliases:               []string{"/插件管理"},
		CommandPattern:        "(?P<manage_command>^/pm(?:\\s+.+)?\\s*$)",
		Permission:            "operator",
		InterceptMessageLevel: 1,
		pattern:               pattern,
	}
	entry.localHandler = func(ctx context.Context, execCtx CommandExecutionContext) CommandExecutionResult {
		return state.execute(h, ctx, execCtx)
	}
	h.components.RegisterLocalComponent(entry)
}

func pluginManagementResult(success bool, response string) CommandExecutionResult {
	return CommandExecutionResult{Success: success, Response: response, InterceptMessageLevel: 1}
}

func (s *pluginManagementState) sendText(ctx context.Context, host *Host, streamID, text string) error {
	if host == nil {
		return fmt.Errorf("Host 未初始化")
	}
	value, err := host.InvokeCapability(ctx, builtinPluginManagementID, "send.text", map[string]any{
		"text":      text,
		"stream_id": streamID,
	})
	if err != nil {
		return err
	}
	if result, ok := value.(map[string]any); ok {
		if success, exists := result["success"]; exists && !truthyPayloadValue(success) {
			return fmt.Errorf("%s", firstNonEmptyText(stringValue(result["error"]), "发送失败"))
		}
	}
	return nil
}

func (s *pluginManagementState) sendResult(ctx context.Context, host *Host, streamID string, success bool, response string) CommandExecutionResult {
	if err := s.sendText(ctx, host, streamID, response); err != nil {
		return pluginManagementResult(false, "发送失败: "+err.Error())
	}
	return pluginManagementResult(success, response)
}

func (s *pluginManagementState) execute(host *Host, ctx context.Context, execCtx CommandExecutionContext) CommandExecutionResult {
	if host == nil {
		return pluginManagementResult(false, "Host 未初始化")
	}
	streamID := strings.TrimSpace(execCtx.SessionID)
	if streamID == "" {
		return pluginManagementResult(false, "无法获取聊天流信息")
	}
	rawCommand := strings.TrimSpace(execCtx.MatchedGroups["manage_command"])
	if rawCommand == "" {
		rawCommand = strings.TrimSpace(execCtx.Text)
	}
	rawCommand = s.resolve(rawCommand)
	parts := splitPluginCommand(rawCommand)
	if len(parts) == 0 {
		parts = []string{"/pm"}
	}
	n := len(parts)

	if n >= 2 && (parts[1] == "command" || parts[1] == "cmd") {
		ok := s.handleCommandCatalog(ctx, host, streamID, parts[2:], parts[1] == "cmd")
		if ok {
			return pluginManagementResult(true, "命令执行完成")
		}
		return pluginManagementResult(false, "指令列表查询失败")
	}

	if n == 1 {
		return s.sendResult(ctx, host, streamID, true, pluginManagementHelpAll)
	}
	if n == 2 {
		switch parts[1] {
		case "plugin":
			return s.sendResult(ctx, host, streamID, true, pluginManagementHelpPlugin)
		case "component":
			return s.sendResult(ctx, host, streamID, true, pluginManagementHelpComponent)
		case "alias":
			return s.sendResult(ctx, host, streamID, true, pluginManagementHelpAlias)
		case "config":
			return s.sendResult(ctx, host, streamID, true, pluginManagementHelpConfig)
		case "help":
			return s.sendResult(ctx, host, streamID, true, pluginManagementHelpAll)
		default:
			return s.sendResult(ctx, host, streamID, false, "插件管理命令不合法")
		}
	}
	if n == 3 {
		switch parts[1] {
		case "plugin":
			s.handlePluginAction(ctx, host, streamID, parts[2], "")
		case "config":
			if parts[2] == "help" {
				return s.sendResult(ctx, host, streamID, true, pluginManagementHelpConfig)
			}
			return s.sendResult(ctx, host, streamID, false, "插件管理命令不合法")
		case "alias":
			switch parts[2] {
			case "help":
				return s.sendResult(ctx, host, streamID, true, pluginManagementHelpAlias)
			case "list":
				s.handleAliasList(ctx, host, streamID)
			default:
				return s.sendResult(ctx, host, streamID, false, "插件管理命令不合法")
			}
		case "component":
			switch parts[2] {
			case "list":
				s.handleComponentList(ctx, host, streamID, "", "", "")
			case "help":
				return s.sendResult(ctx, host, streamID, true, pluginManagementHelpComponent)
			default:
				return s.sendResult(ctx, host, streamID, false, "插件管理命令不合法")
			}
		default:
			return s.sendResult(ctx, host, streamID, false, "插件管理命令不合法")
		}
		return pluginManagementResult(true, "命令执行完成")
	}
	if n == 4 {
		switch parts[1] {
		case "plugin":
			s.handlePluginAction(ctx, host, streamID, parts[2], parts[3])
		case "config":
			s.handleConfigGet(ctx, host, streamID, parts[2], parts[3], "")
		case "alias":
			if parts[2] == "remove" {
				s.handleAliasRemove(ctx, host, streamID, parts[3])
			} else {
				return s.sendResult(ctx, host, streamID, false, "插件管理命令不合法")
			}
		case "component":
			if parts[2] == "list" {
				s.handleComponentList(ctx, host, streamID, parts[3], "", "")
			} else {
				return s.sendResult(ctx, host, streamID, false, "插件管理命令不合法")
			}
		default:
			return s.sendResult(ctx, host, streamID, false, "插件管理命令不合法")
		}
		return pluginManagementResult(true, "命令执行完成")
	}
	if n == 5 {
		switch {
		case parts[1] == "alias" && parts[2] == "add":
			s.handleAliasAdd(ctx, host, streamID, parts[3], parts[4])
		case parts[1] == "component" && parts[2] == "list":
			s.handleComponentList(ctx, host, streamID, parts[3], parts[4], "")
		case parts[1] == "config" && parts[2] == "get":
			s.handleConfigGet(ctx, host, streamID, parts[2], parts[3], parts[4])
		default:
			return s.sendResult(ctx, host, streamID, false, "插件管理命令不合法")
		}
		return pluginManagementResult(true, "命令执行完成")
	}
	if n >= 6 {
		switch {
		case parts[1] == "alias" && parts[2] == "add":
			s.handleAliasAdd(ctx, host, streamID, parts[3], strings.Join(parts[4:], " "))
		case parts[1] == "component" && n == 6:
			s.handleComponentToggle(ctx, host, streamID, parts[2], parts[3], parts[4], parts[5])
		case parts[1] == "config" && parts[2] == "set":
			s.handleConfigSet(ctx, host, streamID, parts[3], parts[4], strings.Join(parts[5:], " "))
		default:
			return s.sendResult(ctx, host, streamID, false, "插件管理命令不合法")
		}
		return pluginManagementResult(true, "命令执行完成")
	}
	return s.sendResult(ctx, host, streamID, false, "插件管理命令不合法")
}

func (s *pluginManagementState) handlePluginAction(ctx context.Context, host *Host, streamID, action, name string) {
	switch action {
	case "help":
		_ = s.sendText(ctx, host, streamID, pluginManagementHelpPlugin)
		return
	case "list":
		ids := host.ListPluginIDsForManagement()
		_ = s.sendText(ctx, host, streamID, "已注册的插件: "+joinOrNone(ids))
		return
	case "list_enabled":
		ids := host.LoadedPluginIDsForManagement()
		_ = s.sendText(ctx, host, streamID, "已加载的插件: "+joinOrNone(ids))
		return
	}
	if action != "load" && action != "unload" && action != "reload" {
		_ = s.sendText(ctx, host, streamID, "插件管理命令不合法")
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		_ = s.sendText(ctx, host, streamID, "插件管理命令不合法")
		return
	}
	if name == builtinPluginManagementID {
		prefix := map[string]string{"load": "插件加载失败", "unload": "插件卸载失败", "reload": "插件重新加载失败"}[action]
		_ = s.sendText(ctx, host, streamID, prefix+": "+name+"（内置插件管理由 Go 宿主提供）")
		return
	}
	var ok bool
	switch action {
	case "load", "reload":
		result, err := host.ReloadPlugin(ctx, name, action+" "+name, nil)
		ok = err == nil && result.Success
	case "unload":
		result, err := host.UnloadPlugins(ctx, []string{name}, "manual")
		ok = err == nil && result.Success
	}
	prefix := map[string]string{"load": "插件加载", "unload": "插件卸载", "reload": "插件重新加载"}[action]
	message := prefix + "成功: " + name
	if !ok {
		message = prefix + "失败: " + name
	}
	_ = s.sendText(ctx, host, streamID, message)
}

func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "无"
	}
	return strings.Join(values, ", ")
}
