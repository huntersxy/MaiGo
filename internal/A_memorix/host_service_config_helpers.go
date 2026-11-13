package memorix

// A_memorix 配置字典的归一化、深拷贝与 TOML 合并辅助

import "reflect"

// updateTOMLDoc 递归合并配置；对齐 Python _update_toml_doc，nil 值同样写入
func updateTOMLDoc(target, source map[string]any) {
	for key, value := range source {
		if key == "version" {
			continue
		}
		if current, ok := target[key]; ok {
			targetMap, targetIsMap := current.(map[string]any)
			sourceMap, sourceIsMap := value.(map[string]any)
			if targetIsMap && sourceIsMap {
				updateTOMLDoc(targetMap, sourceMap)
				continue
			}
		}
		target[key] = value
	}
}

// isEnabledConfig 对齐 Python bool(plugin_config.get("enabled", True))
//
// 显式 null 按 Python 真值语义视为 False（禁用），与 payloadBool 的「缺失才用默认值」不同。
func isEnabledConfig(config map[string]any) bool {
	pluginConfig := anyToMap(config["plugin"])
	if pluginConfig == nil {
		return true
	}
	if raw, ok := pluginConfig["enabled"]; ok {
		return pythonTruthy(raw)
	}
	return true
}

func mergeConfigMaps(base, override map[string]any) map[string]any {
	merged := deepCloneMap(base)
	for key, value := range override {
		if targetMap, ok := merged[key].(map[string]any); ok {
			if sourceMap, ok := value.(map[string]any); ok {
				merged[key] = mergeConfigMaps(targetMap, sourceMap)
				continue
			}
		}
		merged[key] = deepCloneValue(value)
	}
	return merged
}

func normalizeConfigMap(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}
	out := make(map[string]any, len(config))
	for key, value := range config {
		out[key] = normalizeConfigValue(value)
	}
	return out
}

func normalizeConfigValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return normalizeConfigMap(typed)
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[anyToString(key)] = normalizeConfigValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = normalizeConfigValue(item)
		}
		return out
	case []map[string]any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = normalizeConfigMap(item)
		}
		return out
	case int:
		return int64(typed)
	case int8:
		return int64(typed)
	case int16:
		return int64(typed)
	case int32:
		return int64(typed)
	case uint:
		return int64(typed)
	case uint32:
		return int64(typed)
	case uint64:
		return int64(typed)
	case float32:
		return float64(typed)
	default:
		return value
	}
}

func deepCloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = deepCloneValue(value)
	}
	return out
}

func deepCloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return deepCloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = deepCloneValue(item)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(typed))
		for index, item := range typed {
			out[index] = deepCloneMap(item)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

func toBuiltinData(value any) any {
	return normalizeConfigValue(value)
}

func stripInternalConfigFields(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if key == "field_docs" || key == "_validate_any" || key == "suppress_any_warning" {
				continue
			}
			out[key] = stripInternalConfigFields(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = stripInternalConfigFields(item)
		}
		return out
	default:
		return value
	}
}

func configValuesEqual(left, right any) bool {
	if leftNumber, ok := configNumber(left); ok {
		rightNumber, rightOK := configNumber(right)
		return rightOK && leftNumber == rightNumber
	}
	leftMap, leftIsMap := left.(map[string]any)
	rightMap, rightIsMap := right.(map[string]any)
	if leftIsMap || rightIsMap {
		return leftIsMap && rightIsMap && reflect.DeepEqual(leftMap, rightMap)
	}
	return reflect.DeepEqual(left, right)
}

func configNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	default:
		return 0, false
	}
}
