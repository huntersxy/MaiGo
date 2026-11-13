package pluginmarket

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func trimFold(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func containsFold(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	return strings.Contains(strings.ToLower(haystack), needle)
}

// safeString 对齐 catalog._safe_string：外部字段转稳定字符串
func safeString(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case bool:
		if t {
			return "True"
		}
		return "False"
	case float64:
		return strings.TrimSpace(strconv.FormatFloat(t, 'f', -1, 64))
	case float32:
		return strings.TrimSpace(strconv.FormatFloat(float64(t), 'f', -1, 32))
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case fmt.Stringer:
		return strings.TrimSpace(t.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// safeInt 对齐 catalog._safe_int：布尔与不可转换值回落默认值
func safeInt(v any, fallback int) int {
	if v == nil {
		return fallback
	}
	switch t := v.(type) {
	case bool:
		return fallback
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return fallback
		}
		return parsed
	}
	return fallback
}

// stringList 对齐 catalog._string_list：非列表回落空列表并丢弃空项
func stringList(v any) []string {
	if typed, ok := v.([]string); ok {
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s := safeString(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	list, ok := v.([]any)
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s := safeString(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func anyMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok && m != nil
}

func anyList(v any) ([]any, bool) {
	l, ok := v.([]any)
	return l, ok
}

// copyAnyMap 复制一层字典，避免调用方改写缓存
func copyAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// mergeAnyMap 复制后覆盖指定键，对齐 Python 的 {**base, "key": value}
func mergeAnyMap(base map[string]any, key string, value any) map[string]any {
	out := copyAnyMap(base)
	if out == nil {
		out = map[string]any{}
	}
	out[key] = value
	return out
}

// deepCopyJSON 复制 JSON 结构，对齐 market_cache 的 deepcopy 保护
func deepCopyJSON(value any) any {
	switch t := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, v := range t {
			out[k] = deepCopyJSON(v)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, v := range t {
			out[i] = deepCopyJSON(v)
		}
		return out
	default:
		return value
	}
}

// httpStatusText 对齐 urllib 的 reason 文案，未知状态码回落空串
func httpStatusText(code int) string {
	return http.StatusText(code)
}
