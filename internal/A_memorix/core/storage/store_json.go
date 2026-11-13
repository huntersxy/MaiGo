package storage

// A_memorix 元数据 JSON 编解码与合并工具，对齐 Python metadata_store.py 的静态方法

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/bytedance/sonic"
)

// jsonDumps 对齐 Python _json_dumps（ensure_ascii=False）
func jsonDumps(value any) string {
	if value == nil {
		return "{}"
	}
	data, err := sonic.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// jsonLoads 对齐 Python _json_loads，解析失败返回默认值
func jsonLoads(value any, def any) any {
	text, ok := value.(string)
	if !ok || text == "" {
		return def
	}
	var result any
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.UseNumber()
	if err := dec.Decode(&result); err != nil {
		return def
	}
	return normalizeJSONNumbers(result)
}

// normalizeJSONNumbers 把 json.Number 归一为 float64/int64，贴近 Python 解析结果
func normalizeJSONNumbers(value any) any {
	switch v := value.(type) {
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i
		}
		if f, err := v.Float64(); err == nil {
			return f
		}
		return v.String()
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = normalizeJSONNumbers(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = normalizeJSONNumbers(item)
		}
		return out
	default:
		return value
	}
}

// encodeMetadata 对齐 Python _encode_metadata
func encodeMetadata(value map[string]any) string {
	if len(value) == 0 {
		return "{}"
	}
	return jsonDumps(value)
}

// decodeMetadata 对齐 Python _decode_metadata
func decodeMetadata(value any) map[string]any {
	loaded := jsonLoads(value, map[string]any{})
	if dict, ok := loaded.(map[string]any); ok {
		return dict
	}
	return map[string]any{}
}

// deepMergeDict 对齐 Python _deep_merge_dict：字典递归合并，其余类型覆盖
func deepMergeDict(base map[string]any, patch map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(patch))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range patch {
		patchMap, patchIsMap := value.(map[string]any)
		baseMap, baseIsMap := merged[key].(map[string]any)
		if patchIsMap && baseIsMap {
			merged[key] = deepMergeDict(baseMap, patchMap)
			continue
		}
		merged[key] = value
	}
	return merged
}

// appendMetadataTokens 对齐 Python _append_metadata_tokens
func appendMetadataTokens(tokens []string, value any) []string {
	switch v := value.(type) {
	case nil:
		return tokens
	case string:
		return append(tokens, v)
	case []any:
		for _, item := range v {
			tokens = appendMetadataTokens(tokens, item)
		}
		return tokens
	case []string:
		return append(tokens, v...)
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		for i := 0; i < len(keys); i++ {
			for j := i + 1; j < len(keys); j++ {
				if keys[j] < keys[i] {
					keys[i], keys[j] = keys[j], keys[i]
				}
			}
		}
		for _, key := range keys {
			tokens = appendMetadataTokens(tokens, v[key])
		}
		return tokens
	default:
		return append(tokens, fmt.Sprint(value))
	}
}

// mergeMetadataBindingIDs 对齐 Python _merge_metadata_binding_ids
func mergeMetadataBindingIDs(base map[string]any, patch map[string]any) map[string]any {
	merged := make(map[string]any, len(base))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range patch {
		if key != "binding_ids" {
			merged[key] = value
			continue
		}
		existing := toStringSet(merged[key])
		for item := range toStringSet(value) {
			existing[item] = struct{}{}
		}
		list := make([]any, 0, len(existing))
		for item := range existing {
			list = append(list, item)
		}
		sortStrings(list)
		merged[key] = list
	}
	return merged
}

// mergeParagraphMetadata 对齐 Python _merge_paragraph_metadata
func mergeParagraphMetadata(base map[string]any, patch map[string]any) map[string]any {
	return mergeMetadataBindingIDs(deepMergeDict(base, patch), patch)
}

func toStringSet(value any) map[string]struct{} {
	out := make(map[string]struct{})
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if text, ok := item.(string); ok && text != "" {
				out[text] = struct{}{}
			}
		}
	case []string:
		for _, item := range v {
			if item != "" {
				out[item] = struct{}{}
			}
		}
	case string:
		if v != "" {
			out[v] = struct{}{}
		}
	}
	return out
}

func sortStrings(items []any) {
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if fmt.Sprint(items[j]) < fmt.Sprint(items[i]) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
}
