package retrieval

// A_memorix 双路检索的召回构建与融合，对齐 core/retrieval/dual_path.py 的 648-1079 行

import (
	"sort"
	"strings"

	"go.uber.org/zap"
)

// searchVectorStore 对齐 _search_vector_store，allowed_ids 为 nil 时不过滤
func searchVectorStore(store VectorSearcher, queryEmbedding []float32, k int, allowedIDs map[string]struct{}) ([]string, []float64, error) {
	if store == nil {
		return []string{}, []float64{}, nil
	}
	var allowed []string
	if allowedIDs != nil {
		allowed = make([]string, 0, len(allowedIDs))
		for hashValue := range allowedIDs {
			allowed = append(allowed, hashValue)
		}
	}
	return store.Search(queryEmbedding, k, false, allowed)
}

// extractGraphSeedEntities 对齐 _extract_graph_seed_entities
func (r *DualPathRetriever) extractGraphSeedEntities(query string, limit int) []string {
	entities := r.extractEntities(query)
	if len(entities) == 0 {
		return []string{}
	}
	type entityWeight struct {
		Name   string
		Weight float64
	}
	ranked := make([]entityWeight, 0, len(entities))
	for name, weight := range entities {
		ranked = append(ranked, entityWeight{Name: name, Weight: weight})
	}
	sort.SliceStable(ranked, func(left, right int) bool {
		a, b := ranked[left], ranked[right]
		if a.Weight != b.Weight {
			return a.Weight > b.Weight
		}
		if len([]rune(a.Name)) != len([]rune(b.Name)) {
			return len([]rune(a.Name)) > len([]rune(b.Name))
		}
		return lowerText(a.Name) < lowerText(b.Name)
	})
	limit = max(0, limit)
	if limit < len(ranked) {
		ranked = ranked[:limit]
	}
	names := make([]string, 0, len(ranked))
	for _, item := range ranked {
		names = append(names, item.Name)
	}
	return names
}

// searchRelationsGraph 对齐 _search_relations_graph
func (r *DualPathRetriever) searchRelationsGraph(query string, temporal *TemporalQueryOptions, scope *RetrievalScope) []RetrievalResult {
	if r.graphRelationRecall == nil || !r.config.GraphRecall.Enabled {
		return []RetrievalResult{}
	}
	seedEntities := r.extractGraphSeedEntities(query, 2)
	if len(seedEntities) == 0 {
		return []RetrievalResult{}
	}
	var payloads []GraphRelationCandidate
	if scope != nil {
		payloads = r.graphRelationRecall.Recall(seedEntities, scope.RelationIDs)
	} else {
		payloads = r.graphRelationRecall.Recall(seedEntities, nil)
	}
	results := make([]RetrievalResult, 0, len(payloads))
	for _, payload := range payloads {
		metadata := payload.ToPayload()
		seedCopy := make([]string, len(payload.GraphSeedEntities))
		copy(seedCopy, payload.GraphSeedEntities)
		results = append(results, RetrievalResult{
			HashValue:  payload.HashValue,
			Content:    payload.Subject + " " + payload.Predicate + " " + payload.Object,
			Score:      0.0,
			ResultType: "relation",
			Source:     "graph_relation_recall",
			Metadata: map[string]any{
				"subject":                    metadata["subject"],
				"predicate":                  metadata["predicate"],
				"object":                     metadata["object"],
				"confidence":                 payload.Confidence,
				"graph_seed_entities":        seedCopy,
				"graph_hops":                 payload.GraphHops,
				"graph_candidate_type":       payload.GraphCandidateType,
				"supporting_paragraph_count": payload.SupportingParagraphCount,
			},
		})
	}
	return r.applyTemporalFilterToRelations(results, temporal)
}

// buildParagraphResultsFromIDs 按向量或稀疏召回顺序批量回表构造段落结果
func (r *DualPathRetriever) buildParagraphResultsFromIDs(hashValues []string, scores []float64, source string, temporal *TemporalQueryOptions, scope *RetrievalScope) ([]RetrievalResult, error) {
	paragraphMap, err := r.metadataStore.GetParagraphsByHashes(hashValues)
	if err != nil {
		return nil, err
	}
	results := make([]RetrievalResult, 0, len(hashValues))
	seen := make(map[string]struct{}, len(hashValues))
	limit := len(hashValues)
	if len(scores) < limit {
		limit = len(scores)
	}
	for index := 0; index < limit; index++ {
		hashValue := hashValues[index]
		if scope != nil {
			if _, visible := scope.ParagraphIDs[hashValue]; !visible {
				continue
			}
		}
		if _, exists := seen[hashValue]; exists {
			continue
		}
		paragraph, exists := paragraphMap[hashValue]
		if !exists {
			continue
		}
		if temporal != nil && !r.isTemporalMatch(paragraph, temporal) {
			continue
		}
		seen[hashValue] = struct{}{}
		wordCount := any(0)
		if value, exists := paragraph["word_count"]; exists {
			wordCount = value
		}
		results = append(results, RetrievalResult{
			HashValue:  hashValue,
			Content:    stringFromMap(paragraph, "content"),
			Score:      scores[index],
			ResultType: "paragraph",
			Source:     source,
			Metadata: map[string]any{
				"word_count": wordCount,
				"time_meta":  r.buildTimeMetaFromParagraph(paragraph, temporal),
			},
		})
	}
	if temporal != nil {
		return r.sortResultsWithTemporal(results, temporal), nil
	}
	return results, nil
}

// buildRelationResultsFromIDs 按向量或稀疏召回顺序批量回表构造关系结果
func (r *DualPathRetriever) buildRelationResultsFromIDs(hashValues []string, scores []float64, source string, temporal *TemporalQueryOptions, extraMetadataByHash map[string]map[string]any, scope *RetrievalScope) ([]RetrievalResult, error) {
	relationMap, err := r.metadataStore.GetRelationsByHashes(hashValues, false)
	if err != nil {
		return nil, err
	}
	supportingTimeMeta := map[string]map[string]any{}
	if temporal != nil {
		mapKeys := make([]string, 0, len(relationMap))
		for hashValue := range relationMap {
			mapKeys = append(mapKeys, hashValue)
		}
		supportingTimeMeta = r.bestSupportingTimeMetaBatch(mapKeys, temporal)
	}

	results := make([]RetrievalResult, 0, len(hashValues))
	seen := make(map[string]struct{}, len(hashValues))
	limit := len(hashValues)
	if len(scores) < limit {
		limit = len(scores)
	}
	for index := 0; index < limit; index++ {
		hashValue := hashValues[index]
		if _, exists := seen[hashValue]; exists {
			continue
		}
		relation, exists := relationMap[hashValue]
		if !exists {
			continue
		}
		if scope != nil {
			if _, visible := scope.RelationIDs[hashValue]; !visible {
				continue
			}
		}
		var relationTimeMeta map[string]any
		if temporal != nil {
			relationTimeMeta = supportingTimeMeta[hashValue]
			if relationTimeMeta == nil {
				continue
			}
		}
		seen[hashValue] = struct{}{}
		var confidence any = 1.0
		if value, exists := relation["confidence"]; exists {
			confidence = value
		}
		metadata := map[string]any{
			"subject":    relation["subject"],
			"predicate":  relation["predicate"],
			"object":     relation["object"],
			"confidence": confidence,
			"time_meta":  relationTimeMeta,
		}
		if extraMetadataByHash != nil {
			for key, value := range extraMetadataByHash[hashValue] {
				metadata[key] = value
			}
		}
		results = append(results, RetrievalResult{
			HashValue:  hashValue,
			Content:    stringFromMap(relation, "subject") + " " + stringFromMap(relation, "predicate") + " " + stringFromMap(relation, "object"),
			Score:      scores[index],
			ResultType: "relation",
			Source:     source,
			Metadata:   metadata,
		})
	}
	if temporal != nil {
		return r.sortResultsWithTemporal(results, temporal), nil
	}
	return results, nil
}

// fuseRankedListsWeightedRRF 按 weighted RRF 融合两路段段落召回
func (r *DualPathRetriever) fuseRankedListsWeightedRRF(vectorResults []RetrievalResult, sparseResults []RetrievalResult) []RetrievalResult {
	if len(vectorResults) == 0 {
		out := cloneResultSlice(sparseResults)
		if r.config.Fusion.NormalizeScore {
			r.normalizeScoresMinmax(out)
		}
		return out
	}
	if len(sparseResults) == 0 {
		out := cloneResultSlice(vectorResults)
		if r.config.Fusion.NormalizeScore {
			r.normalizeScoresMinmax(out)
		}
		return out
	}
	rrfK := r.config.Fusion.RRFK
	vectorWeight := r.config.Fusion.VectorWeight
	sparseWeight := r.config.Fusion.BM25Weight

	merged := map[string]RetrievalResult{}
	order := make([]string, 0, len(vectorResults)+len(sparseResults))
	scoreMap := map[string]float64{}
	for index, item := range vectorResults {
		rank := index + 1
		hashValue := item.HashValue
		if _, exists := merged[hashValue]; !exists {
			cloned := cloneRetrievalResult(item)
			cloned.Source = "fusion_rrf"
			merged[hashValue] = cloned
			order = append(order, hashValue)
		}
		scoreMap[hashValue] += vectorWeight * (1.0 / float64(rrfK+rank))
	}
	for index, item := range sparseResults {
		rank := index + 1
		hashValue := item.HashValue
		if _, exists := merged[hashValue]; !exists {
			cloned := cloneRetrievalResult(item)
			cloned.Source = "fusion_rrf"
			merged[hashValue] = cloned
			order = append(order, hashValue)
		}
		scoreMap[hashValue] += sparseWeight * (1.0 / float64(rrfK+rank))
	}
	out := make([]RetrievalResult, 0, len(order))
	for _, hashValue := range order {
		item := merged[hashValue]
		item.Score = scoreMap[hashValue]
		out = append(out, item)
	}
	sort.SliceStable(out, func(left, right int) bool { return out[left].Score > out[right].Score })
	if r.config.Fusion.NormalizeScore && r.config.Fusion.NormalizeMethod == "minmax" {
		r.normalizeScoresMinmax(out)
	}
	return out
}

// searchParagraphsSparse 对齐 _search_paragraphs_sparse
func (r *DualPathRetriever) searchParagraphsSparse(query string, topK int, temporal *TemporalQueryOptions, scope *RetrievalScope) ([]RetrievalResult, error) {
	if r.sparseIndex == nil || !r.config.Sparse.Enabled {
		return []RetrievalResult{}, nil
	}
	candidateK := max(topK, r.config.Sparse.CandidateK)
	candidateK = r.capTemporalScanK(candidateK, temporal)
	var rows []map[string]any
	if scope != nil {
		rows = r.sparseIndex.Search(query, candidateK, setToSlice(scope.ParagraphIDs))
	} else {
		rows = r.sparseIndex.Search(query, candidateK, nil)
	}
	rows = r.filterSparseParagraphRows(rows)
	hashValues := make([]string, 0, len(rows))
	scores := make([]float64, 0, len(rows))
	bm25Scores := make(map[string]float64, len(rows))
	for _, row := range rows {
		hashValue := stringFromMap(row, "hash")
		hashValues = append(hashValues, hashValue)
		score, _ := numericValue(row["score"])
		scores = append(scores, score)
		bm25Score, _ := numericValue(row["bm25_score"])
		bm25Scores[hashValue] = bm25Score
	}
	results, err := r.buildParagraphResultsFromIDs(hashValues, scores, "sparse_bm25", temporal, scope)
	if err != nil {
		return nil, err
	}
	for index := range results {
		results[index].Metadata["bm25_score"] = bm25Scores[results[index].HashValue]
	}
	if r.config.Fusion.NormalizeScore && r.config.Fusion.NormalizeMethod == "minmax" {
		r.normalizeScoresMinmax(results)
	}
	return results, nil
}

// filterSparseParagraphRows 过滤段落 sparse 尾部弱命中
func (r *DualPathRetriever) filterSparseParagraphRows(rows []map[string]any) []map[string]any {
	if len(rows) <= 2 {
		return rows
	}
	topScore, _ := numericValue(rows[0]["score"])
	if topScore < 0 {
		topScore = 0
	}
	if topScore <= 0 {
		return rows[:2]
	}
	relativeFloor := topScore * 0.2
	filteredRows := make([]map[string]any, 0, len(rows))
	removedCount := 0
	for index, row := range rows {
		if index < 2 {
			filteredRows = append(filteredRows, row)
			continue
		}
		rawScore, _ := numericValue(row["score"])
		matchedTokenCount := dualPathIntValue(row["matched_token_count"])
		matchedTokenRatio, _ := numericValue(row["matched_token_ratio"])
		if rawScore >= relativeFloor || matchedTokenCount >= 3 || (matchedTokenCount >= 2 && matchedTokenRatio >= 0.12) {
			filteredRows = append(filteredRows, row)
			continue
		}
		removedCount++
	}
	if removedCount > 0 {
		zap.L().Debug("sparse_paragraph_tail_pruned=1 " + "removed_count=" + dualPathItoa(removedCount) + " kept_count=" + dualPathItoa(len(filteredRows)))
	}
	return filteredRows
}

// searchRelationsSparse 对齐 _search_relations_sparse
func (r *DualPathRetriever) searchRelationsSparse(query string, topK int, temporal *TemporalQueryOptions, scope *RetrievalScope) ([]RetrievalResult, error) {
	if r.sparseIndex == nil || !r.config.Sparse.Enabled {
		return []RetrievalResult{}, nil
	}
	if !r.config.Sparse.EnableRelationSparseFallback {
		return []RetrievalResult{}, nil
	}
	candidateK := max(topK, r.config.Sparse.RelationCandidateK)
	candidateK = r.capTemporalScanK(candidateK, temporal)
	var rows []map[string]any
	if scope != nil {
		rows = r.sparseIndex.SearchRelations(query, candidateK, setToSlice(scope.RelationIDs))
	} else {
		rows = r.sparseIndex.SearchRelations(query, candidateK, nil)
	}
	hashValues := make([]string, 0, len(rows))
	scores := make([]float64, 0, len(rows))
	bm25Scores := make(map[string]float64, len(rows))
	for _, row := range rows {
		hashValue := stringFromMap(row, "hash")
		hashValues = append(hashValues, hashValue)
		score, _ := numericValue(row["score"])
		scores = append(scores, score)
		bm25Score, _ := numericValue(row["bm25_score"])
		bm25Scores[hashValue] = bm25Score
	}
	results, err := r.buildRelationResultsFromIDs(hashValues, scores, "sparse_relation_bm25", temporal, nil, scope)
	if err != nil {
		return nil, err
	}
	for index := range results {
		results[index].Metadata["bm25_score"] = bm25Scores[results[index].HashValue]
	}
	if r.config.Fusion.NormalizeScore && r.config.Fusion.NormalizeMethod == "minmax" {
		r.normalizeScoresMinmax(results)
	}
	return results, nil
}

// mergeRelationResults 合并关系候选，按 hash 去重并保留更高分
func (r *DualPathRetriever) mergeRelationResults(vectorResults []RetrievalResult, sparseResults []RetrievalResult) []RetrievalResult {
	merged := map[string]RetrievalResult{}
	order := make([]string, 0, len(vectorResults)+len(sparseResults))
	for _, item := range vectorResults {
		if _, exists := merged[item.HashValue]; !exists {
			order = append(order, item.HashValue)
		}
		merged[item.HashValue] = item
	}
	for _, item := range sparseResults {
		old, exists := merged[item.HashValue]
		if !exists {
			merged[item.HashValue] = item
			order = append(order, item.HashValue)
		} else if item.Score > old.Score {
			merged[item.HashValue] = item
		} else if old.Source != item.Source {
			old.Source = "relation_fusion"
			merged[item.HashValue] = old
		}
	}
	out := make([]RetrievalResult, 0, len(order))
	for _, hashValue := range order {
		out = append(out, merged[hashValue])
	}
	sort.SliceStable(out, func(left, right int) bool { return out[left].Score > out[right].Score })
	return out
}

// mergeRelationResultsGraphEnhanced 融合语义、图结构和证据评分的图感知关系结果
func (r *DualPathRetriever) mergeRelationResultsGraphEnhanced(vectorResults []RetrievalResult, sparseResults []RetrievalResult, graphResults []RetrievalResult) []RetrievalResult {
	vectorNorm := r.buildMinmaxScoreMap(vectorResults)
	sparseNorm := r.buildMinmaxScoreMap(sparseResults)
	graphScoreMap := map[string]float64{
		"direct_pair":  1.0,
		"one_hop_seed": 0.75,
		"two_hop_pair": 0.55,
	}

	merged := map[string]RetrievalResult{}
	order := make([]string, 0, len(vectorResults)+len(sparseResults)+len(graphResults))
	sourceSets := map[string]map[string]struct{}{}
	for _, group := range [][]RetrievalResult{vectorResults, sparseResults, graphResults} {
		for _, item := range group {
			existing, exists := merged[item.HashValue]
			if !exists {
				existing = cloneRetrievalResult(item)
				merged[item.HashValue] = existing
				order = append(order, item.HashValue)
			} else {
				if existing.Metadata == nil {
					existing.Metadata = map[string]any{}
				}
				for key, value := range coerceMetadataDict(item.Metadata) {
					current, present := existing.Metadata[key]
					if !present || dualPathMetadataPlaceholder(current) {
						existing.Metadata[key] = value
					}
				}
				merged[item.HashValue] = existing
			}
			sourceSet, exists := sourceSets[item.HashValue]
			if !exists {
				sourceSet = map[string]struct{}{}
				sourceSets[item.HashValue] = sourceSet
			}
			sourceText := strings.TrimSpace(item.Source)
			if sourceText == "" {
				sourceText = "relation_search"
			}
			sourceSet[sourceText] = struct{}{}
		}
	}

	out := make([]RetrievalResult, 0, len(order))
	for _, hashValue := range order {
		out = append(out, merged[hashValue])
	}

	missingSupportHashes := make([]string, 0, len(out))
	for _, item := range out {
		metadata := coerceMetadataDict(item.Metadata)
		if value, exists := metadata["supporting_paragraph_count"]; !exists || value == nil {
			missingSupportHashes = append(missingSupportHashes, item.HashValue)
		}
	}
	supportCounts := map[string]int{}
	if len(missingSupportHashes) > 0 {
		paragraphsByRelation, err := r.metadataStore.GetParagraphsByRelationHashes(missingSupportHashes)
		if err != nil {
			zap.L().Warn("查询关系支撑段落失败", zap.Error(err))
		} else {
			for hashValue, paragraphs := range paragraphsByRelation {
				supportCounts[hashValue] = len(paragraphs)
			}
		}
	}
	supportCache := map[string]int{}
	for index := range out {
		item := &out[index]
		if item.Metadata == nil {
			item.Metadata = map[string]any{}
		}
		metadata := item.Metadata
		semanticNorm := vectorNorm[item.HashValue]
		if sparseNorm[item.HashValue] > semanticNorm {
			semanticNorm = sparseNorm[item.HashValue]
		}
		graphCandidateType := stringValue(metadata["graph_candidate_type"])
		graphScore := graphScoreMap[graphCandidateType]

		cached, cacheExists := supportCache[item.HashValue]
		if !cacheExists {
			rawSupport, hasSupport := metadata["supporting_paragraph_count"]
			if !hasSupport || rawSupport == nil {
				cached = supportCounts[item.HashValue]
			} else {
				cached = max(0, dualPathIntValue(rawSupport))
			}
			supportCache[item.HashValue] = cached
		}
		supportingParagraphCount := cached
		evidenceScore := 1.0
		if float64(supportingParagraphCount)/3.0 < 1.0 {
			evidenceScore = float64(supportingParagraphCount) / 3.0
		}

		metadata["supporting_paragraph_count"] = supportingParagraphCount
		metadata["graph_seed_entities"] = dualPathStringList(metadata["graph_seed_entities"])
		if value, exists := metadata["graph_hops"]; exists {
			metadata["graph_hops"] = dualPathIntValue(value)
		}
		item.Score = 0.60*semanticNorm + 0.30*graphScore + 0.10*evidenceScore

		sources := sourceSets[item.HashValue]
		if len(sources) > 1 {
			item.Source = "relation_fusion"
		} else if len(sources) == 1 {
			for sourceText := range sources {
				item.Source = sourceText
			}
		}
	}
	sort.SliceStable(out, func(left, right int) bool { return out[left].Score > out[right].Score })
	return out
}

// coerceMetadataDict 对齐 coerce_metadata_dict：非字典返回空字典
func coerceMetadataDict(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

// dualPathMetadataPlaceholder 判断合并时是否应被新值覆盖
func dualPathMetadataPlaceholder(value any) bool {
	if value == nil {
		return true
	}
	if text, ok := value.(string); ok {
		return text == ""
	}
	if values, ok := value.([]any); ok {
		return len(values) == 0
	}
	if values, ok := value.([]string); ok {
		return len(values) == 0
	}
	return false
}

// dualPathStringList 把元数据值转成字符串切片，nil 或空值返回空切片
func dualPathStringList(value any) []string {
	switch typed := value.(type) {
	case nil:
		return []string{}
	case []string:
		out := make([]string, len(typed))
		copy(out, typed)
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, stringValue(item))
		}
		return out
	case string:
		if typed == "" {
			return []string{}
		}
		return []string{typed}
	}
	return []string{}
}

// cloneResultSlice 复制结果切片，避免融合时修改调用方数据
func cloneResultSlice(results []RetrievalResult) []RetrievalResult {
	out := make([]RetrievalResult, 0, len(results))
	for _, item := range results {
		out = append(out, cloneRetrievalResult(item))
	}
	return out
}

// MergeRelationResultsGraphEnhanced 导出图增强关系合并，满足 PosteriorGraphRetriever
func (r *DualPathRetriever) MergeRelationResultsGraphEnhanced(vectorResults []RetrievalResult, sparseResults []RetrievalResult, graphResults []RetrievalResult) []RetrievalResult {
	return r.mergeRelationResultsGraphEnhanced(vectorResults, sparseResults, graphResults)
}
