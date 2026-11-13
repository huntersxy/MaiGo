package host

import (
	"context"
	"strings"

	"maibot-go/internal/plugin_runtime/protocol"
)

func pluginNameArg(args map[string]any, fallback string) string {
	name := strings.TrimSpace(coalesceString(args, "plugin_name"))
	if name == "" {
		return fallback
	}
	return name
}

func makeComponentGetAllPluginsHandler(host *Host) CapabilityHandler {
	return func(_ context.Context, _, _ string, _ map[string]any) (any, error) {
		if host == nil {
			return capabilityFailure("Host 未初始化"), nil
		}
		registry := host.Components()
		result := map[string]any{}
		for _, rec := range host.ListPlugins() {
			if _, exists := result[rec.PluginID]; exists {
				return capabilityFailure("检测到重复插件 ID: %s", rec.PluginID), nil
			}
			components := []map[string]any{}
			if registry != nil {
				for _, entry := range registry.ComponentsByPlugin(rec.PluginID, false, "") {
					components = append(components, map[string]any{
						"name":      entry.Name,
						"full_name": entry.FullName,
						"type":      entry.ComponentType,
						"enabled":   entry.Enabled,
						"metadata":  cloneAnyMap(entry.Metadata),
					})
				}
			}
			for _, entry := range host.APIRegistry().List(APIFilter{PluginID: rec.PluginID}) {
				components = append(components, SerializeAPIComponentEntry(&entry))
			}
			result[rec.PluginID] = map[string]any{
				"name":         rec.PluginID,
				"display_name": rec.DisplayName,
				"version":      rec.PluginVersion,
				"description":  "",
				"author":       "",
				"enabled":      true,
				"components":   components,
			}
		}
		return map[string]any{"success": true, "plugins": result}, nil
	}
}

func pluginRecordByID(host *Host, pluginID string) (PluginRecord, bool) {
	for _, rec := range host.ListPlugins() {
		if rec.PluginID == pluginID {
			return rec, true
		}
	}
	return PluginRecord{}, false
}

func makeComponentGetPluginInfoHandler(host *Host) CapabilityHandler {
	return func(_ context.Context, pluginID, _ string, args map[string]any) (any, error) {
		if host == nil {
			return capabilityFailure("Host 未初始化"), nil
		}
		name := pluginNameArg(args, pluginID)
		rec, ok := pluginRecordByID(host, name)
		if !ok {
			return capabilityFailure("未找到插件: %s", name), nil
		}
		return map[string]any{"success": true, "plugin": map[string]any{
			"name":           name,
			"version":        rec.PluginVersion,
			"description":    "",
			"author":         "",
			"enabled":        true,
			"default_config": rec.DefaultConfig,
			"config_schema":  rec.ConfigSchema,
		}}, nil
	}
}

func makeComponentGetPluginConfigSchemaHandler(host *Host) CapabilityHandler {
	return func(_ context.Context, pluginID, _ string, args map[string]any) (any, error) {
		if host == nil {
			return capabilityFailure("Host 未初始化"), nil
		}
		name := pluginNameArg(args, pluginID)
		rec, ok := pluginRecordByID(host, name)
		if !ok {
			return capabilityFailure("未找到插件: %s", name), nil
		}
		return map[string]any{
			"success":        true,
			"plugin_id":      name,
			"schema":         rec.ConfigSchema,
			"default_config": rec.DefaultConfig,
		}, nil
	}
}

func makeComponentUpdatePluginConfigHandler(host *Host, deps HostCapabilityDeps) CapabilityHandler {
	return func(ctx context.Context, pluginID, capability string, args map[string]any) (any, error) {
		_ = capability
		if pluginID != "builtin.plugin-management" {
			return capabilityFailure("仅插件管理内置插件可以修改插件配置"), nil
		}
		target := strings.TrimSpace(coalesceString(args, "plugin_name"))
		key := strings.TrimSpace(coalesceString(args, "key"))
		if target == "" || key == "" {
			return capabilityFailure("缺少必要参数 plugin_name 或 key"), nil
		}
		components := deps.Components
		if components == nil && host != nil {
			components = host.ComponentAdmin()
		}
		if components == nil {
			return pluginPortNotWired("component.update_plugin_config"), nil
		}
		configData, err := components.UpdatePluginConfig(ctx, target, key, args["value"])
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		delivered := false
		if host != nil && configData != nil {
			notified, notifyErr := host.NotifyPluginConfigUpdated(ctx, target, configData, "", "")
			if notifyErr == nil {
				delivered = notified
			}
		}
		return map[string]any{
			"success":     true,
			"plugin_name": target,
			"key":         key,
			"hot_updated": delivered,
		}, nil
	}
}

func makeComponentListPluginsHandler(host *Host) CapabilityHandler {
	return func(_ context.Context, _, _ string, _ map[string]any) (any, error) {
		if host == nil {
			return capabilityFailure("Host 未初始化"), nil
		}
		plugins := make([]string, 0)
		for _, rec := range host.ListPlugins() {
			plugins = append(plugins, rec.PluginID)
		}
		return map[string]any{"success": true, "plugins": plugins}, nil
	}
}

func resolveComponentToggleTarget(host *Host, name, componentType string) (*ComponentEntry, string) {
	normalized, err := normalizeComponentType(componentType)
	if err != nil {
		return nil, err.Error()
	}
	registry := host.Components()
	if registry == nil {
		return nil, "组件注册表未就绪"
	}
	if entry := registry.GetComponent(name); entry != nil && entry.ComponentType == normalized {
		return entry, ""
	}
	matches := []*ComponentEntry{}
	entries, err := registry.ComponentsByType(normalized, false, ComponentFilter{})
	if err == nil {
		for _, candidate := range entries {
			if candidate.Name == name {
				matches = append(matches, candidate)
			}
		}
	}
	if len(matches) == 1 {
		return matches[0], ""
	}
	if len(matches) > 1 {
		return nil, "组件名不唯一: " + name + " (" + normalized + ")，请使用完整名 plugin_id.component_name"
	}
	return nil, "未找到组件: " + name + " (" + normalized + ")"
}

func makeComponentToggleHandler(host *Host, enable bool) CapabilityHandler {
	return func(_ context.Context, _, capability string, args map[string]any) (any, error) {
		name := strings.TrimSpace(coalesceString(args, "name"))
		componentType := strings.TrimSpace(coalesceString(args, "component_type"))
		version := strings.TrimSpace(coalesceString(args, "version"))
		scope := strings.TrimSpace(coalesceString(args, "scope"))
		streamID := strings.TrimSpace(coalesceString(args, "stream_id"))
		if scope == "" {
			scope = "global"
		}
		if name == "" || componentType == "" {
			return capabilityFailure("缺少必要参数 name 或 component_type"), nil
		}
		action := "启用"
		if !enable {
			action = "禁用"
		}
		if scope != "global" || streamID != "" {
			return capabilityFailure("当前仅支持全局组件%s，不支持 scope/stream_id 定位", action), nil
		}
		if host == nil {
			return capabilityFailure("Host 未初始化"), nil
		}
		if IsAPIComponent(componentType) {
			entry, message := host.APIRegistry().ResolveAPIToggle(name, version)
			if entry == nil {
				return capabilityFailure("%s", firstNonEmptyText(message, "未找到 API: "+name)), nil
			}
			if !host.APIRegistry().ToggleAPIStatus(entry.FullName, entry.Version, enable, "") {
				return capabilityFailure("未找到 API: %s", name), nil
			}
			return map[string]any{"success": true}, nil
		}
		entry, message := resolveComponentToggleTarget(host, name, componentType)
		if entry == nil {
			return capabilityFailure("%s", firstNonEmptyText(message, "未找到组件: "+name+" ("+componentType+")")), nil
		}
		host.Components().SetComponentEnabled(entry.FullName, enable, "")
		return map[string]any{"success": true}, nil
	}
}

func makeComponentLoadPluginHandler(host *Host, deps HostCapabilityDeps) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		name := strings.TrimSpace(coalesceString(args, "plugin_name"))
		if name == "" {
			return capabilityFailure("缺少必要参数 plugin_name"), nil
		}
		if deps.Components != nil {
			loaded, err := deps.Components.LoadPlugin(ctx, name, "load "+name)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			if !loaded {
				return capabilityFailure("插件 %s 热重载失败", name), nil
			}
			return map[string]any{"success": true, "count": 1}, nil
		}
		// 未接线组件管理端口时回退到 Host 自己的重载通道，保证 /pm plugin load 仍可执行
		if host == nil {
			return pluginPortNotWired("component.load_plugin"), nil
		}
		result, err := host.ReloadPlugin(ctx, name, "load "+name, nil)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		if !result.Success {
			return capabilityFailure("插件 %s 热重载失败", name), nil
		}
		return map[string]any{"success": true, "count": 1}, nil
	}
}

func makeComponentUnloadPluginHandler() CapabilityHandler {
	return func(_ context.Context, _, _ string, _ map[string]any) (any, error) {
		return capabilityFailure("新运行时不支持单独卸载插件，请使用 reload"), nil
	}
}

func makeComponentReloadPluginHandler(host *Host, deps HostCapabilityDeps) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		name := strings.TrimSpace(coalesceString(args, "plugin_name"))
		if name == "" {
			return capabilityFailure("缺少必要参数 plugin_name"), nil
		}
		if deps.Components != nil {
			reloaded, err := deps.Components.ReloadPlugins(ctx, []string{name}, "reload "+name)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			if !reloaded {
				return capabilityFailure("插件 %s 热重载失败", name), nil
			}
			return map[string]any{"success": true}, nil
		}
		// 未接线组件管理端口时回退到 Host 自己的重载通道，保证 /pm plugin reload 仍可执行
		if host == nil {
			return pluginPortNotWired("component.reload_plugin"), nil
		}
		result, err := host.ReloadPlugin(ctx, name, "reload "+name, nil)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		if !result.Success {
			return capabilityFailure("插件 %s 热重载失败", name), nil
		}
		return map[string]any{"success": true}, nil
	}
}

func makeAPICallHandler(host *Host) CapabilityHandler {
	return func(ctx context.Context, pluginID, _ string, args map[string]any) (any, error) {
		apiName := strings.TrimSpace(coalesceString(args, "api_name"))
		version := strings.TrimSpace(coalesceString(args, "version"))
		apiArgs, ok := args["args"].(map[string]any)
		if raw, exists := args["args"]; exists && raw != nil && !ok {
			return capabilityFailure("参数 args 必须为字典"), nil
		}
		if host == nil {
			return capabilityFailure("Host 未初始化"), nil
		}
		entry, message := host.APIRegistry().ResolveAPI(pluginID, apiName, version)
		if entry == nil {
			return capabilityFailure("%s", firstNonEmptyText(message, "API 解析失败")), nil
		}
		invokeArgs := cloneAnyMap(apiArgs)
		if invokeArgs == nil {
			invokeArgs = map[string]any{}
		}
		if entry.Dynamic {
			if _, exists := invokeArgs["__maibot_api_name__"]; !exists {
				invokeArgs["__maibot_api_name__"] = entry.Name
			}
			if _, exists := invokeArgs["__maibot_api_full_name__"]; !exists {
				invokeArgs["__maibot_api_full_name__"] = entry.FullName
			}
			if _, exists := invokeArgs["__maibot_api_version__"]; !exists {
				invokeArgs["__maibot_api_version__"] = entry.Version
			}
		}
		timeoutMS, timeoutErr := ResolveComponentRPCTimeoutMS(entry.TimeoutMS)
		if timeoutErr != nil {
			return capabilityFailure("%s", timeoutErr.Error()), nil
		}
		envelope, err := host.InvokeAPI(ctx, entry.PluginID, entry.HandlerName, invokeArgs, timeoutMS)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		if len(envelope.Error) > 0 {
			message := stringValue(envelope.Error["message"])
			if message == "" {
				message = "API 调用失败"
			}
			return capabilityFailure("%s", message), nil
		}
		payload := envelope.Payload
		if payload == nil {
			payload = map[string]any{}
		}
		if !truthyPayloadValue(payload["success"]) {
			result := payload["result"]
			text := ""
			if result != nil {
				text = stringValue(result)
			}
			return capabilityFailure("%s", text), nil
		}
		return map[string]any{"success": true, "result": payload["result"]}, nil
	}
}

func makeAPIGetHandler(host *Host) CapabilityHandler {
	return func(_ context.Context, pluginID, _ string, args map[string]any) (any, error) {
		apiName := strings.TrimSpace(coalesceString(args, "api_name"))
		version := strings.TrimSpace(coalesceString(args, "version"))
		if apiName == "" {
			return capabilityFailure("缺少必要参数 api_name"), nil
		}
		if host == nil {
			return capabilityFailure("Host 未初始化"), nil
		}
		entry, _ := host.APIRegistry().ResolveAPI(pluginID, apiName, version)
		if entry == nil {
			return map[string]any{"success": true, "api": nil}, nil
		}
		return map[string]any{"success": true, "api": SerializeAPIEntry(entry)}, nil
	}
}

func makeAPIListHandler(host *Host) CapabilityHandler {
	return func(_ context.Context, pluginID, _ string, args map[string]any) (any, error) {
		if host == nil {
			return capabilityFailure("Host 未初始化"), nil
		}
		targetPluginID := strings.TrimSpace(coalesceString(args, "plugin_id"))
		apiName := strings.TrimSpace(coalesceString(args, "api_name", "name"))
		version := strings.TrimSpace(coalesceString(args, "version"))
		entries := host.APIRegistry().List(APIFilter{PluginID: targetPluginID, Name: apiName, Version: version, EnabledOnly: true})
		apis := make([]map[string]any, 0, len(entries))
		for index := range entries {
			if !IsAPIVisibleToPlugin(&entries[index], pluginID) {
				continue
			}
			apis = append(apis, SerializeAPIEntry(&entries[index]))
		}
		return map[string]any{"success": true, "apis": apis}, nil
	}
}

func makeAPIReplaceDynamicHandler(host *Host) CapabilityHandler {
	return func(_ context.Context, pluginID, _ string, args map[string]any) (any, error) {
		raw, ok := args["apis"]
		if raw == nil {
			raw = []any{}
		}
		items, ok := raw.([]any)
		if !ok {
			return capabilityFailure("参数 apis 必须为列表"), nil
		}
		offlineReason := strings.TrimSpace(coalesceString(args, "offline_reason"))
		if offlineReason == "" {
			offlineReason = "动态 API 已下线"
		}
		if host == nil {
			return capabilityFailure("Host 未初始化"), nil
		}
		seen := map[string]struct{}{}
		components := make([]protocol.ComponentDeclaration, 0, len(items))
		for index, item := range items {
			api, ok := item.(map[string]any)
			if !ok {
				return capabilityFailure("apis[%d] 必须为字典", index), nil
			}
			apiName := strings.TrimSpace(coalesceString(api, "name"))
			componentType := strings.TrimSpace(firstNonEmptyText(coalesceString(api, "component_type"), coalesceString(api, "type"), "API"))
			if apiName == "" {
				return capabilityFailure("apis[%d] 缺少 name", index), nil
			}
			if !IsAPIComponent(componentType) {
				return capabilityFailure("apis[%d] 不是 API 组件", index), nil
			}
			metadata, _ := api["metadata"].(map[string]any)
			normalized := cloneAnyMap(metadata)
			if normalized == nil {
				normalized = map[string]any{}
			}
			normalized["dynamic"] = true
			version := firstNonEmptyText(coalesceString(map[string]any{"version": normalized["version"]}, "version"), "1")
			key := BuildAPIRegistryKey(pluginID, apiName, version)
			if _, exists := seen[key]; exists {
				return capabilityFailure("动态 API 重复声明: %s", key), nil
			}
			seen[key] = struct{}{}
			existing := host.APIRegistry().GetAPI(pluginID, apiName, version, false)
			if existing != nil && !existing.Dynamic {
				return capabilityFailure("动态 API 不能覆盖静态 API: %s", key), nil
			}
			components = append(components, protocol.ComponentDeclaration{
				Name:          apiName,
				ComponentType: "API",
				PluginID:      pluginID,
				Metadata:      normalized,
			})
		}
		registered, offlined := host.APIRegistry().ReplacePluginDynamicAPIs(pluginID, components, offlineReason)
		return map[string]any{"success": true, "count": registered, "offlined": offlined}, nil
	}
}
