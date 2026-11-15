package retrieval

// A_memorix 双路检索入口、混合候选与结果融合，对齐 core/retrieval/dual_path.py 的 1081-1574 与 2340-2675 行

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"
)

// retrieveParagraphsOnly 对齐 _retrieve_paragraphs_only
func (r *DualPathRetriever) retrieveParagraphsOnly(ctx context.Context, query string, topK int, temporal *TemporalQueryOptions, scope *RetrievalScope) ([]RetrievalResult, error) {
	if r.isSparseOnlyRuntime() {
		results, err := r.searchParagraphsSparse(query, topK, temporal, scope)
		if err != nil {
			return nil, err
		}
		return sliceTopK(results, topK), nil
	}

	var queryEmbedding []float32
	embeddingOK := false
	vectorResults := []RetrievalResult{}
	if r.embeddingManager != nil {
		encoded, err := r.embeddingManager.EncodeOne(ctx, query, 0, false, true)
		if err != nil {
			zap.L().Warn(fmt.Sprintf("段落检索 embedding 生成失败，将尝试 sparse 回退: %v", err))
		} else {
			queryEmbedding = encoded
			embeddingOK = r.isEmbeddingReadyForVectorSearch(encoded, "paragraph_only")
		}
	}

	if embeddingOK {
		multiplier := 1
		if temporal != nil {
			multiplier = max(1, temporal.CandidateMultiplier)
		}
		candidateK := r.capTemporalScanK(topK*2*multiplier, temporal)
		paragraphIDs, paragraphScores, err := searchVectorStore(r.vectorStore, queryEmbedding, candidateK, scopeParagraphIDs(scope))
		if err != nil {
			return nil, err
		}
		vectorResults, err = r.buildParagraphResultsFromIDs(paragraphIDs, paragraphScores, "paragraph_search", temporal, scope)
		if err != nil {
			return nil, err
		}
	}

	sparseResults := []RetrievalResult{}
	if r.shouldUseSparse(embeddingOK, vectorResults) {
		results, err := r.searchParagraphsSparse(query, topK, temporal, scope)
		if err != nil {
			return nil, err
		}
		sparseResults = results
	}

	var results []RetrievalResult
	switch {
	case r.config.Fusion.Method == "weighted_rrf" && len(vectorResults) > 0 && len(sparseResults) > 0:
		results = r.fuseRankedListsWeightedRRF(vectorResults, sparseResults)
	case len(vectorResults) > 0 && len(sparseResults) > 0:
		results = append(vectorResults, sparseResults...)
		sortResultsByScoreStable(results)
	case len(vectorResults) > 0:
		results = vectorResults
	default:
		results = sparseResults
	}
	return sliceTopK(results, topK), nil
}

// scopeParagraphIDs 返回段落可见集合，scope 为空时返回 nil 表示不过滤
func scopeParagraphIDs(scope *RetrievalScope) map[string]struct{} {
	if scope == nil {
		return nil
	}
	return scope.ParagraphIDs
}

// scopeRelationIDs 返回关系可见集合
func scopeRelationIDs(scope *RetrievalScope) map[string]struct{} {
	if scope == nil {
		return nil
	}
	return scope.RelationIDs
}

// scopeEntityIDs 返回实体可见集合
func scopeEntityIDs(scope *RetrievalScope) map[string]struct{} {
	if scope == nil {
		return nil
	}
	return scope.EntityIDs
}

// retrieveRelationsOnly 对齐 _retrieve_relations_only
func (r *DualPathRetriever) retrieveRelationsOnly(ctx context.Context, query string, topK int, temporal *TemporalQueryOptions, scope *RetrievalScope) ([]RetrievalResult, error) {
	if r.isSparseOnlyRuntime() {
		sparseResults, err := r.searchRelationsSparse(query, topK, temporal, scope)
		if err != nil {
			return nil, err
		}
		graphResults := r.searchRelationsGraph(query, temporal, scope)
		if len(graphResults) > 0 {
			merged := r.mergeRelationResultsGraphEnhanced(nil, sparseResults, graphResults)
			return sliceTopK(merged, topK), nil
		}
		return sliceTopK(sparseResults, topK), nil
	}

	var queryEmbedding []float32
	embeddingOK := false
	if r.embeddingManager != nil {
		encoded, err := r.embeddingManager.EncodeOne(ctx, query, 0, false, true)
		if err != nil {
			zap.L().Warn(fmt.Sprintf("关系检索 embedding 生成失败，将尝试 sparse 回退: %v", err))
		} else {
			queryEmbedding = encoded
			embeddingOK = r.isEmbeddingReadyForVectorSearch(encoded, "relation_only")
		}
	}

	vectorResults := []RetrievalResult{}
	if embeddingOK {
		multiplier := 1
		if temporal != nil {
			multiplier = max(1, temporal.CandidateMultiplier)
		}
		candidateK := r.capTemporalScanK(topK*3*multiplier, temporal)
		ids, scores, err := searchVectorStore(r.vectorStore, queryEmbedding, candidateK, scopeEntityIDs(scope))
		if err != nil {
			return nil, err
		}
		entityMap, err := r.metadataStore.GetEntitiesByHashes(ids)
		if err != nil {
			return nil, err
		}
		seenRelations := map[string]struct{}{}
		relationScores := map[string]float64{}
		relationPivots := map[string]string{}
		relationOrder := make([]string, 0, len(ids))
		for index, hashValue := range ids {
			if index >= len(scores) {
				break
			}
			entity, exists := entityMap[hashValue]
			if !exists || entity == nil {
				continue
			}
			entityName := stringFromMap(entity, "name")
			relatedRelations, err := r.metadataStore.GetRelations(entityName, "", "", false)
			if err != nil {
				return nil, err
			}
			objectRelations, err := r.metadataStore.GetRelations("", "", entityName, false)
			if err != nil {
				return nil, err
			}
			relatedRelations = append(relatedRelations, objectRelations...)
			for _, relation := range relatedRelations {
				relationHash := stringFromMap(relation, "hash")
				if scope != nil {
					if _, visible := scope.RelationIDs[relationHash]; !visible {
						continue
					}
				}
				if _, exists := seenRelations[relationHash]; exists {
					continue
				}
				seenRelations[relationHash] = struct{}{}
				relationScores[relationHash] = scores[index]
				relationPivots[relationHash] = entityName
				relationOrder = append(relationOrder, relationHash)
			}
		}
		pivotMetadata := make(map[string]map[string]any, len(relationPivots))
		for hashValue, pivot := range relationPivots {
			pivotMetadata[hashValue] = map[string]any{"pivot_entity": pivot}
		}
		relationScoresOrdered := make([]float64, 0, len(relationOrder))
		for _, hashValue := range relationOrder {
			relationScoresOrdered = append(relationScoresOrdered, relationScores[hashValue])
		}
		vectorResults, err = r.buildRelationResultsFromIDs(relationOrder, relationScoresOrdered, "relation_search (via entity)", temporal, pivotMetadata, scope)
		if err != nil {
			return nil, err
		}
	}

	sparseResults := []RetrievalResult{}
	if r.shouldUseSparseRelations(embeddingOK, vectorResults, false) {
		results, err := r.searchRelationsSparse(query, topK, temporal, scope)
		if err != nil {
			return nil, err
		}
		sparseResults = results
	}

	graphResults := r.searchRelationsGraph(query, temporal, scope)
	var results []RetrievalResult
	switch {
	case len(graphResults) > 0:
		results = r.mergeRelationResultsGraphEnhanced(vectorResults, sparseResults, graphResults)
	case len(vectorResults) > 0 && len(sparseResults) > 0:
		results = r.mergeRelationResults(vectorResults, sparseResults)
	case len(vectorResults) > 0:
		results = vectorResults
	default:
		results = sparseResults
	}
	return sliceTopK(results, topK), nil
}

// retrieveDualPath 对齐 _retrieve_dual_path
func (r *DualPathRetriever) retrieveDualPath(ctx context.Context, query string, topK int, temporal *TemporalQueryOptions, relationIntent map[string]any, enablePPR bool, scope *RetrievalScope) ([]RetrievalResult, error) {
	if r.config.VectorPools.Mode == "dual" && !r.isSparseOnlyRuntime() {
		return r.retrieveDualVectorPools(ctx, query, topK, temporal, relationIntent, enablePPR, scope)
	}
	if relationIntent == nil {
		relationIntent = map[string]any{}
	}
	relationTopK := max(1, dualPathIntValue(relationIntent["relation_top_k"]))
	if _, exists := relationIntent["relation_top_k"]; !exists {
		relationTopK = max(1, r.config.TopKRelations)
	}
	forceRelationSparse := dualPathBool(relationIntent["force_relation_sparse"], false)
	preserveTopRelations := max(0, dualPathIntValue(relationIntent["preserve_top_relations"]))
	pairPredicateRerankEnabled := dualPathBool(relationIntent["pair_predicate_rerank_enabled"], false)
	pairPredicateLimit := max(1, dualPathIntValue(relationIntent["pair_predicate_limit"]))
	if _, exists := relationIntent["pair_predicate_limit"]; !exists {
		pairPredicateLimit = max(1, r.config.RelationIntent.PairPredicateLimit)
	}
	alphaOverride := optionalFloat(relationIntent["alpha_override"])

	if r.isSparseOnlyRuntime() {
		paraResults, err := r.searchParagraphsSparse(query, max(topK*2, r.config.Sparse.CandidateK), temporal, scope)
		if err != nil {
			return nil, err
		}
		sparseRelationResults, err := r.searchRelationsSparse(query, max(max(topK, r.config.Sparse.RelationCandidateK), relationTopK), temporal, scope)
		if err != nil {
			return nil, err
		}
		graphRelationResults := []RetrievalResult{}
		if dualPathBool(relationIntent["enabled"], false) {
			graphRelationResults = r.searchRelationsGraph(query, temporal, scope)
		}
		var relResults []RetrievalResult
		if len(graphRelationResults) > 0 {
			relResults = r.mergeRelationResultsGraphEnhanced(nil, sparseRelationResults, graphRelationResults)
		} else {
			relResults = sparseRelationResults
		}
		fusedResults, err := r.fuseResults(paraResults, relResults, alphaOverride, preserveTopRelations)
		if err != nil {
			return nil, err
		}
		if enablePPR {
			fusedResults = r.rerankWithPPR(ctx, fusedResults, query)
		}
		if temporal != nil {
			fusedResults = r.sortResultsWithTemporal(fusedResults, temporal)
		}
		fusedResults = r.applyPosteriorGraphGate(query, fusedResults, topK, temporal, relationIntent, scope)
		fusedResults = r.applyRelationIntentPairRerank(fusedResults, dualPathBool(relationIntent["enabled"], false), pairPredicateRerankEnabled, pairPredicateLimit)
		return sliceTopK(fusedResults, topK), nil
	}

	var queryEmbedding []float32
	embeddingOK := false
	if r.embeddingManager != nil {
		encoded, err := r.embeddingManager.EncodeOne(ctx, query, 0, false, true)
		if err != nil {
			zap.L().Warn(fmt.Sprintf("双路检索 embedding 生成失败，将尝试 sparse 回退: %v", err))
		} else {
			queryEmbedding = encoded
			embeddingOK = r.isEmbeddingReadyForVectorSearch(encoded, "dual_path")
		}
	}

	paraResults := []RetrievalResult{}
	relResults := []RetrievalResult{}
	if embeddingOK {
		var err error
		if r.config.EnableParallel {
			paraResults, relResults = r.parallelRetrieve(queryEmbedding, temporal, relationTopK, scope)
		} else {
			paraResults, relResults, err = r.sequentialRetrieve(queryEmbedding, temporal, relationTopK, scope)
			if err != nil {
				return nil, err
			}
		}
	} else {
		zap.L().Warn("embedding 不可用，跳过向量段落/关系召回")
	}

	sparseParaResults := []RetrievalResult{}
	if r.shouldUseSparse(embeddingOK, paraResults) {
		results, err := r.searchParagraphsSparse(query, max(topK*2, r.config.Sparse.CandidateK), temporal, scope)
		if err != nil {
			return nil, err
		}
		sparseParaResults = results
	}
	sparseRelationResults := []RetrievalResult{}
	if r.shouldUseSparseRelations(embeddingOK, relResults, forceRelationSparse) {
		results, err := r.searchRelationsSparse(query, max(max(topK, r.config.Sparse.RelationCandidateK), relationTopK), temporal, scope)
		if err != nil {
			return nil, err
		}
		sparseRelationResults = results
	}

	graphRelationResults := []RetrievalResult{}
	if dualPathBool(relationIntent["enabled"], false) {
		graphRelationResults = r.searchRelationsGraph(query, temporal, scope)
	}

	if len(paraResults) > 0 && len(sparseParaResults) > 0 {
		if r.config.Fusion.Method == "weighted_rrf" {
			paraResults = r.fuseRankedListsWeightedRRF(paraResults, sparseParaResults)
		} else {
			paraResults = append(paraResults, sparseParaResults...)
			sortResultsByScoreStable(paraResults)
		}
	} else if len(sparseParaResults) > 0 && (len(paraResults) == 0 || !embeddingOK) {
		paraResults = sparseParaResults
	}

	switch {
	case len(graphRelationResults) > 0:
		relResults = r.mergeRelationResultsGraphEnhanced(relResults, sparseRelationResults, graphRelationResults)
	case len(relResults) > 0 && len(sparseRelationResults) > 0:
		relResults = r.mergeRelationResults(relResults, sparseRelationResults)
	case len(sparseRelationResults) > 0 && (len(relResults) == 0 || !embeddingOK):
		relResults = sparseRelationResults
	}

	fusedResults, err := r.fuseResults(paraResults, relResults, alphaOverride, preserveTopRelations)
	if err != nil {
		return nil, err
	}
	if enablePPR {
		fusedResults = r.rerankWithPPR(ctx, fusedResults, query)
	}
	if temporal != nil {
		fusedResults = r.sortResultsWithTemporal(fusedResults, temporal)
	}
	fusedResults = r.applyPosteriorGraphGate(query, fusedResults, topK, temporal, relationIntent, scope)
	fusedResults = r.applyRelationIntentPairRerank(fusedResults, dualPathBool(relationIntent["enabled"], false), pairPredicateRerankEnabled, pairPredicateLimit)
	return sliceTopK(fusedResults, topK), nil
}

// parallelRetrieve 对齐 _parallel_retrieve，异常时记录并返回空结果
func (r *DualPathRetriever) parallelRetrieve(queryEmbedding []float32, temporal *TemporalQueryOptions, relationTopK int, scope *RetrievalScope) ([]RetrievalResult, []RetrievalResult) {
	paragraphResults, relationResults, err := r.collectMixedCandidates(queryEmbedding, temporal, relationTopK, scope)
	if err != nil {
		zap.L().Error(fmt.Sprintf("并行检索失败: %v", err))
		return []RetrievalResult{}, []RetrievalResult{}
	}
	return paragraphResults, relationResults
}

// sequentialRetrieve 对齐 _sequential_retrieve
func (r *DualPathRetriever) sequentialRetrieve(queryEmbedding []float32, temporal *TemporalQueryOptions, relationTopK int, scope *RetrievalScope) ([]RetrievalResult, []RetrievalResult, error) {
	return r.collectMixedCandidates(queryEmbedding, temporal, relationTopK, scope)
}

// mixedCandidateBudget 对齐 _mixed_candidate_budget
func (r *DualPathRetriever) mixedCandidateBudget(paraTopK int, relTopK int, temporal *TemporalQueryOptions) int {
	multiplier := 1
	if temporal != nil {
		multiplier = max(1, temporal.CandidateMultiplier)
	}
	base := max(paraTopK+relTopK, max(paraTopK, relTopK)*2)
	return max(base*6*multiplier, 48)
}

// mergeBackfilledResults 对齐 _merge_backfilled_results
func (r *DualPathRetriever) mergeBackfilledResults(primaryResults []RetrievalResult, backfillResults []RetrievalResult, topK int) []RetrievalResult {
	merged := make(map[string]RetrievalResult, len(primaryResults)+len(backfillResults))
	order := make([]string, 0, len(primaryResults)+len(backfillResults))
	for _, item := range primaryResults {
		if _, exists := merged[item.HashValue]; !exists {
			order = append(order, item.HashValue)
		}
		merged[item.HashValue] = item
	}
	for _, item := range backfillResults {
		existing, exists := merged[item.HashValue]
		if !exists {
			order = append(order, item.HashValue)
			merged[item.HashValue] = item
			continue
		}
		if item.Score > existing.Score {
			merged[item.HashValue] = item
		}
	}
	results := make([]RetrievalResult, 0, len(order))
	for _, hashValue := range order {
		results = append(results, merged[hashValue])
	}
	sortResultsByScoreStable(results)
	return sliceTopK(results, topK)
}

// graphVectorID 对齐 _graph_vector_id
func graphVectorID(itemType string, hashValue string) string {
	return strings.TrimSpace(itemType) + ":" + strings.TrimSpace(hashValue)
}

// parseGraphVectorID 对齐 _parse_graph_vector_id
func parseGraphVectorID(value string) (string, string) {
	token := strings.TrimSpace(value)
	index := strings.Index(token, ":")
	if index < 0 {
		return "", token
	}
	return strings.ToLower(strings.TrimSpace(token[:index])), strings.TrimSpace(token[index+1:])
}

// collectMixedCandidates 对齐 _collect_mixed_candidates
func (r *DualPathRetriever) collectMixedCandidates(queryEmbedding []float32, temporal *TemporalQueryOptions, relationTopK int, scope *RetrievalScope) ([]RetrievalResult, []RetrievalResult, error) {
	paraTopK := r.config.TopKParagraphs
	relTopK := relationTopK
	if relTopK <= 0 {
		relTopK = r.config.TopKRelations
	}
	candidateK := r.mixedCandidateBudget(paraTopK, relTopK, temporal)
	candidateK = r.capTemporalScanK(candidateK, temporal)

	var allowed map[string]struct{}
	if scope != nil {
		allowed = map[string]struct{}{}
		for hashValue := range scope.ParagraphIDs {
			allowed[hashValue] = struct{}{}
		}
		for hashValue := range scope.RelationIDs {
			allowed[hashValue] = struct{}{}
		}
	}
	ids, scores, err := searchVectorStore(r.vectorStore, queryEmbedding, candidateK, allowed)
	if err != nil {
		return nil, nil, err
	}
	paragraphMap, err := r.metadataStore.GetParagraphsByHashes(ids)
	if err != nil {
		return nil, nil, err
	}
	relationMap, err := r.metadataStore.GetRelationsByHashes(ids, false)
	if err != nil {
		return nil, nil, err
	}
	relationTimeMeta := map[string]map[string]any{}
	if temporal != nil {
		relationKeys := make([]string, 0, len(relationMap))
		for hashValue := range relationMap {
			relationKeys = append(relationKeys, hashValue)
		}
		relationTimeMeta = r.bestSupportingTimeMetaBatch(relationKeys, temporal)
	}

	paraCandidates := []RetrievalResult{}
	relCandidates := []RetrievalResult{}
	seenPara := map[string]struct{}{}
	seenRel := map[string]struct{}{}

	for index, hashValue := range ids {
		if index >= len(scores) {
			break
		}
		score := scores[index]
		paragraph, hasParagraph := paragraphMap[hashValue]
		if scope != nil {
			if _, visible := scope.ParagraphIDs[hashValue]; !visible {
				hasParagraph = false
			}
		}
		if hasParagraph && paragraph != nil {
			if _, seen := seenPara[hashValue]; !seen {
				if temporal != nil && !r.isTemporalMatch(paragraph, temporal) {
					continue
				}
				seenPara[hashValue] = struct{}{}
				wordCount := any(0)
				if value, exists := paragraph["word_count"]; exists {
					wordCount = value
				}
				paraCandidates = append(paraCandidates, RetrievalResult{
					HashValue:  hashValue,
					Content:    stringFromMap(paragraph, "content"),
					Score:      score,
					ResultType: "paragraph",
					Source:     "paragraph_search",
					Metadata: map[string]any{
						"word_count": wordCount,
						"time_meta":  r.buildTimeMetaFromParagraph(paragraph, temporal),
					},
				})
				continue
			}
		}

		relation, hasRelation := relationMap[hashValue]
		if scope != nil {
			if _, visible := scope.RelationIDs[hashValue]; !visible {
				hasRelation = false
			}
		}
		if !hasRelation || relation == nil {
			continue
		}
		if _, seen := seenRel[hashValue]; seen {
			continue
		}
		var itemTimeMeta map[string]any
		if temporal != nil {
			itemTimeMeta = relationTimeMeta[hashValue]
			if itemTimeMeta == nil {
				continue
			}
		}
		seenRel[hashValue] = struct{}{}
		confidence := any(1.0)
		if value, exists := relation["confidence"]; exists {
			confidence = value
		}
		relCandidates = append(relCandidates, RetrievalResult{
			HashValue:  hashValue,
			Content:    stringFromMap(relation, "subject") + " " + stringFromMap(relation, "predicate") + " " + stringFromMap(relation, "object"),
			Score:      score,
			ResultType: "relation",
			Source:     "relation_search",
			Metadata: map[string]any{
				"subject":    relation["subject"],
				"predicate":  relation["predicate"],
				"object":     relation["object"],
				"confidence": confidence,
				"time_meta":  itemTimeMeta,
			},
		})
	}

	if temporal != nil {
		paraCandidates = r.sortResultsWithTemporal(paraCandidates, temporal)
		relCandidates = r.sortResultsWithTemporal(relCandidates, temporal)
	}

	paraBackfill, err := r.searchParagraphs(queryEmbedding, paraTopK, temporal, scope)
	if err != nil {
		return nil, nil, err
	}
	relBackfill, err := r.searchRelations(queryEmbedding, relTopK, temporal, scope)
	if err != nil {
		return nil, nil, err
	}
	paraResults := r.mergeBackfilledResults(paraCandidates, paraBackfill, paraTopK)
	relResults := r.mergeBackfilledResults(relCandidates, relBackfill, relTopK)
	return paraResults, relResults, nil
}

// searchParagraphs 对齐 _search_paragraphs
func (r *DualPathRetriever) searchParagraphs(queryEmbedding []float32, topK int, temporal *TemporalQueryOptions, scope *RetrievalScope) ([]RetrievalResult, error) {
	multiplier := 1
	if temporal != nil {
		multiplier = max(1, temporal.CandidateMultiplier)
	}
	candidateK := r.capTemporalScanK(topK*multiplier, temporal)
	paraIDs, paraScores, err := searchVectorStore(r.vectorStore, queryEmbedding, candidateK, scopeParagraphIDs(scope))
	if err != nil {
		return nil, err
	}
	return r.buildParagraphResultsFromIDs(paraIDs, paraScores, "paragraph_search", temporal, scope)
}

// searchRelations 对齐 _search_relations
func (r *DualPathRetriever) searchRelations(queryEmbedding []float32, topK int, temporal *TemporalQueryOptions, scope *RetrievalScope) ([]RetrievalResult, error) {
	multiplier := 1
	if temporal != nil {
		multiplier = max(1, temporal.CandidateMultiplier)
	}
	candidateK := r.capTemporalScanK(topK*multiplier, temporal)
	relIDs, relScores, err := searchVectorStore(r.vectorStore, queryEmbedding, candidateK, scopeRelationIDs(scope))
	if err != nil {
		return nil, err
	}
	return r.buildRelationResultsFromIDs(relIDs, relScores, "relation_search", temporal, nil, scope)
}

// fuseResults 对齐 _fuse_results
func (r *DualPathRetriever) fuseResults(paraResults []RetrievalResult, relResults []RetrievalResult, alphaOverride *float64, preserveTopRelations int) ([]RetrievalResult, error) {
	alpha := r.config.Alpha
	if alphaOverride != nil {
		alpha = *alphaOverride
	}
	for index := range paraResults {
		paraResults[index].Score = paraResults[index].Score * alpha
		paraResults[index].Source = "fusion"
	}
	for index := range relResults {
		relResults[index].Score = relResults[index].Score * (1 - alpha)
		relResults[index].Source = "fusion"
	}

	preserveTopRelations = max(0, preserveTopRelations)
	preservedRelationHashes := map[string]struct{}{}
	if preserveTopRelations > 0 && len(relResults) > 0 {
		rankedRelations := cloneResultSlice(relResults)
		sortResultsByScoreStable(rankedRelations)
		if preserveTopRelations > len(rankedRelations) {
			preserveTopRelations = len(rankedRelations)
		}
		for _, item := range rankedRelations[:preserveTopRelations] {
			preservedRelationHashes[item.HashValue] = struct{}{}
		}
	}

	allResults := make([]RetrievalResult, 0, len(paraResults)+len(relResults))
	allResults = append(allResults, paraResults...)
	allResults = append(allResults, relResults...)
	sortResultsByScoreStable(allResults)

	relationHashes := make([]string, 0, len(relResults))
	for _, item := range allResults {
		if item.ResultType == "relation" {
			relationHashes = append(relationHashes, item.HashValue)
		}
	}
	relationSupportHashes, err := r.metadataStore.GetParagraphHashesByRelationHashes(relationHashes)
	if err != nil {
		return nil, err
	}

	seenParagraphs := map[string]struct{}{}
	seenItems := map[string]struct{}{}
	deduplicated := make([]RetrievalResult, 0, len(allResults))
	for _, item := range allResults {
		if _, seen := seenItems[item.HashValue]; seen {
			continue
		}
		if item.ResultType == "paragraph" {
			if _, seen := seenParagraphs[item.HashValue]; !seen {
				seenParagraphs[item.HashValue] = struct{}{}
				seenItems[item.HashValue] = struct{}{}
				deduplicated = append(deduplicated, item)
			}
			continue
		}
		if _, preserved := preservedRelationHashes[item.HashValue]; preserved {
			seenItems[item.HashValue] = struct{}{}
			deduplicated = append(deduplicated, item)
			continue
		}
		supportHashes := relationSupportHashes[item.HashValue]
		if len(supportHashes) > 0 {
			overlaps := false
			for _, supportHash := range supportHashes {
				if _, seen := seenParagraphs[supportHash]; seen {
					overlaps = true
					break
				}
			}
			if overlaps {
				continue
			}
		}
		seenItems[item.HashValue] = struct{}{}
		deduplicated = append(deduplicated, item)
	}
	sortResultsByScoreStable(deduplicated)
	return deduplicated, nil
}

// sortResultsByScoreStable 按分数降序稳定排序，保持并列项原有顺序
func sortResultsByScoreStable(results []RetrievalResult) {
	sort.SliceStable(results, func(left, right int) bool { return results[left].Score > results[right].Score })
}

// applyRelationIntentPairRerank 对齐 _apply_relation_intent_pair_rerank
func (r *DualPathRetriever) applyRelationIntentPairRerank(results []RetrievalResult, enabled bool, pairRerankEnabled bool, pairLimit int) []RetrievalResult {
	if !enabled || !pairRerankEnabled {
		return results
	}
	return r.rerankRelationItemsByPair(results, pairLimit)
}

// dualPathPairKey 关系项分组键，缺失主客体时退化为按 hash 分组
type dualPathPairKey struct {
	Subject     string
	Object      string
	MissingHash string
}

// rerankRelationItemsByPair 对齐 _rerank_relation_items_by_pair
func (r *DualPathRetriever) rerankRelationItemsByPair(results []RetrievalResult, pairLimit int) []RetrievalResult {
	if len(results) <= 1 {
		return results
	}
	type indexedRelation struct {
		Index int
		Item  RetrievalResult
	}
	relationPositions := make([]int, 0, len(results))
	relationItems := make([]indexedRelation, 0, len(results))
	for index, item := range results {
		if item.ResultType == "relation" {
			relationPositions = append(relationPositions, index)
			relationItems = append(relationItems, indexedRelation{Index: index, Item: item})
		}
	}
	if len(relationItems) <= 1 {
		return results
	}
	pairLimit = max(1, pairLimit)

	groupOrder := make([]dualPathPairKey, 0, len(relationItems))
	grouped := make(map[dualPathPairKey][]indexedRelation)
	for _, entry := range relationItems {
		metadata := entry.Item.Metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		subject := strings.ToLower(strings.TrimSpace(stringFromMap(metadata, "subject")))
		object := strings.ToLower(strings.TrimSpace(stringFromMap(metadata, "object")))
		key := dualPathPairKey{Subject: subject, Object: object}
		if subject == "" || object == "" {
			key = dualPathPairKey{MissingHash: entry.Item.HashValue}
		}
		if _, exists := grouped[key]; !exists {
			groupOrder = append(groupOrder, key)
		}
		grouped[key] = append(grouped[key], entry)
	}

	for key := range grouped {
		items := grouped[key]
		sort.SliceStable(items, func(left, right int) bool {
			if items[left].Item.Score != items[right].Item.Score {
				return items[left].Item.Score > items[right].Item.Score
			}
			return items[left].Index < items[right].Index
		})
		grouped[key] = items
	}
	orderedGroups := make([][]indexedRelation, 0, len(groupOrder))
	for _, key := range groupOrder {
		orderedGroups = append(orderedGroups, grouped[key])
	}
	sort.SliceStable(orderedGroups, func(left, right int) bool {
		leftFirst := orderedGroups[left][0]
		rightFirst := orderedGroups[right][0]
		if leftFirst.Item.Score != rightFirst.Item.Score {
			return leftFirst.Item.Score > rightFirst.Item.Score
		}
		return leftFirst.Index < rightFirst.Index
	})

	prioritized := make([]RetrievalResult, 0, len(relationItems))
	overflow := make([]RetrievalResult, 0, len(relationItems))
	for _, items := range orderedGroups {
		limit := pairLimit
		if limit > len(items) {
			limit = len(items)
		}
		for _, entry := range items[:limit] {
			prioritized = append(prioritized, entry.Item)
		}
		if limit < len(items) {
			for _, entry := range items[limit:] {
				overflow = append(overflow, entry.Item)
			}
		}
	}
	reorderedRelations := append(prioritized, overflow...)
	if len(reorderedRelations) != len(relationItems) {
		return results
	}
	zap.L().Debug(fmt.Sprintf(
		"relation_rerank_applied=1 relation_pair_groups=%d relation_pair_overflow_count=%d relation_pair_limit=%d",
		len(orderedGroups), len(overflow), pairLimit,
	))
	rebuilt := make([]RetrievalResult, len(results))
	copy(rebuilt, results)
	for index, slot := range relationPositions {
		rebuilt[slot] = reorderedRelations[index]
	}
	return rebuilt
}

// applyPosteriorGraphGate 包装后验图门控调用，统一 nil temporal 的传参
func (r *DualPathRetriever) applyPosteriorGraphGate(query string, baseResults []RetrievalResult, topK int, temporal *TemporalQueryOptions, relationIntent map[string]any, scope *RetrievalScope) []RetrievalResult {
	var temporalValue any
	if temporal != nil {
		temporalValue = temporal
	}
	return ApplyPosteriorGraphGate(r, query, baseResults, topK, temporalValue, relationIntent, scope)
}

// optionalFloat 把可选值转成指针，nil 或非法返回 nil
func optionalFloat(value any) *float64 {
	if value == nil {
		return nil
	}
	number, ok := numericValue(value)
	if !ok {
		return nil
	}
	return &number
}
