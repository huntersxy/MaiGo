package maisaka

import (
	"strings"
)

// LightweightToolParameters 轻量私聊常驻内置工具及其允许的参数
var LightweightToolParameters = map[string][]string{
	"query_memory":         {"query"},
	"query_person_profile": {"person_name"},
	"view_forward_message": {"msg_id", "path"},
	"tool_search":          {"query"},
	"wait":                 {},
	"schedule":             {"action", "text", "delay_seconds", "due_at", "todo_id", "include_completed"},
}

// IsLightweightTool 判断工具是否属于轻量模式常驻内置工具
func IsLightweightTool(name string) bool {
	_, ok := LightweightToolParameters[strings.TrimSpace(name)]
	return ok
}

// BuildLightweightToolSpecs 复用已通过可用性检查的内置工具，隐藏高级检索参数
func BuildLightweightToolSpecs(specs []ToolSpec) []ToolSpec {
	out := make([]ToolSpec, 0, len(specs))
	for _, spec := range specs {
		allowed, ok := LightweightToolParameters[strings.TrimSpace(spec.Name)]
		if !ok {
			continue
		}
		definition := spec
		parameters := cloneDiscoveryMap(spec.Parameters)
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		properties, _ := parameters["properties"].(map[string]any)
		filtered := make(map[string]any, len(allowed))
		for _, name := range allowed {
			if value, exists := properties[name]; exists {
				filtered[name] = value
			}
		}
		parameters["properties"] = filtered
		parameters["additionalProperties"] = false
		if spec.Name == "query_memory" {
			parameters["required"] = []any{"query"}
		} else if spec.Name == "query_person_profile" {
			parameters["required"] = []any{"person_name"}
		}
		definition.Parameters = parameters
		if spec.Name == "wait" {
			definition.Description = "不发送消息，静默结束本轮思考并等待新消息；无需回复时单独调用，不填参数。"
		}
		out = append(out, definition)
	}
	return out
}
