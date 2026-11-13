package utils

// A_memorix 检索调优参数导出与差异，对齐 export_toml_snippet 与 _profile_diff
// TOML 片段顺序、浮点位数和布尔文案必须与 Python 保持一致

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func retrievalTuningFormatFloat4(value float64) string {
	return strconv.FormatFloat(value, 'f', 4, 64)
}

// retrievalTuningFormatBoolTOML 对齐 str(bool(value)).lower()
func retrievalTuningFormatBoolTOML(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// ExportRetrievalTOMLSnippet 对齐 export_toml_snippet，输出可直接粘贴的 TOML 片段
func ExportRetrievalTOMLSnippet(profile map[string]any) string {
	normalized := NormalizeRetrievalProfile(profile, nil)
	sparse := normalized["retrieval"].(map[string]any)["sparse"].(map[string]any)
	fusion := normalized["retrieval"].(map[string]any)["fusion"].(map[string]any)
	pools := normalized["retrieval"].(map[string]any)["vector_pools"].(map[string]any)
	smartFallback := normalized["retrieval"].(map[string]any)["search"].(map[string]any)["smart_fallback"].(map[string]any)

	lines := []string{
		"[retrieval]",
		fmt.Sprintf("top_k_paragraphs = %d", RetrievalTuningProfileInt(normalized, "retrieval.top_k_paragraphs")),
		fmt.Sprintf("top_k_relations = %d", RetrievalTuningProfileInt(normalized, "retrieval.top_k_relations")),
		fmt.Sprintf("top_k_final = %d", RetrievalTuningProfileInt(normalized, "retrieval.top_k_final")),
		"alpha = " + retrievalTuningFormatFloat4(RetrievalTuningProfileFloat(normalized, "retrieval.alpha")),
		"enable_ppr = " + retrievalTuningFormatBoolTOML(retrievalTuningCoerceBool(normalized["retrieval"].(map[string]any)["enable_ppr"], true)),
		"ppr_alpha = " + retrievalTuningFormatFloat4(RetrievalTuningProfileFloat(normalized, "retrieval.ppr_alpha")),
		"ppr_timeout_seconds = " + retrievalTuningFormatFloat4(RetrievalTuningProfileFloat(normalized, "retrieval.ppr_timeout_seconds")),
		"",
		"[retrieval.search.smart_fallback]",
		"enabled = " + retrievalTuningFormatBoolTOML(retrievalTuningCoerceBool(smartFallback["enabled"], true)),
		"",
		"[retrieval.sparse]",
		"enabled = " + retrievalTuningFormatBoolTOML(retrievalTuningCoerceBool(sparse["enabled"], true)),
		"mode = " + strconv.Quote(pyStr(sparse["mode"])),
		fmt.Sprintf("candidate_k = %d", RetrievalTuningProfileInt(normalized, "retrieval.sparse.candidate_k")),
		fmt.Sprintf("relation_candidate_k = %d", RetrievalTuningProfileInt(normalized, "retrieval.sparse.relation_candidate_k")),
		"",
		"[retrieval.fusion]",
		"method = " + strconv.Quote(pyStr(fusion["method"])),
		fmt.Sprintf("rrf_k = %d", RetrievalTuningProfileInt(normalized, "retrieval.fusion.rrf_k")),
		"vector_weight = " + retrievalTuningFormatFloat4(RetrievalTuningProfileFloat(normalized, "retrieval.fusion.vector_weight")),
		"bm25_weight = " + retrievalTuningFormatFloat4(RetrievalTuningProfileFloat(normalized, "retrieval.fusion.bm25_weight")),
		"",
		"[retrieval.vector_pools]",
		"mode = " + strconv.Quote(pyStr(pools["mode"])),
		fmt.Sprintf("paragraph_top_k = %d", RetrievalTuningProfileInt(normalized, "retrieval.vector_pools.paragraph_top_k")),
		fmt.Sprintf("graph_top_k = %d", RetrievalTuningProfileInt(normalized, "retrieval.vector_pools.graph_top_k")),
		fmt.Sprintf("graph_expand_paragraph_k = %d", RetrievalTuningProfileInt(normalized, "retrieval.vector_pools.graph_expand_paragraph_k")),
		fmt.Sprintf("relation_expand_per_hit = %d", RetrievalTuningProfileInt(normalized, "retrieval.vector_pools.relation_expand_per_hit")),
		fmt.Sprintf("entity_expand_per_hit = %d", RetrievalTuningProfileInt(normalized, "retrieval.vector_pools.entity_expand_per_hit")),
		"relation_evidence_weight = " + retrievalTuningFormatFloat4(RetrievalTuningProfileFloat(normalized, "retrieval.vector_pools.relation_evidence_weight")),
		"entity_evidence_weight = " + retrievalTuningFormatFloat4(RetrievalTuningProfileFloat(normalized, "retrieval.vector_pools.entity_evidence_weight")),
		"semantic_weight = " + retrievalTuningFormatFloat4(RetrievalTuningProfileFloat(normalized, "retrieval.vector_pools.semantic_weight")),
		"sparse_weight = " + retrievalTuningFormatFloat4(RetrievalTuningProfileFloat(normalized, "retrieval.vector_pools.sparse_weight")),
		"graph_weight = " + retrievalTuningFormatFloat4(RetrievalTuningProfileFloat(normalized, "retrieval.vector_pools.graph_weight")),
		"",
		"[retrieval.vector_pools.relation_intent]",
		fmt.Sprintf("graph_top_k = %d", RetrievalTuningProfileInt(normalized, "retrieval.vector_pools.relation_intent.graph_top_k")),
		"semantic_weight = " + retrievalTuningFormatFloat4(RetrievalTuningProfileFloat(normalized, "retrieval.vector_pools.relation_intent.semantic_weight")),
		"sparse_weight = " + retrievalTuningFormatFloat4(RetrievalTuningProfileFloat(normalized, "retrieval.vector_pools.relation_intent.sparse_weight")),
		"graph_weight = " + retrievalTuningFormatFloat4(RetrievalTuningProfileFloat(normalized, "retrieval.vector_pools.relation_intent.graph_weight")),
		"",
		"[threshold]",
		"min_threshold = " + retrievalTuningFormatFloat4(RetrievalTuningProfileFloat(normalized, "threshold.min_threshold")),
		"max_threshold = " + retrievalTuningFormatFloat4(RetrievalTuningProfileFloat(normalized, "threshold.max_threshold")),
		"percentile = " + retrievalTuningFormatFloat4(RetrievalTuningProfileFloat(normalized, "threshold.percentile")),
		fmt.Sprintf("min_results = %d", RetrievalTuningProfileInt(normalized, "threshold.min_results")),
	}
	return pyStrip(strings.Join(lines, "\x0a")) + "\x0a"
}

// RetrievalTuningProfileDiff 对齐 _profile_diff，输出参数变化明细
func RetrievalTuningProfileDiff(before map[string]any, after map[string]any) map[string]any {
	diff := map[string]any{}
	var walk func(prefix string, left any, right any)
	walk = func(prefix string, left any, right any) {
		leftObject, leftOk := left.(map[string]any)
		rightObject, rightOk := right.(map[string]any)
		if leftOk && rightOk {
			keys := make([]string, 0, len(leftObject)+len(rightObject))
			seen := map[string]struct{}{}
			for key := range leftObject {
				seen[key] = struct{}{}
				keys = append(keys, key)
			}
			for key := range rightObject {
				if _, ok := seen[key]; ok {
					continue
				}
				keys = append(keys, key)
			}
			sortStrings(keys)
			for _, key := range keys {
				next := key
				if prefix != "" {
					next = prefix + "." + key
				}
				walk(next, leftObject[key], rightObject[key])
			}
			return
		}
		if !retrievalTuningValueEqual(left, right) {
			diff[prefix] = map[string]any{"before": left, "after": right}
		}
	}
	if before == nil {
		before = map[string]any{}
	}
	if after == nil {
		after = map[string]any{}
	}
	walk("", before, after)
	return diff
}

// retrievalTuningValueEqual 判断两个 JSON 值是否相等，对齐 Python 的 == 语义
func retrievalTuningValueEqual(left any, right any) bool {
	return pyJSONValue(left) == pyJSONValue(right)
}

// pyJSONValue 把值转换成可比较的规范化文本
func pyJSONValue(value any) string {
	if value == nil {
		return "null"
	}
	switch typed := value.(type) {
	case map[string]any, []any, string, bool:
		encoded, err := pyJSONMarshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return encoded
	case float64:
		if typed == math.Trunc(typed) {
			return strconv.FormatFloat(typed, 'f', 1, 64)
		}
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case float32:
		return pyJSONValue(float64(typed))
	default:
		return fmt.Sprint(typed)
	}
}

// maxIntValue 与 minIntValue 提供整数比较
func maxIntValue(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func minIntValue(left int, right int) int {
	if left < right {
		return left
	}
	return right
}
