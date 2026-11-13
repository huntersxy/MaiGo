package services

// Python 兼容的 canonical JSON 编码，用于记忆包内容摘要与成员字节
// 对齐 Python json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// pythonCanonicalJSON 输出与 Python json.dumps 字节级一致的紧凑 JSON
func pythonCanonicalJSON(value any) ([]byte, error) {
	normalized, err := normalizePythonJSON(value)
	if err != nil {
		return nil, err
	}
	buffer := &bytes.Buffer{}
	if err := appendPythonJSON(buffer, normalized); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// normalizePythonJSON 把常见 Go 容器收敛为 map[string]any 与 []any
func normalizePythonJSON(value any) (any, error) {
	switch typed := value.(type) {
	case nil, string, bool:
		return typed, nil
	case json.Number:
		return typed, nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return typed, nil
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			converted, err := normalizePythonJSON(item)
			if err != nil {
				return nil, err
			}
			out[key] = converted
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			converted, err := normalizePythonJSON(item)
			if err != nil {
				return nil, err
			}
			out = append(out, converted)
		}
		return out, nil
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			converted, err := normalizePythonJSON(item)
			if err != nil {
				return nil, err
			}
			out = append(out, converted)
		}
		return out, nil
	case []string:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out, nil
	}
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil, nil
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("canonical JSON 不支持非字符串键: %s", rv.Type())
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			converted, err := normalizePythonJSON(iter.Value().Interface())
			if err != nil {
				return nil, err
			}
			out[iter.Key().String()] = converted
		}
		return out, nil
	case reflect.Slice, reflect.Array:
		out := make([]any, 0, rv.Len())
		for index := 0; index < rv.Len(); index++ {
			converted, err := normalizePythonJSON(rv.Index(index).Interface())
			if err != nil {
				return nil, err
			}
			out = append(out, converted)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("canonical JSON 不支持的类型: %T", value)
	}
}

// appendPythonJSON 按 Python json.dumps 的规则写出值
func appendPythonJSON(buffer *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		buffer.WriteString("null")
		return nil
	case bool:
		if typed {
			buffer.WriteString("true")
		} else {
			buffer.WriteString("false")
		}
		return nil
	case string:
		appendPythonJSONString(buffer, typed)
		return nil
	case json.Number:
		text := typed.String()
		if strings.ContainsAny(text, ".eE") {
			parsed, err := typed.Float64()
			if err != nil {
				return err
			}
			return appendPythonJSONFloat(buffer, parsed)
		}
		if _, err := strconv.ParseInt(text, 10, 64); err != nil {
			if _, unsignedErr := strconv.ParseUint(text, 10, 64); unsignedErr != nil {
				return fmt.Errorf("canonical JSON 数值无效: %s", text)
			}
		}
		buffer.WriteString(text)
		return nil
	case int:
		buffer.WriteString(strconv.FormatInt(int64(typed), 10))
		return nil
	case int8:
		buffer.WriteString(strconv.FormatInt(int64(typed), 10))
		return nil
	case int16:
		buffer.WriteString(strconv.FormatInt(int64(typed), 10))
		return nil
	case int32:
		buffer.WriteString(strconv.FormatInt(int64(typed), 10))
		return nil
	case int64:
		buffer.WriteString(strconv.FormatInt(typed, 10))
		return nil
	case uint:
		buffer.WriteString(strconv.FormatUint(uint64(typed), 10))
		return nil
	case uint8:
		buffer.WriteString(strconv.FormatUint(uint64(typed), 10))
		return nil
	case uint16:
		buffer.WriteString(strconv.FormatUint(uint64(typed), 10))
		return nil
	case uint32:
		buffer.WriteString(strconv.FormatUint(uint64(typed), 10))
		return nil
	case uint64:
		buffer.WriteString(strconv.FormatUint(typed, 10))
		return nil
	case float32:
		return appendPythonJSONFloat(buffer, float64(typed))
	case float64:
		return appendPythonJSONFloat(buffer, typed)
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buffer.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			appendPythonJSONString(buffer, key)
			buffer.WriteByte(':')
			if err := appendPythonJSON(buffer, typed[key]); err != nil {
				return err
			}
		}
		buffer.WriteByte('}')
		return nil
	case []any:
		buffer.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				buffer.WriteByte(',')
			}
			if err := appendPythonJSON(buffer, item); err != nil {
				return err
			}
		}
		buffer.WriteByte(']')
		return nil
	default:
		return fmt.Errorf("canonical JSON 不支持的类型: %T", value)
	}
}

// appendPythonJSONString 按 ensure_ascii=False 规则转义字符串
func appendPythonJSONString(buffer *bytes.Buffer, text string) {
	buffer.WriteByte('"')
	for _, char := range text {
		switch char {
		case '"':
			buffer.WriteString("\\\"")
		case '\\':
			buffer.WriteString("\\\\")
		case '\b':
			buffer.WriteString("\\b")
		case '\f':
			buffer.WriteString("\\f")
		case '\n':
			buffer.WriteString("\\n")
		case '\r':
			buffer.WriteString("\\r")
		case '\t':
			buffer.WriteString("\\t")
		default:
			if char < 0x20 {
				buffer.WriteString(fmt.Sprintf("\\u%04x", char))
			} else {
				buffer.WriteRune(char)
			}
		}
	}
	buffer.WriteByte('"')
}

// appendPythonJSONFloat 对齐 Python repr(float) 的定长与科学计数法切换
func appendPythonJSONFloat(buffer *bytes.Buffer, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("canonical JSON 不支持非有限浮点数")
	}
	if value == 0 {
		if math.Signbit(value) {
			buffer.WriteString("-0.0")
		} else {
			buffer.WriteString("0.0")
		}
		return nil
	}
	scientific := strconv.FormatFloat(value, 'e', -1, 64)
	exponentIndex := strings.IndexByte(scientific, 'e')
	if exponentIndex < 0 {
		return fmt.Errorf("canonical JSON 浮点数格式异常: %s", scientific)
	}
	mantissa := scientific[:exponentIndex]
	exponent, err := strconv.Atoi(scientific[exponentIndex+1:])
	if err != nil {
		return err
	}
	sign := ""
	if strings.HasPrefix(mantissa, "-") {
		sign = "-"
		mantissa = mantissa[1:]
	}
	digits := strings.Replace(mantissa, ".", "", 1)
	if exponent < -4 || exponent >= 16 {
		buffer.WriteString(sign)
		buffer.WriteByte(digits[0])
		if len(digits) > 1 {
			buffer.WriteByte('.')
			buffer.WriteString(digits[1:])
		}
		buffer.WriteByte('e')
		if exponent < 0 {
			buffer.WriteByte('-')
		} else {
			buffer.WriteByte('+')
		}
		exponentText := strconv.Itoa(absInt(exponent))
		if len(exponentText) < 2 {
			exponentText = "0" + exponentText
		}
		buffer.WriteString(exponentText)
		return nil
	}
	buffer.WriteString(sign)
	if exponent >= 0 {
		if len(digits) <= exponent+1 {
			buffer.WriteString(digits)
			buffer.WriteString(strings.Repeat("0", exponent+1-len(digits)))
			buffer.WriteString(".0")
			return nil
		}
		buffer.WriteString(digits[:exponent+1])
		buffer.WriteByte('.')
		buffer.WriteString(digits[exponent+1:])
		return nil
	}
	buffer.WriteString("0.")
	buffer.WriteString(strings.Repeat("0", -exponent-1))
	buffer.WriteString(digits)
	return nil
}

// absInt 返回整数绝对值
func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
