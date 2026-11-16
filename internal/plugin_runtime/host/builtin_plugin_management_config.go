package host

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
)

func (s *pluginManagementState) handleAliasList(ctx context.Context, host *Host, streamID string) {
	mapping := s.snapshot()
	if len(mapping) == 0 {
		_ = s.sendText(ctx, host, streamID, "未配置指令别名")
		return
	}
	aliases := make([]string, 0, len(mapping))
	for alias := range mapping {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	lines := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		lines = append(lines, alias+" -> "+mapping[alias])
	}
	_ = s.sendText(ctx, host, streamID, "已配置的指令别名:\n"+strings.Join(lines, "\n"))
}

func (s *pluginManagementState) handleAliasAdd(ctx context.Context, host *Host, streamID, alias, target string) {
	normalizedAlias := strings.TrimSpace(alias)
	normalizedTarget := normalizePluginAliasTarget(target)
	if message := validatePluginAliasShortcut(normalizedAlias, normalizedTarget); message != "" {
		_ = s.sendText(ctx, host, streamID, message)
		return
	}
	if _, err := host.writePMShortcut(normalizedAlias, normalizedTarget); err != nil {
		_ = s.sendText(ctx, host, streamID, "指令别名保存失败: "+err.Error())
		return
	}
	s.add(normalizedAlias, normalizedTarget)
	s.syncAliases(host)
	_ = s.sendText(ctx, host, streamID, fmt.Sprintf("指令别名已保存: %s -> %s", normalizedAlias, normalizedTarget))
}

func (s *pluginManagementState) handleAliasRemove(ctx context.Context, host *Host, streamID, alias string) {
	normalizedAlias := strings.TrimSpace(alias)
	if normalizedAlias == "" {
		_ = s.sendText(ctx, host, streamID, "别名不能为空")
		return
	}
	removed, err := host.writePMShortcut(normalizedAlias, "")
	if err != nil {
		_ = s.sendText(ctx, host, streamID, "指令别名删除失败: "+err.Error())
		return
	}
	if !removed {
		_ = s.sendText(ctx, host, streamID, "未找到指令别名: "+normalizedAlias)
		return
	}
	s.remove(normalizedAlias)
	s.syncAliases(host)
	_ = s.sendText(ctx, host, streamID, "指令别名已删除: "+normalizedAlias)
}

// pluginCommandAliasTargets 对齐 Python 内置插件的 _COMMAND_ALIAS_TARGETS
var pluginCommandAliasTargets = map[string]string{
	"help":                "/pm help",
	"plugin":              "/pm plugin",
	"plugin.help":         "/pm plugin help",
	"plugin.list":         "/pm plugin list",
	"plugin.list_enabled": "/pm plugin list_enabled",
	"plugin.load":         "/pm plugin load",
	"plugin.unload":       "/pm plugin unload",
	"plugin.reload":       "/pm plugin reload",
	"component":           "/pm component",
	"component.help":      "/pm component help",
	"component.list":      "/pm component list",
	"component.enable":    "/pm component enable",
	"component.disable":   "/pm component disable",
	"config":              "/pm config",
	"config.help":         "/pm config help",
	"config.get":          "/pm config get",
	"config.set":          "/pm config set",
	"command":             "/pm command",
	"command.help":        "/pm command help",
	"command.list":        "/pm command list",
	"command.plugin":      "/pm command plugin",
	"cmd":                 "/pm cmd",
	"cmd.help":            "/pm cmd help",
}

// loadPluginManagementAliases 从实例 config/commands/pm.toml 载入别名映射
//
// 读取失败时保留进程内已有映射，不阻断 /pm 命令。
func (h *Host) loadPluginManagementAliases() {
	if h == nil {
		return
	}
	h.mu.RLock()
	state := h.pluginManagement
	h.mu.RUnlock()
	if state == nil {
		return
	}
	configData, err := h.readPMConfig()
	if err != nil {
		return
	}
	state.loadConfigAliases(configData)
	if h.components != nil {
		state.syncAliases(h)
	}
}

func (s *pluginManagementState) loadConfigAliases(configData map[string]any) {
	prefixes := map[string]string{"/插件管理": "/pm"}
	shortcuts := map[string]string{}
	aliases, _ := configData["aliases"].(map[string]any)
	if raw, ok := aliases["management_prefixes"]; ok {
		for _, alias := range pluginAliasStrings(raw) {
			if alias != "" {
				prefixes[alias] = "/pm"
			}
		}
	}
	if raw, ok := aliases["shortcuts"].(map[string]any); ok {
		for alias, target := range raw {
			alias = strings.TrimSpace(alias)
			normalized := normalizePluginAliasTarget(stringValue(target))
			if validatePluginAliasShortcut(alias, normalized) == "" {
				shortcuts[alias] = normalized
			}
		}
	}
	if raw, ok := aliases["command_aliases"].(map[string]any); ok {
		for commandKey, aliasList := range raw {
			target, exists := pluginCommandAliasTargets[strings.TrimSpace(commandKey)]
			if !exists {
				continue
			}
			for _, alias := range pluginAliasStrings(aliasList) {
				if alias != "" {
					shortcuts[alias] = target
				}
			}
		}
	}
	s.replaceAliases(prefixes, shortcuts)
}

func pluginAliasStrings(raw any) []string {
	switch typed := raw.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if value := strings.TrimSpace(stringValue(item)); value != "" {
				out = append(out, value)
			}
		}
		return out
	case string:
		if value := strings.TrimSpace(typed); value != "" {
			return []string{value}
		}
	}
	return nil
}

// syncAliases 让新增别名进入组件匹配表，否则 FindCommandByText 无法命中 /pm 之外的别名
func (s *pluginManagementState) syncAliases(host *Host) {
	if host == nil || host.components == nil {
		return
	}
	mapping := s.snapshot()
	aliases := make([]string, 0, len(mapping))
	for alias := range mapping {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	host.components.SetCommandAliases(builtinPluginManagementID+".management", aliases)
}

func (s *pluginManagementState) handleConfigGet(ctx context.Context, host *Host, streamID, action, pluginName, key string) {
	if action != "get" {
		_ = s.sendText(ctx, host, streamID, "插件管理命令不合法")
		return
	}
	value, err := host.InvokeCapability(ctx, builtinPluginManagementID, "config.get_plugin", map[string]any{
		"plugin_name": pluginName,
	})
	if err != nil {
		_ = s.sendText(ctx, host, streamID, "未读取到插件配置: "+pluginName)
		return
	}
	result, ok := value.(map[string]any)
	if !ok || !truthyPayloadValue(result["success"]) {
		_ = s.sendText(ctx, host, streamID, "未读取到插件配置: "+pluginName)
		return
	}
	// SDK 归一化后 config.get_plugin 的载荷键是 value，对齐 Python ctx.config.get_plugin 直接返回配置字典
	config, ok := result["value"].(map[string]any)
	if !ok || len(config) == 0 {
		_ = s.sendText(ctx, host, streamID, "未读取到插件配置: "+pluginName)
		return
	}
	display := any(config)
	displayKey := ""
	if strings.TrimSpace(key) != "" {
		display = nestedPluginConfigValue(config, key)
		displayKey = "." + key
	}
	encoded, marshalErr := sonic.Marshal(display)
	if marshalErr != nil {
		encoded = []byte(fmt.Sprint(display))
	}
	_ = s.sendText(ctx, host, streamID, fmt.Sprintf("%s%s = %s", pluginName, displayKey, string(encoded)))
}

func (s *pluginManagementState) handleConfigSet(ctx context.Context, host *Host, streamID, pluginName, key, rawValue string) {
	key = strings.TrimSpace(key)
	if key == "" {
		_ = s.sendText(ctx, host, streamID, "配置键不能为空")
		return
	}
	var value any
	if err := sonic.Unmarshal([]byte(rawValue), &value); err != nil {
		value = rawValue
	}
	result, err := host.InvokeCapability(ctx, builtinPluginManagementID, "component.update_plugin_config", map[string]any{
		"plugin_name": pluginName,
		"key":         key,
		"value":       value,
	})
	if err != nil {
		_ = s.sendText(ctx, host, streamID, "插件配置更新失败: "+err.Error())
		return
	}
	response, ok := result.(map[string]any)
	if !ok || !truthyPayloadValue(response["success"]) {
		message := "未知错误"
		if ok {
			message = firstNonEmptyText(stringValue(response["error"]), "未知错误")
		}
		_ = s.sendText(ctx, host, streamID, "插件配置更新失败: "+message)
		return
	}
	_ = s.sendText(ctx, host, streamID, fmt.Sprintf("插件配置已更新: %s.%s", pluginName, key))
}

func nestedPluginConfigValue(config map[string]any, key string) any {
	current := any(config)
	for _, part := range strings.Split(strings.TrimSpace(key), ".") {
		if part == "" {
			return nil
		}
		values, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = values[part]
		if !ok {
			return nil
		}
	}
	return current
}
