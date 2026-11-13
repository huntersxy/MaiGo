package payloadcontent

// Context Item 纯数据快照
// 对等 MaiBot src/llm_models/payload_content/context_snapshot.py：
//   - item_type/meta/parts 等字段名逐字一致
//   - replay fragment 不进入快照；extra_content 中的 encrypted_content/thought_signature 剥离
//   - 反序列化执行严格类型校验，供插件 output_items 与快照读取复用

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// SerializeContextItemSnapshot 序列化单个 Context Item
func SerializeContextItemSnapshot(item ContextItem) map[string]any {
	payload := map[string]any{
		"item_type": string(item.Type),
		"meta":      serializeContextItemMeta(item.Meta),
	}
	switch item.Type {
	case ContextItemSystemMessage, ContextItemUserMessage, ContextItemAssistantMessage:
		payload["parts"] = serializeContextContentParts(item.Parts)
		if item.Type == ContextItemAssistantMessage && item.Phase != "" {
			payload["phase"] = item.Phase
		}
	case ContextItemReasoning:
		representation := item.Representation
		if representation == "" {
			representation = "opaque"
		}
		payload["representation"] = representation
		payload["summary_parts"] = stringSliceAny(item.SummaryParts)
		payload["text_parts"] = stringSliceAny(item.TextParts)
	case ContextItemFunctionCall:
		if item.ToolCall != nil {
			payload["tool_call"] = map[string]any{
				"call_id":       item.ToolCall.CallID,
				"func_name":     item.ToolCall.FuncName,
				"args":          cloneContextMap(item.ToolCall.Args),
				"extra_content": stripPrivateProviderFields(item.ToolCall.ExtraContent),
			}
		}
	case ContextItemFunctionCallOutput:
		payload["call_id"] = item.CallID
		payload["output"] = item.Output
		payload["success"] = item.Success
		payload["tool_name"] = item.ToolName
	case ContextItemProviderActivity:
		payload["action_type"] = item.ActionType
		payload["call_id"] = item.CallID
		payload["details"] = stringSliceAny(item.Details)
		payload["display_summary"] = item.DisplaySummary
		payload["provider_type"] = item.ProviderType
		payload["source_count"] = item.SourceCount
		payload["status"] = item.Status
	case ContextItemProviderOpaque:
		payload["display_summary"] = item.DisplaySummary
		payload["provider_type"] = item.ProviderType
	}
	return payload
}

func serializeContextItemMeta(meta ContextItemMeta) map[string]any {
	var logicalTurnID any
	if strings.TrimSpace(meta.LogicalTurnID) != "" {
		logicalTurnID = meta.LogicalTurnID
	}
	timestamp := meta.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	return map[string]any{
		"item_id":         meta.ItemID,
		"logical_turn_id": logicalTurnID,
		"timestamp":       timestamp.Format(time.RFC3339Nano),
	}
}

func serializeContextContentParts(parts []ContextContentPart) []any {
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case ContextPartText:
			out = append(out, map[string]any{"type": "text", "text": part.Text})
		case ContextPartImage:
			out = append(out, map[string]any{
				"type":         "image",
				"image_base64": part.ImageBase64,
				"image_format": part.ImageFormat,
			})
		case ContextPartRefusal:
			out = append(out, map[string]any{"type": "refusal", "refusal": part.Refusal})
		}
	}
	return out
}

// SerializeContextItemsSnapshot 序列化 Context Items
func SerializeContextItemsSnapshot(items []ContextItem) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, SerializeContextItemSnapshot(item))
	}
	return out
}

// DeserializeContextItemSnapshot 从快照恢复单个无 replay 的 Context Item
func DeserializeContextItemSnapshot(raw any) (ContextItem, error) {
	rawMap, ok := raw.(map[string]any)
	if !ok {
		return ContextItem{}, fmt.Errorf("快照中的 Context Item 必须是字典")
	}
	itemType := contextString(rawMap["item_type"])
	if itemType == "" {
		return ContextItem{}, fmt.Errorf("快照中的 item_type 必须是非空字符串")
	}
	meta, err := deserializeContextItemMeta(rawMap["meta"])
	if err != nil {
		return ContextItem{}, err
	}
	item := ContextItem{Meta: meta}
	switch ContextItemType(itemType) {
	case ContextItemSystemMessage, ContextItemUserMessage, ContextItemAssistantMessage:
		parts, err := deserializeContextContentParts(rawMap["parts"])
		if err != nil {
			return ContextItem{}, err
		}
		item.Type = ContextItemType(itemType)
		item.Parts = parts
		if ContextItemType(itemType) == ContextItemAssistantMessage {
			if rawPhase, exists := rawMap["phase"]; exists && rawPhase != nil {
				phase, ok := rawPhase.(string)
				if !ok {
					return ContextItem{}, fmt.Errorf("AssistantMessageItem phase 必须是字符串或 null")
				}
				item.Phase = phase
			}
		}
	case ContextItemReasoning:
		summaryParts, err := contextStringTuple(rawMap["summary_parts"], "summary_parts")
		if err != nil {
			return ContextItem{}, err
		}
		textParts, err := contextStringTuple(rawMap["text_parts"], "text_parts")
		if err != nil {
			return ContextItem{}, err
		}
		item.Type = ContextItemReasoning
		item.SummaryParts = summaryParts
		item.TextParts = textParts
		item.Representation = contextString(rawMap["representation"])
		if item.Representation == "" {
			return ContextItem{}, fmt.Errorf("ReasoningItem representation 不合法")
		}
		switch item.Representation {
		case "raw_text", "summary", "opaque":
		default:
			return ContextItem{}, fmt.Errorf("ReasoningItem representation 不合法")
		}
	case ContextItemFunctionCall:
		rawToolCall, ok := rawMap["tool_call"].(map[string]any)
		if !ok {
			return ContextItem{}, fmt.Errorf("FunctionCallItem 快照缺少 tool_call")
		}
		callID, _ := rawToolCall["call_id"].(string)
		funcName, _ := rawToolCall["func_name"].(string)
		if callID == "" || funcName == "" {
			return ContextItem{}, fmt.Errorf("FunctionCallItem tool_call 缺少字符串 call_id 或 func_name")
		}
		args, ok := rawToolCall["args"].(map[string]any)
		if !ok {
			return ContextItem{}, fmt.Errorf("FunctionCallItem tool_call.args 必须是字典")
		}
		var extraContent map[string]any
		if rawExtra, exists := rawToolCall["extra_content"]; exists && rawExtra != nil {
			extraContent, ok = rawExtra.(map[string]any)
			if !ok {
				return ContextItem{}, fmt.Errorf("FunctionCallItem tool_call.extra_content 必须是字典或 null")
			}
		}
		item.Type = ContextItemFunctionCall
		call := NewContextToolCall(callID, funcName, cloneContextMap(args))
		call.ExtraContent = cloneContextMap(extraContent)
		item.ToolCall = &call
	case ContextItemFunctionCallOutput:
		callID, callIDOK := rawMap["call_id"].(string)
		output, outputOK := rawMap["output"].(string)
		toolName, toolNameOK := rawMap["tool_name"].(string)
		success, successOK := rawMap["success"].(bool)
		if !callIDOK || !outputOK || !toolNameOK || callID == "" {
			return ContextItem{}, fmt.Errorf("FunctionCallOutputItem call_id、output、tool_name 必须是字符串")
		}
		if !successOK {
			return ContextItem{}, fmt.Errorf("FunctionCallOutputItem success 必须是布尔值")
		}
		item.Type = ContextItemFunctionCallOutput
		item.CallID = callID
		item.Output = output
		item.ToolName = toolName
		item.Success = success
	case ContextItemProviderActivity:
		providerType := contextString(rawMap["provider_type"])
		callID := contextString(rawMap["call_id"])
		status := contextString(rawMap["status"])
		displaySummary := contextString(rawMap["display_summary"])
		actionType := contextString(rawMap["action_type"])
		details, err := contextStringTuple(rawMap["details"], "details")
		if err != nil {
			return ContextItem{}, err
		}
		sourceCount, err := contextInt(rawMap["source_count"])
		if err != nil {
			return ContextItem{}, fmt.Errorf("ProviderActivityItem source_count 必须是整数")
		}
		item.Type = ContextItemProviderActivity
		item.ProviderType = providerType
		item.CallID = callID
		item.Status = status
		item.DisplaySummary = displaySummary
		item.ActionType = actionType
		item.Details = details
		item.SourceCount = sourceCount
	case ContextItemProviderOpaque:
		providerType := contextString(rawMap["provider_type"])
		displaySummary := contextString(rawMap["display_summary"])
		item.Type = ContextItemProviderOpaque
		item.ProviderType = providerType
		item.DisplaySummary = displaySummary
	default:
		return ContextItem{}, fmt.Errorf("不支持的 Context Item 快照类型: %s", itemType)
	}
	return item, nil
}

// DeserializeContextItemsSnapshot 从快照恢复 Context Items
func DeserializeContextItemsSnapshot(raw any) ([]ContextItem, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("快照中的 context_items 必须是列表")
	}
	items := make([]ContextItem, 0, len(list))
	for _, entry := range list {
		item, err := DeserializeContextItemSnapshot(entry)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func deserializeContextItemMeta(raw any) (ContextItemMeta, error) {
	rawMap, ok := raw.(map[string]any)
	if !ok {
		return ContextItemMeta{}, fmt.Errorf("快照中的 item.meta 必须是字典")
	}
	itemID := contextString(rawMap["item_id"])
	if itemID == "" {
		return ContextItemMeta{}, fmt.Errorf("快照中的 item.meta.item_id 必须是非空字符串")
	}
	rawLogicalTurn, exists := rawMap["logical_turn_id"]
	if !exists {
		return ContextItemMeta{}, fmt.Errorf("快照中的 item.meta 缺少 logical_turn_id")
	}
	logicalTurnID := ""
	if rawLogicalTurn != nil {
		text, ok := rawLogicalTurn.(string)
		if !ok || text == "" {
			return ContextItemMeta{}, fmt.Errorf("快照中的 item.meta.logical_turn_id 必须是非空字符串或 null")
		}
		logicalTurnID = text
	}
	rawTimestamp := contextString(rawMap["timestamp"])
	if rawTimestamp == "" {
		return ContextItemMeta{}, fmt.Errorf("快照中的 item.meta.timestamp 必须是非空字符串")
	}
	timestamp, err := parseContextTimestamp(rawTimestamp)
	if err != nil {
		return ContextItemMeta{}, fmt.Errorf("快照中的 item.meta.timestamp 不是合法 ISO 时间")
	}
	return ContextItemMeta{ItemID: itemID, LogicalTurnID: logicalTurnID, Timestamp: timestamp}, nil
}

func parseContextTimestamp(value string) (time.Time, error) {
	layouts := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999", "2006-01-02T15:04:05"}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("非法时间")
}

func deserializeContextContentParts(raw any) ([]ContextContentPart, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("快照中的 item.parts 必须是列表")
	}
	parts := make([]ContextContentPart, 0, len(list))
	for _, entry := range list {
		rawMap, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("快照中的 item part 必须是字典")
		}
		partType := strings.ToLower(contextString(rawMap["type"]))
		switch partType {
		case "text":
			text, ok := rawMap["text"].(string)
			if !ok {
				return nil, fmt.Errorf("文本 part 缺少 text 字段")
			}
			parts = append(parts, ContextContentPart{Type: ContextPartText, Text: text})
		case "image":
			format, formatOK := rawMap["image_format"].(string)
			base64Data, dataOK := rawMap["image_base64"].(string)
			if !formatOK || !dataOK {
				return nil, fmt.Errorf("图片 part 缺少 image_format 或 image_base64")
			}
			parts = append(parts, ContextContentPart{Type: ContextPartImage, ImageFormat: format, ImageBase64: base64Data})
		case "refusal":
			refusal, ok := rawMap["refusal"].(string)
			if !ok {
				return nil, fmt.Errorf("拒答 part 缺少 refusal 字段")
			}
			parts = append(parts, ContextContentPart{Type: ContextPartRefusal, Refusal: refusal})
		default:
			return nil, fmt.Errorf("不支持的快照 Item part 类型: %s", partType)
		}
	}
	return parts, nil
}

func stripPrivateProviderFields(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "-", "_")
		if normalized == "encrypted_content" || normalized == "thought_signature" {
			continue
		}
		out[key] = stripPrivateProviderValue(item)
	}
	return out
}

// StripPrivateProviderValue 去掉私有 provider 扩展字段后返回可快照化的值
//
// 父包的 cache_stats.go 与 context_projection.go 同样需要该能力，
// 因此导出；内部调用点保持原函数名（见下方别名）。
func StripPrivateProviderValue(value any) any {
	return stripPrivateProviderValue(value)
}

func stripPrivateProviderValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return stripPrivateProviderFields(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = stripPrivateProviderValue(item)
		}
		return out
	default:
		return typed
	}
}

func contextString(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func contextStringTuple(value any, fieldName string) ([]string, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("快照中的 %s 必须是字符串列表", fieldName)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("快照中的 %s 必须是字符串列表", fieldName)
		}
		out = append(out, text)
	}
	return out, nil
}

func contextInt(value any) (int, error) {
	switch typed := value.(type) {
	case float64:
		return int(typed), nil
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	default:
		return 0, fmt.Errorf("不是整数")
	}
}

func stringSliceAny(values []string) []any {
	out := make([]any, len(values))
	for index, value := range values {
		out[index] = value
	}
	return out
}

func cloneContextMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

// decodeContextImageBase64 校验并解码图片内容片段
func decodeContextImageBase64(value string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(value)
}
