package services

// 服务层共用小工具，供各运行时服务复用 Python 侧的同名语义

import "strings"

// normalizeAction 对齐 Python 的 str(action or "").strip().lower()
func normalizeAction(action string) string {
	return strings.ToLower(strings.TrimSpace(action))
}

// pyIntOr 对齐 Python 的 max(minimum, int(value or fallback)) 取值语义
func pyIntOr(value any, fallback int, minimum int) int {
	if !pyTruthy(value) {
		return clampInt(fallback, minimum)
	}
	return intOr(value, fallback, minimum)
}

// pyFloatOr 对齐 Python float(value or fallback)：0/nil/空视为缺省
func pyFloatOr(value any, fallback float64) float64 {
	if !pyTruthy(value) {
		return fallback
	}
	return spaceFloatAny(value)
}

// clampInt 对整数应用下界
func clampInt(value int, minimum int) int {
	if value < minimum {
		return minimum
	}
	return value
}

// clampIntRange 对整数同时应用上下界，对齐 Python max(min, min(max, value))
func clampIntRange(value int, minimum int, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

// truncateRunes 按字符截断字符串，对齐 Python 的 str(value)[:limit]
func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// cloneAnyMap 浅拷贝字符串键对象，nil 折叠为空对象
func cloneAnyMap(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

// deepCopyAny 递归拷贝 JSON 结构，对齐 Python copy.deepcopy 在本项目的使用范围
func deepCopyAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = deepCopyAny(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, deepCopyAny(item))
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if item == nil {
				out = append(out, nil)
				continue
			}
			out = append(out, deepCopyAny(item).(map[string]any))
		}
		return out
	default:
		return value
	}
}
