package utils

// A_memorix 检索调优的 LLM 辅助，对齐 Python _llm_call_text / _generate_nl_queries_with_llm / _suggest_profiles_with_llm
// 模型选择与网关由注入的 RetrievalTuningLLM 承担，这里只负责重试、提示词与响应解析

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// callLLMText 对齐 _llm_call_text，带指数退避重试
func (m *RetrievalTuningManager) callLLMText(ctx context.Context, prompt string, requestType string, useFastModel bool) (string, error) {
	if m.llm == nil {
		return "", fmt.Errorf("llm_api unavailable")
	}
	retry := m.llmRetryConfig()
	maxAttempts := retrievalTuningClampInt(retry["max_attempts"], 3, 1, 10)
	minWait := retrievalTuningClampFloat(retry["min_wait_seconds"], 2.0, 0.1, 60.0)
	maxWait := retrievalTuningClampFloat(retry["max_wait_seconds"], 20.0, 0.2, 120.0)
	backoff := retrievalTuningClampFloat(retry["backoff_multiplier"], 2.0, 1.0, 10.0)

	lastError := ""
	for index := 0; index < maxAttempts; index++ {
		var text string
		var err error
		if useFastModel {
			if fast, ok := m.llm.(RetrievalTuningFastLLM); ok {
				text, err = fast.GenerateFast(ctx, requestType, prompt)
			} else {
				text, err = m.llm.Generate(ctx, requestType, prompt)
			}
		} else {
			text, err = m.llm.Generate(ctx, requestType, prompt)
		}
		if err == nil {
			trimmed := pyStrip(text)
			if trimmed != "" {
				return trimmed, nil
			}
			lastError = "empty_llm_response"
		} else {
			lastError = err.Error()
			if lastError == "" {
				lastError = "llm_generation_failed"
			}
		}
		if index >= maxAttempts-1 {
			break
		}
		delay := math.Min(maxWait, minWait*math.Pow(backoff, float64(index)))
		if delay < 0.05 {
			delay = 0.05
		}
		if sleepErr := m.sleepFunc(ctx, time.Duration(delay*float64(time.Second))); sleepErr != nil {
			return "", sleepErr
		}
	}
	return "", fmt.Errorf("LLM call failed: %s", lastError)
}

// parseRetrievalTuningLLMObject 解析模型返回的 JSON 对象，对齐 _safe_json_loads 的 dict 过滤
func parseRetrievalTuningLLMObject(raw string) map[string]any {
	trimmed := pyStrip(raw)
	if trimmed == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return object
}

// generateNLQueriesWithLLM 对齐 _generate_nl_queries_with_llm
func (m *RetrievalTuningManager) generateNLQueriesWithLLM(ctx context.Context, anchors []*RetrievalTuningAnchor, enabled bool) map[string]string {
	result := map[string]string{}
	if !enabled || m.llm == nil || len(anchors) == 0 {
		return result
	}
	limit := len(anchors)
	if limit > 60 {
		limit = 60
	}
	payload := make([]any, 0, limit)
	for _, anchor := range anchors[:limit] {
		payload = append(payload, map[string]any{
			"anchor_id":         anchor.AnchorID,
			"subject":           anchor.Subject,
			"predicate":         anchor.Predicate,
			"object":            anchor.Object,
			"paragraph_excerpt": retrievalTuningTruncateRunes(anchor.ParagraphExcerpt, 180),
		})
	}
	prompt := "你是检索评估问题生成器。" +
		"请基于给定 SPO 与简短上下文，为每条样本生成 1 条自然语言检索问题，返回 JSON：" +
		"{\"items\":[{\"anchor_id\":\"...\",\"query\":\"...\"}]}。\n" +
		"样本：\n" + retrievalTuningJSONDumps(payload, 0)
	raw, err := m.callLLMText(ctx, prompt, "A_Memorix.RetrievalTuning.NLCaseGen", true)
	if err != nil {
		m.logger.Warning("检索调优 NL 用例生成失败: " + err.Error())
		return result
	}
	object := parseRetrievalTuningLLMObject(raw)
	if object == nil {
		return result
	}
	items, ok := object["items"].([]any)
	if !ok {
		return result
	}
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		anchorID := pyStrip(pyStr(row["anchor_id"]))
		query := pyStrip(pyStr(row["query"]))
		if anchorID != "" && query != "" {
			result[anchorID] = query
		}
	}
	return result
}

// suggestProfilesWithLLM 对齐 _suggest_profiles_with_llm
func (m *RetrievalTuningManager) suggestProfilesWithLLM(ctx context.Context, baseProfile map[string]any, failureSummary map[string]any, objective string, maxCount int, enabled bool) []map[string]any {
	if !enabled || m.llm == nil || maxCount <= 0 {
		return nil
	}
	if failureSummary == nil {
		failureSummary = map[string]any{}
	}
	var builder strings.Builder
	builder.WriteString("你是检索调参专家。")
	builder.WriteString("请基于基础参数与失败摘要，给出最多 ")
	builder.WriteString(pyStr(maxCount))
	builder.WriteString(" 组候选参数，返回 JSON: {\"profiles\": [ ... ]}。\n")
	builder.WriteString("字段仅可包含：retrieval.top_k_paragraphs, retrieval.top_k_relations, retrieval.top_k_final, ")
	builder.WriteString("retrieval.alpha, retrieval.enable_ppr, retrieval.ppr_alpha, retrieval.ppr_timeout_seconds, ")
	builder.WriteString("retrieval.search.smart_fallback.enabled, ")
	builder.WriteString("retrieval.sparse.enabled, retrieval.sparse.mode, retrieval.sparse.candidate_k, retrieval.sparse.relation_candidate_k, ")
	builder.WriteString("retrieval.fusion.method, retrieval.fusion.rrf_k, retrieval.fusion.vector_weight, retrieval.fusion.bm25_weight, ")
	builder.WriteString("retrieval.vector_pools.paragraph_top_k, retrieval.vector_pools.graph_top_k, ")
	builder.WriteString("retrieval.vector_pools.graph_expand_paragraph_k, retrieval.vector_pools.relation_expand_per_hit, ")
	builder.WriteString("retrieval.vector_pools.entity_expand_per_hit, retrieval.vector_pools.relation_evidence_weight, ")
	builder.WriteString("retrieval.vector_pools.entity_evidence_weight, retrieval.vector_pools.semantic_weight, ")
	builder.WriteString("retrieval.vector_pools.sparse_weight, retrieval.vector_pools.graph_weight, ")
	builder.WriteString("retrieval.vector_pools.relation_intent.graph_top_k, ")
	builder.WriteString("retrieval.vector_pools.relation_intent.semantic_weight, ")
	builder.WriteString("retrieval.vector_pools.relation_intent.sparse_weight, ")
	builder.WriteString("retrieval.vector_pools.relation_intent.graph_weight, ")
	builder.WriteString("threshold.min_threshold, threshold.max_threshold, threshold.percentile, ")
	builder.WriteString("threshold.min_results。\n")
	builder.WriteString("objective=" + objective + "\n")
	builder.WriteString("base=" + retrievalTuningJSONDumps(baseProfile, 0) + "\n")
	builder.WriteString("failure_summary=" + retrievalTuningJSONDumps(failureSummary, 0))
	raw, err := m.callLLMText(ctx, builder.String(), "A_Memorix.RetrievalTuning.ProfileSuggest", false)
	if err != nil {
		m.logger.Warning("检索调优参数建议失败: " + err.Error())
		return nil
	}
	object := parseRetrievalTuningLLMObject(raw)
	if object == nil {
		return nil
	}
	profiles, ok := object["profiles"].([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, maxCount)
	for _, item := range profiles {
		if len(result) >= maxCount {
			break
		}
		profile, ok := item.(map[string]any)
		if !ok {
			continue
		}
		result = append(result, m.normalizeProfileWithFallback(profile, baseProfile))
	}
	return result
}

// normalizeProfileWithFallback 对齐 _normalize_profile(profile, fallback=...)
func (m *RetrievalTuningManager) normalizeProfileWithFallback(profile map[string]any, fallback map[string]any) map[string]any {
	if fallback == nil {
		fallback = m.GetProfileSnapshot()
	}
	return NormalizeRetrievalProfile(profile, fallback)
}
