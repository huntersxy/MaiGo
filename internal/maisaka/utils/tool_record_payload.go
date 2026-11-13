package utils

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"maibot-go/internal/maisaka"
)

// ToolRecordOptions 工具记录落库开关
type ToolRecordOptions struct {
	// RecordStructuredContent 对应 debug.record_tool_structured_content
	RecordStructuredContent bool
}

// NormalizeToolRecordValue 将工具记录值规范化为可 JSON 序列化的结构
func NormalizeToolRecordValue(value any) any {
	if value == nil {
		return nil
	}
	switch typed := value.(type) {
	case string, int, int64, float64, bool:
		return typed
	case time.Time:
		return typed.Format(time.RFC3339Nano)
	case []byte:
		return "<bytes:" + itoa(len(typed)) + ">"
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized[key] = NormalizeToolRecordValue(item)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for index, item := range typed {
			normalized[index] = NormalizeToolRecordValue(item)
		}
		return normalized
	case []string:
		normalized := make([]any, len(typed))
		for index, item := range typed {
			normalized[index] = item
		}
		return normalized
	}
	// 对齐 Python：其余容器逐项递归，未知对象退化为字符串，保证可序列化
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Pointer, reflect.Interface:
		if reflected.IsNil() {
			return nil
		}
		return NormalizeToolRecordValue(reflected.Elem().Interface())
	case reflect.Map:
		normalized := make(map[string]any, reflected.Len())
		iter := reflected.MapRange()
		for iter.Next() {
			normalized[fmt.Sprint(iter.Key().Interface())] = NormalizeToolRecordValue(iter.Value().Interface())
		}
		return normalized
	case reflect.Slice, reflect.Array:
		normalized := make([]any, reflected.Len())
		for index := 0; index < reflected.Len(); index++ {
			normalized[index] = NormalizeToolRecordValue(reflected.Index(index).Interface())
		}
		return normalized
	default:
		return fmt.Sprint(value)
	}
}

// OmitToolRecordLargeMedia 移除工具记录落库数据中的内联媒体大字段
func OmitToolRecordLargeMedia(value any) any {
	mediaKeys := map[string]struct{}{
		"base64": {}, "data": {}, "image_base64": {}, "audio_base64": {}, "emoji_base64": {},
	}
	switch typed := value.(type) {
	case map[string]any:
		contentType := lowerTrim(stringField(typed, "content_type"))
		mimeType := trimField(typed, "mime_type")
		imageFormat := trimField(typed, "image_format")
		if imageFormat == "" {
			imageFormat = trimField(typed, "format")
		}
		omitted := make(map[string]any, len(typed))
		for key, item := range typed {
			if _, isMedia := mediaKeys[key]; isMedia {
				if text, ok := item.(string); ok && IsLargeInlineMediaText(text) {
					omitted[key] = BuildOmittedMediaMarker(text, contentType, mimeType, imageFormat)
					continue
				}
			}
			omitted[key] = OmitToolRecordLargeMedia(item)
		}
		return omitted
	case []any:
		omitted := make([]any, len(typed))
		for index, item := range typed {
			omitted[index] = OmitToolRecordLargeMedia(item)
		}
		return omitted
	case string:
		if IsLargeInlineMediaText(typed) {
			return BuildOmittedMediaMarker(typed, "", "", "")
		}
		return typed
	default:
		return value
	}
}

// IsLargeInlineMediaText 判断字符串是否是内联媒体大字段
func IsLargeInlineMediaText(value string) bool {
	if len(value) <= 1024 {
		return false
	}
	normalized := strings.TrimSpace(value)
	if strings.HasPrefix(normalized, "data:image/") || strings.HasPrefix(normalized, "data:audio/") {
		return true
	}
	if len(normalized) < 4096 {
		return false
	}
	sample := normalized[:4096]
	for _, char := range sample {
		if !isBase64Char(char) {
			return false
		}
	}
	return true
}

func isBase64Char(char rune) bool {
	switch {
	case char >= 'A' && char <= 'Z':
		return true
	case char >= 'a' && char <= 'z':
		return true
	case char >= '0' && char <= '9':
		return true
	}
	switch char {
	case '+', '/', '=', '\r', '\n':
		return true
	}
	return false
}

// BuildOmittedMediaMarker 构造媒体省略标记
func BuildOmittedMediaMarker(value, contentType, mimeType, imageFormat string) map[string]any {
	marker := map[string]any{
		"base64_omitted":  true,
		"original_length": len(value),
		"approx_bytes":    len(value) * 3 / 4,
	}
	if contentType != "" {
		marker["content_type"] = contentType
	}
	if mimeType != "" {
		marker["mime_type"] = mimeType
	}
	if imageFormat != "" {
		marker["image_format"] = imageFormat
	}
	return marker
}

// BuildOmittedStructuredContentMarker 构造结构化内容省略标记
func BuildOmittedStructuredContentMarker(value any) map[string]any {
	marker := map[string]any{
		"structured_content_omitted": true,
		"reason":                     "debug.record_tool_structured_content=false",
		"value_type":                 typeName(value),
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		marker["top_level_keys"] = keys
	case []any:
		marker["item_count"] = len(typed)
	}
	return marker
}

// BuildToolRecordStructuredContent 构造工具记录中的 structured_content 字段
func BuildToolRecordStructuredContent(value any, options ToolRecordOptions) any {
	if value == nil {
		return nil
	}
	normalized := NormalizeToolRecordValue(value)
	if !options.RecordStructuredContent {
		return BuildOmittedStructuredContentMarker(normalized)
	}
	return OmitToolRecordLargeMedia(normalized)
}

// BuildToolRecordPayload 构造统一工具落库数据
func BuildToolRecordPayload(
	invocation maisaka.ToolInvocation,
	result maisaka.ToolExecutionResult,
	toolSpec *maisaka.ToolSpec,
	options ToolRecordOptions,
) map[string]any {
	payload := map[string]any{
		"call_id":              invocation.CallID,
		"session_id":           invocation.SessionID,
		"stream_id":            invocation.StreamID,
		"arguments":            NormalizeToolRecordValue(invocation.Arguments),
		"success":              result.Success,
		"content":              result.Content,
		"error_message":        errorMessage(result),
		"history_content":      result.GetHistoryContent(),
		"structured_content":   BuildToolRecordStructuredContent(structuredContent(result), options),
		"metadata":             NormalizeToolRecordValue(result.Metadata),
		"stop_after_execution": result.StopAfterExecution,
	}
	if toolSpec != nil {
		payload["provider_name"] = toolSpec.Provider
		payload["provider_type"] = toolSpec.ProviderType
		payload["description"] = toolSpec.Description
		payload["title"] = toolSpec.Title
	}
	return payload
}

func structuredContent(result maisaka.ToolExecutionResult) any {
	if result.Metadata == nil {
		return nil
	}
	return result.Metadata["structured_content"]
}

func errorMessage(result maisaka.ToolExecutionResult) string {
	if result.Metadata == nil {
		return ""
	}
	if value, ok := result.Metadata["error_message"].(string); ok {
		return value
	}
	return ""
}

func stringField(items map[string]any, key string) string {
	value, _ := items[key].(string)
	return value
}

func trimField(items map[string]any, key string) string {
	return strings.TrimSpace(stringField(items, key))
}

func lowerTrim(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func typeName(value any) string {
	switch value.(type) {
	case nil:
		return "NoneType"
	case string:
		return "str"
	case bool:
		return "bool"
	case int, int64:
		return "int"
	case float64:
		return "float"
	case []any:
		return "list"
	case map[string]any:
		return "dict"
	default:
		return "object"
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 20)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

var _ = utf8.RuneLen
