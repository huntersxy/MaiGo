package maisaka

import (
	"strings"
)

// ToolLoadedInstruction 末尾加载模式下 tool_search 结果的调用指引
const ToolLoadedInstruction = "本次返回的工具定义已经加载，现在可直接按定义的名称和参数发起工具调用；无需等待顶层 tools 数组更新，也不要再次搜索已返回的工具。"

// DiscoveredToolDefinition tool_search 结果中携带的完整工具定义
type DiscoveredToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// BuildToolSearchResult 冻结命中定义和加载状态；后续注册表变化不改写已写入的搜索结果
func BuildToolSearchResult(query string, tools []DiscoveredToolDefinition, newlyDiscoveredToolNames []string) map[string]any {
	matchedNames := make([]any, 0, len(tools))
	definitions := make([]any, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		matchedNames = append(matchedNames, name)
		parameters := tool.Parameters
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		definitions = append(definitions, map[string]any{
			"name":        name,
			"description": strings.TrimSpace(tool.Description),
			"parameters":  cloneDiscoveryMap(parameters),
		})
	}
	newly := make([]any, 0, len(newlyDiscoveredToolNames))
	for _, name := range newlyDiscoveredToolNames {
		newly = append(newly, name)
	}
	status := "not_found"
	instruction := "未找到匹配工具，请换用工具名或其他关键词重新搜索。"
	if len(tools) > 0 {
		status = "loaded"
		instruction = ToolLoadedInstruction
	}
	return map[string]any{
		"query":                       strings.TrimSpace(query),
		"matched_tool_names":          matchedNames,
		"newly_discovered_tool_names": newly,
		"status":                      status,
		"instruction":                 instruction,
		"tools":                       definitions,
	}
}

// SearchToolSpecs 按工具名、前缀和描述排序，普通聊天和共享 Planner 使用相同检索规则
func SearchToolSpecs(specs []ToolSpec, query string, limit int) []ToolSpec {
	normalizedQuery := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(query))), " ")
	if normalizedQuery == "" {
		return nil
	}
	queryTerms := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(normalizedQuery))
	type scored struct {
		score int
		name  string
		spec  ToolSpec
	}
	matches := make([]scored, 0, len(specs))
	for _, spec := range specs {
		name := strings.ToLower(strings.TrimSpace(spec.Name))
		description := strings.ToLower(spec.Description)
		score := 0
		if normalizedQuery == name {
			score += 1000
		}
		if strings.HasPrefix(name, normalizedQuery) {
			score += 300
		}
		if strings.Contains(name, normalizedQuery) {
			score += 200
		}
		if strings.Contains(description, normalizedQuery) {
			score += 100
		}
		for _, term := range queryTerms {
			if strings.Contains(name, term) {
				score += 25
			}
			if strings.Contains(description, term) {
				score += 10
			}
		}
		if score > 0 {
			matches = append(matches, scored{score: score, name: name, spec: spec})
		}
	}
	for i := 0; i < len(matches); i++ {
		for j := i + 1; j < len(matches); j++ {
			if matches[j].score > matches[i].score ||
				(matches[j].score == matches[i].score && matches[j].name < matches[i].name) {
				matches[i], matches[j] = matches[j], matches[i]
			}
		}
	}
	if limit < 1 {
		limit = 1
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]ToolSpec, 0, len(matches))
	for _, match := range matches {
		out = append(out, match.spec)
	}
	return out
}

func cloneDiscoveryMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	cloned, _ := cloneDiscoveryValue(value).(map[string]any)
	return cloned
}

func cloneDiscoveryValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		copied := make(map[string]any, len(typed))
		for key, item := range typed {
			copied[key] = cloneDiscoveryValue(item)
		}
		return copied
	case []any:
		copied := make([]any, 0, len(typed))
		for _, item := range typed {
			copied = append(copied, cloneDiscoveryValue(item))
		}
		return copied
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
