package utils

// Web 导入基础类型转换工具，对齐 web_import_manager.py 模块函数

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// webImportCoerceInt 对齐 _coerce_int
func webImportCoerceInt(value any, defaultValue int) int {
	switch typed := value.(type) {
	case nil:
		return defaultValue
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
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return defaultValue
		}
		return parsed
	default:
		return defaultValue
	}
}

// webImportCoerceFloat 对齐 _coerce_float
func webImportCoerceFloat(value any, defaultValue float64) float64 {
	switch typed := value.(type) {
	case nil:
		return defaultValue
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case uint64:
		return float64(typed)
	case bool:
		if typed {
			return 1
		}
		return 0
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return defaultValue
		}
		return parsed
	default:
		return defaultValue
	}
}

// webImportCoerceBool 对齐 _coerce_bool
func webImportCoerceBool(value any, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "y", "on":
			return true
		case "0", "false", "no", "n", "off", "":
			return false
		default:
			return defaultValue
		}
	default:
		return defaultValue
	}
}

// webImportClamp 对齐 _clamp
func webImportClamp(value int, minimum int, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

// webImportCoerceList 对齐 _coerce_list
func webImportCoerceList(value any) []string {
	items := []any{}
	switch typed := value.(type) {
	case nil:
		return []string{}
	case []string:
		for _, item := range typed {
			items = append(items, item)
		}
	case []any:
		items = typed
	case string:
		normalized := strings.ReplaceAll(typed, "\r", "\n")
		for _, line := range strings.Split(normalized, "\n") {
			for _, item := range strings.Split(line, ",") {
				items = append(items, item)
			}
		}
	default:
		items = []any{value}
	}
	out := []string{}
	seen := map[string]struct{}{}
	for _, item := range items {
		text := strings.TrimSpace(importValueText(item))
		if text == "" {
			continue
		}
		key := strings.ToLower(text)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, text)
	}
	return out
}

// webImportParseOptionalPositiveInt 对齐 _parse_optional_positive_int
func webImportParseOptionalPositiveInt(value any, field string) (*int, error) {
	if value == nil {
		return nil, nil
	}
	text := strings.TrimSpace(importValueText(value))
	if text == "" {
		return nil, nil
	}
	parsed, err := strconv.Atoi(text)
	if err != nil {
		return nil, fmt.Errorf("%s 必须为整数", field)
	}
	if parsed <= 0 {
		return nil, fmt.Errorf("%s 必须 > 0", field)
	}
	return &parsed, nil
}

// webImportParseOptionalNonNegativeInt 对齐 _parse_optional_non_negative_int
func webImportParseOptionalNonNegativeInt(value any, field string) (*int, error) {
	if value == nil {
		return nil, nil
	}
	text := strings.TrimSpace(importValueText(value))
	if text == "" {
		return nil, nil
	}
	parsed, err := strconv.Atoi(text)
	if err != nil {
		return nil, fmt.Errorf("%s 必须为整数", field)
	}
	if parsed < 0 {
		return nil, fmt.Errorf("%s 必须 >= 0", field)
	}
	return &parsed, nil
}

// webImportSafeFilename 对齐 _safe_filename
func webImportSafeFilename(value any) string {
	text := strings.TrimSpace(importValueText(value))
	text = strings.ReplaceAll(text, "\\", "/")
	base := filepath.Base(text)
	if base == "." || base == "/" || base == "" {
		buffer := make([]byte, 4)
		_, _ = rand.Read(buffer)
		return "unnamed_" + hex.EncodeToString(buffer) + ".txt"
	}
	return base
}
