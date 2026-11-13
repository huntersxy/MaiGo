package retrieval

// A_memorix 双路检索的通用小工具

import (
	"math"
	"strconv"
	"strings"
)

// dualPathIntValue 对齐 Python int(value)：数值截断取整，字符串解析失败返回 0
func dualPathIntValue(value any) int {
	switch typed := value.(type) {
	case nil:
		return 0
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case uint:
		return int(typed)
	case uint8:
		return int(typed)
	case uint16:
		return int(typed)
	case uint32:
		return int(typed)
	case uint64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	case bool:
		if typed {
			return 1
		}
		return 0
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return 0
		}
		if parsed, err := strconv.ParseFloat(text, 64); err == nil {
			return int(parsed)
		}
		return 0
	}
	return 0
}

// dualPathFloatValue 把任意数值型配置值转成 float，bool 按 0/1 处理
func dualPathFloatValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

// setToSlice 把可见集合转成允许列表，nil 集合返回 nil 表示不过滤
func setToSlice(values map[string]struct{}) []string {
	if values == nil {
		return nil
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	return out
}

// dualPathRoundTo 对齐 Python round(value, digits)，用于写回元数据
func dualPathRoundTo(value float64, digits int) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return value
	}
	factor := math.Pow(10, float64(digits))
	return math.RoundToEven(value*factor) / factor
}

// dualPathItoa 包装整数转字符串
func dualPathItoa(value int) string {
	return strconv.Itoa(value)
}
