package platformio

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// NormalizeNativeConfig 用原生适配器的字段 schema 执行 SDK 配置转换与校验。
func NormalizeNativeConfig(config, schema map[string]any) (map[string]map[string]any, error) {
	sections, _ := schema["sections"].(map[string]any)
	keys := make([]string, 0, len(sections))
	for key := range sections {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make(map[string]map[string]any)
	for _, key := range keys {
		section := sections[key].(map[string]any)
		name := section["name"].(string)
		fields := section["fields"].(map[string]any)
		input, present := config[name]
		raw, ok := input.(map[string]any)
		if present && !ok {
			return nil, fmt.Errorf("%s 必须为配置对象", name)
		}
		out := result[name]
		if out == nil {
			out = make(map[string]any)
			result[name] = out
		}
		fieldNames := make([]string, 0, len(fields))
		for k := range fields {
			fieldNames = append(fieldNames, k)
		}
		sort.Strings(fieldNames)
		for _, fieldName := range fieldNames {
			field := fields[fieldName].(map[string]any)
			value, exists := raw[fieldName]
			if !exists {
				value = field["default"]
			}
			normalized, err := normalizeNativeField(value, field)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", name, fieldName, err)
			}
			out[fieldName] = normalized
		}
	}
	return result, nil
}

func normalizeNativeField(value any, field map[string]any) (any, error) {
	if value == nil {
		return nil, fmt.Errorf("字段值不能为空")
	}
	switch field["type"] {
	case "boolean":
		return ParseNativeConfigBool(value)
	case "string":
		if s, ok := value.(string); ok {
			return s, nil
		}
		return nil, fmt.Errorf("必须为字符串")
	case "array":
		switch values := value.(type) {
		case []string:
			return append([]string{}, values...), nil
		case []any:
			out := make([]string, 0, len(values))
			for i, item := range values {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("第 %d 项必须为字符串", i+1)
				}
				out = append(out, s)
			}
			return out, nil
		}
		return nil, fmt.Errorf("必须为列表")
	case "integer":
		integer, ok := parseConfigInt(value)
		if !ok {
			return nil, fmt.Errorf("必须为整数")
		}
		if err := checkNativeBounds(float64(integer), field); err != nil {
			return nil, err
		}
		return integer, nil
	case "number":
		number, ok := parseConfigNumber(value)
		if !ok {
			return nil, fmt.Errorf("必须为有效数字")
		}
		if err := checkNativeBounds(number, field); err != nil {
			return nil, err
		}
		return number, nil
	}
	return nil, fmt.Errorf("未知的配置字段类型")
}

// ParseNativeConfigBool 对齐 SDK Pydantic bool 字段的转换。
func ParseNativeConfigBool(value any) (bool, error) {
	switch v := value.(type) {
	case bool:
		return v, nil
	case string:
		switch strings.ToLower(v) {
		case "true", "1", "yes", "y", "t", "on":
			return true, nil
		case "false", "0", "no", "n", "f", "off":
			return false, nil
		}
	default:
		if n, ok := parseConfigNumber(v); ok && (n == 0 || n == 1) {
			return n == 1, nil
		}
	}
	return false, fmt.Errorf("必须为布尔值")
}

var configIntPattern = regexp.MustCompile(`^([+-]?[0-9]+)(?:\.0+)?$`)

func parseConfigInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), int64(int(v)) == v
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n), int64(int(n)) == n
		}
	case string:
		match := configIntPattern.FindStringSubmatch(strings.TrimSpace(v))
		if match == nil {
			return 0, false
		}
		n, err := strconv.Atoi(match[1])
		return n, err == nil
	}
	n, ok := parseConfigNumber(value)
	limit := math.Ldexp(1, strconv.IntSize-1)
	if !ok || n < -limit || n >= limit || math.Trunc(n) != n {
		return 0, false
	}
	return int(n), true
}

func parseConfigNumber(value any) (float64, bool) {
	var n float64
	switch v := value.(type) {
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case float64:
		n = v
	case bool:
		if v {
			n = 1
		}
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		n = parsed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		n = parsed
	default:
		return 0, false
	}
	return n, !math.IsInf(n, 0) && !math.IsNaN(n)
}

func checkNativeBounds(value float64, field map[string]any) error {
	if min, ok := parseConfigNumber(field["min"]); ok && value < min {
		return fmt.Errorf("必须大于或等于 %v", field["min"])
	}
	if max, ok := parseConfigNumber(field["max"]); ok && value > max {
		return fmt.Errorf("必须小于或等于 %v", field["max"])
	}
	return nil
}
