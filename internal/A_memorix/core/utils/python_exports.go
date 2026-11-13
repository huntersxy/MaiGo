package utils

// 跨包复用的 Python 兼容取值助手，避免下游重复实现

// PyJSONMarshal 对齐 json.dumps(value, ensure_ascii=False, sort_keys=True) 的常用语义
func PyJSONMarshal(value any) (string, error) { return pyJSONMarshal(value) }

// PyStr 对齐 Python str() 的常用语义
func PyStr(value any) string { return pyStr(value) }

// PyFloatOrZero 对齐 float(value)，失败时返回 0
func PyFloatOrZero(value any) float64 {
	number, err := pyFloat64(value)
	if err != nil {
		return 0
	}
	return number
}

// PyIntOrZero 对齐 int(value)，失败时返回 0
func PyIntOrZero(value any) int {
	number, err := pyFloat64(value)
	if err != nil {
		return 0
	}
	return int(number)
}

// PyBool 对齐 bool(value)，异常按 Python 真值语义回退
func PyBool(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	default:
		number, err := pyFloat64(typed)
		if err != nil {
			return isTruthy(typed)
		}
		return number != 0
	}
}
