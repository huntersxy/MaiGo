package utils

// A_memorix 运行时载荷共用工具，对齐 Python core/utils/runtime_payloads.py

import (
	"math"
	"strconv"
	"strings"
	"time"

	jsonrepair "github.com/lee00jx/json-repair-go"
)

// CoerceDatetime 对齐 coerce_datetime，失败返回 nil
func CoerceDatetime(value any) *time.Time {
	switch typed := value.(type) {
	case time.Time:
		copied := typed
		return &copied
	case *time.Time:
		if typed == nil {
			return nil
		}
		copied := *typed
		return &copied
	case int:
		return unixToLocal(float64(typed))
	case int64:
		return unixToLocal(float64(typed))
	case float32:
		return unixToLocal(float64(typed))
	case float64:
		return unixToLocal(typed)
	}
	text := pyStrip(importValueText(value))
	if text == "" {
		return nil
	}
	return parseISODatetime(text)
}

// unixToLocal 对齐 datetime.fromtimestamp
func unixToLocal(seconds float64) *time.Time {
	whole := int64(seconds)
	nanos := int64((seconds - float64(whole)) * 1e9)
	parsed := time.Unix(whole, nanos).In(time.Local)
	return &parsed
}

// isoLayouts 覆盖 Python datetime.fromisoformat 的常用输入
var isoLayouts = []string{
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	"2006-01-02",
	time.RFC3339Nano,
}

// parseISODatetime 按 fromisoformat 语义解析，失败返回 nil
func parseISODatetime(text string) *time.Time {
	for _, layout := range isoLayouts {
		if parsed, err := time.ParseInLocation(layout, text, time.Local); err == nil {
			return &parsed
		}
	}
	return nil
}

// SafeJSONLoads 对齐 safe_json_loads：经 json_repair 修复后，非对象一律返回空字典
func SafeJSONLoads(raw any) map[string]any {
	if object, ok := raw.(map[string]any); ok {
		return object
	}
	text := pyStrip(importValueText(raw))
	if text == "" {
		return map[string]any{}
	}
	payload, err := repairJSONValue(text)
	if err != nil {
		return map[string]any{}
	}
	if object, ok := payload.(map[string]any); ok {
		return object
	}
	return map[string]any{}
}

// repairJSONValue 对齐 Python json_repair.repair_json(..., return_objects=True)
func repairJSONValue(text string) (any, error) {
	repaired, _, err := jsonrepair.RepairJSON(text, jsonrepair.Options{
		ReturnObjects: true,
		Logging:       false,
	})
	if err != nil {
		return nil, err
	}
	return plainJSONValue(repaired), nil
}

// plainJSONValue 把 json-repair-go 的 OrderedMap 递归转成 map[string]any
func plainJSONValue(value any) any {
	switch typed := value.(type) {
	case *jsonrepair.OrderedMap:
		converted := make(map[string]any, typed.Len())
		for _, key := range typed.Keys() {
			converted[key] = plainJSONValue(typed.GetOrNil(key))
		}
		return converted
	case map[string]any:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			converted[key] = plainJSONValue(item)
		}
		return converted
	case []any:
		converted := make([]any, len(typed))
		for index, item := range typed {
			converted[index] = plainJSONValue(item)
		}
		return converted
	default:
		return value
	}
}

// repairJSONText 保留给仍需字符串输出的调用方：修复后重新 dumps
func repairJSONText(text string) string {
	payload, err := repairJSONValue(text)
	if err != nil {
		return text
	}
	dumped, err := jsonrepair.Dump(payload, jsonrepair.DumpOptions{EnsureASCII: false})
	if err != nil {
		return text
	}
	return dumped
}

// MergeTokens 对齐 merge_tokens：多组 token 合并并保序去重
func MergeTokens(groups ...any) []string {
	merged := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	for _, group := range groups {
		for _, item := range RuntimeTokens(group) {
			if _, exists := seen[item]; exists {
				continue
			}
			seen[item] = struct{}{}
			merged = append(merged, item)
		}
	}
	return merged
}

// MergeArgumentTokens 对齐 merge_argument_tokens
func MergeArgumentTokens(groups ...any) []string {
	merged := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	for _, group := range groups {
		for _, item := range ArgumentTokens(group) {
			if _, exists := seen[item]; exists {
				continue
			}
			seen[item] = struct{}{}
			merged = append(merged, item)
		}
	}
	return merged
}

// BuildSource 对齐 build_source
func BuildSource(sourceType string, chatID string, personIDs []string) string {
	cleanType := pyStrip(sourceType)
	if cleanType == "" {
		cleanType = "memory"
	}
	if cleanType == "chat_summary" && chatID != "" {
		return "chat_summary:" + chatID
	}
	if cleanType == "person_fact" && len(personIDs) > 0 {
		return "person_fact:" + personIDs[0]
	}
	if chatID != "" {
		return cleanType + ":" + chatID
	}
	return cleanType
}

// ResolveSourceKnowledgeType 对齐 resolve_knowledge_type
func ResolveSourceKnowledgeType(sourceType string) string {
	switch strings.ToLower(pyStrip(sourceType)) {
	case "person_fact":
		return "factual"
	case "chat_summary":
		return "narrative"
	default:
		return "mixed"
	}
}

// SourceTimeMeta 对齐 time_meta：仅在存在时间时补充粒度与置信度
func SourceTimeMeta(timestamp *float64, timeStart *float64, timeEnd *float64) map[string]any {
	payload := map[string]any{}
	if timestamp != nil {
		payload["event_time"] = *timestamp
	}
	if timeStart != nil {
		payload["event_time_start"] = *timeStart
	}
	if timeEnd != nil {
		payload["event_time_end"] = *timeEnd
	}
	if len(payload) > 0 {
		payload["time_granularity"] = "minute"
		payload["time_confidence"] = 0.95
	}
	return payload
}

// OptionalFloat 对齐 optional_float
func OptionalFloat(value any) *float64 {
	if value == nil {
		return nil
	}
	if text, ok := value.(string); ok && text == "" {
		return nil
	}
	parsed, err := pyFloat64(value)
	if err != nil {
		return nil
	}
	return &parsed
}

// OptionalInt 对齐 optional_int
func OptionalInt(value any) *int {
	if value == nil {
		return nil
	}
	if text, ok := value.(string); ok && text == "" {
		return nil
	}
	switch typed := value.(type) {
	case bool:
		converted := 0
		if typed {
			converted = 1
		}
		return &converted
	case int:
		return &typed
	case int64:
		converted := int(typed)
		return &converted
	case float64:
		// 对齐 Python int(float)：向零截断；Inf/NaN 报错
		if math.IsInf(typed, 0) || math.IsNaN(typed) {
			return nil
		}
		converted := int(typed)
		return &converted
	case float32:
		value64 := float64(typed)
		if math.IsInf(value64, 0) || math.IsNaN(value64) {
			return nil
		}
		converted := int(typed)
		return &converted
	}
	text := pyStrip(importValueText(value))
	if text == "" {
		return nil
	}
	// 对齐 Python int("3.5") 抛 ValueError：只接受整数文本
	parsed, err := strconv.Atoi(text)
	if err != nil {
		return nil
	}
	return &parsed
}
