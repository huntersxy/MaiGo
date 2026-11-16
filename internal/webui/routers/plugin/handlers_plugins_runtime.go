package plugin

import (
	"MaiGo/internal/webui/httputil"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"MaiGo/internal/plugin_runtime"
)

const (
	maxHomeCardTextLength      = 4000
	maxHomeCardShortTextLength = 120
	maxHomeCardBlocks          = 20
)

var allowedHomeCardWidths = map[string]struct{}{"small": {}, "medium": {}, "large": {}, "wide": {}, "full": {}}
var allowedHomeCardBlockTypes = map[string]struct{}{
	"markdown": {}, "text": {}, "stat": {}, "key_value": {}, "list": {}, "actions": {},
}

// handleRuntimePluginComponents 对齐 GET /plugins/runtime/plugins/{plugin_id}/components
func (d Deps) handleRuntimePluginComponents(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "plugin_id")
	if _, err := d.resolvePluginConfigPaths(pluginID); err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	components := make([]map[string]any, 0)
	if d.PluginHost != nil {
		if registry := d.PluginHost.Components(); registry != nil {
			for _, entry := range registry.ComponentsByPlugin(pluginID, false, "") {
				if entry.ComponentType != pluginruntime.ComponentTypeCommand && entry.ComponentType != pluginruntime.ComponentTypeTool {
					continue
				}
				components = append(components, serializePluginComponent(entry))
			}
		}
	}
	sort.SliceStable(components, func(i, j int) bool {
		return fmt.Sprint(components[i]["name"]) < fmt.Sprint(components[j]["name"])
	})
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "components": components})
}

// handleRuntimeCommands 对齐 GET /plugins/runtime/commands
func (d Deps) handleRuntimeCommands(w http.ResponseWriter, r *http.Request) {
	commands := []map[string]any{{
		"id":          "core.clear",
		"name":        "clear",
		"description": "清空当前聊天的 Maisaka 历史上下文",
		"plugin_name": "MaiBot 核心",
		"pattern":     "^/clear$",
		"aliases":     []string{},
		"permission":  "operator",
		"enabled":     true,
	}}
	if d.PluginHost != nil {
		if registry := d.PluginHost.Components(); registry != nil {
			entries, err := registry.ComponentsByType(pluginruntime.ComponentTypeCommand, false, pluginruntime.ComponentFilter{})
			if err == nil {
				for _, entry := range entries {
					command := serializePluginComponent(entry)
					command["id"] = entry.PluginID + "." + entry.Name
					commands = append(commands, command)
				}
			}
		}
	}
	sort.SliceStable(commands, func(i, j int) bool {
		left, right := fmt.Sprint(commands[i]["plugin_name"]), fmt.Sprint(commands[j]["plugin_name"])
		if left != right {
			return left < right
		}
		return fmt.Sprint(commands[i]["name"]) < fmt.Sprint(commands[j]["name"])
	})
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "commands": commands})
}

// handleRuntimeHomeCards 对齐 GET /plugins/runtime/home-cards
func (d Deps) handleRuntimeHomeCards(w http.ResponseWriter, r *http.Request) {
	cards := make([]map[string]any, 0)
	if d.PluginHost != nil {
		if registry := d.PluginHost.Components(); registry != nil {
			entries, err := registry.ComponentsByType(pluginruntime.ComponentTypeHomeCard, true, pluginruntime.ComponentFilter{})
			if err == nil {
				for _, entry := range entries {
					cards = append(cards, serializeHomeCard(entry))
				}
			}
		}
	}
	sort.SliceStable(cards, func(i, j int) bool {
		left, right := intValue(cards[i]["order"], 1000), intValue(cards[j]["order"], 1000)
		if left != right {
			return left < right
		}
		if fmt.Sprint(cards[i]["plugin_id"]) != fmt.Sprint(cards[j]["plugin_id"]) {
			return fmt.Sprint(cards[i]["plugin_id"]) < fmt.Sprint(cards[j]["plugin_id"])
		}
		return fmt.Sprint(cards[i]["name"]) < fmt.Sprint(cards[j]["name"])
	})
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "cards": cards})
}

// handleRuntimeHookSpecs 对齐 GET /plugins/runtime/hooks
func (d Deps) handleRuntimeHookSpecs(w http.ResponseWriter, r *http.Request) {
	hookList := make([]map[string]any, 0)
	if d.PluginHost != nil {
		if specs := d.PluginHost.HookSpecs(); specs != nil {
			for _, spec := range specs.ListHookSpecs() {
				parameters := spec.ParametersSchema
				if parameters == nil {
					parameters = map[string]any{}
				}
				hookList = append(hookList, map[string]any{
					"name":                  spec.Name,
					"description":           spec.Description,
					"parameters_schema":     parameters,
					"default_timeout_ms":    spec.DefaultTimeoutMS,
					"allow_blocking":        spec.AllowBlocking(),
					"allow_observe":         spec.AllowObserve(),
					"allow_abort":           spec.AllowAbort(),
					"allow_kwargs_mutation": spec.AllowKwargsMutation(),
				})
			}
		}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "hooks": hookList})
}

// serializePluginComponent 对齐 _serialize_component_entry
func serializePluginComponent(entry *pluginruntime.ComponentEntry) map[string]any {
	componentType := strings.ToLower(entry.ComponentType)
	description := entry.Description
	if description == "" {
		description = stringFromAny(entry.Metadata["description"])
	}
	if description == "" {
		description = stringFromAny(entry.Metadata["brief_description"])
	}
	data := map[string]any{
		"name":           entry.Name,
		"description":    description,
		"enabled":        entry.Enabled,
		"plugin_name":    entry.PluginID,
		"component_type": componentType,
	}
	switch componentType {
	case "tool":
		data["parameters_schema"] = entry.BuildParametersSchema()
	case "command":
		data["pattern"] = entry.CommandPattern
		if data["pattern"] == "" {
			data["pattern"] = stringFromAny(entry.Metadata["command_pattern"])
		}
		data["aliases"] = entry.Aliases
		permission := entry.Permission
		if permission == "" {
			permission = "public"
		}
		data["permission"] = permission
	}
	return data
}

// serializeHomeCard 对齐 _serialize_home_card_entry
func serializeHomeCard(entry *pluginruntime.ComponentEntry) map[string]any {
	metadata := entry.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	width := strings.ToLower(truncateHomeCardText(metadata["width"], 20))
	if width == "" {
		width = strings.ToLower(truncateHomeCardText(entry.Width, 20))
	}
	if _, ok := allowedHomeCardWidths[width]; !ok {
		width = "medium"
	}
	title := truncateHomeCardText(metadata["title"], maxHomeCardShortTextLength)
	if title == "" {
		title = truncateHomeCardText(entry.Title, maxHomeCardShortTextLength)
	}
	showTitle := true
	if value, exists := metadata["show_title"]; exists {
		if boolean, ok := value.(bool); ok && !boolean {
			showTitle = false
		}
	}
	description := truncateHomeCardText(metadata["description"], 300)
	if description == "" {
		description = truncateHomeCardText(entry.Description, 300)
	}
	return map[string]any{
		"id":          "plugin:" + entry.PluginID + ":" + entry.Name,
		"name":        entry.Name,
		"plugin_id":   entry.PluginID,
		"title":       title,
		"show_title":  showTitle,
		"description": description,
		"content":     sanitizeHomeCardContent(metadata["content"]),
		"link_url":    sanitizeHomeCardLink(metadata["link_url"]),
		"link_label":  truncateHomeCardText(metadata["link_label"], 40),
		"icon":        truncateHomeCardText(metadata["icon"], 40),
		"width":       width,
		"order":       entry.Order,
		"enabled":     entry.Enabled,
	}
}

func truncateHomeCardText(value any, maxLength int) string {
	text := ""
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		text = typed
	default:
		text = fmt.Sprint(typed)
	}
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > maxLength {
		text = string(runes[:maxLength])
	}
	return text
}

func sanitizeHomeCardLink(value any) string {
	link := truncateHomeCardText(value, 500)
	if link == "" {
		return ""
	}
	lower := strings.ToLower(link)
	if strings.HasPrefix(lower, "//") {
		return ""
	}
	for _, prefix := range []string{"http://", "https://", "mailto:", "/"} {
		if strings.HasPrefix(lower, prefix) {
			return link
		}
	}
	return ""
}

func sanitizeHomeCardContent(value any) any {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return truncateHomeCardText(typed, maxHomeCardTextLength)
	case []any:
		blocks := make([]any, 0, maxHomeCardBlocks)
		for _, item := range typed {
			if len(blocks) >= maxHomeCardBlocks {
				break
			}
			if block, ok := item.(map[string]any); ok {
				blocks = append(blocks, sanitizeHomeCardBlock(block))
			}
		}
		return blocks
	case map[string]any:
		return sanitizeHomeCardBlock(typed)
	default:
		return ""
	}
}

func sanitizeHomeCardBlock(block map[string]any) map[string]any {
	blockType := strings.ToLower(truncateHomeCardText(block["type"], maxHomeCardShortTextLength))
	if _, ok := allowedHomeCardBlockTypes[blockType]; !ok {
		blockType = "text"
	}
	sanitized := map[string]any{"type": blockType}
	for key, value := range block {
		normalizedKey := truncateHomeCardText(key, 40)
		if normalizedKey == "" || normalizedKey == "type" {
			continue
		}
		switch {
		case normalizedKey == "url" || normalizedKey == "href" || normalizedKey == "link_url":
			sanitized[normalizedKey] = sanitizeHomeCardLink(value)
		case isHomeCardScalar(value):
			if text, ok := value.(string); ok {
				sanitized[normalizedKey] = truncateHomeCardText(text, maxHomeCardTextLength)
			} else {
				sanitized[normalizedKey] = value
			}
		case isHomeCardList(value):
			items := make([]any, 0, maxHomeCardBlocks)
			for _, item := range value.([]any) {
				if len(items) >= maxHomeCardBlocks {
					break
				}
				if child, ok := item.(map[string]any); ok {
					items = append(items, sanitizeHomeCardBlock(child))
				} else {
					items = append(items, truncateHomeCardText(item, maxHomeCardShortTextLength))
				}
			}
			sanitized[normalizedKey] = items
		case isHomeCardMap(value):
			children := map[string]any{}
			count := 0
			for childKey, childValue := range value.(map[string]any) {
				if count >= maxHomeCardBlocks {
					break
				}
				children[truncateHomeCardText(childKey, 40)] = truncateHomeCardText(childValue, maxHomeCardShortTextLength)
				count++
			}
			sanitized[normalizedKey] = children
		}
	}
	return sanitized
}

func isHomeCardScalar(value any) bool {
	switch value.(type) {
	case string, bool, int, int64, float64, float32, int32:
		return true
	}
	return false
}

func isHomeCardList(value any) bool {
	_, ok := value.([]any)
	return ok
}

func isHomeCardMap(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

func mapValue(value any) map[string]any {
	if mapping, ok := value.(map[string]any); ok {
		return mapping
	}
	return map[string]any{}
}

func listValue(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	return []any{}
}

func floatValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	}
	return 0
}

func intValue(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	}
	return fallback
}

func boolValue(value any) bool {
	if boolean, ok := value.(bool); ok {
		return boolean
	}
	return false
}
