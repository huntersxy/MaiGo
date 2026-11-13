package memruntime

// 运行时负载纯函数，对齐 core/utils/runtime_payloads.py 与 runtime_config_service 的合并逻辑

import (
	"strings"
)

// ResolveKnowledgeType 对齐 resolve_knowledge_type
func ResolveKnowledgeType(sourceType string) string {
	cleanType := strings.ToLower(strings.TrimSpace(sourceType))
	if cleanType == "person_fact" {
		return "factual"
	}
	if cleanType == "chat_summary" {
		return "narrative"
	}
	return "mixed"
}

// TimeMeta 对齐 time_meta
func TimeMeta(timestamp *float64, timeStart *float64, timeEnd *float64) map[string]any {
	payload := map[string]any{}
	if timestamp != nil {
		payload["event_time"] = *timestamp
	}
	if timeStart != nil {
		payload["event_time_start"] = *timeStart
	}
	if timeEnd != nil {
		payload["event_time_end"] = *timeEnd
	}
	if len(payload) > 0 {
		payload["time_granularity"] = "minute"
		payload["time_confidence"] = 0.95
	}
	return payload
}

// MergeRuntimeConfigPatch 对齐 _merge_runtime_config_patch，按字典递归深合并
func MergeRuntimeConfigPatch(base map[string]any, patch map[string]any) map[string]any {
	merged := copyAnyMap(base)
	if merged == nil {
		merged = map[string]any{}
	}
	for key, value := range patch {
		patchDict, patchIsDict := value.(map[string]any)
		baseDict, baseIsDict := merged[key].(map[string]any)
		if patchIsDict && baseIsDict {
			merged[key] = MergeRuntimeConfigPatch(baseDict, patchDict)
			continue
		}
		merged[key] = deepCopyAny(value)
	}
	return merged
}

// deepCopyAny 深拷贝字典与切片，标量原样返回
func deepCopyAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return copyAnyMap(typed)
	case []any:
		copied := make([]any, 0, len(typed))
		for _, item := range typed {
			copied = append(copied, deepCopyAny(item))
		}
		return copied
	default:
		return value
	}
}
