package retrieval

// A_memorix 时序检索、实体抽取与统计，对齐 core/retrieval/dual_path.py 的 2930-3272 行

import (
	"math"
	"sort"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"maibot-go/internal/A_memorix/core/utils"
)

// dualPathCaseFolder 对齐 Python 的 casefold 语义
var dualPathCaseFolder = cases.Fold()

// dualPathTimeValue 读取数值时间字段，缺失或非法返回 nil
func dualPathTimeValue(paragraph map[string]any, key string) *float64 {
	if paragraph == nil {
		return nil
	}
	value, exists := paragraph[key]
	if !exists || value == nil {
		return nil
	}
	number, ok := numericValue(value)
	if !ok {
		return nil
	}
	return &number
}

// extractEffectiveTime 对齐 _extract_effective_time，返回有效时间区间与命中依据
func (r *DualPathRetriever) extractEffectiveTime(paragraph map[string]any, temporal *TemporalQueryOptions) (*float64, *float64, string) {
	eventTime := dualPathTimeValue(paragraph, "event_time")
	eventStart := dualPathTimeValue(paragraph, "event_time_start")
	eventEnd := dualPathTimeValue(paragraph, "event_time_end")

	if eventStart != nil || eventEnd != nil {
		var effectiveStart *float64
		switch {
		case eventStart != nil:
			effectiveStart = eventStart
		case eventTime != nil:
			effectiveStart = eventTime
		default:
			effectiveStart = eventEnd
		}
		var effectiveEnd *float64
		switch {
		case eventEnd != nil:
			effectiveEnd = eventEnd
		case eventTime != nil:
			effectiveEnd = eventTime
		default:
			effectiveEnd = eventStart
		}
		return effectiveStart, effectiveEnd, "event_time_range"
	}

	if eventTime != nil {
		return eventTime, eventTime, "event_time"
	}

	allowFallback := true
	if temporal != nil {
		allowFallback = temporal.AllowCreatedFallback
	}
	if allowFallback {
		if createdAt := dualPathTimeValue(paragraph, "created_at"); createdAt != nil {
			return createdAt, createdAt, "created_at_fallback"
		}
	}
	return nil, nil, ""
}

// formatTimestampText 包装 format_timestamp，失败时返回 nil 以对齐 Optional 返回
func formatTimestampText(value *float64) any {
	text, err := utils.FormatTimestamp(value)
	if err != nil || text == nil {
		return nil
	}
	return *text
}

// buildTimeMetaFromParagraph 对齐 _build_time_meta_from_paragraph
func (r *DualPathRetriever) buildTimeMetaFromParagraph(paragraph map[string]any, temporal *TemporalQueryOptions) map[string]any {
	effectiveStart, effectiveEnd, matchBasis := r.extractEffectiveTime(paragraph, temporal)
	if matchBasis == "" {
		matchBasis = "none"
	}
	var timeConfidence any = 1.0
	if paragraph != nil {
		if value, exists := paragraph["time_confidence"]; exists {
			timeConfidence = value
		}
	}
	getValue := func(key string) any {
		if paragraph == nil {
			return nil
		}
		return paragraph[key]
	}
	return map[string]any{
		"event_time":           getValue("event_time"),
		"event_time_start":     getValue("event_time_start"),
		"event_time_end":       getValue("event_time_end"),
		"ingest_time":          getValue("created_at"),
		"time_granularity":     getValue("time_granularity"),
		"time_confidence":      timeConfidence,
		"effective_start":      effectiveStart,
		"effective_end":        effectiveEnd,
		"effective_start_text": formatTimestampText(effectiveStart),
		"effective_end_text":   formatTimestampText(effectiveEnd),
		"match_basis":          matchBasis,
	}
}

// matchesPersonFilter 对齐 _matches_person_filter，按实体名包含关系判断人物命中
func (r *DualPathRetriever) matchesPersonFilter(paragraphHash string, person string) bool {
	if strings.TrimSpace(person) == "" {
		return true
	}
	target := strings.ToLower(strings.TrimSpace(person))
	if target == "" {
		return true
	}
	if r.metadataStore == nil {
		return false
	}
	entitiesByParagraph, err := r.metadataStore.GetParagraphEntitiesByHashes([]string{paragraphHash})
	if err != nil {
		zap.L().Warn("查询段落实体失败", zap.String("hash", paragraphHash), zap.Error(err))
		return false
	}
	for _, entity := range entitiesByParagraph[paragraphHash] {
		name := strings.ToLower(strings.TrimSpace(stringFromMap(entity, "name")))
		if strings.Contains(name, target) {
			return true
		}
	}
	return false
}

// isTemporalMatch 对齐 _is_temporal_match
func (r *DualPathRetriever) isTemporalMatch(paragraph map[string]any, temporal *TemporalQueryOptions) bool {
	if temporal == nil {
		return true
	}
	if temporal.Source != "" && stringFromMap(paragraph, "source") != temporal.Source {
		return false
	}
	if !r.matchesPersonFilter(stringFromMap(paragraph, "hash"), temporal.Person) {
		return false
	}
	effectiveStart, effectiveEnd, _ := r.extractEffectiveTime(paragraph, temporal)
	if effectiveStart == nil || effectiveEnd == nil {
		return false
	}
	if temporal.TimeFrom != nil && temporal.TimeTo != nil {
		return *effectiveEnd >= *temporal.TimeFrom && *effectiveStart <= *temporal.TimeTo
	}
	if temporal.TimeFrom != nil {
		return *effectiveEnd >= *temporal.TimeFrom
	}
	if temporal.TimeTo != nil {
		return *effectiveStart <= *temporal.TimeTo
	}
	return true
}

// applyTemporalFilterToParagraphs 对齐 _apply_temporal_filter_to_paragraphs
func (r *DualPathRetriever) applyTemporalFilterToParagraphs(results []RetrievalResult, temporal *TemporalQueryOptions) []RetrievalResult {
	if temporal == nil {
		return results
	}
	filtered := make([]RetrievalResult, 0, len(results))
	for _, item := range results {
		paragraph, err := r.metadataStore.GetParagraph(item.HashValue)
		if err != nil {
			zap.L().Warn("查询段落失败", zap.String("hash", item.HashValue), zap.Error(err))
			continue
		}
		if paragraph == nil {
			continue
		}
		if !r.isTemporalMatch(paragraph, temporal) {
			continue
		}
		if item.Metadata == nil {
			item.Metadata = map[string]any{}
		}
		item.Metadata["time_meta"] = r.buildTimeMetaFromParagraph(paragraph, temporal)
		filtered = append(filtered, item)
	}
	return r.sortResultsWithTemporal(filtered, temporal)
}

// bestSupportingTimeMeta 对齐 _best_supporting_time_meta
func (r *DualPathRetriever) bestSupportingTimeMeta(relationHash string, temporal *TemporalQueryOptions) map[string]any {
	supports, err := r.metadataStore.GetParagraphsByRelation(relationHash)
	if err != nil {
		zap.L().Warn("查询关系支撑段落失败", zap.String("hash", relationHash), zap.Error(err))
		return nil
	}
	if len(supports) == 0 {
		return nil
	}
	var bestMeta map[string]any
	bestTime := math.Inf(-1)
	for _, paragraph := range supports {
		if !r.isTemporalMatch(paragraph, temporal) {
			continue
		}
		meta := r.buildTimeMetaFromParagraph(paragraph, temporal)
		score := math.Inf(-1)
		if effective, ok := numericValue(meta["effective_end"]); ok {
			score = effective
		}
		if score >= bestTime {
			bestTime = score
			bestMeta = meta
		}
	}
	return bestMeta
}

// bestSupportingTimeMetaBatch 对齐 _best_supporting_time_meta_batch
func (r *DualPathRetriever) bestSupportingTimeMetaBatch(relationHashes []string, temporal *TemporalQueryOptions) map[string]map[string]any {
	out := make(map[string]map[string]any)
	if temporal == nil {
		return out
	}
	if len(relationHashes) == 0 {
		return out
	}
	grouped, err := r.metadataStore.GetParagraphsByRelationHashes(relationHashes)
	if err != nil {
		zap.L().Warn("批量查询关系支撑段落失败", zap.Error(err))
		return out
	}
	for relationHash, supports := range grouped {
		var bestMeta map[string]any
		bestTime := math.Inf(-1)
		for _, paragraph := range supports {
			if !r.isTemporalMatch(paragraph, temporal) {
				continue
			}
			meta := r.buildTimeMetaFromParagraph(paragraph, temporal)
			score := math.Inf(-1)
			if effective, ok := numericValue(meta["effective_end"]); ok {
				score = effective
			}
			if score >= bestTime {
				bestTime = score
				bestMeta = meta
			}
		}
		out[relationHash] = bestMeta
	}
	return out
}

// applyTemporalFilterToRelations 对齐 _apply_temporal_filter_to_relations
func (r *DualPathRetriever) applyTemporalFilterToRelations(results []RetrievalResult, temporal *TemporalQueryOptions) []RetrievalResult {
	if temporal == nil {
		return results
	}
	missingHashes := make([]string, 0, len(results))
	for _, item := range results {
		if item.Metadata == nil || item.Metadata["time_meta"] == nil {
			missingHashes = append(missingHashes, item.HashValue)
		}
	}
	batchTimeMeta := r.bestSupportingTimeMetaBatch(missingHashes, temporal)
	filtered := make([]RetrievalResult, 0, len(results))
	for _, item := range results {
		var meta map[string]any
		if item.Metadata != nil {
			meta, _ = item.Metadata["time_meta"].(map[string]any)
		}
		if meta == nil {
			meta = batchTimeMeta[item.HashValue]
			if meta == nil {
				continue
			}
			if item.Metadata == nil {
				item.Metadata = map[string]any{}
			}
			item.Metadata["time_meta"] = meta
		}
		filtered = append(filtered, item)
	}
	return r.sortResultsWithTemporal(filtered, temporal)
}

// sortResultsWithTemporal 对齐 _sort_results_with_temporal，语义优先、时间次排序
func (r *DualPathRetriever) sortResultsWithTemporal(results []RetrievalResult, temporal *TemporalQueryOptions) []RetrievalResult {
	if temporal == nil {
		return results
	}
	effectiveEnd := func(item RetrievalResult) float64 {
		if item.Metadata == nil {
			return math.Inf(-1)
		}
		timeMeta, ok := item.Metadata["time_meta"].(map[string]any)
		if !ok || timeMeta == nil {
			return math.Inf(-1)
		}
		if value, ok := numericValue(timeMeta["effective_end"]); ok {
			return value
		}
		if value, ok := numericValue(timeMeta["effective_start"]); ok {
			return value
		}
		return math.Inf(-1)
	}
	sort.SliceStable(results, func(left, right int) bool {
		if results[left].Score != results[right].Score {
			return results[left].Score > results[right].Score
		}
		return effectiveEnd(results[left]) > effectiveEnd(results[right])
	})
	return results
}

// retrieveTemporalOnly 对齐 _retrieve_temporal_only，无语义 query 时直接走时序索引
func (r *DualPathRetriever) retrieveTemporalOnly(temporal *TemporalQueryOptions, topK int, scope *RetrievalScope) ([]RetrievalResult, error) {
	if temporal == nil {
		return []RetrievalResult{}, nil
	}
	multiplier := max(1, temporal.CandidateMultiplier)
	limit := r.capTemporalScanK(topK*multiplier, temporal)
	var allowedHashes []string
	if scope != nil {
		allowedHashes = setToSlice(scope.ParagraphIDs)
	}
	paragraphs, err := r.metadataStore.QueryParagraphsTemporal(
		temporal.TimeFrom,
		temporal.TimeTo,
		temporal.Person,
		temporal.Source,
		limit,
		temporal.AllowCreatedFallback,
		allowedHashes,
	)
	if err != nil {
		return nil, err
	}
	results := make([]RetrievalResult, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		wordCount := any(0)
		if value, exists := paragraph["word_count"]; exists {
			wordCount = value
		}
		results = append(results, RetrievalResult{
			HashValue:  stringFromMap(paragraph, "hash"),
			Content:    stringFromMap(paragraph, "content"),
			Score:      1.0,
			ResultType: "paragraph",
			Source:     "temporal_scan",
			Metadata: map[string]any{
				"word_count": wordCount,
				"time_meta":  r.buildTimeMetaFromParagraph(paragraph, temporal),
			},
		})
	}
	results = r.sortResultsWithTemporal(results, temporal)
	return sliceTopK(results, topK), nil
}

// extractEntities 对齐 _extract_entities，按图节点构建 Aho-Corasick 匹配器
func (r *DualPathRetriever) extractEntities(text string) map[string]float64 {
	entities := map[string]float64{}
	if r.graphStore == nil {
		return entities
	}
	allEntities := r.graphStore.GetNodes()
	if len(allEntities) == 0 {
		return entities
	}
	nodeRevision := r.graphStore.NodeRevision()
	if r.acMatcher == nil || r.acNodeRevision != int64(nodeRevision) {
		matcher := utils.NewAhoCorasick(nil)
		nodeMap := make(map[string]string, len(allEntities))
		for _, entity := range allEntities {
			lowerName := strings.ToLower(entity)
			matcher.AddPattern(lowerName)
			nodeMap[lowerName] = entity
		}
		matcher.Build()
		r.acMatcher = matcher
		r.acNodesCount = len(allEntities)
		r.acNodeRevision = int64(nodeRevision)
		r.acNodeMap = nodeMap
	}
	stats := r.acMatcher.FindAll(strings.ToLower(text))
	for lowerName, count := range stats {
		original, exists := r.acNodeMap[lowerName]
		if !exists {
			continue
		}
		entities[original] = float64(count)
	}
	return entities
}

// ExtractEntities 导出实体抽取，满足 PosteriorGraphRetriever
func (r *DualPathRetriever) ExtractEntities(text string) map[string]float64 {
	return r.extractEntities(text)
}

// ApplyTemporalFilterToRelations 导出时序过滤，满足 PosteriorGraphRetriever
func (r *DualPathRetriever) ApplyTemporalFilterToRelations(results []RetrievalResult, temporal any) []RetrievalResult {
	return r.applyTemporalFilterToRelations(results, temporalFromAny(temporal))
}

// temporalFromAny 把后验图门控透传的时序选项还原为具体类型
func temporalFromAny(value any) *TemporalQueryOptions {
	if value == nil {
		return nil
	}
	if temporal, ok := value.(*TemporalQueryOptions); ok {
		return temporal
	}
	return nil
}

// isSparseOnlyRuntime 对齐 _is_sparse_only_runtime
func (r *DualPathRetriever) isSparseOnlyRuntime() bool {
	mode := strings.ToLower(strings.TrimSpace(r.config.Sparse.Mode))
	return r.runtimeSparseOnly.Load() || mode == "fallback_only" || r.vectorStore == nil || r.embeddingManager == nil
}

// Statistics 对齐 get_statistics
func (r *DualPathRetriever) Statistics() map[string]any {
	vectorSize := 0
	if r.vectorStore != nil {
		vectorSize = r.vectorStore.NumVectors()
	}
	numNodes := 0
	numEdges := 0
	if r.graphStore != nil {
		numNodes = r.graphStore.NumNodes()
		numEdges = r.graphStore.NumEdges()
	}
	metadataStats := map[string]int{}
	if r.metadataStore != nil {
		if stats, err := r.metadataStore.GetStatistics(); err == nil {
			metadataStats = stats
		}
	}
	var sparseStats any
	if r.sparseIndex != nil {
		sparseStats = r.sparseIndex.Stats()
	}
	return map[string]any{
		"config": map[string]any{
			"top_k_paragraphs":                       r.config.TopKParagraphs,
			"top_k_relations":                        r.config.TopKRelations,
			"top_k_final":                            r.config.TopKFinal,
			"alpha":                                  r.config.Alpha,
			"enable_ppr":                             r.config.EnablePPR,
			"ppr_local_enabled":                      r.config.PPRLocalEnabled,
			"ppr_local_max_nodes":                    r.config.PPRLocalMaxNodes,
			"ppr_local_hops":                         r.config.PPRLocalHops,
			"ppr_local_min_graph_nodes":              r.config.PPRLocalMinGraphNodes,
			"enable_parallel":                        r.config.EnableParallel,
			"strategy":                               string(r.config.RetrievalStrategy),
			"sparse_mode":                            r.config.Sparse.Mode,
			"fusion_method":                          r.config.Fusion.Method,
			"relation_intent_enabled":                r.config.RelationIntent.Enabled,
			"relation_intent_alpha_override":         r.config.RelationIntent.AlphaOverride,
			"relation_intent_candidate_multiplier":   r.config.RelationIntent.RelationCandidateMultiplier,
			"relation_intent_preserve_top_relations": r.config.RelationIntent.PreserveTopRelations,
			"relation_intent_force_sparse":           r.config.RelationIntent.ForceRelationSparse,
			"relation_intent_pair_rerank_enabled":    r.config.RelationIntent.PairPredicateRerankEnabled,
			"relation_intent_pair_predicate_limit":   r.config.RelationIntent.PairPredicateLimit,
			"graph_recall_enabled":                   r.config.GraphRecall.Enabled,
			"graph_recall_candidate_k":               r.config.GraphRecall.CandidateK,
			"graph_recall_allow_two_hop_pair":        r.config.GraphRecall.AllowTwoHopPair,
			"graph_recall_max_paths":                 r.config.GraphRecall.MaxPaths,
		},
		"vector_store":   map[string]any{"size": vectorSize},
		"graph_store":    map[string]any{"num_nodes": numNodes, "num_edges": numEdges},
		"metadata_store": metadataStats,
		"sparse":         sparseStats,
	}
}

// dualPathLowerFold 把文本做 NFKC + casefold，对齐 Python 的归一化顺序
func dualPathLowerFold(text string) string {
	return dualPathCaseFolder.String(norm.NFKC.String(text))
}
