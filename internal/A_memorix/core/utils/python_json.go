package utils

// Python json.dumps 兼容编码器，用于事实去重的提示词负载与观察标识
// 采用 ensure_ascii=False 与默认分隔符 ", " / ": "，非 ASCII 字符原样输出

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// pyJSONMarshal 对齐 json.dumps(value, ensure_ascii=False)
// 与 Python 的差异：Go map 没有插入顺序，编码时按键名排序
func pyJSONMarshal(value any) (string, error) {
	var builder strings.Builder
	if err := pyJSONWrite(&builder, reflect.ValueOf(value)); err != nil {
		return "", err
	}
	return builder.String(), nil
}

func pyJSONWrite(builder *strings.Builder, value reflect.Value) error {
	if !value.IsValid() {
		builder.WriteString("null")
		return nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			builder.WriteString("null")
			return nil
		}
		return pyJSONWrite(builder, value.Elem())
	}
	if value.Kind() == reflect.Ptr {
		if value.IsNil() {
			builder.WriteString("null")
			return nil
		}
		return pyJSONWrite(builder, value.Elem())
	}
	if number, ok := value.Interface().(json.Number); ok {
		return pyJSONWriteNumber(builder, string(number))
	}
	switch value.Kind() {
	case reflect.Bool:
		if value.Bool() {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case reflect.String:
		pyJSONWriteString(builder, value.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		builder.WriteString(strconv.FormatInt(value.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		builder.WriteString(strconv.FormatUint(value.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		pyJSONWriteFloat(builder, value.Float())
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			builder.WriteString("null")
			return nil
		}
		builder.WriteByte('[')
		for index := 0; index < value.Len(); index++ {
			if index > 0 {
				builder.WriteString(", ")
			}
			if err := pyJSONWrite(builder, value.Index(index)); err != nil {
				return err
			}
		}
		builder.WriteByte(']')
	case reflect.Map:
		if value.IsNil() {
			builder.WriteString("null")
			return nil
		}
		if value.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("JSON 对象键必须是字符串: %s", value.Type())
		}
		keys := make([]string, 0, value.Len())
		iter := value.MapRange()
		for iter.Next() {
			keys = append(keys, iter.Key().String())
		}
		sort.Strings(keys)
		builder.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				builder.WriteString(", ")
			}
			pyJSONWriteString(builder, key)
			builder.WriteString(": ")
			if err := pyJSONWrite(builder, value.MapIndex(reflect.ValueOf(key))); err != nil {
				return err
			}
		}
		builder.WriteByte('}')
	default:
		return fmt.Errorf("不支持的 JSON 类型: %s", value.Type())
	}
	return nil
}

// pyJSONWriteNumber 保留 json.Number 的原始数字文本
func pyJSONWriteNumber(builder *strings.Builder, text string) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		builder.WriteString("null")
		return nil
	}
	if _, err := strconv.ParseFloat(trimmed, 64); err != nil {
		return fmt.Errorf("无效的数字文本: %s", text)
	}
	builder.WriteString(trimmed)
	return nil
}

// pyJSONWriteFloat 对齐 Python repr 的浮点格式
func pyJSONWriteFloat(builder *strings.Builder, number float64) {
	switch {
	case math.IsNaN(number):
		builder.WriteString("NaN")
		return
	case math.IsInf(number, 1):
		builder.WriteString("Infinity")
		return
	case math.IsInf(number, -1):
		builder.WriteString("-Infinity")
		return
	}
	scientific := strconv.FormatFloat(number, 'e', -1, 64)
	exponent := 0
	if index := strings.IndexByte(scientific, 'e'); index >= 0 {
		exponent, _ = strconv.Atoi(scientific[index+1:])
	}
	if exponent >= -4 && exponent < 16 {
		fixed := strconv.FormatFloat(number, 'f', -1, 64)
		if !strings.ContainsAny(fixed, ".eE") {
			fixed += ".0"
		}
		builder.WriteString(fixed)
		return
	}
	builder.WriteString(scientific)
}

// pyJSONWriteString 对齐 json.dumps 的字符串转义
func pyJSONWriteString(builder *strings.Builder, text string) {
	builder.WriteByte('"')
	for _, char := range text {
		switch char {
		case '"':
			builder.WriteString("\\\"")
		case '\\':
			builder.WriteString("\\\\")
		case '\b':
			builder.WriteString("\\b")
		case '\f':
			builder.WriteString("\\f")
		case '\n':
			builder.WriteString("\\n")
		case '\r':
			builder.WriteString("\\r")
		case '\t':
			builder.WriteString("\\t")
		default:
			if char < 0x20 {
				builder.WriteString(fmt.Sprintf("\\u%04x", char))
				continue
			}
			builder.WriteRune(char)
		}
	}
	builder.WriteByte('"')
}
