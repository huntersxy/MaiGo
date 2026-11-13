package webuiapi

import (
	"net/http"
	"strconv"
)

// 从任意 JSON 解出的 map/slice 中安全取值的小工具。
//
// 父包与 plugin 子包都要用，属于跨域通用工具，因此留在父包。

// listValue 取数组，非数组返回空切片
func listValue(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	return []any{}
}

// floatValue 取浮点数，无法识别返回 0
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

// intValue 取整数，无法识别返回 fallback
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

// boolValue 取布尔值，非布尔返回 false
func boolValue(value any) bool {
	if boolean, ok := value.(bool); ok {
		return boolean
	}
	return false
}

// queryInt 读取整型查询参数并钳制到 [min, max]
// handlers_resources / handlers_system_cache 等父包文件使用；chat 子包有同名副本。
func queryInt(r *http.Request, key string, def, min, max int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}
