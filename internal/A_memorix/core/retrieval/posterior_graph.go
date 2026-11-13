package retrieval

// A_memorix 后验图补位：配置归一、分词与竞争打分，对齐 core/retrieval/posterior_graph.py
// 本文件只做纯计算与实体抽取；图召回、竞争合并和入口门控见 posterior_graph_gate.go

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"maibot-go/internal/A_memorix/core/storage"
)

// PosteriorGraphConfig 双重方案中的后验图补位配置，字段与 Python dataclass 一一对应
type PosteriorGraphConfig struct {
	Enabled                           bool
	DropRatio                         float64
	MinCoreResults                    int
	MaxGraphSlots                     int
	GateScanTopK                      int
	GroundedConfidenceThreshold       float64
	IncidentalConfidenceThreshold     float64
	MinQueryTokenCoverage             float64
	IncidentalQueryRelevanceThreshold float64
	IncidentalCoreOverlapThreshold    float64
	IncidentalSpecificityThreshold    float64
	QueryWeight                       float64
	NoveltyWeight                     float64
	ComplementarityWeight             float64
	SpecificityWeight                 float64
	GapFillWeight                     float64
	MaxCandidateTokens                int
}

// DefaultPosteriorGraphConfig 返回与 Python dataclass 默认值一致的配置
func DefaultPosteriorGraphConfig() PosteriorGraphConfig {
	config := PosteriorGraphConfig{
		Enabled:                           false,
		DropRatio:                         0.15,
		MinCoreResults:                    2,
		MaxGraphSlots:                     2,
		GateScanTopK:                      5,
		GroundedConfidenceThreshold:       0.48,
		IncidentalConfidenceThreshold:     0.22,
		MinQueryTokenCoverage:             0.78,
		IncidentalQueryRelevanceThreshold: 0.68,
		IncidentalCoreOverlapThreshold:    0.34,
		IncidentalSpecificityThreshold:    0.42,
		QueryWeight:                       0.28,
		NoveltyWeight:                     0.18,
		ComplementarityWeight:             0.16,
		SpecificityWeight:                 0.12,
		GapFillWeight:                     0.26,
		MaxCandidateTokens:                48,
	}
	config.Normalize()
	return config
}

// Normalize 对齐 __post_init__：计数项保底、比例项非负、阈值裁剪到 [0, 1]
func (c *PosteriorGraphConfig) Normalize() {
	c.DropRatio = posteriorMaxZero(c.DropRatio)
	c.MinCoreResults = maxInt(c.MinCoreResults, 1)
	c.MaxGraphSlots = maxInt(c.MaxGraphSlots, 0)
	c.GateScanTopK = maxInt(c.GateScanTopK, 1)
	c.GroundedConfidenceThreshold = posteriorClipScore(c.GroundedConfidenceThreshold)
	c.IncidentalConfidenceThreshold = posteriorClipScore(c.IncidentalConfidenceThreshold)
	c.MinQueryTokenCoverage = posteriorClipScore(c.MinQueryTokenCoverage)
	c.IncidentalQueryRelevanceThreshold = posteriorClipScore(c.IncidentalQueryRelevanceThreshold)
	c.IncidentalCoreOverlapThreshold = posteriorClipScore(c.IncidentalCoreOverlapThreshold)
	c.IncidentalSpecificityThreshold = posteriorClipScore(c.IncidentalSpecificityThreshold)
	c.QueryWeight = posteriorMaxZero(c.QueryWeight)
	c.NoveltyWeight = posteriorMaxZero(c.NoveltyWeight)
	c.ComplementarityWeight = posteriorMaxZero(c.ComplementarityWeight)
	c.SpecificityWeight = posteriorMaxZero(c.SpecificityWeight)
	c.GapFillWeight = posteriorMaxZero(c.GapFillWeight)
	c.MaxCandidateTokens = maxInt(c.MaxCandidateTokens, 8)
}

// posteriorCompetitionProfile 竞争打分所需的文本档次信息
type posteriorCompetitionProfile struct {
	text     string
	tokens   map[string]struct{}
	entities map[string]struct{}
}

// posteriorSeedEvidence 后验图种子证据
type posteriorSeedEvidence struct {
	name         string
	strength     string
	supportCount int
	rankHint     int
}

const (
	// posteriorSeedGrounded 查询实体在核心结果中命中
	posteriorSeedGrounded = "grounded"
	// posteriorSeedIncidental 查询实体未命中、仅由核心结果附带
	posteriorSeedIncidental = "incidental"
)

var (
	// posteriorTokenPattern 对齐 _TOKEN_PATTERN
	posteriorTokenPattern = regexp.MustCompile("[A-Za-z0-9_]+|[\\x{4e00}-\\x{9fff}]+")
	// posteriorBroadPredicates 对齐 _BROAD_PREDICATES
	posteriorBroadPredicates = map[string]struct{}{
		"contains_fact": {},
		"describe":      {},
		"describes":     {},
		"description":   {},
		"mention":       {},
		"mentions":      {},
		"summary":       {},
		"summarizes":    {},
	}
)

// posteriorMaxZero 对齐 Python 的 max(0.0, value)：NaN 也收敛为 0
func posteriorMaxZero(value float64) float64 {
	if math.IsNaN(value) || value < 0 {
		return 0
	}
	return value
}

// posteriorClipScore 对齐 _clip_score：等价于 Python 的 max(0.0, min(1.0, value))
// Python 的 min(1.0, NaN) 返回 1.0，这里同样保留 NaN 的既有语义
func posteriorClipScore(value float64) float64 {
	if math.IsNaN(value) || value > 1.0 {
		return 1.0
	}
	if value < 0.0 {
		return 0.0
	}
	return value
}

// posteriorSafeRatio 对齐 _safe_ratio：分母非正时返回 0
func posteriorSafeRatio(numerator, denominator int) float64 {
	if denominator <= 0 {
		return 0.0
	}
	return float64(numerator) / float64(denominator)
}

// posteriorRoundTo 对齐 Python 内置 round：按十进制正确舍入
func posteriorRoundTo(value float64, digits int) float64 {
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(value, 'f', digits, 64), 64)
	if err != nil {
		return value
	}
	return rounded
}

// posteriorMetadataText 对齐 Python 的 str(value or "")，nil 与空串都视为空
func posteriorMetadataText(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		if !typed {
			return ""
		}
		return "True"
	case int:
		if typed == 0 {
			return ""
		}
		return strconv.Itoa(typed)
	case int64:
		if typed == 0 {
			return ""
		}
		return strconv.FormatInt(typed, 10)
	case int32:
		if typed == 0 {
			return ""
		}
		return strconv.FormatInt(int64(typed), 10)
	case uint64:
		if typed == 0 {
			return ""
		}
		return strconv.FormatUint(typed, 10)
	case float64:
		if typed == 0 {
			return ""
		}
		return posteriorFormatPythonFloat(typed)
	case float32:
		if typed == 0 {
			return ""
		}
		return posteriorFormatPythonFloat(float64(typed))
	}
	return fmt.Sprint(value)
}

// posteriorFormatPythonFloat 对齐 Python str(float) 的整数值与特殊值表示
func posteriorFormatPythonFloat(value float64) string {
	switch {
	case math.IsNaN(value):
		return "nan"
	case math.IsInf(value, 1):
		return "inf"
	case math.IsInf(value, -1):
		return "-inf"
	}
	text := strconv.FormatFloat(value, 'g', -1, 64)
	if !strings.ContainsAny(text, ".eE") && math.Abs(value) < 1e16 {
		return text + ".0"
	}
	return text
}

// posteriorIsCJKChunk 对齐 _is_cjk_chunk：非空且全部为汉字
func posteriorIsCJKChunk(token string) bool {
	if token == "" {
		return false
	}
	for _, char := range token {
		if char < '\u4e00' || char > '\u9fff' {
			return false
		}
	}
	return true
}

// posteriorTokenizeForCompetition 对齐 _tokenize_for_competition
// segmenter 为空时整段中文作为一个 token，避免中文候选完全丢失
func posteriorTokenizeForCompetition(text string, maxTokens int, segmenter storage.Segmenter) []string {
	normalized := strings.TrimSpace(strings.ToLower(text))
	if normalized == "" {
		return nil
	}
	tokens := make([]string, 0, 16)
	for _, chunk := range posteriorTokenPattern.FindAllString(normalized, -1) {
		if posteriorIsCJKChunk(chunk) {
			if segmenter == nil {
				tokens = append(tokens, chunk)
				continue
			}
			for _, item := range segmenter.CutForSearch(chunk) {
				trimmed := strings.ToLower(strings.TrimSpace(item))
				if trimmed != "" {
					tokens = append(tokens, trimmed)
				}
			}
			continue
		}
		tokens = append(tokens, chunk)
	}
	filtered := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if utf8.RuneCountInString(token) <= 1 {
			continue
		}
		filtered = append(filtered, token)
		if len(filtered) >= maxTokens {
			break
		}
	}
	return filtered
}

// posteriorResultTextForEntityMatch 对齐 _result_text_for_entity_match
func posteriorResultTextForEntityMatch(result RetrievalResult) string {
	metadata := result.Metadata
	parts := []string{
		result.Content,
		posteriorMetadataText(metadata["subject"]),
		posteriorMetadataText(metadata["object"]),
		posteriorMetadataText(metadata["context_title"]),
		posteriorMetadataText(metadata["benchmark_title"]),
	}
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, "\n")
}

// posteriorCandidateTextForCompetition 对齐 _candidate_text_for_competition
func posteriorCandidateTextForCompetition(result RetrievalResult) string {
	parts := make([]string, 0, 2)
	if text := posteriorResultTextForEntityMatch(result); text != "" {
		parts = append(parts, text)
	}
	if predicate := posteriorMetadataText(result.Metadata["predicate"]); predicate != "" {
		parts = append(parts, predicate)
	}
	return strings.Join(parts, "\n")
}

// posteriorExtractCandidateEntities 对齐 _extract_candidate_entities
func posteriorExtractCandidateEntities(retriever PosteriorGraphRetriever, result RetrievalResult) map[string]struct{} {
	entities := make(map[string]struct{})
	for name := range retriever.ExtractEntities(posteriorCandidateTextForCompetition(result)) {
		if normalized := strings.ToLower(strings.TrimSpace(name)); normalized != "" {
			entities[normalized] = struct{}{}
		}
	}
	metadata := result.Metadata
	for _, key := range []string{"benchmark_title", "context_title", "object", "subject"} {
		if normalized := strings.ToLower(strings.TrimSpace(posteriorMetadataText(metadata[key]))); normalized != "" {
			entities[normalized] = struct{}{}
		}
	}
	return entities
}

// posteriorBuildQueryProfile 对齐 _build_query_profile
func posteriorBuildQueryProfile(retriever PosteriorGraphRetriever, query string, maxTokens int) posteriorCompetitionProfile {
	tokens := make(map[string]struct{})
	for _, token := range posteriorTokenizeForCompetition(query, maxTokens, retriever.Segmenter()) {
		tokens[token] = struct{}{}
	}
	entities := make(map[string]struct{})
	for name := range retriever.ExtractEntities(query) {
		if normalized := strings.ToLower(strings.TrimSpace(name)); normalized != "" {
			entities[normalized] = struct{}{}
		}
	}
	return posteriorCompetitionProfile{text: query, tokens: tokens, entities: entities}
}

// posteriorBuildCandidateProfile 对齐 _build_candidate_profile
func posteriorBuildCandidateProfile(retriever PosteriorGraphRetriever, result RetrievalResult, maxTokens int) posteriorCompetitionProfile {
	text := posteriorCandidateTextForCompetition(result)
	tokens := make(map[string]struct{})
	for _, token := range posteriorTokenizeForCompetition(text, maxTokens, retriever.Segmenter()) {
		tokens[token] = struct{}{}
	}
	return posteriorCompetitionProfile{
		text:     text,
		tokens:   tokens,
		entities: posteriorExtractCandidateEntities(retriever, result),
	}
}

// posteriorBuildCoreProfile 对齐 _build_core_profile
func posteriorBuildCoreProfile(retriever PosteriorGraphRetriever, results []RetrievalResult, maxTokens int) posteriorCompetitionProfile {
	parts := make([]string, 0, len(results))
	tokens := make(map[string]struct{})
	entities := make(map[string]struct{})
	for _, result := range results {
		profile := posteriorBuildCandidateProfile(retriever, result, maxTokens)
		parts = append(parts, profile.text)
		for token := range profile.tokens {
			tokens[token] = struct{}{}
		}
		for entity := range profile.entities {
			entities[entity] = struct{}{}
		}
	}
	return posteriorCompetitionProfile{text: strings.Join(parts, "\n"), tokens: tokens, entities: entities}
}

// posteriorIntersectionSize 返回两个集合的交集大小
func posteriorIntersectionSize(left, right map[string]struct{}) int {
	if len(left) > len(right) {
		left, right = right, left
	}
	count := 0
	for item := range left {
		if _, exists := right[item]; exists {
			count++
		}
	}
	return count
}

// posteriorDifferenceSize 返回 left - right 的大小
func posteriorDifferenceSize(left, right map[string]struct{}) int {
	count := 0
	for item := range left {
		if _, exists := right[item]; !exists {
			count++
		}
	}
	return count
}

// posteriorSetDifference 返回 left - right 的新集合
func posteriorSetDifference(left, right map[string]struct{}) map[string]struct{} {
	difference := make(map[string]struct{}, len(left))
	for item := range left {
		if _, exists := right[item]; !exists {
			difference[item] = struct{}{}
		}
	}
	return difference
}

// posteriorQueryRelevance 对齐 _compute_query_relevance
func posteriorQueryRelevance(candidate, query posteriorCompetitionProfile) float64 {
	entityHit := posteriorSafeRatio(posteriorIntersectionSize(candidate.entities, query.entities), len(query.entities))
	tokenHit := posteriorSafeRatio(posteriorIntersectionSize(candidate.tokens, query.tokens), len(query.tokens))
	if len(query.entities) > 0 {
		return posteriorClipScore(0.65*entityHit + 0.35*tokenHit)
	}
	return posteriorClipScore(math.Max(entityHit, tokenHit))
}

// posteriorNovelty 对齐 _compute_novelty
func posteriorNovelty(candidate, core posteriorCompetitionProfile) float64 {
	entityNovelty := posteriorSafeRatio(posteriorDifferenceSize(candidate.entities, core.entities), len(candidate.entities))
	tokenNovelty := posteriorSafeRatio(posteriorDifferenceSize(candidate.tokens, core.tokens), len(candidate.tokens))
	return posteriorClipScore(0.5*entityNovelty + 0.5*tokenNovelty)
}

// posteriorComplementarity 对齐 _compute_complementarity
func posteriorComplementarity(candidate, core posteriorCompetitionProfile, queryRelevance float64) float64 {
	if len(core.tokens) == 0 && len(core.entities) == 0 {
		return posteriorClipScore(queryRelevance)
	}
	entityOverlap := posteriorSafeRatio(posteriorIntersectionSize(candidate.entities, core.entities), len(candidate.entities))
	tokenOverlap := posteriorSafeRatio(posteriorIntersectionSize(candidate.tokens, core.tokens), len(candidate.tokens))
	coreOverlap := 0.5*entityOverlap + 0.5*tokenOverlap
	sweetSpot := 1.0 - math.Abs(coreOverlap-0.4)/0.4
	return posteriorClipScore(math.Max(0.0, sweetSpot) * math.Max(queryRelevance, 0.2))
}

// posteriorSpecificity 对齐 _compute_specificity
func posteriorSpecificity(candidate posteriorCompetitionProfile, result RetrievalResult) float64 {
	tokenCount := maxInt(1, len(candidate.tokens))
	entityDensity := posteriorClipScore(posteriorSafeRatio(len(candidate.entities), tokenCount) * 4.0)
	brevity := 1.0 - math.Min(1.0, float64(maxInt(0, tokenCount-16))/16.0)
	predicateBonus := 0.0
	predicate := strings.ToLower(strings.TrimSpace(posteriorMetadataText(result.Metadata["predicate"])))
	if predicate != "" {
		if _, broad := posteriorBroadPredicates[predicate]; broad {
			predicateBonus = -0.25
		} else if result.ResultType == "relation" {
			predicateBonus = 0.10
		}
	}
	return posteriorClipScore(0.6*entityDensity + 0.4*brevity + predicateBonus)
}

// posteriorGapFill 对齐 _compute_gap_fill
func posteriorGapFill(candidate, query, core posteriorCompetitionProfile) float64 {
	missingEntities := posteriorSetDifference(query.entities, core.entities)
	missingTokens := posteriorSetDifference(query.tokens, core.tokens)
	entityFill := posteriorSafeRatio(posteriorIntersectionSize(candidate.entities, missingEntities), len(missingEntities))
	tokenFill := posteriorSafeRatio(posteriorIntersectionSize(candidate.tokens, missingTokens), len(missingTokens))
	if len(missingEntities) > 0 {
		return posteriorClipScore(0.7*entityFill + 0.3*tokenFill)
	}
	return posteriorClipScore(math.Max(entityFill, tokenFill))
}

// posteriorCoreOverlap 对齐 _core_overlap
func posteriorCoreOverlap(candidate, core posteriorCompetitionProfile) float64 {
	entityOverlap := posteriorSafeRatio(posteriorIntersectionSize(candidate.entities, core.entities), len(candidate.entities))
	tokenOverlap := posteriorSafeRatio(posteriorIntersectionSize(candidate.tokens, core.tokens), len(candidate.tokens))
	return posteriorClipScore(0.5*entityOverlap + 0.5*tokenOverlap)
}

// posteriorComputeCompetitionScore 对齐 _compute_competition_score
func posteriorComputeCompetitionScore(retriever PosteriorGraphRetriever, candidate RetrievalResult, queryProfile, coreProfile posteriorCompetitionProfile, cfg PosteriorGraphConfig) (float64, map[string]float64) {
	candidateProfile := posteriorBuildCandidateProfile(retriever, candidate, cfg.MaxCandidateTokens)
	queryRelevance := posteriorQueryRelevance(candidateProfile, queryProfile)
	novelty := posteriorNovelty(candidateProfile, coreProfile)
	complementarity := posteriorComplementarity(candidateProfile, coreProfile, queryRelevance)
	specificity := posteriorSpecificity(candidateProfile, candidate)
	gapFill := posteriorGapFill(candidateProfile, queryProfile, coreProfile)

	finalScore := cfg.QueryWeight*queryRelevance +
		cfg.NoveltyWeight*novelty +
		cfg.ComplementarityWeight*complementarity +
		cfg.SpecificityWeight*specificity +
		cfg.GapFillWeight*gapFill
	breakdown := map[string]float64{
		"query_relevance":   posteriorRoundTo(queryRelevance, 4),
		"novelty":           posteriorRoundTo(novelty, 4),
		"complementarity":   posteriorRoundTo(complementarity, 4),
		"specificity":       posteriorRoundTo(specificity, 4),
		"gap_fill":          posteriorRoundTo(gapFill, 4),
		"competition_score": posteriorRoundTo(posteriorClipScore(finalScore), 4),
	}
	return posteriorClipScore(finalScore), breakdown
}
