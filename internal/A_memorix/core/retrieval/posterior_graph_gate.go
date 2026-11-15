package retrieval

// A_memorix 后验图补位的图召回、竞争合并与入口门控，对齐 core/retrieval/posterior_graph.py
// 上游检索器通过 PosteriorGraphRetriever 提供配置、实体抽取、图召回与元数据查询能力

import (
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"go.uber.org/zap"

	"maibot-go/internal/A_memorix/core/storage"
)

// PosteriorGraphRetriever 后验图补位所需的上游检索器能力
// 双路检索器实现该接口后，ApplyPosteriorGraphGate 按 Python 语义改写 Top-K
type PosteriorGraphRetriever interface {
	// PosteriorGraphConfig 返回后验图配置，nil 表示未配置
	PosteriorGraphConfig() *PosteriorGraphConfig
	// Alpha 返回段落与关系的融合权重
	Alpha() float64
	// ExtractEntities 返回文本中命中的实体及权重，对齐 _extract_entities
	ExtractEntities(text string) map[string]float64
	// Segmenter 返回中文分词器，nil 时中文按整段退化
	Segmenter() storage.Segmenter
	// GraphRelationRecall 返回图关系召回服务，nil 表示该能力不可用
	GraphRelationRecall() *GraphRelationRecallService
	// MetadataQuery 执行元数据查询，语义对齐 metadata_store.query
	MetadataQuery(sql string, params ...any) ([]map[string]any, error)
	// ApplyTemporalFilterToRelations 对齐 _apply_temporal_filter_to_relations
	ApplyTemporalFilterToRelations(results []RetrievalResult, temporal any) []RetrievalResult
	// MergeRelationResultsGraphEnhanced 对齐 _merge_relation_results_graph_enhanced
	MergeRelationResultsGraphEnhanced(vectorResults, sparseResults, graphResults []RetrievalResult) []RetrievalResult
	// SetPosteriorGraphGateLastDecision 记录最近一次门控判定
	SetPosteriorGraphGateLastDecision(decision map[string]any)
}

// FindScoreCliff 对齐 find_score_cliff：在已按分数排序的结果中寻找核心段与尾部的断崖位置
func FindScoreCliff(results []RetrievalResult, dropRatio float64, minCoreResults int) int {
	if len(results) == 0 {
		return 0
	}
	if len(results) <= minCoreResults {
		return len(results)
	}
	for index := 1; index < len(results); index++ {
		previousScore := math.Max(results[index-1].Score, 1e-8)
		currentScore := results[index].Score
		scoreDrop := previousScore - currentScore
		if scoreDrop/previousScore > dropRatio {
			return max(minCoreResults, index)
		}
	}
	fallback := max(minCoreResults, len(results)/2)
	if fallback > len(results) {
		fallback = len(results)
	}
	return fallback
}

// posteriorTopScore 对齐 _top_score：空结果返回 0
func posteriorTopScore(results []RetrievalResult) float64 {
	if len(results) == 0 {
		return 0.0
	}
	top := results[0].Score
	for _, item := range results[1:] {
		if item.Score > top {
			top = item.Score
		}
	}
	return top
}

// posteriorTruncateResults 对齐 Python 切片 results[:limit]，负值按空结果处理
func posteriorTruncateResults(results []RetrievalResult, limit int) []RetrievalResult {
	if limit <= 0 {
		return []RetrievalResult{}
	}
	if limit >= len(results) {
		return results
	}
	return results[:limit]
}

// posteriorStringList 复制字符串切片，保证空列表不是 nil
func posteriorStringList(values []string) []string {
	copied := make([]string, len(values))
	copy(copied, values)
	return copied
}

// posteriorLimitStrings 对齐 Python 的 list[:limit]
func posteriorLimitStrings(values []string, limit int) []string {
	if limit < 0 {
		limit = 0
	}
	if len(values) > limit {
		return values[:limit]
	}
	return values
}

// posteriorExtractSeedEvidence 对齐 _extract_seed_evidence：按实体聚合证据并排序
func posteriorExtractSeedEvidence(retriever PosteriorGraphRetriever, queryProfile posteriorCompetitionProfile, results []RetrievalResult, scanTopK, maxTokens int) []posteriorSeedEvidence {
	type seedKey struct {
		name     string
		strength string
	}
	scoreMap := make(map[seedKey]posteriorSeedEvidence)
	topResults := posteriorTruncateResults(results, max(1, scanTopK))
	for index, item := range topResults {
		rank := index + 1
		profile := posteriorBuildCandidateProfile(retriever, item, maxTokens)
		for entity := range profile.entities {
			strength := posteriorSeedIncidental
			if _, grounded := queryProfile.entities[entity]; grounded {
				strength = posteriorSeedGrounded
			}
			key := seedKey{name: entity, strength: strength}
			existing, exists := scoreMap[key]
			if !exists {
				scoreMap[key] = posteriorSeedEvidence{name: entity, strength: strength, supportCount: 1, rankHint: rank}
				continue
			}
			existing.supportCount++
			if rank < existing.rankHint {
				existing.rankHint = rank
			}
			scoreMap[key] = existing
		}
	}
	evidence := make([]posteriorSeedEvidence, 0, len(scoreMap))
	for _, item := range scoreMap {
		evidence = append(evidence, item)
	}
	sort.SliceStable(evidence, func(left, right int) bool {
		a, b := evidence[left], evidence[right]
		if a.strength != b.strength {
			return a.strength == posteriorSeedGrounded
		}
		if a.supportCount != b.supportCount {
			return a.supportCount > b.supportCount
		}
		if a.rankHint != b.rankHint {
			return a.rankHint < b.rankHint
		}
		aLength, bLength := utf8.RuneCountInString(a.name), utf8.RuneCountInString(b.name)
		if aLength != bLength {
			return aLength > bLength
		}
		return a.name < b.name
	})
	return evidence
}

// posteriorGroundedSeedNames 对齐 _grounded_seed_names
func posteriorGroundedSeedNames(seedEvidence []posteriorSeedEvidence) []string {
	names := make([]string, 0, len(seedEvidence))
	for _, item := range seedEvidence {
		if item.strength == posteriorSeedGrounded {
			names = append(names, item.name)
		}
	}
	return names
}

// posteriorIncidentalSeedNames 对齐 _incidental_seed_names
func posteriorIncidentalSeedNames(seedEvidence []posteriorSeedEvidence) []string {
	names := make([]string, 0, len(seedEvidence))
	for _, item := range seedEvidence {
		if item.strength == posteriorSeedIncidental {
			names = append(names, item.name)
		}
	}
	return names
}

// posteriorNeedForGraph 对齐 _need_for_graph
func posteriorNeedForGraph(queryProfile, coreProfile posteriorCompetitionProfile, coreProfiles []posteriorCompetitionProfile, groundedSeeds []string, ragConfidence float64, cfg PosteriorGraphConfig) (bool, string) {
	uncoveredQueryEntities := posteriorSetDifference(queryProfile.entities, coreProfile.entities)
	if len(uncoveredQueryEntities) > 0 {
		return true, "uncovered_query_entities"
	}
	if len(groundedSeeds) >= 2 {
		bridgeTargets := make(map[string]struct{}, 2)
		for _, seed := range posteriorLimitStrings(groundedSeeds, 2) {
			bridgeTargets[seed] = struct{}{}
		}
		sameCoreHit := false
		for _, profile := range coreProfiles {
			if posteriorIntersectionSize(profile.entities, bridgeTargets) >= 2 {
				sameCoreHit = true
				break
			}
		}
		if !sameCoreHit {
			return true, "grounded_bridge_gap"
		}
	}
	tokenCoverage := posteriorSafeRatio(posteriorIntersectionSize(coreProfile.tokens, queryProfile.tokens), len(queryProfile.tokens))
	if len(groundedSeeds) > 0 && tokenCoverage < cfg.MinQueryTokenCoverage {
		return true, "low_core_query_coverage"
	}
	if len(groundedSeeds) > 0 && ragConfidence < cfg.GroundedConfidenceThreshold {
		return true, "low_confidence_grounded"
	}
	return false, "core_already_sufficient"
}

// posteriorPassesIncidentalHighBar 对齐 _passes_incidental_high_bar
func posteriorPassesIncidentalHighBar(retriever PosteriorGraphRetriever, candidate RetrievalResult, queryProfile, coreProfile posteriorCompetitionProfile, cfg PosteriorGraphConfig) bool {
	candidateProfile := posteriorBuildCandidateProfile(retriever, candidate, cfg.MaxCandidateTokens)
	uncoveredQueryEntities := posteriorSetDifference(queryProfile.entities, coreProfile.entities)
	if posteriorIntersectionSize(candidateProfile.entities, uncoveredQueryEntities) > 0 {
		return true
	}
	queryRelevance := posteriorQueryRelevance(candidateProfile, queryProfile)
	specificity := posteriorSpecificity(candidateProfile, candidate)
	overlap := posteriorCoreOverlap(candidateProfile, coreProfile)
	gapFill := posteriorGapFill(candidateProfile, queryProfile, coreProfile)
	return queryRelevance >= cfg.IncidentalQueryRelevanceThreshold &&
		specificity >= cfg.IncidentalSpecificityThreshold &&
		overlap <= cfg.IncidentalCoreOverlapThreshold &&
		gapFill > 0.0
}

// posteriorLinkedCoreParagraphHashSQL 对齐 _linked_core_paragraph_hashes 的查询
const posteriorLinkedCoreParagraphHashSQL = "SELECT paragraph_hash FROM paragraph_relations WHERE relation_hash = ?"

// posteriorLinkedCoreParagraphHashes 对齐 _linked_core_paragraph_hashes
func posteriorLinkedCoreParagraphHashes(retriever PosteriorGraphRetriever, relationHash string) map[string]struct{} {
	rows, err := retriever.MetadataQuery(posteriorLinkedCoreParagraphHashSQL, relationHash)
	if err != nil {
		zap.L().Warn("后验图查询关系关联段落失败",
			zap.String("relation_hash", relationHash),
			zap.Error(err))
		return map[string]struct{}{}
	}
	hashes := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		hashValue := strings.TrimSpace(posteriorMetadataText(row["paragraph_hash"]))
		if hashValue != "" {
			hashes[hashValue] = struct{}{}
		}
	}
	return hashes
}

// posteriorBuildGraphResultsFromSeeds 对齐 _build_graph_results_from_seeds
func posteriorBuildGraphResultsFromSeeds(retriever PosteriorGraphRetriever, seedEntities []string, temporal any, alpha float64, scope *RetrievalScope) []RetrievalResult {
	service := retriever.GraphRelationRecall()
	if service == nil {
		return []RetrievalResult{}
	}
	var allowedRelationIDs map[string]struct{}
	if scope != nil {
		allowedRelationIDs = scope.RelationIDs
		if allowedRelationIDs == nil {
			// Python 的 RetrievalScope.relation_ids 默认是空 frozenset，表示没有可见关系
			allowedRelationIDs = map[string]struct{}{}
		}
	}
	payloads := service.Recall(seedEntities, allowedRelationIDs)
	if len(payloads) == 0 {
		return []RetrievalResult{}
	}
	graphResults := make([]RetrievalResult, 0, len(payloads))
	for _, payload := range payloads {
		seedCopy := make([]string, len(payload.GraphSeedEntities))
		copy(seedCopy, payload.GraphSeedEntities)
		graphResults = append(graphResults, RetrievalResult{
			HashValue:  payload.HashValue,
			Content:    payload.Subject + " " + payload.Predicate + " " + payload.Object,
			Score:      0.0,
			ResultType: "relation",
			Source:     "posterior_graph_recall",
			Metadata: map[string]any{
				"subject":                    payload.Subject,
				"predicate":                  payload.Predicate,
				"object":                     payload.Object,
				"confidence":                 payload.Confidence,
				"graph_seed_entities":        seedCopy,
				"graph_hops":                 payload.GraphHops,
				"graph_candidate_type":       payload.GraphCandidateType,
				"supporting_paragraph_count": payload.SupportingParagraphCount,
			},
		})
	}
	graphResults = retriever.ApplyTemporalFilterToRelations(graphResults, temporal)
	graphResults = retriever.MergeRelationResultsGraphEnhanced(nil, nil, graphResults)
	relationWeight := posteriorMaxZero(1.0 - alpha)
	for index := range graphResults {
		graphResults[index].Score *= relationWeight
		graphResults[index].Source = "posterior_graph_competition"
	}
	return graphResults
}

// posteriorCompetitionScorer 竞争打分函数，测试时可替换，对应 Python 对 _compute_competition_score 的 monkeypatch
type posteriorCompetitionScorer func(candidate RetrievalResult, queryProfile, coreProfile posteriorCompetitionProfile, cfg PosteriorGraphConfig) (float64, map[string]float64)

// posteriorScoredCandidate 参与尾部竞争的候选及其打分
type posteriorScoredCandidate struct {
	result           RetrievalResult
	score            float64
	isGraphCandidate bool
}

// posteriorCopyMetadata 复制 metadata 顶层，对齐 Python 的 dict(item.metadata)
func posteriorCopyMetadata(metadata map[string]any) map[string]any {
	copied := make(map[string]any, len(metadata)+3)
	for key, value := range metadata {
		copied[key] = value
	}
	return copied
}

// posteriorCompetitionMetadata 写入后验竞争的诊断元数据
func posteriorCompetitionMetadata(item RetrievalResult, breakdown map[string]float64) map[string]any {
	metadata := posteriorCopyMetadata(item.Metadata)
	metadata["posterior_original_score"] = posteriorRoundTo(item.Score, 4)
	metadata["posterior_competition_breakdown"] = breakdown
	metadata["posterior_competition_source"] = "posterior_graph_gate"
	return metadata
}

// posteriorRelationRank 对齐排序键里的关系类型权重
func posteriorRelationRank(result RetrievalResult) int {
	if result.ResultType == "relation" {
		return 1
	}
	return 0
}

// posteriorCompetitionMerge 对齐 _competition_merge
// scorer 为 nil 时使用默认竞争打分，测试可注入固定分数
func posteriorCompetitionMerge(retriever PosteriorGraphRetriever, query string, baseResults, graphResults []RetrievalResult, topK int, cfg PosteriorGraphConfig, scorer posteriorCompetitionScorer) []RetrievalResult {
	ranked := posteriorTruncateResults(baseResults, max(1, topK))
	if len(ranked) == 0 || len(graphResults) == 0 {
		return ranked
	}
	if scorer == nil {
		scorer = func(candidate RetrievalResult, queryProfile, coreProfile posteriorCompetitionProfile, cfg PosteriorGraphConfig) (float64, map[string]float64) {
			return posteriorComputeCompetitionScore(retriever, candidate, queryProfile, coreProfile, cfg)
		}
	}
	cliff := FindScoreCliff(ranked, cfg.DropRatio, cfg.MinCoreResults)
	coreResults := ranked[:cliff]
	remainingSlots := max(0, topK-len(coreResults))
	graphSlotLimit := remainingSlots
	if cfg.MaxGraphSlots < graphSlotLimit {
		graphSlotLimit = cfg.MaxGraphSlots
	}
	if remainingSlots <= 0 || graphSlotLimit <= 0 {
		return posteriorTruncateResults(ranked, topK)
	}
	coreParagraphHashes := make(map[string]struct{})
	selectedHashes := make(map[string]struct{}, len(coreResults))
	for _, item := range coreResults {
		selectedHashes[item.HashValue] = struct{}{}
		if item.ResultType == "paragraph" {
			coreParagraphHashes[item.HashValue] = struct{}{}
		}
	}
	filteredGraphResults := make([]RetrievalResult, 0, len(graphResults))
	for _, item := range graphResults {
		if _, exists := selectedHashes[item.HashValue]; exists {
			continue
		}
		linkedHashes := posteriorLinkedCoreParagraphHashes(retriever, item.HashValue)
		linkedToCore := false
		for hashValue := range coreParagraphHashes {
			if _, exists := linkedHashes[hashValue]; exists {
				linkedToCore = true
				break
			}
		}
		if linkedToCore {
			continue
		}
		filteredGraphResults = append(filteredGraphResults, item)
	}
	tailEnd := topK
	if tailEnd > len(ranked) {
		tailEnd = len(ranked)
	}
	if tailEnd < cliff {
		tailEnd = cliff
	}
	queryProfile := posteriorBuildQueryProfile(retriever, query, cfg.MaxCandidateTokens)
	coreProfile := posteriorBuildCoreProfile(retriever, coreResults, cfg.MaxCandidateTokens)
	scoredCandidates := make([]posteriorScoredCandidate, 0, tailEnd-cliff+len(filteredGraphResults))
	for index := cliff; index < tailEnd; index++ {
		item := ranked[index]
		if _, exists := selectedHashes[item.HashValue]; exists {
			continue
		}
		score, breakdown := scorer(item, queryProfile, coreProfile, cfg)
		item.Metadata = posteriorCompetitionMetadata(item, breakdown)
		ranked[index] = item
		scoredCandidates = append(scoredCandidates, posteriorScoredCandidate{result: item, score: score})
	}
	for index := range filteredGraphResults {
		item := filteredGraphResults[index]
		score, breakdown := scorer(item, queryProfile, coreProfile, cfg)
		item.Metadata = posteriorCompetitionMetadata(item, breakdown)
		filteredGraphResults[index] = item
		scoredCandidates = append(scoredCandidates, posteriorScoredCandidate{result: item, score: score, isGraphCandidate: true})
	}
	sort.SliceStable(scoredCandidates, func(left, right int) bool {
		a, b := scoredCandidates[left], scoredCandidates[right]
		if a.score != b.score {
			return a.score > b.score
		}
		return posteriorRelationRank(a.result) > posteriorRelationRank(b.result)
	})
	tailWinners := make([]RetrievalResult, 0, remainingSlots)
	seenHashes := make(map[string]struct{}, len(selectedHashes))
	for hashValue := range selectedHashes {
		seenHashes[hashValue] = struct{}{}
	}
	selectedGraphCount := 0
	for _, candidate := range scoredCandidates {
		if _, exists := seenHashes[candidate.result.HashValue]; exists {
			continue
		}
		if candidate.isGraphCandidate && selectedGraphCount >= graphSlotLimit {
			continue
		}
		tailWinners = append(tailWinners, candidate.result)
		seenHashes[candidate.result.HashValue] = struct{}{}
		if candidate.isGraphCandidate {
			selectedGraphCount++
		}
		if len(tailWinners) >= remainingSlots {
			break
		}
	}
	// 图候选没有赢得竞争时必须保持原始结果，避免后验阶段无意义地重排尾部
	if selectedGraphCount == 0 {
		return posteriorTruncateResults(ranked, topK)
	}
	// 去重或异常候选可能导致竞争结果不足，继续用原始尾部补齐 Top-K 预算
	for index := cliff; index < tailEnd; index++ {
		if len(tailWinners) >= remainingSlots {
			break
		}
		item := ranked[index]
		if _, exists := seenHashes[item.HashValue]; exists {
			continue
		}
		tailWinners = append(tailWinners, item)
		seenHashes[item.HashValue] = struct{}{}
	}
	finalResults := make([]RetrievalResult, 0, len(coreResults)+len(tailWinners))
	finalResults = append(finalResults, coreResults...)
	finalResults = append(finalResults, tailWinners...)
	return posteriorTruncateResults(finalResults, topK)
}

// posteriorGateDecision 构造后验图门控的判定记录
func posteriorGateDecision(query string, enabled bool, bucket string, groundedSeeds, incidentalSeeds []string, seedType string, needForGraph bool, needReason string, ragConfidence float64) map[string]any {
	return map[string]any{
		"scheme":             "posterior_graph_gate",
		"query":              query,
		"enabled":            enabled,
		"bucket":             bucket,
		"grounded_seeds":     posteriorStringList(groundedSeeds),
		"incidental_seeds":   posteriorStringList(incidentalSeeds),
		"selected_seed_type": seedType,
		"need_for_graph":     needForGraph,
		"need_reason":        needReason,
		"rag_confidence":     posteriorRoundTo(ragConfidence, 4),
	}
}

// ApplyPosteriorGraphGate 对齐 apply_posterior_graph_gate：按后验图补位规则改写 Top-K
// 参数顺序对应 Python 的关键字参数 query、base_results、top_k、temporal、relation_intent、scope
func ApplyPosteriorGraphGate(retriever PosteriorGraphRetriever, query string, baseResults []RetrievalResult, topK int, temporal any, relationIntent map[string]any, scope *RetrievalScope) []RetrievalResult {
	cfg := retriever.PosteriorGraphConfig()
	if cfg == nil || !cfg.Enabled {
		return posteriorTruncateResults(baseResults, topK)
	}
	if len(baseResults) == 0 {
		retriever.SetPosteriorGraphGateLastDecision(map[string]any{
			"scheme":  "posterior_graph_gate",
			"query":   query,
			"enabled": false,
			"bucket":  "posterior_gate_empty",
		})
		return []RetrievalResult{}
	}
	topKInt := max(1, topK)
	alpha := retriever.Alpha()
	if relationIntent != nil {
		if override, exists := relationIntent["alpha_override"]; exists && override != nil {
			if value, ok := numericValue(override); ok {
				alpha = value
			}
		}
	}
	ragConfidence := posteriorTopScore(posteriorTruncateResults(baseResults, topKInt))
	queryProfile := posteriorBuildQueryProfile(retriever, query, cfg.MaxCandidateTokens)
	seedEvidence := posteriorExtractSeedEvidence(retriever, queryProfile, baseResults, cfg.GateScanTopK, cfg.MaxCandidateTokens)
	groundedSeeds := posteriorLimitStrings(posteriorGroundedSeedNames(seedEvidence), 2)
	incidentalSeeds := posteriorLimitStrings(posteriorIncidentalSeedNames(seedEvidence), 2)
	coreLimit := FindScoreCliff(posteriorTruncateResults(baseResults, topKInt), cfg.DropRatio, cfg.MinCoreResults)
	coreResults := posteriorTruncateResults(baseResults, coreLimit)
	coreProfile := posteriorBuildCoreProfile(retriever, coreResults, cfg.MaxCandidateTokens)
	coreProfiles := make([]posteriorCompetitionProfile, 0, len(coreResults))
	for _, item := range coreResults {
		coreProfiles = append(coreProfiles, posteriorBuildCandidateProfile(retriever, item, cfg.MaxCandidateTokens))
	}
	needForGraph, needReason := posteriorNeedForGraph(queryProfile, coreProfile, coreProfiles, groundedSeeds, ragConfidence, *cfg)
	seedType := "none"
	seedNames := make([]string, 0, 2)
	if len(groundedSeeds) > 0 && needForGraph {
		seedType = posteriorSeedGrounded
		seedNames = append(seedNames, groundedSeeds...)
	} else if len(groundedSeeds) == 0 && len(incidentalSeeds) > 0 && ragConfidence < cfg.IncidentalConfidenceThreshold {
		seedType = posteriorSeedIncidental
		seedNames = append(seedNames, incidentalSeeds...)
	}
	if len(seedNames) == 0 {
		decision := posteriorGateDecision(query, false, "posterior_gate_none", groundedSeeds, incidentalSeeds, seedType, needForGraph, needReason, ragConfidence)
		retriever.SetPosteriorGraphGateLastDecision(decision)
		return posteriorTruncateResults(baseResults, topKInt)
	}
	graphResults := posteriorBuildGraphResultsFromSeeds(retriever, seedNames, temporal, alpha, scope)
	rawGraphCount := len(graphResults)
	if seedType == posteriorSeedIncidental {
		filtered := make([]RetrievalResult, 0, len(graphResults))
		for _, item := range graphResults {
			if posteriorPassesIncidentalHighBar(retriever, item, queryProfile, coreProfile, *cfg) {
				filtered = append(filtered, item)
			}
		}
		graphResults = filtered
	}
	if len(graphResults) == 0 {
		decision := posteriorGateDecision(query, false, "posterior_gate_graph_filtered", groundedSeeds, incidentalSeeds, seedType, needForGraph, needReason, ragConfidence)
		decision["graph_result_count"] = rawGraphCount
		retriever.SetPosteriorGraphGateLastDecision(decision)
		return posteriorTruncateResults(baseResults, topKInt)
	}
	finalResults := posteriorCompetitionMerge(retriever, query, baseResults, graphResults, topKInt, *cfg, nil)
	selectedHashes := make(map[string]struct{}, len(finalResults))
	for _, item := range finalResults {
		selectedHashes[item.HashValue] = struct{}{}
	}
	graphSelected := false
	for _, item := range graphResults {
		if _, exists := selectedHashes[item.HashValue]; exists {
			graphSelected = true
			break
		}
	}
	bucket := "posterior_gate_tail_rejected"
	if graphSelected {
		bucket = "posterior_gate_enabled"
	}
	decision := posteriorGateDecision(query, graphSelected, bucket, groundedSeeds, incidentalSeeds, seedType, needForGraph, needReason, ragConfidence)
	baseTopKCount := len(baseResults)
	if baseTopKCount > topKInt {
		baseTopKCount = topKInt
	}
	decision["graph_result_count"] = rawGraphCount
	decision["filtered_graph_count"] = max(0, rawGraphCount-len(graphResults))
	decision["base_top_k_count"] = baseTopKCount
	retriever.SetPosteriorGraphGateLastDecision(decision)
	return posteriorTruncateResults(finalResults, topKInt)
}
