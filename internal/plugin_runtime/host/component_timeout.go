package host

import (
	"fmt"
	"math"
)

// DefaultComponentRPCTimeoutMS 组件未声明超时时的默认 RPC 超时
const DefaultComponentRPCTimeoutMS int64 = 60000

// NormalizeComponentTimeoutMS 规范化组件超时元数据，返回 0 表示未声明
//
// 对齐 host/component_timeout.py normalize_component_timeout_ms
func NormalizeComponentTimeoutMS(raw any) (int64, error) {
	if raw == nil {
		return 0, nil
	}
	value, err := coerceInt64(raw)
	if err != nil {
		return 0, fmt.Errorf("component timeout_ms is invalid: %v", raw)
	}
	if value < 0 {
		return 0, fmt.Errorf("component timeout_ms cannot be negative: %v", raw)
	}
	return value, nil
}

// ResolveComponentRPCTimeoutMS 返回实际调用使用的超时毫秒数；非法元数据报错
func ResolveComponentRPCTimeoutMS(raw any) (int64, error) {
	timeoutMS, err := NormalizeComponentTimeoutMS(raw)
	if err != nil {
		return 0, err
	}
	if timeoutMS == 0 {
		return DefaultComponentRPCTimeoutMS, nil
	}
	return timeoutMS, nil
}

func coerceInt64(raw any) (int64, error) {
	switch value := raw.(type) {
	case int:
		return int64(value), nil
	case int8:
		return int64(value), nil
	case int16:
		return int64(value), nil
	case int32:
		return int64(value), nil
	case int64:
		return value, nil
	case uint:
		return int64(value), nil
	case uint8:
		return int64(value), nil
	case uint16:
		return int64(value), nil
	case uint32:
		return int64(value), nil
	case uint64:
		if value > math.MaxInt64 {
			return 0, fmt.Errorf("overflow")
		}
		return int64(value), nil
	case float32:
		return int64(value), nil
	case float64:
		return int64(value), nil
	case string:
		var parsed int64
		if _, err := fmt.Sscan(value, &parsed); err != nil {
			return 0, err
		}
		return parsed, nil
	case bool:
		if value {
			return 1, nil
		}
		return 0, nil
	default:
		return 0, fmt.Errorf("unsupported")
	}
}

// coerceFloat64 把任意数字参数转换为 float64
func coerceFloat64(raw any) (float64, error) {
	switch value := raw.(type) {
	case int:
		return float64(value), nil
	case int32:
		return float64(value), nil
	case int64:
		return float64(value), nil
	case float32:
		return float64(value), nil
	case float64:
		return value, nil
	case string:
		var parsed float64
		if _, err := fmt.Sscan(value, &parsed); err != nil {
			return 0, err
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unsupported")
	}
}
