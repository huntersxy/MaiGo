package capabilities

import (
	"strconv"
	"strings"
)

// 能力参数解析的通用小工具。
//
// 这些函数在父包与能力层都要用（父包处理 RPC payload，能力层解析 args），
// 两处都是纯函数，无状态、无依赖，因此各保留一份，避免为 10 行代码
// 引入跨包依赖。语义必须一致，由 TestCapabilityHelpersMatchHost 兜底校验。

func coalesceFloat(args map[string]any, key string, def float64) float64 {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err == nil {
			return f
		}
	}
	return def
}

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func payloadMap(payload map[string]any, key string) map[string]any {
	if raw, ok := payload[key].(map[string]any); ok && raw != nil {
		return raw
	}
	return map[string]any{}
}

func truthyPayloadValue(raw any) bool {
	switch value := raw.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return strings.TrimSpace(value) != ""
	case int:
		return value != 0
	case int8:
		return value != 0
	case int16:
		return value != 0
	case int32:
		return value != 0
	case int64:
		return value != 0
	case uint:
		return value != 0
	case uint8:
		return value != 0
	case uint16:
		return value != 0
	case uint32:
		return value != 0
	case uint64:
		return value != 0
	case float32:
		return value != 0
	case float64:
		return value != 0
	}
	return true
}
