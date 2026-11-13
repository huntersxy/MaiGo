package capabilities

// 数值参数归一工具。
//
// 对齐 Python component_timeout 的数值解析：插件传来的 timeout/参数可能是
// 任意 JSON 数值类型，统一收敛到 int64/float64，无法识别时报错而不是静默取 0。

import (
	"fmt"
	"math"
)

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
