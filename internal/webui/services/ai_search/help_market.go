package search

// 答疑 Agent 的插件检索、安装准备和配置工具，对齐 src/webui/services/ai_search_market.py

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

// PluginToolset 插件市场与配置端口，由 WebUI 插件路由适配器实现
type PluginToolset struct {
	Search        func(ctx context.Context, query string, page, limit int, compatibleOnly bool) (map[string]any, error)
	Detail        func(ctx context.Context, pluginID string) (map[string]any, error)
	Readme        func(ctx context.Context, pluginID, branch string, maxChars int, preferLocal bool) (map[string]any, error)
	ListInstalled func(ctx context.Context) (map[string]any, error)
	ReadConfig    func(ctx context.Context, pluginID string) (map[string]any, error)
	UpdateConfig  func(ctx context.Context, pluginID string, changes []map[string]any) (map[string]any, error)
}

var pluginMarketToolNames = map[string]struct{}{
	"lookup_plugins": {}, "search_plugins": {}, "read_plugin_detail": {}, "read_plugin_readme": {},
	"prepare_plugin_install": {}, "list_installed_plugins": {}, "read_plugin_config": {}, "update_plugin_config": {},
}

func buildMarketToolInfos(allowWrite bool) []*schema.ToolInfo {
	tools := []*schema.ToolInfo{
		buildToolInfo("lookup_plugins",
			"按名称或功能查找插件，并行核对前两个匹配项的兼容性和推荐版本。询问用法时可附带读取一个 README 摘录。",
			map[string]any{
				"query":          map[string]any{"type": "string", "minLength": 1, "maxLength": 200, "description": "插件名称或功能关键词"},
				"include_readme": map[string]any{"type": "boolean", "description": "同时读取首个合适候选的 README 摘录"},
			}, []any{"query"}),
		buildToolInfo("search_plugins",
			"按功能搜索或浏览 MaiBot 插件市场，返回真实插件 ID、摘要及来源。具体兼容性须查询详情。",
			map[string]any{
				"query":           map[string]any{"type": "string", "maxLength": 200, "description": "功能、名称、作者或分类关键词；空值浏览市场"},
				"page":            map[string]any{"type": "integer", "minimum": 1, "description": "页码，从 1 开始"},
				"limit":           map[string]any{"type": "integer", "minimum": 1, "maximum": 10, "description": "每页返回数量，最多 10 个"},
				"compatible_only": map[string]any{"type": "boolean", "description": "仅搜索当前 Host 和 SDK 环境兼容的插件"},
			}, nil),
		buildToolInfo("read_plugin_detail",
			"读取插件详情、依赖、Host/SDK 要求、各发布版本兼容原因及推荐版本；兼容性以服务返回值为准。",
			pluginReferenceSchema(), []any{"plugin_id"}),
		buildToolInfo("read_plugin_readme",
			"读取插件安装、配置和使用说明。默认优先读取本地已安装版本；远程只读指定分支，不切换分支。",
			map[string]any{
				"plugin_id":    map[string]any{"type": "string", "minLength": 1, "maxLength": 500, "description": "插件 ID、名称或市场登记的仓库地址"},
				"branch":       map[string]any{"type": "string", "minLength": 1, "maxLength": 200, "description": "远程 README 分支或提交；仅查询指定版本"},
				"prefer_local": map[string]any{"type": "boolean", "description": "优先读取本地已安装插件的 README"},
				"max_chars":    map[string]any{"type": "integer", "minimum": 500, "maximum": 20000, "description": "最多返回的正文字符数"},
			}, []any{"plugin_id"}),
		buildToolInfo("prepare_plugin_install",
			"用户要求安装时生成安装确认卡片，核对真实插件与兼容版本；此工具不会安装。必须等待用户手动点击确认，不能通过工具或用户文字替代按钮确认。",
			map[string]any{
				"plugin_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 500, "description": "插件 ID、名称或市场登记的仓库地址"},
				"version":   map[string]any{"type": "string", "maxLength": 100, "description": "指定兼容发布版本；留空使用推荐版本，分支模式仅支持 latest"},
			}, []any{"plugin_id"}),
		buildToolInfo("list_installed_plugins",
			"列出本机已安装插件的真实 ID、名称、版本和启用状态。",
			map[string]any{}, nil),
		buildToolInfo("read_plugin_config",
			"读取已安装插件的配置字段定义和当前值，敏感值隐藏。修改前必须调用。",
			pluginReferenceSchema(), []any{"plugin_id"}),
	}
	if allowWrite {
		tools = append(tools, buildToolInfo("update_plugin_config",
			"仅在用户明确要求修改时，提交已读取插件字段的局部改动；保留其他字段，执行配置校验与备份。不能修改敏感或只读字段。",
			map[string]any{
				"plugin_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 500, "description": "插件 ID、名称或市场登记的仓库地址"},
				"changes": map[string]any{
					"type": "array", "minItems": 1, "maxItems": 20,
					"items": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{
							"path": map[string]any{
								"type": "array", "minItems": 2, "maxItems": 2,
								"items":       map[string]any{"type": "string"},
								"description": "read_plugin_config 返回的字段 path 数组",
							},
							"value": map[string]any{"description": "要设置的新值，保持字段所需的数据类型"},
						},
						"required": []any{"path", "value"},
					},
				},
			}, []any{"plugin_id", "changes"}))
	}
	return tools
}

func pluginReferenceSchema() map[string]any {
	return map[string]any{
		"plugin_id": map[string]any{
			"type": "string", "minLength": 1, "maxLength": 500,
			"description": "插件 ID、名称或市场登记的仓库地址",
		},
	}
}

// MarketStore 每次答疑单独持有查询记录与配置快照
type MarketStore struct {
	Toolset  *PluginToolset
	Plugins  map[string]PluginInfo
	Sources  map[string]Source
	Used     bool
	Installs map[string]InstallRequest
	// InstallOrder 保持安装请求的创建顺序
	InstallOrder []string
	Updates      []ConfigUpdate

	configSnapshots map[string]map[string]map[string]any
}

// NewMarketStore 创建插件工具状态
func NewMarketStore(toolset *PluginToolset) *MarketStore {
	return &MarketStore{
		Toolset: toolset, Plugins: map[string]PluginInfo{}, Sources: map[string]Source{},
		Installs: map[string]InstallRequest{}, configSnapshots: map[string]map[string]map[string]any{},
	}
}

// Enabled 是否接入插件工具
func (s *MarketStore) Enabled() bool {
	return s != nil && s.Toolset != nil
}

func (s *MarketStore) recordPlugin(plugin map[string]any) PluginInfo {
	id := strings.TrimSpace(stringValue(plugin["id"]))
	card := s.Plugins[id]
	card.ID = id
	card.Name = stringValue(plugin["name"])
	if description, ok := plugin["description"].(string); ok {
		card.Description = description
	}
	if version, ok := plugin["version"].(string); ok {
		card.Version = version
	}
	s.Plugins[id] = card
	return card
}

func (s *MarketStore) recordSource(plugin map[string]any, kind string, urlOverride string) string {
	id := strings.TrimSpace(stringValue(plugin["id"]))
	sourceID := "plugin:" + kind + ":" + id
	url := urlOverride
	if url == "" {
		url = "/plugins?pluginId=" + id
	}
	title := stringValue(plugin["name"]) + " · 市场"
	if kind == "readme" {
		title = stringValue(plugin["name"]) + " · README"
	}
	s.Sources[sourceID] = Source{Title: title, URL: url}
	return sourceID
}

// Execute 执行插件工具并记录可复用结果
func (s *MarketStore) Execute(ctx context.Context, name string, arguments map[string]any) map[string]any {
	if s == nil || s.Toolset == nil {
		return map[string]any{"error": "插件工具未接线"}
	}
	s.Used = true
	switch name {
	case "search_plugins":
		query := stringValue(arguments["query"])
		page := intValue(arguments["page"], 1)
		limit := intValue(arguments["limit"], 6)
		compatibleOnly, _ := arguments["compatible_only"].(bool)
		result, err := s.Toolset.Search(ctx, query, page, limit, compatibleOnly)
		if err != nil {
			return map[string]any{"error": "插件市场查询失败: " + err.Error()}
		}
		plugins := make([]any, 0)
		for _, raw := range mapSliceValue(result["plugins"]) {
			card := s.recordPlugin(raw)
			sourceID := s.recordSource(raw, "detail", "")
			item := pluginCardMap(card)
			item["source_id"] = sourceID
			plugins = append(plugins, item)
		}
		result["plugins"] = plugins
		return result
	case "read_plugin_detail":
		return s.readDetail(ctx, stringValue(arguments["plugin_id"]))
	case "read_plugin_readme":
		branch := stringValue(arguments["branch"])
		if branch == "" {
			branch = "main"
		}
		preferLocal := true
		if value, ok := arguments["prefer_local"].(bool); ok {
			preferLocal = value
		}
		maxChars := intValue(arguments["max_chars"], 8000)
		result, err := s.Toolset.Readme(ctx, stringValue(arguments["plugin_id"]), branch, maxChars, preferLocal)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		plugin := mapValue(result["plugin"])
		s.recordPlugin(plugin)
		sourceType := stringValue(result["source_type"])
		sourceURL := stringValue(result["source"])
		if sourceType == "local" {
			sourceURL = ""
		}
		result["source_id"] = s.recordSource(plugin, "readme", sourceURL)
		if sourceType == "local" || sourceType == "" {
			result["source"] = "local"
			result["branch"] = nil
		}
		return result
	case "prepare_plugin_install":
		return s.prepareInstall(ctx, stringValue(arguments["plugin_id"]), stringValue(arguments["version"]))
	case "list_installed_plugins":
		result, err := s.Toolset.ListInstalled(ctx)
		return s.toolResult(result, err)
	case "read_plugin_config":
		return s.readConfig(ctx, stringValue(arguments["plugin_id"]))
	case "update_plugin_config":
		return s.updateConfig(ctx, arguments)
	case "lookup_plugins":
		return s.lookup(ctx, stringValue(arguments["query"]), boolValue(arguments["include_readme"]))
	default:
		return map[string]any{"error": "未知插件工具: " + name}
	}
}

func (s *MarketStore) toolResult(result map[string]any, err error) map[string]any {
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return result
}

func (s *MarketStore) lookup(ctx context.Context, query string, includeReadme bool) map[string]any {
	result, err := s.Toolset.Search(ctx, query, 1, 2, false)
	if err != nil {
		return map[string]any{"error": "插件市场查询失败: " + err.Error()}
	}
	plugins := make([]any, 0)
	for _, raw := range mapSliceValue(result["plugins"]) {
		card := s.recordPlugin(raw)
		detail := s.readDetail(ctx, card.ID)
		item := map[string]any{"id": card.ID, "name": card.Name, "description": card.Description, "version": card.Version}
		if plugin, ok := detail["plugin"].(map[string]any); ok {
			item = plugin
			if sourceID, ok := detail["source_id"].(string); ok {
				item["source_id"] = sourceID
			}
			item["requirements"] = detail["requirements"]
			item["versions"] = mapValue(detail["releases"])["versions"]
			item["sync_error"] = mapValue(detail["releases"])["sync_error"]
		} else {
			item["compatibility"] = "unknown"
			item["recommended_version"] = nil
			item["detail_error"] = detail["error"]
		}
		plugins = append(plugins, item)
	}
	result["plugins"] = plugins
	if includeReadme && len(plugins) > 0 {
		first := mapValue(plugins[0])
		// 只对已核验兼容或未知的候选补 README 摘录
		if compatibility, _ := first["compatibility"].(string); compatibility == "compatible" || compatibility == "unknown" {
			branch := "main"
			if recommended, ok := first["recommended_version"].(string); ok && recommended != "" {
				for _, raw := range mapSliceValue(first["versions"]) {
					version := mapValue(raw)
					if stringValue(version["version"]) == recommended {
						branch = stringValue(version["tag"])
						break
					}
				}
			}
			readme := s.Execute(ctx, "read_plugin_readme", map[string]any{
				"plugin_id": first["id"], "branch": branch, "max_chars": 4000, "prefer_local": true,
			})
			if _, hasError := readme["error"]; hasError {
				result["readme_error"] = readme["error"]
			} else {
				result["readme"] = readme
			}
		}
	}
	return result
}

func (s *MarketStore) readDetail(ctx context.Context, pluginID string) map[string]any {
	result, err := s.Toolset.Detail(ctx, pluginID)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	plugin := mapValue(result["plugin"])
	card := s.recordPlugin(plugin)
	releases := mapValue(result["releases"])
	recommended := stringValue(releases["recommended_version"])
	if recommended != "" {
		card.RecommendedVersion = &recommended
	}
	if stringValue(releases["mode"]) == "branch" {
		if compatible, _ := releases["branch_compatible"].(bool); compatible {
			card.Compatibility = "compatible"
		} else {
			card.Compatibility = "incompatible"
		}
	} else if recommended != "" {
		card.Compatibility = "compatible"
	} else {
		card.Compatibility = "incompatible"
	}
	s.Plugins[card.ID] = card
	result["plugin"] = pluginCardMap(card)
	result["source_id"] = s.recordSource(plugin, "detail", "")
	return result
}

func (s *MarketStore) prepareInstall(ctx context.Context, pluginID, version string) map[string]any {
	result, err := s.Toolset.Detail(ctx, pluginID)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	plugin := mapValue(result["plugin"])
	releases := mapValue(result["releases"])
	if installed, err := s.Toolset.ListInstalled(ctx); err == nil {
		for _, raw := range mapSliceValue(installed["plugins"]) {
			if stringValue(mapValue(raw)["id"]) == stringValue(plugin["id"]) {
				return map[string]any{"error": "插件已经安装，请直接读取并修改插件配置"}
			}
		}
	}
	if syncError := stringValue(releases["sync_error"]); syncError != "" {
		return map[string]any{"error": "插件版本同步失败: " + syncError}
	}
	id := strings.TrimSpace(stringValue(plugin["id"]))
	name := stringValue(plugin["name"])
	mode := stringValue(releases["mode"])
	resolvedVersion := version
	if mode == "branch" {
		if compatible, _ := releases["branch_compatible"].(bool); !compatible || (version != "" && version != "latest") {
			return map[string]any{"error": "分支不兼容或指定了不存在的发布版本"}
		}
		resolvedVersion = "latest"
	} else {
		if resolvedVersion == "" {
			resolvedVersion = stringValue(releases["recommended_version"])
		}
		found := false
		for _, raw := range mapSliceValue(releases["versions"]) {
			item := mapValue(raw)
			if stringValue(item["version"]) == resolvedVersion {
				compatible, _ := item["compatible"].(bool)
				found = compatible
				break
			}
		}
		if !found {
			return map[string]any{"error": "没有指定的兼容发布版本"}
		}
	}
	key := id + ":" + resolvedVersion
	proposal, ok := s.Installs[key]
	if !ok {
		proposal = InstallRequest{
			ID: uuid.NewString(), PluginID: id, Name: name, Version: resolvedVersion,
			RepositoryURL: stringValue(plugin["repository_url"]), Branch: "main", Mode: mode,
		}
		s.Installs[key] = proposal
		s.InstallOrder = append(s.InstallOrder, key)
	}
	s.recordPlugin(plugin)
	return map[string]any{
		"plugin": map[string]any{"id": id, "name": name}, "install_request": proposal,
		"message": "等待用户在界面手动确认，尚未安装",
	}
}

func (s *MarketStore) readConfig(ctx context.Context, pluginID string) map[string]any {
	result, err := s.Toolset.ReadConfig(ctx, pluginID)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	snapshots := map[string]map[string]any{}
	for _, raw := range mapSliceValue(result["fields"]) {
		field := mapValue(raw)
		path := stringSliceValue(field["path"])
		if len(path) != 2 {
			continue
		}
		snapshots[path[0]+"\x00"+path[1]] = field
	}
	s.configSnapshots[pluginID] = snapshots
	return result
}

func (s *MarketStore) updateConfig(ctx context.Context, arguments map[string]any) map[string]any {
	pluginID := stringValue(arguments["plugin_id"])
	snapshot, ok := s.configSnapshots[pluginID]
	if !ok {
		return map[string]any{"error": "修改前必须先调用 read_plugin_config 读取真实字段与当前值"}
	}
	rawChanges := mapSliceValue(arguments["changes"])
	changes := make([]map[string]any, 0, len(rawChanges))
	for _, raw := range rawChanges {
		path := stringSliceValue(raw["path"])
		if len(path) != 2 {
			return map[string]any{"error": "字段路径无效"}
		}
		field, exists := snapshot[path[0]+"\x00"+path[1]]
		if !exists {
			return map[string]any{"error": "字段不存在: " + path[0] + "." + path[1]}
		}
		if readOnly, _ := field["read_only"].(bool); readOnly {
			return map[string]any{"error": "敏感或只读字段请在插件配置页修改: " + path[0] + "." + path[1]}
		}
		change := map[string]any{"path": raw["path"], "value": raw["value"], "before": field["value"]}
		changes = append(changes, change)
	}
	result, err := s.Toolset.UpdateConfig(ctx, pluginID, changes)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	if update := mapValue(result["config_update"]); len(update) > 0 {
		encoded, _ := json.Marshal(update)
		var parsed ConfigUpdate
		if json.Unmarshal(encoded, &parsed) == nil {
			s.Updates = append(s.Updates, parsed)
		}
	}
	delete(s.configSnapshots, pluginID)
	return result
}

// SelectPlugins 只保留本次查询中的真实插件
func (s *MarketStore) SelectPlugins(ids []string) []PluginInfo {
	out := make([]PluginInfo, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		if plugin, ok := s.Plugins[id]; ok {
			seen[id] = struct{}{}
			out = append(out, plugin)
		}
	}
	return out
}

// InstallRequestList 返回按创建顺序的安装请求
func (s *MarketStore) InstallRequestList() []InstallRequest {
	out := make([]InstallRequest, 0, len(s.Installs))
	for _, key := range s.InstallOrder {
		if request, ok := s.Installs[key]; ok {
			out = append(out, request)
		}
	}
	return out
}

func pluginCardMap(plugin PluginInfo) map[string]any {
	return map[string]any{
		"id": plugin.ID, "name": plugin.Name, "description": plugin.Description, "version": plugin.Version,
		"compatibility": plugin.Compatibility, "recommended_version": plugin.RecommendedVersion,
		"reasons": plugin.Reasons,
	}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func boolValue(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func intValue(value any, fallback int) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	default:
		return fallback
	}
}

func mapValue(value any) map[string]any {
	out, _ := value.(map[string]any)
	return out
}

func mapSliceValue(value any) []map[string]any {
	raw, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]map[string]any); ok {
			return typed
		}
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if mapped, ok := item.(map[string]any); ok {
			out = append(out, mapped)
		}
	}
	return out
}

func stringSliceValue(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]string); ok {
			return typed
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		out = append(out, stringValue(item))
	}
	return out
}
