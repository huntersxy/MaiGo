package utils

// A_memorix 人物侧策略，对齐 Python core/utils/profile_policy.py

import "strings"

// ConfigGetter 读取配置项，defaultValue 为 Python 侧默认值
type ConfigGetter func(key string, defaultValue any) any

// ShouldAutoEnqueueEpisode 对齐 should_auto_enqueue_episode
func ShouldAutoEnqueueEpisode(configGetter ConfigGetter, sourceType string) bool {
	if configGetter == nil {
		return false
	}
	if !isTruthy(configGetter("episode.enabled", true)) {
		return false
	}
	if !isTruthy(configGetter("episode.generation_enabled", true)) {
		return false
	}

	normalizedSourceType := pyLower(pyStrip(sourceType))
	disabledTypes := make(map[string]struct{})
	for _, item := range ArgumentTokens(configGetter("episode.disabled_source_types", []any{"person_fact", "knowledge_pack"})) {
		disabledTypes[pyLower(pyStrip(item))] = struct{}{}
	}
	// 已加工知识包含有权威段落、实体和关系，不应再次交给 LLM 生成 Episode
	disabledTypes["knowledge_pack"] = struct{}{}
	_, disabled := disabledTypes[normalizedSourceType]
	return !disabled
}

// PersonProfileRefreshQueueIntervalSeconds 对齐 person_profile_refresh_queue_interval_seconds
func PersonProfileRefreshQueueIntervalSeconds(configGetter ConfigGetter) float64 {
	return policyFloatOr(configGetter, "person_profile.refresh_queue_interval_seconds", 60, 1.0)
}

// PersonProfileRefreshQueueBatchSize 对齐 person_profile_refresh_queue_batch_size
func PersonProfileRefreshQueueBatchSize(configGetter ConfigGetter) int {
	return policyIntOr(configGetter, "person_profile.refresh_queue_batch_size", 10, 1)
}

// PersonProfileRefreshDebounceSeconds 对齐 person_profile_refresh_debounce_seconds
func PersonProfileRefreshDebounceSeconds(configGetter ConfigGetter) float64 {
	return policyFloatOr(configGetter, "person_profile.refresh_debounce_seconds", 120, 0.0)
}

// PersonProfileRefreshRetryBackoffSeconds 对齐 person_profile_refresh_retry_backoff_seconds
func PersonProfileRefreshRetryBackoffSeconds(configGetter ConfigGetter) float64 {
	return policyFloatOr(configGetter, "person_profile.refresh_retry_backoff_seconds", 300, 0.0)
}

// PersonProfileRefreshMaxRetry 对齐 person_profile_refresh_max_retry
func PersonProfileRefreshMaxRetry(configGetter ConfigGetter) int {
	return policyIntOr(configGetter, "person_profile.max_retry", 3, 0)
}

// ArgumentTokens 对齐 argument_tokens：字符串视为单个值，其余按列表处理
func ArgumentTokens(value any) []string {
	if text, ok := value.(string); ok {
		return RuntimeTokens([]any{text})
	}
	return RuntimeTokens(value)
}

// RuntimeTokens 对齐 tokens：去空白、去空值、按出现顺序去重
func RuntimeTokens(values any) []string {
	items := runtimeTokenItems(values)
	result := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		token := ""
		if isTruthy(item) {
			token = pyStrip(pyStr(item))
		}
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		result = append(result, token)
	}
	return result
}

// runtimeTokenItems 把任意配置值展开为可迭代元素
func runtimeTokenItems(values any) []any {
	switch typed := values.(type) {
	case nil:
		return nil
	case string:
		return []any{typed}
	case []string:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = item
		}
		return items
	case []any:
		return typed
	case []int:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = item
		}
		return items
	default:
		return []any{typed}
	}
}

// policyFloatOr 对齐 max(minimum, float(getter(key, default) or default))
func policyFloatOr(configGetter ConfigGetter, key string, defaultValue float64, minimum float64) float64 {
	value := defaultValue
	if configGetter != nil {
		if raw := configGetter(key, defaultValue); isTruthy(raw) {
			if parsed, err := pyFloat64(raw); err == nil {
				value = parsed
			}
		}
	}
	if value < minimum {
		return minimum
	}
	return value
}

// policyIntOr 对齐 max(minimum, int(getter(key, default) or default))
func policyIntOr(configGetter ConfigGetter, key string, defaultValue int, minimum int) int {
	value := defaultValue
	if configGetter != nil {
		if raw := configGetter(key, defaultValue); isTruthy(raw) {
			if parsed, err := pyFloat64(raw); err == nil {
				value = int(parsed)
			}
		}
	}
	if value < minimum {
		return minimum
	}
	return value
}

// policyDisabledType 判断来源类型是否被配置禁用，保留给后续调用方复用
func policyDisabledType(disabledTypes map[string]struct{}, sourceType string) bool {
	_, ok := disabledTypes[strings.TrimSpace(sourceType)]
	return ok
}
