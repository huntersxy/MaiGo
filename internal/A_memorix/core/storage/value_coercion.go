package storage

// A_memorix 取值转换工具，对齐 Python core/storage/value_coercion.py

import "strconv"

// OptionalFloat 对齐 Python optional_float：无法转换时返回 nil
func OptionalFloat(value any) *float64 {
	if value == nil {
		return nil
	}
	switch v := value.(type) {
	case float64:
		return &v
	case float32:
		f := float64(v)
		return &f
	case int:
		f := float64(v)
		return &f
	case int64:
		f := float64(v)
		return &f
	case []byte:
		if f, err := strconv.ParseFloat(string(v), 64); err == nil {
			return &f
		}
		return nil
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return &f
		}
		return nil
	default:
		return nil
	}
}
