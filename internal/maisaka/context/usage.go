package context

import (
	"strings"

	maisaka "maibot-go/internal/maisaka"
)

// 提示词分段键
const (
	SectionSystemPrompt  = "system_prompt"
	SectionMessages      = "messages"
	SectionInstantNotice = "instant_notice"
	SectionMCPTools      = "mcp_tools"
	SectionBuiltinTools  = "builtin_tools"
	SectionPluginTools   = "plugin_tools"
	SectionOtherTools    = "other_tools"
)

// SectionOrder 分段展示顺序，与前端面板行顺序一致
var SectionOrder = []string{
	SectionSystemPrompt,
	SectionMessages,
	SectionInstantNotice,
	SectionMCPTools,
	SectionBuiltinTools,
	SectionPluginTools,
	SectionOtherTools,
}

var toolSectionByProviderType = map[string]string{
	"builtin": SectionBuiltinTools,
	"mcp":     SectionMCPTools,
	"plugin":  SectionPluginTools,
}

// ContextSectionUsage 单个提示词分段的字符占用
type ContextSectionUsage struct {
	Key   string
	Chars int
	Count int
}

// ResolveToolDefinitionName 读取工具定义名称，兼容内部定义与 OpenAI function 结构
func ResolveToolDefinitionName(definition map[string]any) string {
	if function, ok := definition["function"].(map[string]any); ok && definition["type"] == "function" {
		if name, ok := function["name"].(string); ok {
			return name
		}
	}
	if name, ok := definition["name"].(string); ok {
		return name
	}
	return ""
}

// emptyObjectSchema 无参工具的参数 Schema，对齐 Python _build_empty_object_schema
func emptyObjectSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}}
}

// openAIFunctionSchema 把任意工具定义规范化为 OpenAI function 结构
// 对齐 Python ToolOption.from_definition + to_openai_function_schema：属性映射补齐为对象 Schema，
// 缺少 parameters 时回退带 required 的空对象 Schema
func openAIFunctionSchema(definition map[string]any) map[string]any {
	name := ResolveToolDefinitionName(definition)
	description := ""
	var parameters any
	if definition["type"] == "function" {
		if function, ok := definition["function"].(map[string]any); ok {
			description, _ = function["description"].(string)
			parameters = function["parameters"]
		}
	} else {
		description, _ = definition["description"].(string)
		if value, ok := definition["parameters_schema"]; ok {
			parameters = value
		} else {
			parameters = definition["parameters"]
		}
	}
	if strings.TrimSpace(description) == "" {
		description = "工具 " + name
	}
	schema, ok := parameters.(map[string]any)
	if !ok || len(schema) == 0 {
		schema = emptyObjectSchema()
	} else if schema["type"] != "object" && schema["properties"] == nil && schema["required"] == nil {
		schema = map[string]any{"type": "object", "properties": schema}
	}
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": description,
			"parameters":  schema,
		},
	}
}

// MeasureToolDefinitionChars 测量单个工具定义实际发送给模型的字符数
// 对齐 Python len(json.dumps(schema, ensure_ascii=False))：默认分隔符带空格，字符按码点计
func MeasureToolDefinitionChars(definition map[string]any) int {
	encoded := pythonJSONDumps(openAIFunctionSchema(definition), ", ", ": ")
	return len([]rune(encoded))
}

// MeasureItemChars 测量单个上下文 Item 的可见文本字符数
func MeasureItemChars(item maisaka.ContextItem) int {
	chars := len([]rune(item.Text))
	for _, call := range item.ToolCalls {
		chars += len([]rune(call.Name))
		chars += len([]rune(pythonJSONDumps(call.Arguments, ",", ":")))
	}
	return chars
}

func accumulateSection(sections map[string][2]int, key string, chars, count int) {
	if chars <= 0 && count <= 0 {
		return
	}
	entry := sections[key]
	entry[0] += chars
	entry[1] += count
	sections[key] = entry
}

// MeasureRequestSections 测量一次模型请求的提示词分段占用
func MeasureRequestSections(
	items []maisaka.ContextItem,
	historyItemCount int,
	toolDefinitions []map[string]any,
	toolProviderTypes []string,
) []ContextSectionUsage {
	sections := map[string][2]int{}
	systemItemCount := 0
	if len(items) > 0 && items[0].Role == maisaka.RoleSystem {
		systemItemCount = 1
	}
	for _, item := range items[:systemItemCount] {
		accumulateSection(sections, SectionSystemPrompt, MeasureItemChars(item), 1)
	}
	availableHistory := len(items) - systemItemCount
	resolvedHistory := historyItemCount
	if resolvedHistory < 0 {
		resolvedHistory = 0
	}
	if resolvedHistory > availableHistory {
		resolvedHistory = availableHistory
	}
	for _, item := range items[systemItemCount : systemItemCount+resolvedHistory] {
		accumulateSection(sections, SectionMessages, MeasureItemChars(item), 1)
	}
	for _, item := range items[systemItemCount+resolvedHistory:] {
		accumulateSection(sections, SectionInstantNotice, MeasureItemChars(item), 1)
	}
	for index, definition := range toolDefinitions {
		providerType := ""
		if index < len(toolProviderTypes) {
			providerType = toolProviderTypes[index]
		}
		sectionKey, ok := toolSectionByProviderType[providerType]
		if !ok {
			sectionKey = SectionOtherTools
		}
		accumulateSection(sections, sectionKey, MeasureToolDefinitionChars(definition), 1)
	}
	usages := make([]ContextSectionUsage, 0, len(SectionOrder))
	for _, key := range SectionOrder {
		entry, ok := sections[key]
		if !ok || (entry[0] <= 0 && entry[1] <= 0) {
			continue
		}
		usages = append(usages, ContextSectionUsage{Key: key, Chars: entry[0], Count: entry[1]})
	}
	return usages
}
