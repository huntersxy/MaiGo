package retrieval

// A_memorix 双向量池候选构造、图证据可信度与权重校准，对齐 core/retrieval/dual_path.py 的 1576-1915 行

import (
	"math"
	"sort"
	"strings"
)

// dualPoolWeights 对齐 _dual_pool_weights：关系意图开启时改用关系意图权重
func (r *DualPathRetriever) dualPoolWeights(relationIntent map[string]any) (float64, float64, float64, int) {
	cfg := r.config.VectorPools
	if dualPathBool(relationIntent["enabled"], false) {
		return cfg.RelationIntentSemanticWeight, cfg.RelationIntentSparseWeight,
			cfg.RelationIntentGraphWeight, cfg.RelationIntentGraphTopK
	}
	return cfg.SemanticWeight, cfg.SparseWeight, cfg.GraphWeight, cfg.GraphTopK
}

// ensureParagraphCandidate 对齐 _ensure_paragraph_candidate：按 hash 建候选并保留首次插入序
func (r *DualPathRetriever) ensureParagraphCandidate(candidates map[string]*RetrievalResult, order *[]string, paragraph map[string]any, temporal *TemporalQueryOptions) *RetrievalResult {
	paragraphHash := strings.TrimSpace(stringFromMap(paragraph, "hash"))
	if item, exists := candidates[paragraphHash]; exists {
		return item
	}
	wordCount := any(0)
	if value, exists := paragraph["word_count"]; exists {
		wordCount = value
	}
	metadata := map[string]any{
		"word_count":      wordCount,
		"score_breakdown": map[string]any{},
		"evidence_items":  []any{},
	}
	if temporal != nil {
		metadata["time_meta"] = r.buildTimeMetaFromParagraph(paragraph, temporal)
	}
	item := &RetrievalResult{
		HashValue:  paragraphHash,
		Content:    stringFromMap(paragraph, "content"),
		Score:      0.0,
		ResultType: "paragraph",
		Source:     "dual_vector_pool",
		Metadata:   metadata,
	}
	candidates[paragraphHash] = item
	*order = append(*order, paragraphHash)
	return item
}

// candidateScoreMeta 对齐 _candidate_score_meta：确保 metadata 与 score_breakdown 存在
func candidateScoreMeta(candidate *RetrievalResult) map[string]any {
	if candidate.Metadata == nil {
		candidate.Metadata = map[string]any{}
	}
	scoreMeta, ok := candidate.Metadata["score_breakdown"].(map[string]any)
	if !ok || scoreMeta == nil {
		scoreMeta = map[string]any{}
		candidate.Metadata["score_breakdown"] = scoreMeta
	}
	return scoreMeta
}

// candidateEvidenceItems 对齐 _candidate_evidence_items：确保 evidence_items 为列表
func candidateEvidenceItems(candidate *RetrievalResult) []any {
	if candidate.Metadata == nil {
		candidate.Metadata = map[string]any{}
	}
	switch typed := candidate.Metadata["evidence_items"].(type) {
	case []any:
		return typed
	case []map[string]any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		candidate.Metadata["evidence_items"] = items
		return items
	default:
		items := []any{}
		candidate.Metadata["evidence_items"] = items
		return items
	}
}

// markCandidateSource 对齐 _mark_candidate_source：记录候选来源，去重保序
func markCandidateSource(candidate *RetrievalResult, source string) {
	if candidate.Metadata == nil {
		candidate.Metadata = map[string]any{}
	}
	sources := dualPathStringList(candidate.Metadata["dual_pool_sources"])
	for _, existing := range sources {
		if existing == source {
			candidate.Metadata["dual_pool_sources"] = sources
			return
		}
	}
	sources = append(sources, source)
	candidate.Metadata["dual_pool_sources"] = sources
}

// dualPathMetadataNumber 把元数据值转成浮点，缺失或非法按 0 处理
func dualPathMetadataNumber(value any) float64 {
	if number, ok := numericValue(value); ok {
		return number
	}
	if number, ok := dualPathFloatValue(value); ok {
		return number
	}
	return 0.0
}

// addCandidateScore 对齐 _add_candidate_score：同一来源分数取最大值
func (r *DualPathRetriever) addCandidateScore(candidates map[string]*RetrievalResult, order *[]string, paragraph map[string]any, scoreKey string, score float64, source string, temporal *TemporalQueryOptions) *RetrievalResult {
	item := r.ensureParagraphCandidate(candidates, order, paragraph, temporal)
	if temporal != nil {
		item.Metadata["time_meta"] = r.buildTimeMetaFromParagraph(paragraph, temporal)
	}
	scoreMeta := candidateScoreMeta(item)
	oldScore := dualPathMetadataNumber(scoreMeta[scoreKey])
	scoreMeta[scoreKey] = math.Max(oldScore, score)
	markCandidateSource(item, source)
	return item
}

// appendGraphEvidence 对齐 _append_graph_evidence：同类型同 hash 只保留更高分证据
func appendGraphEvidence(candidate *RetrievalResult, evidence map[string]any, score float64) {
	items := candidateEvidenceItems(candidate)
	payload := make(map[string]any, len(evidence)+1)
	for key, value := range evidence {
		payload[key] = value
	}
	payload["score"] = score
	payloadType := stringValue(payload["type"])
	payloadHash := stringValue(payload["hash"])
	for _, rawItem := range items {
		old, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		if stringValue(old["type"]) == payloadType && stringValue(old["hash"]) == payloadHash {
			if score > dualPathMetadataNumber(old["score"]) {
				for key, value := range payload {
					old[key] = value
				}
			}
			return
		}
	}
	candidate.Metadata["evidence_items"] = append(items, payload)
}

// aggregateGraphEvidenceScore 对齐 _aggregate_graph_evidence_score
func aggregateGraphEvidenceScore(candidate *RetrievalResult) float64 {
	items := candidateEvidenceItems(candidate)
	relationScores := []float64{}
	entityScores := []float64{}
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		switch stringValue(item["type"]) {
		case "relation":
			relationScores = append(relationScores, dualPathMetadataNumber(item["score"]))
		case "entity":
			entityScores = append(entityScores, dualPathMetadataNumber(item["score"]))
		}
	}
	sort.SliceStable(relationScores, func(left, right int) bool { return relationScores[left] > relationScores[right] })
	sort.SliceStable(entityScores, func(left, right int) bool { return entityScores[left] > entityScores[right] })
	graphScore := 0.0
	if len(relationScores) > 0 {
		graphScore += relationScores[0]
		for _, score := range relationScores[1:] {
			graphScore += 0.35 * score
		}
	}
	for _, score := range entityScores {
		graphScore += 0.20 * score
	}
	return math.Min(1.0, graphScore)
}

// clipUnit 对齐 _clip_unit
func clipUnit(value float64) float64 {
	return math.Min(1.0, math.Max(0.0, value))
}

// estimateGraphReliability 对齐 _estimate_graph_reliability
func (r *DualPathRetriever) estimateGraphReliability(candidates []*RetrievalResult, scanLimit int) graphReliabilityEstimate {
	type graphCandidate struct {
		Score     float64
		Candidate *RetrievalResult
	}
	ranked := []graphCandidate{}
	bestIndependentScore := 0.0
	for _, candidate := range candidates {
		scoreMeta := candidateScoreMeta(candidate)
		semanticScore := math.Max(0.0, dualPathMetadataNumber(scoreMeta["semantic"]))
		sparseScore := math.Max(0.0, dualPathMetadataNumber(scoreMeta["sparse"]))
		bestIndependentScore = math.Max(bestIndependentScore, math.Max(semanticScore, sparseScore))
		graphScore := 0.0
		if rawGraphScore, exists := scoreMeta["graph_evidence"]; exists {
			graphScore = math.Max(0.0, dualPathMetadataNumber(rawGraphScore))
		} else {
			graphScore = math.Max(0.0, aggregateGraphEvidenceScore(candidate))
		}
		if graphScore > 0.0 {
			ranked = append(ranked, graphCandidate{Score: graphScore, Candidate: candidate})
		}
	}
	sort.SliceStable(ranked, func(left, right int) bool { return ranked[left].Score > ranked[right].Score })
	limit := max(1, scanLimit)
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	if len(ranked) == 0 {
		return graphReliabilityEstimate{}
	}

	graphScoreTotal := 0.0
	for _, item := range ranked {
		graphScoreTotal += item.Score
	}
	agreementTotal := 0.0
	groundingWeightTotal := 0.0
	groundingQualityTotal := 0.0
	evidenceCount := 0
	groundedRelationHashes := map[string]struct{}{}
	relationHashes := map[string]struct{}{}

	for _, item := range ranked {
		scoreMeta := candidateScoreMeta(item.Candidate)
		independentScore := math.Max(0.0, math.Max(
			dualPathMetadataNumber(scoreMeta["semantic"]),
			dualPathMetadataNumber(scoreMeta["sparse"]),
		))
		if bestIndependentScore > 0.0 {
			agreementTotal += item.Score * clipUnit(independentScore/bestIndependentScore)
		}
		for _, rawEvidence := range candidateEvidenceItems(item.Candidate) {
			evidence, ok := rawEvidence.(map[string]any)
			if !ok {
				continue
			}
			evidenceType := strings.ToLower(strings.TrimSpace(stringValue(evidence["type"])))
			if evidenceType != "relation" && evidenceType != "entity" {
				continue
			}
			normalizedScore := math.Max(0.0, dualPathMetadataNumber(evidence["normalized_score"]))
			if normalizedScore <= 0.0 {
				continue
			}
			groundingFactor := clipUnit(dualPathMetadataNumber(evidence["grounding_factor"]))
			var normalizedGrounding float64
			if evidenceType == "relation" {
				normalizedGrounding = clipUnit((groundingFactor - relationUngroundedFactor) / (1.0 - relationUngroundedFactor))
				relationHash := strings.TrimSpace(stringValue(evidence["hash"]))
				if relationHash != "" {
					relationHashes[relationHash] = struct{}{}
					if groundingFactor >= relationBothEntitiesGroundingFactor {
						groundedRelationHashes[relationHash] = struct{}{}
					}
				}
			} else {
				normalizedGrounding = 0.35 * clipUnit((groundingFactor-entityUngroundedFactor)/(1.0-entityUngroundedFactor))
			}
			groundingWeightTotal += normalizedScore
			groundingQualityTotal += normalizedScore * normalizedGrounding
			evidenceCount++
		}
	}

	groundingQuality := 0.0
	if groundingWeightTotal > 0.0 {
		groundingQuality = groundingQualityTotal / groundingWeightTotal
	}
	channelAgreement := 0.0
	if graphScoreTotal > 0.0 {
		channelAgreement = agreementTotal / graphScoreTotal
	}
	supportQuantity := clipUnit(float64(len(groundedRelationHashes)) / graphReliabilitySupportTarget)
	supportPrecision := 0.0
	if len(relationHashes) > 0 {
		supportPrecision = float64(len(groundedRelationHashes)) / float64(len(relationHashes))
	}
	supportCoverage := clipUnit(supportQuantity * supportPrecision)
	reliability := clipUnit(
		graphReliabilityGroundingWeight*groundingQuality +
			graphReliabilityAgreementWeight*channelAgreement +
			graphReliabilitySupportWeight*supportCoverage,
	)
	return graphReliabilityEstimate{
		Score:                 reliability,
		GroundingQuality:      groundingQuality,
		ChannelAgreement:      channelAgreement,
		SupportCoverage:       supportCoverage,
		EvidenceCount:         evidenceCount,
		GroundedRelationCount: len(groundedRelationHashes),
		RelationCount:         len(relationHashes),
	}
}

// calibrateDualPoolWeights 对齐 _calibrate_dual_pool_weights：按可信度连续释放图权重
func (r *DualPathRetriever) calibrateDualPoolWeights(candidates []*RetrievalResult, semanticWeight float64, sparseWeight float64, graphWeight float64, scanLimit int) (float64, float64, float64, graphReliabilityEstimate) {
	estimate := r.estimateGraphReliability(candidates, scanLimit)
	graphWeightFloor := math.Min(graphWeight, graphReliabilityWeightFloor)
	graphWeightRange := math.Max(0.0, graphWeight-graphWeightFloor)
	independentWeight := semanticWeight + sparseWeight
	if graphWeightRange <= 0.0 || independentWeight <= 0.0 {
		return semanticWeight, sparseWeight, graphWeight, estimate
	}
	effectiveGraphWeight := graphWeightFloor + graphWeightRange*math.Pow(estimate.Score, graphReliabilityCurveExponent)
	releasedWeight := graphWeight - effectiveGraphWeight
	effectiveSemanticWeight := semanticWeight + releasedWeight*(semanticWeight/independentWeight)
	effectiveSparseWeight := sparseWeight + releasedWeight*(sparseWeight/independentWeight)
	return effectiveSemanticWeight, effectiveSparseWeight, effectiveGraphWeight, estimate
}

// normalizedGraphItem 对齐 _normalize_graph_scores_by_type 的输出项
type normalizedGraphItem struct {
	ItemType        string
	HashValue       string
	RawScore        float64
	NormalizedScore float64
}

// normalizeGraphScoresByType 对齐 _normalize_graph_scores_by_type：按类型分别 minmax
func normalizeGraphScoresByType(parsedItems []normalizedGraphItem) []normalizedGraphItem {
	scoresByType := map[string][]float64{}
	for _, item := range parsedItems {
		scoresByType[item.ItemType] = append(scoresByType[item.ItemType], item.RawScore)
	}
	minimumByType := map[string]float64{}
	maximumByType := map[string]float64{}
	for itemType, scores := range scoresByType {
		minimum := scores[0]
		maximum := scores[0]
		for _, score := range scores[1:] {
			if score < minimum {
				minimum = score
			}
			if score > maximum {
				maximum = score
			}
		}
		minimumByType[itemType] = minimum
		maximumByType[itemType] = maximum
	}
	output := make([]normalizedGraphItem, 0, len(parsedItems))
	for _, item := range parsedItems {
		minimum := minimumByType[item.ItemType]
		maximum := maximumByType[item.ItemType]
		normalized := 0.0
		if maximum > minimum {
			normalized = (item.RawScore - minimum) / (maximum - minimum)
		} else {
			normalized = math.Max(0.0, math.Min(1.0, item.RawScore))
		}
		output = append(output, normalizedGraphItem{
			ItemType:        item.ItemType,
			HashValue:       item.HashValue,
			RawScore:        item.RawScore,
			NormalizedScore: normalized,
		})
	}
	return output
}

// graphEvidenceGroundingFactor 对齐 _graph_evidence_grounding_factor：按逐字落地程度软衰减
func graphEvidenceGroundingFactor(evidence map[string]any, paragraph map[string]any) float64 {
	paragraphText := dualPathLowerFold(stringFromMap(paragraph, "content"))
	isGrounded := func(value any) bool {
		token := strings.TrimSpace(dualPathLowerFold(stringValue(value)))
		return token != "" && strings.Contains(paragraphText, token)
	}
	if stringValue(evidence["type"]) == "entity" {
		if isGrounded(evidence["name"]) {
			return 1.0
		}
		return entityUngroundedFactor
	}
	subjectGrounded := isGrounded(evidence["subject"])
	predicateGrounded := isGrounded(evidence["predicate"])
	objectGrounded := isGrounded(evidence["object"])
	if subjectGrounded && predicateGrounded && objectGrounded {
		return 1.0
	}
	if subjectGrounded && objectGrounded {
		return relationBothEntitiesGroundingFactor
	}
	if subjectGrounded || objectGrounded {
		return relationSingleEntityGroundingFactor
	}
	if predicateGrounded {
		return relationPredicateOnlyGroundingFactor
	}
	return relationUngroundedFactor
}
