package retrieval

// A_memorix 双向量池图证据收集、旧向量合并与主流程，对齐 core/retrieval/dual_path.py 的 1917-2338 行

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"
)

// dualGraphEvidenceItem 图证据及落地后的加权分数
type dualGraphEvidenceItem struct {
	Evidence map[string]any
	Score    float64
}

// dualGraphEvidencePayload 单个段落的图证据聚合
type dualGraphEvidencePayload struct {
	Paragraph map[string]any
	Evidence  []dualGraphEvidenceItem
}

// collectDualGraphEvidence 对齐 _collect_dual_graph_evidence：图向量召回展开到支撑段落
func (r *DualPathRetriever) collectDualGraphEvidence(queryEmbedding []float32, topK int, temporal *TemporalQueryOptions, scope *RetrievalScope) ([]dualGraphEvidencePayload, error) {
	cfg := r.config.VectorPools
	var allowedGraphIDs map[string]struct{}
	if scope != nil {
		allowedGraphIDs = make(map[string]struct{}, len(scope.RelationIDs)+len(scope.EntityIDs))
		for hashValue := range scope.RelationIDs {
			allowedGraphIDs[graphVectorID("relation", hashValue)] = struct{}{}
		}
		for hashValue := range scope.EntityIDs {
			allowedGraphIDs[graphVectorID("entity", hashValue)] = struct{}{}
		}
	}
	graphIDs, graphScores, err := searchVectorStore(r.graphVectorStore, queryEmbedding, topK, allowedGraphIDs)
	if err != nil {
		return nil, err
	}

	parsedItems := make([]normalizedGraphItem, 0, len(graphIDs))
	relationHashes := []string{}
	entityHashes := []string{}
	limit := len(graphIDs)
	if len(graphScores) < limit {
		limit = len(graphScores)
	}
	for index := 0; index < limit; index++ {
		itemType, hashValue := parseGraphVectorID(graphIDs[index])
		if itemType != "relation" && itemType != "entity" {
			continue
		}
		if hashValue == "" {
			continue
		}
		parsedItems = append(parsedItems, normalizedGraphItem{
			ItemType:  itemType,
			HashValue: hashValue,
			RawScore:  graphScores[index],
		})
		if itemType == "relation" {
			relationHashes = append(relationHashes, hashValue)
		} else {
			entityHashes = append(entityHashes, hashValue)
		}
	}
	normalizedItems := normalizeGraphScoresByType(parsedItems)

	relationRows, err := r.metadataStore.GetRelationsByHashes(relationHashes, false)
	if err != nil {
		return nil, err
	}
	entityRows, err := r.metadataStore.GetEntitiesByHashes(entityHashes)
	if err != nil {
		return nil, err
	}
	relationParagraphs, err := r.metadataStore.GetParagraphsByRelationHashes(relationHashes)
	if err != nil {
		return nil, err
	}
	entityParagraphs, err := r.metadataStore.GetParagraphsByEntityHashes(entityHashes)
	if err != nil {
		return nil, err
	}

	type expandedEntry struct {
		Score     float64
		Order     int
		Paragraph map[string]any
		Evidence  map[string]any
	}
	expandedEntries := []expandedEntry{}
	maxExpand := max(1, max(cfg.RelationExpandPerHit, cfg.EntityExpandPerHit))
	for itemIndex, item := range normalizedItems {
		var evidenceScore float64
		var paragraphs []map[string]any
		var evidence map[string]any
		if item.ItemType == "relation" {
			relation, exists := relationRows[item.HashValue]
			if !exists || relation == nil {
				continue
			}
			evidenceScore = item.NormalizedScore * cfg.RelationEvidenceWeight
			paragraphs = relationParagraphs[item.HashValue]
			if len(paragraphs) > cfg.RelationExpandPerHit {
				paragraphs = paragraphs[:cfg.RelationExpandPerHit]
			}
			evidence = map[string]any{
				"type":             "relation",
				"hash":             item.HashValue,
				"subject":          relation["subject"],
				"predicate":        relation["predicate"],
				"object":           relation["object"],
				"source":           "graph_vector_relation",
				"raw_score":        item.RawScore,
				"normalized_score": item.NormalizedScore,
			}
		} else {
			entity, exists := entityRows[item.HashValue]
			if !exists || entity == nil {
				continue
			}
			evidenceScore = item.NormalizedScore * cfg.EntityEvidenceWeight
			paragraphs = entityParagraphs[item.HashValue]
			if len(paragraphs) > cfg.EntityExpandPerHit {
				paragraphs = paragraphs[:cfg.EntityExpandPerHit]
			}
			evidence = map[string]any{
				"type":             "entity",
				"hash":             item.HashValue,
				"name":             entity["name"],
				"source":           "graph_vector_entity",
				"raw_score":        item.RawScore,
				"normalized_score": item.NormalizedScore,
			}
		}
		for paragraphIndex, paragraph := range paragraphs {
			paragraphHash := strings.TrimSpace(stringFromMap(paragraph, "hash"))
			if paragraphHash == "" {
				continue
			}
			if scope != nil {
				if _, visible := scope.ParagraphIDs[paragraphHash]; !visible {
					continue
				}
			}
			if temporal != nil && !r.isTemporalMatch(paragraph, temporal) {
				continue
			}
			groundingFactor := graphEvidenceGroundingFactor(evidence, paragraph)
			groundedScore := evidenceScore * groundingFactor
			groundedEvidence := make(map[string]any, len(evidence)+1)
			for key, value := range evidence {
				groundedEvidence[key] = value
			}
			groundedEvidence["grounding_factor"] = groundingFactor
			order := itemIndex*maxExpand + paragraphIndex
			expandedEntries = append(expandedEntries, expandedEntry{
				Score:     groundedScore,
				Order:     order,
				Paragraph: paragraph,
				Evidence:  groundedEvidence,
			})
		}
	}

	sort.SliceStable(expandedEntries, func(left, right int) bool {
		if expandedEntries[left].Score != expandedEntries[right].Score {
			return expandedEntries[left].Score > expandedEntries[right].Score
		}
		return expandedEntries[left].Order < expandedEntries[right].Order
	})
	limit = min(cfg.GraphExpandParagraphK, len(expandedEntries))
	payloads := []dualGraphEvidencePayload{}
	indexByHash := map[string]int{}
	for _, entry := range expandedEntries[:limit] {
		paragraphHash := strings.TrimSpace(stringFromMap(entry.Paragraph, "hash"))
		if paragraphHash == "" {
			continue
		}
		index, exists := indexByHash[paragraphHash]
		if !exists {
			index = len(payloads)
			indexByHash[paragraphHash] = index
			payloads = append(payloads, dualGraphEvidencePayload{Paragraph: entry.Paragraph})
		}
		payloads[index].Evidence = append(payloads[index].Evidence, dualGraphEvidenceItem{
			Evidence: entry.Evidence,
			Score:    entry.Score,
		})
	}
	return payloads, nil
}

// mergeLegacyVectorCandidates 对齐 _merge_legacy_vector_candidates：旧只读向量视图只做补充
func (r *DualPathRetriever) mergeLegacyVectorCandidates(ctx context.Context, queryEmbedding []float32, candidates map[string]*RetrievalResult, order *[]string, topK int, temporal *TemporalQueryOptions, scope *RetrievalScope) error {
	if r.legacyVectorStore == nil {
		return nil
	}
	var allowed map[string]struct{}
	if scope != nil {
		allowed = make(map[string]struct{}, len(scope.ParagraphIDs)+len(scope.RelationIDs)+len(scope.EntityIDs))
		for hashValue := range scope.ParagraphIDs {
			allowed[hashValue] = struct{}{}
		}
		for hashValue := range scope.RelationIDs {
			allowed[hashValue] = struct{}{}
		}
		for hashValue := range scope.EntityIDs {
			allowed[hashValue] = struct{}{}
		}
	}
	legacyIDs, legacyScores, err := searchVectorStore(r.legacyVectorStore, queryEmbedding, max(1, topK), allowed)
	if err != nil {
		return err
	}
	if len(legacyIDs) == 0 {
		return nil
	}
	paragraphMap, err := r.metadataStore.GetParagraphsByHashes(legacyIDs)
	if err != nil {
		return err
	}
	relationMap, err := r.metadataStore.GetRelationsByHashes(legacyIDs, false)
	if err != nil {
		return err
	}
	entityMap, err := r.metadataStore.GetEntitiesByHashes(legacyIDs)
	if err != nil {
		return err
	}
	relationKeys := make([]string, 0, len(relationMap))
	seenRelationKey := map[string]struct{}{}
	for _, hashValue := range legacyIDs {
		if _, exists := relationMap[hashValue]; !exists {
			continue
		}
		if _, seen := seenRelationKey[hashValue]; seen {
			continue
		}
		seenRelationKey[hashValue] = struct{}{}
		relationKeys = append(relationKeys, hashValue)
	}
	relationParagraphs, err := r.metadataStore.GetParagraphsByRelationHashes(relationKeys)
	if err != nil {
		return err
	}
	entityKeys := make([]string, 0, len(entityMap))
	seenEntityKey := map[string]struct{}{}
	for _, hashValue := range legacyIDs {
		if _, exists := entityMap[hashValue]; !exists {
			continue
		}
		if _, seen := seenEntityKey[hashValue]; seen {
			continue
		}
		seenEntityKey[hashValue] = struct{}{}
		entityKeys = append(entityKeys, hashValue)
	}
	entityParagraphs, err := r.metadataStore.GetParagraphsByEntityHashes(entityKeys)
	if err != nil {
		return err
	}

	limit := len(legacyIDs)
	if len(legacyScores) < limit {
		limit = len(legacyScores)
	}
	for index := 0; index < limit; index++ {
		hashValue := legacyIDs[index]
		score := legacyScores[index]
		paragraph, hasParagraph := paragraphMap[hashValue]
		if hasParagraph && paragraph != nil {
			paragraphHash := stringFromMap(paragraph, "hash")
			if scope != nil {
				if _, visible := scope.ParagraphIDs[paragraphHash]; !visible {
					continue
				}
			}
			if _, exists := candidates[paragraphHash]; exists {
				continue
			}
			if temporal != nil && !r.isTemporalMatch(paragraph, temporal) {
				continue
			}
			r.addCandidateScore(candidates, order, paragraph, "semantic", score, "legacy_vector_view", temporal)
			continue
		}

		var support []map[string]any
		evidenceType := ""
		if _, exists := relationMap[hashValue]; exists {
			support = relationParagraphs[hashValue]
			evidenceType = "relation"
		} else if _, exists := entityMap[hashValue]; exists {
			support = entityParagraphs[hashValue]
			evidenceType = "entity"
		} else {
			continue
		}
		for _, supportParagraph := range support {
			paragraphHash := stringFromMap(supportParagraph, "hash")
			if paragraphHash == "" {
				continue
			}
			if _, exists := candidates[paragraphHash]; exists {
				continue
			}
			if scope != nil {
				if _, visible := scope.ParagraphIDs[paragraphHash]; !visible {
					continue
				}
			}
			if temporal != nil && !r.isTemporalMatch(supportParagraph, temporal) {
				continue
			}
			candidate := r.ensureParagraphCandidate(candidates, order, supportParagraph, temporal)
			markCandidateSource(candidate, "legacy_vector_view")
			clipScore := clipUnit(score)
			appendGraphEvidence(candidate, map[string]any{
				"type":             evidenceType,
				"hash":             hashValue,
				"source":           "legacy_vector_view",
				"raw_score":        score,
				"normalized_score": clipScore,
			}, clipScore)
		}
	}
	return nil
}

// candidateValues 按插入序返回候选对象
func candidateValues(candidates map[string]*RetrievalResult, order []string) []*RetrievalResult {
	values := make([]*RetrievalResult, 0, len(order))
	for _, hashValue := range order {
		if item, exists := candidates[hashValue]; exists {
			values = append(values, item)
		}
	}
	return values
}

// retrieveDualVectorPools 对齐 _retrieve_dual_vector_pools：语义、稀疏、图三通道融合
func (r *DualPathRetriever) retrieveDualVectorPools(ctx context.Context, query string, topK int, temporal *TemporalQueryOptions, relationIntent map[string]any, enablePPR bool, scope *RetrievalScope) ([]RetrievalResult, error) {
	if relationIntent == nil {
		relationIntent = map[string]any{}
	}
	semanticWeight, sparseWeight, graphWeight, graphTopK := r.dualPoolWeights(relationIntent)

	var queryEmbedding []float32
	embeddingOK := false
	if r.embeddingManager != nil {
		encoded, err := r.embeddingManager.EncodeOne(ctx, query, 0, false, true)
		if err != nil {
			zap.L().Warn(fmt.Sprintf("双向量池检索 embedding 生成失败，将尝试 sparse 回退: %v", err))
		} else {
			queryEmbedding = encoded
			embeddingOK = r.isEmbeddingReadyForVectorSearch(encoded, "dual_vector_pool")
		}
	}

	candidates := map[string]*RetrievalResult{}
	order := []string{}
	if embeddingOK && queryEmbedding != nil {
		paragraphTopK := r.expandTemporalCandidateK(max(topK, r.config.VectorPools.ParagraphTopK), temporal)
		paragraphIDs, paragraphScores, err := searchVectorStore(r.paragraphVectorStore, queryEmbedding, paragraphTopK, scopeParagraphIDs(scope))
		if err != nil {
			return nil, err
		}
		paragraphMap, err := r.metadataStore.GetParagraphsByHashes(paragraphIDs)
		if err != nil {
			return nil, err
		}
		limit := len(paragraphIDs)
		if len(paragraphScores) < limit {
			limit = len(paragraphScores)
		}
		for index := 0; index < limit; index++ {
			paragraph, exists := paragraphMap[paragraphIDs[index]]
			if !exists || paragraph == nil {
				continue
			}
			if temporal != nil && !r.isTemporalMatch(paragraph, temporal) {
				continue
			}
			r.addCandidateScore(candidates, &order, paragraph, "semantic", paragraphScores[index], "paragraph_vector_pool", temporal)
		}

		graphEvidence, err := r.collectDualGraphEvidence(queryEmbedding, r.expandTemporalCandidateK(graphTopK, temporal), temporal, scope)
		if err != nil {
			return nil, err
		}
		for _, payload := range graphEvidence {
			if payload.Paragraph == nil {
				continue
			}
			candidate := r.ensureParagraphCandidate(candidates, &order, payload.Paragraph, temporal)
			markCandidateSource(candidate, "graph_vector_pool")
			for _, evidenceItem := range payload.Evidence {
				appendGraphEvidence(candidate, evidenceItem.Evidence, evidenceItem.Score)
			}
			scoreMeta := candidateScoreMeta(candidate)
			scoreMeta["graph_evidence"] = aggregateGraphEvidenceScore(candidate)
		}

		if err := r.mergeLegacyVectorCandidates(ctx, queryEmbedding, candidates, &order, max(paragraphTopK, graphTopK), temporal, scope); err != nil {
			return nil, err
		}
	} else {
		zap.L().Warn("embedding 不可用，跳过双向量池向量召回")
	}

	sparseResults := []RetrievalResult{}
	if r.shouldUseSparse(embeddingOK, resultValues(candidateValues(candidates, order))) {
		results, err := r.searchParagraphsSparse(query, max(topK*2, r.config.Sparse.CandidateK), temporal, scope)
		if err != nil {
			return nil, err
		}
		sparseResults = results
	}
	for _, sparseItem := range sparseResults {
		wordCount := any(0)
		if sparseItem.Metadata != nil {
			if value, exists := sparseItem.Metadata["word_count"]; exists {
				wordCount = value
			}
		}
		paragraph := map[string]any{
			"hash":       sparseItem.HashValue,
			"content":    sparseItem.Content,
			"word_count": wordCount,
		}
		candidate := r.addCandidateScore(candidates, &order, paragraph, "sparse", sparseItem.Score, "paragraph_sparse", temporal)
		for key, value := range sparseItem.Metadata {
			if key == "score_breakdown" || key == "evidence_items" {
				continue
			}
			candidate.Metadata[key] = value
		}
	}

	results := candidateValues(candidates, order)
	configuredSemanticWeight := semanticWeight
	configuredSparseWeight := sparseWeight
	configuredGraphWeight := graphWeight
	semanticWeight, sparseWeight, graphWeight, graphReliability := r.calibrateDualPoolWeights(
		results, semanticWeight, sparseWeight, graphWeight, max(10, topK*2),
	)

	scoreMaps := map[string]map[string]float64{
		"semantic": {},
		"sparse":   {},
		"graph":    {},
	}
	for _, item := range results {
		scoreMeta := candidateScoreMeta(item)
		if value, exists := scoreMeta["semantic"]; exists {
			scoreMaps["semantic"][item.HashValue] = dualPathMetadataNumber(value)
		}
		if value, exists := scoreMeta["sparse"]; exists {
			scoreMaps["sparse"][item.HashValue] = dualPathMetadataNumber(value)
		}
		graphScore := dualPathMetadataNumber(scoreMeta["graph_evidence"])
		if _, exists := scoreMeta["graph_evidence"]; !exists {
			graphScore = aggregateGraphEvidenceScore(item)
		}
		if graphScore > 0.0 {
			scoreMaps["graph"][item.HashValue] = graphScore
		}
	}
	finalScores, calibratedScoreMaps, err := FuseScoreMaps(
		scoreMaps,
		map[string]float64{"semantic": semanticWeight, "sparse": sparseWeight, "graph": graphWeight},
		r.config.VectorPools.ScoreCalibrationMethod,
		r.config.VectorPools.ScoreCalibrationRRFK,
	)
	if err != nil {
		return nil, err
	}
	for _, item := range results {
		scoreMeta := candidateScoreMeta(item)
		semanticScore := dualPathMetadataNumber(scoreMeta["semantic"])
		sparseScore := dualPathMetadataNumber(scoreMeta["sparse"])
		graphScore := aggregateGraphEvidenceScore(item)
		if value, exists := scoreMeta["graph_evidence"]; exists {
			graphScore = dualPathMetadataNumber(value)
		}
		calibratedSemanticScore := calibratedScoreMaps["semantic"][item.HashValue]
		calibratedSparseScore := calibratedScoreMaps["sparse"][item.HashValue]
		calibratedGraphScore := calibratedScoreMaps["graph"][item.HashValue]
		finalScore := finalScores[item.HashValue]
		scoreMeta["semantic"] = semanticScore
		scoreMeta["sparse"] = sparseScore
		scoreMeta["graph_evidence"] = graphScore
		scoreMeta["calibrated_semantic"] = calibratedSemanticScore
		scoreMeta["calibrated_sparse"] = calibratedSparseScore
		scoreMeta["calibrated_graph"] = calibratedGraphScore
		scoreMeta["score_calibration_method"] = r.config.VectorPools.ScoreCalibrationMethod
		scoreMeta["semantic_weight"] = semanticWeight
		scoreMeta["sparse_weight"] = sparseWeight
		scoreMeta["graph_weight"] = graphWeight
		scoreMeta["configured_semantic_weight"] = configuredSemanticWeight
		scoreMeta["configured_sparse_weight"] = configuredSparseWeight
		scoreMeta["configured_graph_weight"] = configuredGraphWeight
		scoreMeta["graph_reliability"] = graphReliability.Score
		scoreMeta["graph_grounding_quality"] = graphReliability.GroundingQuality
		scoreMeta["graph_channel_agreement"] = graphReliability.ChannelAgreement
		scoreMeta["graph_support_coverage"] = graphReliability.SupportCoverage
		scoreMeta["graph_evidence_count"] = graphReliability.EvidenceCount
		scoreMeta["graph_grounded_relation_count"] = graphReliability.GroundedRelationCount
		scoreMeta["graph_relation_count"] = graphReliability.RelationCount
		scoreMeta["final"] = finalScore
		item.Score = finalScore
		item.Source = "dual_vector_pool"
	}
	sort.SliceStable(results, func(left, right int) bool { return results[left].Score > results[right].Score })

	output := make([]RetrievalResult, 0, len(results))
	for _, item := range results {
		output = append(output, *item)
	}
	if enablePPR {
		output = r.rerankWithPPR(ctx, output, query)
	}
	if temporal != nil {
		output = r.sortResultsWithTemporal(output, temporal)
	}
	output = r.applyPosteriorGraphGate(query, output, topK, temporal, relationIntent, scope)
	return sliceTopK(output, topK), nil
}

// resultValues 把指针候选转成值切片，供 sparse 判定使用
func resultValues(candidates []*RetrievalResult) []RetrievalResult {
	results := make([]RetrievalResult, 0, len(candidates))
	for _, item := range candidates {
		results = append(results, *item)
	}
	return results
}
