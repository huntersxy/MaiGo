package memruntime

// A_memorix 运行时配置取值与 Python 类型转换语义，供内核与检索运行时初始化复用
// 说明：这里只保留 Python 内置转换函数的行为差异，不引入业务语义

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ConfigValue 对齐 Python _get_config_value：按点号逐层取值，缺失返回默认值
// 注意：键存在但值为 None 时返回 None，不回退默认值
func ConfigValue(config map[string]any, key string, defaultValue any) any {
	if config == nil {
		return defaultValue
	}
	var current any = config
	for _, part := range strings.Split(key, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return defaultValue
		}
		value, exists := object[part]
		if !exists {
			return defaultValue
		}
		current = value
	}
	return current
}

// SafeDict 对齐 Python _safe_dict：非 dict 一律返回空字典
func SafeDict(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return map[string]any{}
}

// PyTruthy 对齐 Python 真值判断（bool() 与 or 的真值语义）
func PyTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case int:
		return typed != 0
	case int8:
		return typed != 0
	case int16:
		return typed != 0
	case int32:
		return typed != 0
	case int64:
		return typed != 0
	case uint:
		return typed != 0
	case uint8:
		return typed != 0
	case uint16:
		return typed != 0
	case uint32:
		return typed != 0
	case uint64:
		return typed != 0
	case float32:
		return typed != 0
	case float64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// PyStr 对齐 Python str()：None 输出 None
func PyStr(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprint(value)
	}
}

// PyInt 对齐 Python int()：字符串按十进制解析，浮点向零截断
func PyInt(value any) (int, error) {
	switch typed := value.(type) {
	case nil:
		return 0, errors.New("int() argument must be a string or a number, not 'NoneType'")
	case bool:
		if typed {
			return 1, nil
		}
		return 0, nil
	case int:
		return typed, nil
	case int8:
		return int(typed), nil
	case int16:
		return int(typed), nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case uint:
		return int(typed), nil
	case uint8:
		return int(typed), nil
	case uint16:
		return int(typed), nil
	case uint32:
		return int(typed), nil
	case uint64:
		return int(typed), nil
	case float32:
		return int(typed), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, fmt.Errorf("cannot convert float %v to integer", typed)
		}
		return int(typed), nil
	case string:
		text := strings.TrimSpace(typed)
		normalized := strings.ReplaceAll(text, "_", "")
		parsed, err := strconv.ParseInt(normalized, 10, 64)
		if err != nil {
			if floatValue, floatErr := strconv.ParseFloat(normalized, 64); floatErr == nil && !strings.ContainsAny(normalized, "eE.") {
				return int(floatValue), nil
			}
			return 0, fmt.Errorf("invalid literal for int() with base 10: '%s'", text)
		}
		return int(parsed), nil
	default:
		return 0, fmt.Errorf("int() argument must be a string or a number, not '%s'", typeName(value))
	}
}

// PyFloat 对齐 Python float()
func PyFloat(value any) (float64, error) {
	switch typed := value.(type) {
	case nil:
		return 0, errors.New("float() argument must be a string or a number, not 'NoneType'")
	case bool:
		if typed {
			return 1, nil
		}
		return 0, nil
	case int:
		return float64(typed), nil
	case int8:
		return float64(typed), nil
	case int16:
		return float64(typed), nil
	case int32:
		return float64(typed), nil
	case int64:
		return float64(typed), nil
	case uint:
		return float64(typed), nil
	case uint8:
		return float64(typed), nil
	case uint16:
		return float64(typed), nil
	case uint32:
		return float64(typed), nil
	case uint64:
		return float64(typed), nil
	case float32:
		return float64(typed), nil
	case float64:
		return typed, nil
	case string:
		text := strings.TrimSpace(typed)
		normalized := strings.ReplaceAll(text, "_", "")
		parsed, err := strconv.ParseFloat(normalized, 64)
		if err != nil {
			return 0, fmt.Errorf("could not convert string to float: '%s'", text)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("float() argument must be a string or a number, not '%s'", typeName(value))
	}
}

// typeName 返回用于错误文案的类型别名
func typeName(value any) string {
	switch value.(type) {
	case []any:
		return "list"
	case map[string]any:
		return "dict"
	case string:
		return "str"
	case bool:
		return "bool"
	default:
		return fmt.Sprintf("%T", value)
	}
}

// decodeConfigObject 对齐 Python SomeConfig(**raw) 的严格关键字语义
// 未知键与已注册键的错误文案都与 dataclass TypeError 保持同一形态
func decodeConfigObject(typeLabel string, raw map[string]any, setters map[string]func(any) error) error {
	for key, value := range raw {
		setter, ok := setters[key]
		if !ok {
			return fmt.Errorf("%s() got an unexpected keyword argument '%s'", typeLabel, key)
		}
		if err := setter(value); err != nil {
			return err
		}
	}
	return nil
}

// intSetter 生成整型字段赋值函数
func intSetter(target *int) func(any) error {
	return func(value any) error {
		converted, err := PyInt(value)
		if err != nil {
			return err
		}
		*target = converted
		return nil
	}
}

// floatSetter 生成浮点字段赋值函数
func floatSetter(target *float64) func(any) error {
	return func(value any) error {
		converted, err := PyFloat(value)
		if err != nil {
			return err
		}
		*target = converted
		return nil
	}
}

// boolSetter 生成布尔字段赋值函数，对齐 Python bool() 的真值转换
func boolSetter(target *bool) func(any) error {
	return func(value any) error {
		*target = PyTruthy(value)
		return nil
	}
}

// strSetter 生成字符串字段赋值函数，对齐 Python str 字段的 or 归一前置值
func strSetter(target *string) func(any) error {
	return func(value any) error {
		if value == nil {
			*target = ""
			return nil
		}
		*target = PyStr(value)
		return nil
	}
}
