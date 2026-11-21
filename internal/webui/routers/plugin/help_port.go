package plugin

// 把插件市场与配置页能力投影为 AI 答疑 Agent 的工具端口

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/pelletier/go-toml/v2"

	"MaiGo/internal/plugin_market"
	"MaiGo/internal/plugin_runtime"
	ai_search "MaiGo/internal/webui/services/ai_search"
)

const pluginConfigRedacted = "<已隐藏>"

// NewAISearchPluginToolset 构造答疑插件工具端口；服务缺失时返回 nil
func NewAISearchPluginToolset(d Deps) *ai_search.PluginToolset {
	return &ai_search.PluginToolset{
		Search: func(ctx context.Context, query string, page, limit int, compatibleOnly bool) (map[string]any, error) {
			response, err := d.PluginMarket.Search(ctx, pluginmarket.SearchRequest{
				Query: query, Page: page, Limit: limit, CompatibleOnly: compatibleOnly,
			})
			if err != nil {
				return nil, err
			}
			payload := response.Payload()
			payload["plugins"] = response.Plugins
			payload["matched"] = response.Matched
			payload["page"] = response.Page
			payload["pagination"] = response.Pagination
			return payload, nil
		},
		Detail: func(ctx context.Context, pluginID string) (map[string]any, error) {
			response, err := d.PluginMarket.Detail(ctx, pluginID, false)
			if err != nil {
				return nil, err
			}
			payload := response.Payload()
			releases := map[string]any{}
			for key, value := range response.Releases {
				releases[key] = value
			}
			if versions, ok := response.Releases["versions"].([]pluginmarket.DescribedRelease); ok {
				converted := make([]any, 0, len(versions))
				for _, version := range versions {
					converted = append(converted, map[string]any{
						"version": version.Version, "tag": version.Tag, "commit": version.Commit,
						"prerelease": version.Prerelease, "yanked": version.Yanked, "manifest": version.Manifest,
						"published_at": version.PublishedAt, "release_url": version.ReleaseURL,
						"release_notes": version.ReleaseNotes, "compatible": version.Compatible,
						"reasons": version.Reasons,
					})
				}
				releases["versions"] = converted
			}
			payload["plugin"] = response.Plugin
			payload["releases"] = releases
			return payload, nil
		},
		Readme: func(ctx context.Context, pluginID, branch string, maxChars int, preferLocal bool) (map[string]any, error) {
			response, err := d.PluginMarket.GetReadme(ctx, pluginID, maxChars, preferLocal, branch, false)
			if err != nil {
				return nil, err
			}
			payload := response.Payload()
			plugin := map[string]any{}
			for key, value := range response.Plugin {
				plugin[key] = value
			}
			payload["plugin"] = plugin
			return payload, nil
		},
		ListInstalled: func(ctx context.Context) (map[string]any, error) {
			return d.aiSearchInstalledPlugins(ctx)
		},
		ReadConfig: func(ctx context.Context, pluginID string) (map[string]any, error) {
			return d.aiSearchReadPluginConfig(ctx, pluginID)
		},
		UpdateConfig: func(ctx context.Context, pluginID string, changes []map[string]any) (map[string]any, error) {
			return d.aiSearchUpdatePluginConfig(ctx, pluginID, changes)
		},
	}
}

func (d Deps) aiSearchInstalledPlugins(ctx context.Context) (map[string]any, error) {
	candidates, err := d.pluginCandidates()
	if err != nil {
		return nil, err
	}
	plugins := make([]any, 0, len(candidates))
	statuses := map[string]string{}
	failures := map[string]string{}
	if d.PluginHost != nil {
		statuses = d.PluginHost.PluginLoadStatuses()
		failures = d.PluginHost.PluginLoadFailureReasons()
	}
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		if strings.HasPrefix(candidate.name, ".") || strings.HasPrefix(candidate.name, "__") || candidate.name == "data" {
			continue
		}
		manifest, err := candidate.loadManifest()
		if err != nil || manifest == nil {
			continue
		}
		id := strings.TrimSpace(stringFromAny(manifest["id"]))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		var enabled bool
		if pluginruntime.IsNativeAdapter(id) {
			enabled, _ = readNativePluginEnabled(d.pluginCandidateConfigPath(candidate, id))
		} else {
			enabled, _ = readPluginEnabled(d.pluginCandidateConfigPath(candidate, id))
		}
		status := statuses[id]
		if status == "" && d.PluginHost == nil {
			status = "success"
		}
		if status == "" {
			status = "not_loaded"
		}
		if failure := failures[id]; failure != "" && (status == "failed" || status == "timed_out") {
			status = "failed"
		}
		plugins = append(plugins, map[string]any{
			"id": id, "name": stringFromAny(manifest["name"]), "version": stringFromAny(manifest["version"]),
			"enabled": enabled, "builtin": candidate.builtin, "load_status": status,
		})
	}
	return map[string]any{"plugins": plugins}, nil
}

func (d Deps) aiSearchReadPluginConfig(ctx context.Context, pluginID string) (map[string]any, error) {
	if _, err := d.resolvePluginConfigPaths(pluginID); err != nil {
		return nil, err
	}
	snapshot, err := d.inspectPluginConfig(ctx, pluginID, nil, false)
	if err != nil {
		return nil, err
	}
	if snapshot.ConfigError != "" {
		return nil, &pluginmarket.MarketError{StatusCode: 400, Message: "插件配置存在错误，请先在插件配置页修复"}
	}
	schema := snapshot.ConfigSchema
	sections, _ := schema["sections"].(map[string]any)
	if len(sections) == 0 {
		return nil, &pluginmarket.MarketError{StatusCode: 400, Message: "插件未提供配置定义"}
	}
	fields := make([]any, 0)
	for sectionName, rawSection := range sections {
		section, _ := rawSection.(map[string]any)
		rawFields, _ := section["fields"].(map[string]any)
		for name, rawField := range rawFields {
			metadata, _ := rawField.(map[string]any)
			current := configLookup(snapshot.NormalizedConfig, sectionName, name)
			qualified := sectionName + "_" + name
			sensitive := pluginConfigFieldSensitive(qualified, metadata, current)
			field := map[string]any{
				"path": []string{sectionName, name}, "type": stringFromAny(metadata["type"]),
				"description": stringFromAny(metadata["description"]), "label": stringFromAny(metadata["label"]),
				"read_only": sensitive || boolFromAny(metadata["disabled"]),
			}
			if field["label"] == "" {
				field["label"] = name
			}
			if sensitive {
				field["value"] = pluginConfigRedacted
				field["choices"] = nil
			} else {
				field["value"] = current
				field["choices"] = metadata["choices"]
			}
			fields = append(fields, field)
		}
	}
	pluginInfo, _ := schema["plugin_info"].(map[string]any)
	return map[string]any{
		"plugin": map[string]any{"id": pluginID, "name": stringFromAny(pluginInfo["name"])},
		"fields": fields,
	}, nil
}

func (d Deps) aiSearchUpdatePluginConfig(ctx context.Context, pluginID string, changes []map[string]any) (map[string]any, error) {
	paths, err := d.resolvePluginConfigPaths(pluginID)
	if err != nil {
		return nil, err
	}
	snapshot, err := d.inspectPluginConfig(ctx, pluginID, nil, false)
	if err != nil {
		return nil, err
	}
	if snapshot.ConfigError != "" {
		return nil, &pluginmarket.MarketError{StatusCode: 400, Message: "插件配置存在错误，请先在插件配置页修复"}
	}
	schema := snapshot.ConfigSchema
	sections, _ := schema["sections"].(map[string]any)
	if len(sections) == 0 {
		return nil, &pluginmarket.MarketError{StatusCode: 400, Message: "插件未提供配置定义"}
	}
	patch := map[string]any{}
	records := make([]map[string]any, 0, len(changes))
	for _, change := range changes {
		path := stringSliceFromAny(change["path"])
		if len(path) != 2 {
			return nil, &pluginmarket.MarketError{StatusCode: 400, Message: "字段路径无效"}
		}
		sectionName, name := path[0], path[1]
		rawSection, _ := sections[sectionName].(map[string]any)
		rawFields, _ := rawSection["fields"].(map[string]any)
		metadata, _ := rawFields[name].(map[string]any)
		if metadata == nil {
			return nil, &pluginmarket.MarketError{StatusCode: 400, Message: "字段不存在: " + sectionName + "." + name}
		}
		before := configLookup(snapshot.NormalizedConfig, sectionName, name)
		if expected, present := change["before"]; present {
			expectedJSON, _ := json.Marshal(expected)
			actualJSON, _ := json.Marshal(before)
			if string(expectedJSON) != string(actualJSON) {
				return nil, &pluginmarket.MarketError{StatusCode: 400, Message: "字段已被其他操作修改，请重新读取配置: " + sectionName + "." + name}
			}
		}
		if pluginConfigFieldSensitive(sectionName+"_"+name, metadata, before) ||
			pluginConfigValueSensitive(sectionName+"_"+name, change["value"]) {
			return nil, &pluginmarket.MarketError{StatusCode: 400, Message: "敏感或只读字段请在插件配置页修改: " + sectionName + "." + name}
		}
		sectionPatch, _ := patch[sectionName].(map[string]any)
		if sectionPatch == nil {
			sectionPatch = map[string]any{}
			patch[sectionName] = sectionPatch
		}
		sectionPatch[name] = change["value"]
		records = append(records, map[string]any{
			"path": []string{sectionName, name}, "before": before, "after": change["value"],
		})
	}
	if len(records) == 0 {
		pluginInfo, _ := schema["plugin_info"].(map[string]any)
		return map[string]any{
			"plugin":  map[string]any{"id": pluginID, "name": stringFromAny(pluginInfo["name"])},
			"message": "配置已是目标值，无需修改",
		}, nil
	}
	if fields := virtualGeneralFieldNames(snapshot); len(fields) > 0 {
		unfoldVirtualGeneralIntoRoot(patch)
	}
	merged := copyStringAnyMap(snapshot.NormalizedConfig)
	deepMergeMaps(merged, patch)
	validated, err := d.validatePluginConfig(ctx, pluginID, merged)
	if err != nil {
		return nil, err
	}
	if _, err := backupPluginConfigFile(paths.configPath, "backup", false); err != nil {
		return nil, err
	}
	encoded, err := toml.Marshal(validated)
	if err != nil {
		return nil, err
	}
	if err := writePluginConfigText(paths.configPath, string(encoded)); err != nil {
		return nil, err
	}
	pluginInfo, _ := schema["plugin_info"].(map[string]any)
	name := stringFromAny(pluginInfo["name"])
	update := map[string]any{
		"id": uuid.NewString(), "plugin_id": pluginID, "name": name,
		"changes": records, "message": "配置已保存",
	}
	return map[string]any{
		"plugin":        map[string]any{"id": pluginID, "name": name},
		"config_update": update, "message": "配置已保存",
	}, nil
}

func configLookup(config map[string]any, section, name string) any {
	if section == "general" {
		if value, ok := config[name]; ok {
			return value
		}
	}
	sectionValue, _ := config[section].(map[string]any)
	if sectionValue == nil {
		return nil
	}
	return sectionValue[name]
}

func pluginConfigFieldSensitive(qualified string, metadata map[string]any, value any) bool {
	if boolFromAny(metadata["hidden"]) || boolFromAny(metadata["disabled"]) {
		return true
	}
	inputType := strings.ToLower(stringFromAny(metadata["input_type"]))
	uiType := strings.ToLower(stringFromAny(metadata["ui_type"]))
	if inputType == "password" || uiType == "password" {
		return true
	}
	return pluginConfigValueSensitive(qualified, value)
}

func pluginConfigValueSensitive(key string, value any) bool {
	normalized := strings.ToLower(key)
	for _, term := range []string{"api_key", "apikey", "token", "password", "secret", "cookie", "authorization", "credential"} {
		if strings.Contains(normalized, term) && value != nil && value != "" {
			return true
		}
	}
	return false
}

func boolFromAny(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func stringSliceFromAny(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]string); ok {
			return typed
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		out = append(out, stringFromAny(item))
	}
	return out
}
