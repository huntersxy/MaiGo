package services

// 图片语义摘要，对齐 bundle_admin_service.py 的 _canonical_image_semantics

import (
	"encoding/json"
	"sort"
	"strings"
)

// canonicalImageSemantics 构造不受实例 UUID、安装时间和运行状态影响的图片语义摘要
func canonicalImageSemantics(records map[string]any) map[string]any {
	assetsById := map[string]map[string]any{}
	assets := []any{}
	if records != nil {
		for _, item := range anyMapList(records["assets"]) {
			assets = append(assets, item)
			assetsById[asString(item["asset_id"])] = item
		}
	}
	type orderedOccurrence struct {
		assetHash     string
		componentPath string
		occurrenceID  string
		record        map[string]any
	}
	occurrences := []orderedOccurrence{}
	if records != nil {
		for _, item := range anyMapList(records["occurrences"]) {
			assetHash := asString(assetsById[asString(item["asset_id"])]["content_hash"])
			occurrences = append(occurrences, orderedOccurrence{
				assetHash: assetHash, componentPath: asString(item["component_path"]),
				occurrenceID: asString(item["occurrence_id"]), record: item,
			})
		}
	}
	sort.SliceStable(occurrences, func(left, right int) bool {
		if occurrences[left].assetHash != occurrences[right].assetHash {
			return occurrences[left].assetHash < occurrences[right].assetHash
		}
		if occurrences[left].componentPath != occurrences[right].componentPath {
			return occurrences[left].componentPath < occurrences[right].componentPath
		}
		return occurrences[left].occurrenceID < occurrences[right].occurrenceID
	})
	occurrenceKeys := map[string]string{}
	for index, item := range occurrences {
		occurrenceKeys[item.occurrenceID] = "occurrence-" + itoa(index)
	}
	canonicalAssets := []any{}
	for _, item := range assets {
		asset := item.(map[string]any)
		canonicalAssets = append(canonicalAssets, map[string]any{
			"byte_size":    intOr(asset["byte_size"], 0, -1<<62),
			"content_hash": asString(asset["content_hash"]),
			"height":       intOr(asset["height"], 0, -1<<62),
			"mime_type":    asString(asset["mime_type"]),
			"width":        intOr(asset["width"], 0, -1<<62),
		})
	}
	sort.SliceStable(canonicalAssets, func(left, right int) bool {
		return asString(canonicalAssets[left].(map[string]any)["content_hash"]) <
			asString(canonicalAssets[right].(map[string]any)["content_hash"])
	})
	canonicalOccurrences := []any{}
	for _, item := range occurrences {
		canonicalOccurrences = append(canonicalOccurrences, map[string]any{
			"asset_hash":     item.assetHash,
			"component_path": item.componentPath,
		})
	}
	observations := []any{}
	if records != nil {
		for _, item := range anyMapList(records["observations"]) {
			observations = append(observations, map[string]any{
				"confirm_status": asString(item["confirm_status"]),
				"occurrence":     occurrenceKeys[asString(item["occurrence_id"])],
				"source_kind":    asString(item["source_kind"]),
				"text":           asString(item["text"]),
				"evidence":       canonicalImageEvidence(item["evidence_json"]),
			})
		}
	}
	sort.SliceStable(observations, func(left, right int) bool {
		leftItem := observations[left].(map[string]any)
		rightItem := observations[right].(map[string]any)
		if asString(leftItem["occurrence"]) != asString(rightItem["occurrence"]) {
			return asString(leftItem["occurrence"]) < asString(rightItem["occurrence"])
		}
		if asString(leftItem["source_kind"]) != asString(rightItem["source_kind"]) {
			return asString(leftItem["source_kind"]) < asString(rightItem["source_kind"])
		}
		return asString(leftItem["text"]) < asString(rightItem["text"])
	})
	links := []any{}
	if records != nil {
		for _, item := range anyMapList(records["links"]) {
			links = append(links, map[string]any{
				"link_kind":   asString(item["link_kind"]),
				"occurrence":  occurrenceKeys[asString(item["occurrence_id"])],
				"target_id":   asString(item["target_id"]),
				"target_type": asString(item["target_type"]),
				"evidence":    canonicalImageEvidence(item["evidence_json"]),
			})
		}
	}
	sort.SliceStable(links, func(left, right int) bool {
		leftItem := links[left].(map[string]any)
		rightItem := links[right].(map[string]any)
		if asString(leftItem["occurrence"]) != asString(rightItem["occurrence"]) {
			return asString(leftItem["occurrence"]) < asString(rightItem["occurrence"])
		}
		if asString(leftItem["target_type"]) != asString(rightItem["target_type"]) {
			return asString(leftItem["target_type"]) < asString(rightItem["target_type"])
		}
		return asString(leftItem["target_id"]) < asString(rightItem["target_id"])
	})
	return map[string]any{
		"assets": canonicalAssets, "occurrences": canonicalOccurrences,
		"observations": observations, "links": links,
	}
}

// canonicalImageEvidence 归一图片证据，剔除实例相关字段
func canonicalImageEvidence(value any) map[string]any {
	var evidence map[string]any
	switch typed := value.(type) {
	case map[string]any:
		evidence = map[string]any{}
		for key, item := range typed {
			evidence[key] = item
		}
	default:
		text := strings.TrimSpace(asString(value))
		if text == "" {
			text = "{}"
		}
		if err := json.Unmarshal([]byte(text), &evidence); err != nil {
			return map[string]any{}
		}
	}
	if evidence == nil {
		return map[string]any{}
	}
	for _, key := range []string{"chat_id", "external_ref", "imported", "message_id", "source_link_id"} {
		delete(evidence, key)
	}
	return evidence
}

// anyMapList 把 []any 或 []map[string]any 归一为 map 列表
func anyMapList(value any) []map[string]any {
	switch typed := value.(type) {
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if record, ok := item.(map[string]any); ok {
				out = append(out, record)
			}
		}
		return out
	case []map[string]any:
		return typed
	default:
		return nil
	}
}

// itoa 十进制整数转字符串
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	buffer := make([]byte, 0, 12)
	for value > 0 {
		buffer = append(buffer, byte('0'+value%10))
		value /= 10
	}
	if negative {
		buffer = append(buffer, '-')
	}
	for left, right := 0, len(buffer)-1; left < right; left, right = left+1, right-1 {
		buffer[left], buffer[right] = buffer[right], buffer[left]
	}
	return string(buffer)
}
