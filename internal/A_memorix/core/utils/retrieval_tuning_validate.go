package utils

// A_memorix 检索调优的候选参数生成、评分与泛化验证
// 对齐 Python _generate_candidate_profile / _score_metrics / _category_floor_penalty / _validate_best_profile

import (
	"context"
	"math"
)

// generateCandidateProfile 对齐 _generate_candidate_profile
// llmSuggestions 非空时优先消费队首建议，与 Python 的 list.pop(0) 一致
func (m *RetrievalTuningManager) generateCandidateProfile(taskID string, roundIndex int, objective string, baselineProfile map[string]any, bestProfile map[string]any, llmSuggestions *[]map[string]any) map[string]any {
	if llmSuggestions != nil && len(*llmSuggestions) > 0 {
		suggestion := (*llmSuggestions)[0]
		*llmSuggestions = (*llmSuggestions)[1:]
		return m.normalizeProfileWithFallback(suggestion, bestProfile)
	}

	rng := newRetrievalTuningRNG(taskID + ":" + pyStr(roundIndex))
	base := bestProfile
	if roundIndex%4 == 1 {
		base = baselineProfile
	}
	candidate := retrievalTuningDeepCopy(base)

	var paraChoices []int
	var relChoices []int
	var finalChoices []int
	var alphaChoices []float64
	var pctChoices []int
	var minResultsChoices []int
	switch objective {
	case RetrievalTuningObjectivePrecisionPriority:
		paraChoices = []int{40, 80, 120, 180, 240, 320}
		relChoices = []int{4, 8, 12, 16, 24}
		finalChoices = []int{4, 8, 12, 16, 20, 32, 48, 64}
		alphaChoices = []float64{0.0, 0.35, 0.50, 0.62, 0.72, 0.82, 0.90}
		pctChoices = []int{55, 60, 65, 72, 80}
		minResultsChoices = []int{1, 2}
	case RetrievalTuningObjectiveRecallPriority:
		paraChoices = []int{120, 220, 300, 420, 560, 720}
		relChoices = []int{8, 12, 16, 24, 32}
		finalChoices = []int{8, 16, 32, 48, 64, 96, 128}
		alphaChoices = []float64{0.20, 0.35, 0.45, 0.55, 0.65, 0.75}
		pctChoices = []int{40, 48, 55, 62}
		minResultsChoices = []int{1, 2, 3}
	default:
		paraChoices = []int{80, 160, 240, 320, 420, 520}
		relChoices = []int{6, 10, 14, 18, 24, 30}
		finalChoices = []int{6, 12, 20, 32, 48, 64, 80}
		alphaChoices = []float64{0.25, 0.45, 0.55, 0.65, 0.75, 0.85}
		pctChoices = []int{48, 55, 62, 70}
		minResultsChoices = []int{1, 2, 3}
	}

	boolChoices := []bool{true, true, false}
	retrievalTuningNestedSet(candidate, "retrieval.top_k_paragraphs", rng.choiceInt(paraChoices))
	retrievalTuningNestedSet(candidate, "retrieval.top_k_relations", rng.choiceInt(relChoices))
	retrievalTuningNestedSet(candidate, "retrieval.top_k_final", rng.choiceInt(finalChoices))
	retrievalTuningNestedSet(candidate, "retrieval.alpha", rng.choiceFloat(alphaChoices))
	retrievalTuningNestedSet(candidate, "retrieval.enable_ppr", rng.choiceBool(boolChoices))
	retrievalTuningNestedSet(candidate, "retrieval.ppr_alpha", rng.choiceFloat([]float64{0.72, 0.80, 0.85, 0.90, 0.94}))
	retrievalTuningNestedSet(candidate, "retrieval.ppr_timeout_seconds", rng.choiceFloat([]float64{0.8, 1.2, 1.5, 2.0, 3.0}))
	retrievalTuningNestedSet(candidate, "retrieval.search.smart_fallback.enabled", rng.choiceBool(boolChoices))
	retrievalTuningNestedSet(candidate, "retrieval.sparse.enabled", rng.choiceBool(boolChoices))
	retrievalTuningNestedSet(candidate, "retrieval.sparse.mode", rng.choiceString([]string{"auto", "hybrid", "fallback_only"}))
	retrievalTuningNestedSet(candidate, "retrieval.sparse.candidate_k", rng.choiceInt([]int{60, 80, 120, 160, 220, 320}))
	retrievalTuningNestedSet(candidate, "retrieval.sparse.relation_candidate_k", rng.choiceInt([]int{40, 60, 90, 120, 180, 260}))
	retrievalTuningNestedSet(candidate, "retrieval.fusion.method", rng.choiceString([]string{"weighted_rrf", "weighted_rrf", "alpha_legacy"}))
	retrievalTuningNestedSet(candidate, "retrieval.fusion.rrf_k", rng.choiceInt([]int{30, 45, 60, 75, 90}))
	vectorWeight := rng.choiceFloat([]float64{0.55, 0.65, 0.72, 0.80, 0.88})
	retrievalTuningNestedSet(candidate, "retrieval.fusion.vector_weight", vectorWeight)
	retrievalTuningNestedSet(candidate, "retrieval.fusion.bm25_weight", 1.0-vectorWeight)
	semanticWeight := rng.choiceFloat([]float64{0.50, 0.60, 0.65, 0.72, 0.80})
	graphWeight := rng.choiceFloat([]float64{0.10, 0.15, 0.22, 0.30})
	sparsePoolWeight := math.Max(0.0, 1.0-semanticWeight-graphWeight)
	retrievalTuningNestedSet(candidate, "retrieval.vector_pools.paragraph_top_k", rng.choiceInt([]int{12, 20, 32, 48, 64}))
	retrievalTuningNestedSet(candidate, "retrieval.vector_pools.graph_top_k", rng.choiceInt([]int{24, 40, 64, 80, 120}))
	retrievalTuningNestedSet(candidate, "retrieval.vector_pools.graph_expand_paragraph_k", rng.choiceInt([]int{40, 80, 120, 160}))
	retrievalTuningNestedSet(candidate, "retrieval.vector_pools.relation_expand_per_hit", rng.choiceInt([]int{3, 5, 8, 12}))
	retrievalTuningNestedSet(candidate, "retrieval.vector_pools.entity_expand_per_hit", rng.choiceInt([]int{4, 8, 12, 16}))
	retrievalTuningNestedSet(candidate, "retrieval.vector_pools.relation_evidence_weight", rng.choiceFloat([]float64{0.75, 1.0, 1.25}))
	retrievalTuningNestedSet(candidate, "retrieval.vector_pools.entity_evidence_weight", rng.choiceFloat([]float64{0.35, 0.55, 0.75}))
	retrievalTuningNestedSet(candidate, "retrieval.vector_pools.semantic_weight", semanticWeight)
	retrievalTuningNestedSet(candidate, "retrieval.vector_pools.sparse_weight", sparsePoolWeight)
	retrievalTuningNestedSet(candidate, "retrieval.vector_pools.graph_weight", graphWeight)
	relationIntentSemantic := rng.choiceFloat([]float64{0.35, 0.45, 0.55, 0.65})
	relationIntentGraph := rng.choiceFloat([]float64{0.25, 0.35, 0.40, 0.50})
	relationIntentSparse := math.Max(0.0, 1.0-relationIntentSemantic-relationIntentGraph)
	retrievalTuningNestedSet(candidate, "retrieval.vector_pools.relation_intent.graph_top_k", rng.choiceInt([]int{40, 80, 120, 160}))
	retrievalTuningNestedSet(candidate, "retrieval.vector_pools.relation_intent.semantic_weight", relationIntentSemantic)
	retrievalTuningNestedSet(candidate, "retrieval.vector_pools.relation_intent.sparse_weight", relationIntentSparse)
	retrievalTuningNestedSet(candidate, "retrieval.vector_pools.relation_intent.graph_weight", relationIntentGraph)
	retrievalTuningNestedSet(candidate, "threshold.min_threshold", rng.choiceFloat([]float64{0.20, 0.25, 0.30, 0.35, 0.42}))
	retrievalTuningNestedSet(candidate, "threshold.max_threshold", rng.choiceFloat([]float64{0.88, 0.92, 0.95, 0.98}))
	retrievalTuningNestedSet(candidate, "threshold.percentile", rng.choiceInt(pctChoices))
	retrievalTuningNestedSet(candidate, "threshold.min_results", rng.choiceInt(minResultsChoices))

	return m.normalizeProfileWithFallback(candidate, base)
}

// RetrievalTuningScoreMetrics 对齐 _score_metrics
func RetrievalTuningScoreMetrics(metrics map[string]any, objective string) float64 {
	if metrics == nil {
		metrics = map[string]any{}
	}
	precisionAt1 := retrievalTuningMetricFloat(metrics, "precision_at_1")
	precisionAt3 := retrievalTuningMetricFloat(metrics, "precision_at_3")
	mrr := retrievalTuningMetricFloat(metrics, "mrr")
	recall := retrievalTuningMetricFloat(metrics, "recall_at_k")
	spoHit := retrievalTuningMetricFloat(metrics, "spo_relation_hit_rate")
	emptyRate := 1.0
	if value, ok := metrics["empty_rate"]; ok && value != nil {
		if parsed, err := pyFloat64(value); err == nil && parsed != 0 {
			emptyRate = parsed
		}
	}
	categoryPenalty := 0.0
	if value, ok := metrics["category_floor_penalty"]; ok && value != nil {
		if parsed, err := pyFloat64(value); err == nil {
			categoryPenalty = parsed
		}
	} else {
		categoryPenalty = RetrievalTuningCategoryFloorPenalty(metrics, objective)
	}
	if categoryPenalty < 0 {
		categoryPenalty = 0
	}

	var raw float64
	var emptyPenalty float64
	switch objective {
	case RetrievalTuningObjectiveRecallPriority:
		raw = 0.15*precisionAt1 + 0.15*precisionAt3 + 0.15*mrr + 0.40*recall + 0.15*spoHit
		emptyPenalty = 0.05 * emptyRate
	case RetrievalTuningObjectiveBalanced:
		raw = 0.25*precisionAt1 + 0.20*precisionAt3 + 0.15*mrr + 0.25*recall + 0.15*spoHit
		emptyPenalty = 0.10 * emptyRate
	default:
		raw = 0.40*precisionAt1 + 0.20*precisionAt3 + 0.15*mrr + 0.15*recall + 0.10*spoHit
		emptyPenalty = 0.15 * emptyRate
	}
	return raw - emptyPenalty - categoryPenalty
}

// RetrievalTuningCategoryFloorPenalty 对齐 _category_floor_penalty
func RetrievalTuningCategoryFloorPenalty(metrics map[string]any, objective string) float64 {
	category := retrievalTuningMapValue(metrics["category"])
	if len(category) == 0 {
		return 0
	}
	var floors map[string]float64
	var scale float64
	switch pyStr(objective) {
	case RetrievalTuningObjectiveRecallPriority:
		floors = map[string]float64{
			RetrievalTuningCategoryQueryNL:     0.60,
			RetrievalTuningCategoryQueryKW:     0.48,
			RetrievalTuningCategorySPOSearch:   0.52,
			RetrievalTuningCategorySPORelation: 0.88,
		}
		scale = 0.12
	case RetrievalTuningObjectiveBalanced:
		floors = map[string]float64{
			RetrievalTuningCategoryQueryNL:     0.65,
			RetrievalTuningCategoryQueryKW:     0.52,
			RetrievalTuningCategorySPOSearch:   0.55,
			RetrievalTuningCategorySPORelation: 0.90,
		}
		scale = 0.18
	default:
		floors = map[string]float64{
			RetrievalTuningCategoryQueryNL:     0.70,
			RetrievalTuningCategoryQueryKW:     0.55,
			RetrievalTuningCategorySPOSearch:   0.58,
			RetrievalTuningCategorySPORelation: 0.92,
		}
		scale = 0.25
	}
	weights := map[string]float64{
		RetrievalTuningCategoryQueryNL:     1.0,
		RetrievalTuningCategoryQueryKW:     1.1,
		RetrievalTuningCategorySPOSearch:   1.0,
		RetrievalTuningCategorySPORelation: 1.2,
	}

	weighted := 0.0
	weightTotal := 0.0
	for name, floor := range floors {
		row := retrievalTuningMapValue(category[name])
		if len(row) == 0 {
			continue
		}
		total := retrievalTuningMetricFloat(row, "total")
		if total <= 0 {
			continue
		}
		hit := retrievalTuningMetricFloat(row, "hit")
		hitRate := hit / total
		if hitRate < 0 {
			hitRate = 0
		}
		if hitRate > 1 {
			hitRate = 1
		}
		shortfall := floor - hitRate
		if shortfall < 0 {
			shortfall = 0
		}
		weight := 1.0
		if value, ok := weights[name]; ok {
			weight = value
		}
		weighted += weight * shortfall
		weightTotal += weight
	}
	if weightTotal <= 1e-9 {
		return 0
	}
	return scale * (weighted / weightTotal)
}

// validateBestProfile 对齐 _validate_best_profile
func (m *RetrievalTuningManager) validateBestProfile(ctx context.Context, baselineProfile map[string]any, candidateProfile map[string]any, holdoutCases []*RetrievalQueryCase, objective string, topKEval int, queryTimeoutSeconds float64) (map[string]any, error) {
	if len(holdoutCases) == 0 {
		return map[string]any{"recommended": false, "reason": "holdout_empty"}, nil
	}
	stableBaseline, err := m.evaluateProfile(ctx, baselineProfile, holdoutCases, objective, topKEval, queryTimeoutSeconds, "stable")
	if err != nil {
		return nil, err
	}
	stableBest, err := m.evaluateProfile(ctx, candidateProfile, holdoutCases, objective, topKEval, queryTimeoutSeconds, "stable")
	if err != nil {
		return nil, err
	}
	onlineBaseline, err := m.evaluateProfile(ctx, baselineProfile, holdoutCases, objective, topKEval, queryTimeoutSeconds, "online_like")
	if err != nil {
		return nil, err
	}
	onlineBest, err := m.evaluateProfile(ctx, candidateProfile, holdoutCases, objective, topKEval, queryTimeoutSeconds, "online_like")
	if err != nil {
		return nil, err
	}

	baseMetrics := onlineBaseline.metrics
	bestMetrics := onlineBest.metrics
	scoreDelta := onlineBest.score - onlineBaseline.score
	precisionDelta := retrievalTuningMetricFloat(bestMetrics, "precision_at_1") - retrievalTuningMetricFloat(baseMetrics, "precision_at_1")
	recallDelta := retrievalTuningMetricFloat(bestMetrics, "recall_at_k") - retrievalTuningMetricFloat(baseMetrics, "recall_at_k")
	emptyDelta := retrievalTuningMetricFloatDefault(bestMetrics, "empty_rate", 1.0) - retrievalTuningMetricFloatDefault(baseMetrics, "empty_rate", 1.0)
	latencyDelta := retrievalTuningMetricFloat(bestMetrics, "avg_elapsed_ms") - retrievalTuningMetricFloat(baseMetrics, "avg_elapsed_ms")
	latencyLimit := math.Max(RetrievalTuningMaxLatencyIncreaseMS, retrievalTuningMetricFloat(baseMetrics, "avg_elapsed_ms")*RetrievalTuningMaxLatencyIncreaseRatio)

	scoreDeltaOK := scoreDelta >= RetrievalTuningMinValidationScoreDelta
	precisionDropOK := precisionDelta >= -RetrievalTuningMaxPrecisionDrop
	recallDropOK := recallDelta >= -RetrievalTuningMaxRecallDrop
	emptyRateOK := emptyDelta <= RetrievalTuningMaxEmptyRateIncrease
	latencyOK := latencyDelta <= latencyLimit
	recommended := scoreDeltaOK && precisionDropOK && recallDropOK && emptyRateOK && latencyOK
	reason := ""
	if !recommended {
		reason = "holdout_online_like_validation_failed"
	}

	return map[string]any{
		"recommended": recommended,
		"reason":      reason,
		"checks": map[string]any{
			"score_delta_ok":    scoreDeltaOK,
			"precision_drop_ok": precisionDropOK,
			"recall_drop_ok":    recallDropOK,
			"empty_rate_ok":     emptyRateOK,
			"latency_ok":        latencyOK,
		},
		"thresholds": map[string]any{
			"min_score_delta":            RetrievalTuningMinValidationScoreDelta,
			"max_precision_drop":         RetrievalTuningMaxPrecisionDrop,
			"max_recall_drop":            RetrievalTuningMaxRecallDrop,
			"max_empty_rate_increase":    RetrievalTuningMaxEmptyRateIncrease,
			"max_latency_increase_ms":    RetrievalTuningMaxLatencyIncreaseMS,
			"max_latency_increase_ratio": RetrievalTuningMaxLatencyIncreaseRatio,
		},
		"deltas": map[string]any{
			"score":          pyRound(scoreDelta, 6),
			"precision_at_1": pyRound(precisionDelta, 6),
			"recall_at_k":    pyRound(recallDelta, 6),
			"empty_rate":     pyRound(emptyDelta, 6),
			"avg_elapsed_ms": pyRound(latencyDelta, 3),
		},
		"stable": map[string]any{
			"baseline": stableBaseline.toMap(),
			"best":     stableBest.toMap(),
		},
		"online_like": map[string]any{
			"baseline": onlineBaseline.toMap(),
			"best":     onlineBest.toMap(),
		},
		"holdout_case_count": len(holdoutCases),
	}, nil
}

// retrievalTuningMetricFloatDefault 读取指标浮点值，缺失或为零时使用默认值
// 对齐 Python 的 float(metrics.get(name, default) or default)
func retrievalTuningMetricFloatDefault(metrics map[string]any, name string, defaultValue float64) float64 {
	value, ok := metrics[name]
	if !ok || value == nil {
		return defaultValue
	}
	parsed, err := pyFloat64(value)
	if err != nil || parsed == 0 {
		return defaultValue
	}
	return parsed
}
