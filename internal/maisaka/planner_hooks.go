package maisaka

import (
	"context"
	"fmt"
	"strings"

	"maibot-go/internal/plugin_runtime/hooks"

	"go.uber.org/zap"
)

// ContextItemSchemaVersion Hook 载荷版本，对齐 Python item_schema_version
const ContextItemSchemaVersion = 1

// emitPlannerBeforeRequest 发射 maisaka.planner.before_request 并应用 items 覆盖
func emitPlannerBeforeRequest(ctx context.Context, sessionID string, items []ContextItem, tools []ToolSpec, selectedCount int, selectionReason string) ([]ContextItem, []ToolSpec) {
	kwargs := map[string]any{
		"items":                  serializeContextItems(items),
		"item_schema_version":    ContextItemSchemaVersion,
		"tool_definitions":       serializeToolDefinitions(tools),
		"selected_history_count": selectedCount,
		"built_message_count":    len(items),
		"selection_reason":       selectionReason,
		"session_id":             sessionID,
	}
	result := hooks.EmitGlobal(ctx, "maisaka.planner.before_request", kwargs)
	if len(result.Errors) > 0 {
		zap.L().Warn("maisaka.planner.before_request Hook 执行失败", zap.Strings("errors", result.Errors))
	}
	if result.Aborted {
		zap.L().Warn("maisaka.planner.before_request Hook 请求中止，按 DenyAbort 忽略")
	}
	if raw, ok := result.Kwargs["items"]; ok {
		if overridden, ok := deserializeContextItems(raw); ok {
			items = overridden
		}
	}
	if raw, ok := result.Kwargs["tool_definitions"]; ok {
		if overridden, ok := deserializeToolDefinitions(raw); ok {
			tools = overridden
		}
	}
	return items, tools
}

// emitPlannerAfterResponse 发射 maisaka.planner.after_response 并应用正文覆盖
func emitPlannerAfterResponse(ctx context.Context, sessionID string, response *ChatResponse, selectedCount int, selectionReason string) {
	if response == nil {
		return
	}
	outputItems := make([]any, 0, 1+len(response.ToolCalls))
	if response.Content != "" {
		outputItems = append(outputItems, map[string]any{"type": "text", "text": response.Content})
	}
	for _, call := range response.ToolCalls {
		outputItems = append(outputItems, map[string]any{
			"type": "tool_call", "call_id": call.CallID, "name": call.Name, "arguments": call.Arguments,
		})
	}
	kwargs := map[string]any{
		"output_items":           outputItems,
		"item_schema_version":    ContextItemSchemaVersion,
		"selected_history_count": selectedCount,
		"built_message_count":    len(outputItems),
		"selection_reason":       selectionReason,
		"session_id":             sessionID,
		"prompt_tokens":          response.Usage.PromptTokens,
		"completion_tokens":      response.Usage.CompletionTokens,
		"total_tokens":           response.Usage.PromptTokens + response.Usage.CompletionTokens,
	}
	result := hooks.EmitGlobal(ctx, "maisaka.planner.after_response", kwargs)
	if len(result.Errors) > 0 {
		zap.L().Warn("maisaka.planner.after_response Hook 执行失败", zap.Strings("errors", result.Errors))
	}
	if raw, ok := result.Kwargs["output_items"]; ok {
		if text, ok := firstTextOutputItem(raw); ok && text != "" {
			response.Content = text
		}
	}
}

func serializeContextItems(items []ContextItem) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		entry := map[string]any{"role": string(item.Role), "text": item.Text}
		if item.ToolCallID != "" {
			entry["tool_call_id"] = item.ToolCallID
		}
		if item.ToolName != "" {
			entry["tool_name"] = item.ToolName
		}
		if len(item.ToolCalls) > 0 {
			calls := make([]any, 0, len(item.ToolCalls))
			for _, call := range item.ToolCalls {
				callEntry := map[string]any{"call_id": call.CallID, "name": call.Name, "arguments": call.Arguments}
				if len(call.ExtraContent) > 0 {
					callEntry["extra_content"] = call.ExtraContent
				}
				calls = append(calls, callEntry)
			}
			entry["tool_calls"] = calls
		}
		if len(item.Images) > 0 {
			images := make([]any, 0, len(item.Images))
			for _, image := range item.Images {
				images = append(images, map[string]any{"format": image.Format, "description": image.Description, "placeholder": image.Placeholder})
			}
			entry["images"] = images
		}
		out = append(out, entry)
	}
	return out
}

func deserializeContextItems(raw any) ([]ContextItem, bool) {
	entries, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	items := make([]ContextItem, 0, len(entries))
	for _, entry := range entries {
		mapped, ok := entry.(map[string]any)
		if !ok {
			return nil, false
		}
		item := ContextItem{}
		if role, ok := mapped["role"].(string); ok {
			item.Role = ContextRole(role)
		}
		item.Text, _ = mapped["text"].(string)
		item.ToolCallID, _ = mapped["tool_call_id"].(string)
		item.ToolName, _ = mapped["tool_name"].(string)
		if calls, ok := mapped["tool_calls"].([]any); ok {
			for _, callRaw := range calls {
				callMap, ok := callRaw.(map[string]any)
				if !ok {
					continue
				}
				call := ToolCall{}
				call.CallID, _ = callMap["call_id"].(string)
				call.Name, _ = callMap["name"].(string)
				if arguments, ok := callMap["arguments"].(map[string]any); ok {
					call.Arguments = arguments
				}
				if extraContent, ok := callMap["extra_content"].(map[string]any); ok {
					call.ExtraContent = extraContent
				}
				item.ToolCalls = append(item.ToolCalls, call)
			}
		}
		items = append(items, item)
	}
	return items, true
}

func deserializeToolDefinitions(raw any) ([]ToolSpec, bool) {
	entries, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	tools := make([]ToolSpec, 0, len(entries))
	for _, entry := range entries {
		mapped, ok := entry.(map[string]any)
		if !ok {
			return nil, false
		}
		// 兼容 OpenAI function 包装与扁平定义
		if mapped["type"] == "function" {
			if function, ok := mapped["function"].(map[string]any); ok {
				mapped = function
			}
		}
		spec := ToolSpec{}
		spec.Name, _ = mapped["name"].(string)
		spec.Description, _ = mapped["description"].(string)
		if parameters, ok := mapped["parameters"].(map[string]any); ok {
			spec.Parameters = parameters
		} else if schema, ok := mapped["parameters_schema"].(map[string]any); ok {
			spec.Parameters = schema
		}
		if deferred, ok := mapped["deferred"].(bool); ok {
			spec.Deferred = deferred
		}
		if spec.Name == "" {
			return nil, false
		}
		tools = append(tools, spec)
	}
	return tools, true
}

func serializeToolDefinitions(tools []ToolSpec) []any {
	out := make([]any, 0, len(tools))
	for _, spec := range tools {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		description := spec.Description
		if strings.TrimSpace(description) == "" {
			description = "工具 " + name
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": description,
				"parameters":  normalizeToolParameters(spec.Parameters),
			},
		})
	}
	return out
}

// normalizeToolParameters 对齐 Python ToolOption.parameters_schema：
// 对象级 Schema 原样返回，纯属性映射补成 object，无参工具给空对象 Schema
func normalizeToolParameters(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}}
	}
	if raw["type"] == "object" || raw["properties"] != nil || raw["required"] != nil {
		return raw
	}
	properties := map[string]any{}
	required := make([]any, 0)
	for name, value := range raw {
		property, ok := value.(map[string]any)
		if !ok {
			continue
		}
		copied := make(map[string]any, len(property))
		for key, item := range property {
			copied[key] = item
		}
		if isRequired, _ := copied["required"].(bool); isRequired {
			required = append(required, name)
		}
		delete(copied, "required")
		properties[name] = copied
	}
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func firstTextOutputItem(raw any) (string, bool) {
	entries, ok := raw.([]any)
	if !ok {
		return "", false
	}
	for _, entry := range entries {
		mapped, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := mapped["text"].(string); ok && text != "" {
			return text, true
		}
	}
	return "", false
}

// replyToolArgsWithoutInternal 过滤 reply 工具的内部桥接字段，供 Hook 读取
func replyToolArgsWithoutInternal(args map[string]any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]any, len(args))
	for key, value := range args {
		switch key {
		case "content", "text", "_replyer_generated", "reply_to":
			continue
		default:
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func replyToolArgsString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func replyToolArgsInt(args map[string]any, key string) int {
	switch value := args[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func hookString(kwargs map[string]any, key string) string {
	value, _ := kwargs[key].(string)
	return value
}

func hookInt(kwargs map[string]any, key string) int {
	switch value := kwargs[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func hookBool(kwargs map[string]any, key string) bool {
	value, _ := kwargs[key].(bool)
	return value
}

func hookError(errs []string) error {
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", errs[len(errs)-1])
}
