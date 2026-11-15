package retrieval

// A_memorix 双路检索器主体：构造、入口调度与通用辅助，对齐 core/retrieval/dual_path.py

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/A_memorix/core/storage"
)

// DualPathRetriever 双路检索器
// 同时检索段落与关系，并做图增强、RRF 融合、PPR 重排与后验图补位
type DualPathRetriever struct {
	vectorStore          VectorSearcher
	paragraphVectorStore VectorSearcher
	graphVectorStore     VectorSearcher
	legacyVectorStore    VectorSearcher
	graphStore           DualPathGraphStore
	metadataStore        DualPathMetadataStore
	embeddingManager     EmbeddingEncoder
	config               DualPathRetrieverConfig
	sparseIndex          DualPathSparseIndex
	segmenter            storage.Segmenter

	ppr                 *PersonalizedPageRank
	pprSemaphore        chan struct{}
	graphRelationRecall *GraphRelationRecallService

	acMatcher      QueryEntityMatcher
	acNodesCount   int
	acNodeRevision int64
	acNodeMap      map[string]string

	relationIntentPattern *regexp.Regexp
	runtimeSparseOnly     atomic.Bool

	pprCache           map[pprCacheKey]pprCacheEntry
	pprCacheMu         sync.Mutex
	pprCacheTTLSeconds float64
	pprCacheMaxEntries int

	posteriorGateDecision map[string]any
}

// DualPathRetrieverOptions 构造参数，字段与 Python __init__ 一一对应
type DualPathRetrieverOptions struct {
	VectorStore          VectorSearcher
	ParagraphVectorStore VectorSearcher
	GraphVectorStore     VectorSearcher
	LegacyVectorStore    VectorSearcher
	GraphStore           DualPathGraphStore
	MetadataStore        DualPathMetadataStore
	EmbeddingManager     EmbeddingEncoder
	SparseIndex          DualPathSparseIndex
	Segmenter            storage.Segmenter
	Config               *DualPathRetrieverConfig
}

// NewDualPathRetriever 构造双路检索器
func NewDualPathRetriever(opts DualPathRetrieverOptions) (*DualPathRetriever, error) {
	config := DefaultDualPathRetrieverConfig()
	if opts.Config != nil {
		config = *opts.Config
	}
	if err := config.Normalize(); err != nil {
		return nil, err
	}
	retriever := &DualPathRetriever{
		vectorStore:           opts.VectorStore,
		paragraphVectorStore:  opts.ParagraphVectorStore,
		graphVectorStore:      opts.GraphVectorStore,
		legacyVectorStore:     opts.LegacyVectorStore,
		graphStore:            opts.GraphStore,
		metadataStore:         opts.MetadataStore,
		embeddingManager:      opts.EmbeddingManager,
		config:                config,
		sparseIndex:           opts.SparseIndex,
		segmenter:             opts.Segmenter,
		pprCache:              map[pprCacheKey]pprCacheEntry{},
		pprCacheTTLSeconds:    300.0,
		pprCacheMaxEntries:    256,
		relationIntentPattern: regexp.MustCompile("(?i)(什么关系|有哪些关系|和.+关系|关联|关系网|subject|predicate|object|relation|related|between.+and)"),
	}
	if retriever.paragraphVectorStore == nil {
		retriever.paragraphVectorStore = retriever.vectorStore
	}
	if retriever.graphVectorStore == nil {
		retriever.graphVectorStore = retriever.vectorStore
	}
	if retriever.graphStore != nil {
		pprConfig := DefaultPageRankConfig()
		pprConfig.Alpha = config.PPRAlpha
		ppr, err := NewPersonalizedPageRank(retriever.graphStore, pprConfig)
		if err != nil {
			return nil, err
		}
		retriever.ppr = ppr
		retriever.graphRelationRecall = NewGraphRelationRecallService(
			retriever.graphStore,
			retriever.metadataStore,
			config.GraphRecall,
		)
	}
	retriever.pprSemaphore = make(chan struct{}, max(1, config.PPRConcurrencyLimit))

	zap.L().Debug("DualPathRetriever 初始化",
		zap.String("strategy", string(config.RetrievalStrategy)),
		zap.Int("top_k_para", config.TopKParagraphs),
		zap.Int("top_k_rel", config.TopKRelations),
	)
	return retriever, nil
}

// SetRuntimeSparseOnly 由运行时控制强制 sparse-only，不改用户配置文件
func (r *DualPathRetriever) SetRuntimeSparseOnly(enabled bool) {
	r.runtimeSparseOnly.Store(enabled)
}

// Config 返回当前配置，供运行时统计与测试读取
func (r *DualPathRetriever) Config() DualPathRetrieverConfig { return r.config }

// RuntimeDiagnostics 返回检索器运行时关键状态，供健康检查与端到端测试定位
func (r *DualPathRetriever) RuntimeDiagnostics() map[string]any {
	return map[string]any{
		"sparse_only":             r.isSparseOnlyRuntime(),
		"runtime_sparse_only":     r.runtimeSparseOnly.Load(),
		"vector_store_present":    r.vectorStore != nil,
		"paragraph_store_present": r.paragraphVectorStore != nil,
		"embedding_present":       r.embeddingManager != nil,
		"metadata_present":        r.metadataStore != nil,
		"vector_pools_mode":       r.config.VectorPools.Mode,
		"sparse_mode":             r.config.Sparse.Mode,
	}
}

// RetrieveOptions 单次检索的可选覆盖项
type RetrieveOptions struct {
	TopK      *int
	Strategy  *RetrievalStrategy
	Temporal  *TemporalQueryOptions
	EnablePPR *bool
	Scope     *RetrievalScope
}

// Retrieve 执行检索，对齐 Python 的 retrieve
func (r *DualPathRetriever) Retrieve(ctx context.Context, query string, opts *RetrieveOptions) ([]RetrievalResult, error) {
	if opts == nil {
		opts = &RetrieveOptions{}
	}
	topK := r.config.TopKFinal
	if opts.TopK != nil {
		topK = *opts.TopK
	}
	if topK == 0 {
		topK = r.config.TopKFinal
	}
	if opts.Scope != nil && opts.Scope.Empty() {
		return []RetrievalResult{}, nil
	}
	strategy := r.config.RetrievalStrategy
	if opts.Strategy != nil {
		strategy = *opts.Strategy
	}
	requestEnablePPR := r.config.EnablePPR
	if opts.EnablePPR != nil {
		requestEnablePPR = *opts.EnablePPR
	}
	relationIntent := r.buildRelationIntentContext(query, topK)

	zap.L().Info("执行检索",
		zap.String("query", truncateRunes(query, 50)+"..."),
		zap.String("strategy", string(strategy)),
		zap.Bool("relation_intent", relationIntent.Enabled),
	)

	if opts.Temporal != nil && strings.TrimSpace(query) == "" {
		return r.retrieveTemporalOnly(opts.Temporal, topK, opts.Scope)
	}

	var results []RetrievalResult
	var err error
	switch strategy {
	case StrategyParagraphOnly:
		results, err = r.retrieveParagraphsOnly(ctx, query, topK, opts.Temporal, opts.Scope)
	case StrategyRelationOnly:
		results, err = r.retrieveRelationsOnly(ctx, query, topK, opts.Temporal, opts.Scope)
	default:
		results, err = r.retrieveDualPath(ctx, query, topK, opts.Temporal, relationIntent.ToMap(), requestEnablePPR, opts.Scope)
	}
	if err != nil {
		return nil, err
	}

	zap.L().Info(fmt.Sprintf("检索完成: 返回 %d 条结果", len(results)))

	if r.config.Debug {
		zap.L().Info("[DEBUG] 检索结果内容原文:")
		for index, item := range results {
			zap.L().Info(fmt.Sprintf("  %d. [%s] (Score: %.4f) %s", index+1, item.ResultType, item.Score, item.Content))
		}
	}
	return results, nil
}

// relationIntentContext 关系意图增强的运行时上下文，对齐 _build_relation_intent_context 的返回字典
type relationIntentContext struct {
	Enabled                    bool
	AlphaOverride              *float64
	RelationTopK               int
	PreserveTopRelations       int
	ForceRelationSparse        bool
	PairPredicateRerankEnabled bool
	PairPredicateLimit         int
}

// ToMap 转为后验图门控使用的字典，alpha_override 未启用时为 nil
func (c relationIntentContext) ToMap() map[string]any {
	var alpha any
	if c.AlphaOverride != nil {
		alpha = *c.AlphaOverride
	}
	return map[string]any{
		"enabled":                       c.Enabled,
		"alpha_override":                alpha,
		"relation_top_k":                c.RelationTopK,
		"preserve_top_relations":        c.PreserveTopRelations,
		"force_relation_sparse":         c.ForceRelationSparse,
		"pair_predicate_rerank_enabled": c.PairPredicateRerankEnabled,
		"pair_predicate_limit":          c.PairPredicateLimit,
	}
}

// relationIntentContextFromMap 从字典还原上下文，便于后验图与测试复用
func relationIntentContextFromMap(values map[string]any) relationIntentContext {
	context := relationIntentContext{
		Enabled:              dualPathBool(values["enabled"], false),
		RelationTopK:         0,
		PreserveTopRelations: 0,
	}
	if value, exists := values["alpha_override"]; exists && value != nil {
		if number, ok := numericValue(value); ok {
			context.AlphaOverride = &number
		}
	}
	if number, ok := numericValue(values["relation_top_k"]); ok {
		context.RelationTopK = int(number)
	}
	if number, ok := numericValue(values["preserve_top_relations"]); ok {
		context.PreserveTopRelations = int(number)
	}
	context.ForceRelationSparse = dualPathBool(values["force_relation_sparse"], false)
	context.PairPredicateRerankEnabled = dualPathBool(values["pair_predicate_rerank_enabled"], false)
	if number, ok := numericValue(values["pair_predicate_limit"]); ok {
		context.PairPredicateLimit = int(number)
	}
	return context
}

// isRelationIntentQuery 对齐 _is_relation_intent_query
func (r *DualPathRetriever) isRelationIntentQuery(query string) bool {
	text := strings.TrimSpace(query)
	if text == "" {
		return false
	}
	if strings.Contains(text, "|") || strings.Contains(text, "->") {
		return true
	}
	return r.relationIntentPattern.MatchString(text)
}

// buildRelationIntentContext 对齐 _build_relation_intent_context
func (r *DualPathRetriever) buildRelationIntentContext(query string, topK int) relationIntentContext {
	cfg := r.config.RelationIntent
	enabled := cfg.Enabled && r.isRelationIntentQuery(query)
	baseRelationK := max(1, r.config.TopKRelations)
	relationTopK := max(baseRelationK, topK)
	if enabled {
		relationTopK = max(relationTopK, relationTopK*cfg.RelationCandidateMultiplier)
	}
	context := relationIntentContext{
		Enabled:                    enabled,
		RelationTopK:               relationTopK,
		PreserveTopRelations:       0,
		ForceRelationSparse:        false,
		PairPredicateRerankEnabled: false,
		PairPredicateLimit:         0,
	}
	if enabled {
		alpha := cfg.AlphaOverride
		context.AlphaOverride = &alpha
		context.PreserveTopRelations = cfg.PreserveTopRelations
		context.ForceRelationSparse = cfg.ForceRelationSparse
		context.PairPredicateRerankEnabled = cfg.PairPredicateRerankEnabled
		context.PairPredicateLimit = cfg.PairPredicateLimit
	}
	return context
}

// capTemporalScanK 对 temporal 模式候选召回数应用 max_scan 上限
func (r *DualPathRetriever) capTemporalScanK(candidateK int, temporal *TemporalQueryOptions) int {
	k := max(1, candidateK)
	if temporal != nil && temporal.MaxScan > 0 {
		k = min(k, temporal.MaxScan)
	}
	return max(1, k)
}

// expandTemporalCandidateK 在范围过滤前扩大候选池
func (r *DualPathRetriever) expandTemporalCandidateK(candidateK int, temporal *TemporalQueryOptions) int {
	multiplier := 1
	if temporal != nil {
		multiplier = max(1, temporal.CandidateMultiplier)
	}
	return r.capTemporalScanK(max(1, candidateK)*multiplier, temporal)
}

// isValidEmbedding 对齐 _is_valid_embedding
func (r *DualPathRetriever) isValidEmbedding(embedding []float32) bool {
	if len(embedding) == 0 {
		return false
	}
	for _, value := range embedding {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}

// embeddingDim 对齐 _get_embedding_dim 的 1D 分支
func (r *DualPathRetriever) embeddingDim(embedding []float32) (int, bool) {
	if len(embedding) == 0 {
		return 0, false
	}
	return len(embedding), true
}

// isEmbeddingDimensionCompatible 对齐 _is_embedding_dimension_compatible
func (r *DualPathRetriever) isEmbeddingDimensionCompatible(embedding []float32) bool {
	gotDim, ok := r.embeddingDim(embedding)
	if !ok {
		return false
	}
	expectedDim := 0
	if r.vectorStore != nil {
		expectedDim = r.vectorStore.Dimension()
	}
	if expectedDim <= 0 {
		return false
	}
	return gotDim == expectedDim
}

// isEmbeddingReadyForVectorSearch 对齐 _is_embedding_ready_for_vector_search
func (r *DualPathRetriever) isEmbeddingReadyForVectorSearch(embedding []float32, stage string) bool {
	if !r.isValidEmbedding(embedding) {
		return false
	}
	if r.isEmbeddingDimensionCompatible(embedding) {
		return true
	}
	expectedDim := 0
	if r.vectorStore != nil {
		expectedDim = r.vectorStore.Dimension()
	}
	gotDim, _ := r.embeddingDim(embedding)
	zap.L().Warn(fmt.Sprintf(
		"metric.embedding_dim_mismatch_fallback_count=1 stage=%s expected_dim=%d got_dim=%d",
		stage, expectedDim, gotDim,
	))
	return false
}

// shouldUseSparse 对齐 _should_use_sparse
func (r *DualPathRetriever) shouldUseSparse(embeddingOK bool, vectorResults []RetrievalResult) bool {
	if !r.config.Sparse.Enabled || r.sparseIndex == nil {
		return false
	}
	switch r.config.Sparse.Mode {
	case "hybrid", "fallback_only":
		return true
	}
	if !embeddingOK {
		return true
	}
	if len(vectorResults) == 0 {
		return true
	}
	best := 0.0
	for _, item := range vectorResults {
		score := item.Score
		if metadata := item.Metadata; metadata != nil {
			if breakdown, ok := metadata["score_breakdown"].(map[string]any); ok {
				if semantic, exists := breakdown["semantic"]; exists {
					if number, ok := numericValue(semantic); ok {
						score = number
					}
				}
			}
		}
		if score > best {
			best = score
		}
	}
	return best < 0.45
}

// shouldUseSparseRelations 对齐 _should_use_sparse_relations
func (r *DualPathRetriever) shouldUseSparseRelations(embeddingOK bool, relationResults []RetrievalResult, forceEnable bool) bool {
	if forceEnable && r.config.Sparse.Enabled && r.sparseIndex != nil {
		return true
	}
	if !r.config.Sparse.EnableRelationSparseFallback {
		return false
	}
	return r.shouldUseSparse(embeddingOK, relationResults)
}

// normalizeScoresMinmax 原地做 minmax 归一，对齐 _normalize_scores_minmax
func (r *DualPathRetriever) normalizeScoresMinmax(results []RetrievalResult) {
	if len(results) == 0 {
		return
	}
	low := results[0].Score
	high := results[0].Score
	for _, item := range results[1:] {
		if item.Score < low {
			low = item.Score
		}
		if item.Score > high {
			high = item.Score
		}
	}
	if high-low < 1e-12 {
		for index := range results {
			results[index].Score = 1.0
		}
		return
	}
	for index := range results {
		results[index].Score = (results[index].Score - low) / (high - low)
	}
}

// buildMinmaxScoreMap 对齐 _build_minmax_score_map
func (r *DualPathRetriever) buildMinmaxScoreMap(results []RetrievalResult) map[string]float64 {
	if len(results) == 0 {
		return map[string]float64{}
	}
	low := results[0].Score
	high := results[0].Score
	for _, item := range results[1:] {
		if item.Score < low {
			low = item.Score
		}
		if item.Score > high {
			high = item.Score
		}
	}
	scoreMap := make(map[string]float64, len(results))
	if high-low < 1e-12 {
		for _, item := range results {
			scoreMap[item.HashValue] = 1.0
		}
		return scoreMap
	}
	for _, item := range results {
		scoreMap[item.HashValue] = (item.Score - low) / (high - low)
	}
	return scoreMap
}

// cloneRetrievalResult 对齐 _clone_retrieval_result，metadata 缺失时给空字典
func cloneRetrievalResult(item RetrievalResult) RetrievalResult {
	cloned := item.Clone()
	if cloned.Metadata == nil {
		cloned.Metadata = map[string]any{}
	}
	cloned.Score = item.Score
	return cloned
}

// sliceTopK 模拟 Python 的 results[:top_k]，支持负数下标
func sliceTopK(results []RetrievalResult, topK int) []RetrievalResult {
	if topK >= len(results) {
		return results
	}
	if topK >= 0 {
		return results[:topK]
	}
	end := len(results) + topK
	if end < 0 {
		end = 0
	}
	return results[:end]
}

// PosteriorGraphConfig 返回后验图配置，供 ApplyPosteriorGraphGate 读取
func (r *DualPathRetriever) PosteriorGraphConfig() *PosteriorGraphConfig {
	return &r.config.PosteriorGraph
}

// Alpha 返回段落与关系的融合权重
func (r *DualPathRetriever) Alpha() float64 { return r.config.Alpha }

// Segmenter 返回中文分词器，nil 时由调用方退化处理
func (r *DualPathRetriever) Segmenter() storage.Segmenter { return r.segmenter }

// GraphRelationRecall 返回图关系召回服务，图不可用时为 nil
func (r *DualPathRetriever) GraphRelationRecall() *GraphRelationRecallService {
	return r.graphRelationRecall
}

// MetadataQuery 执行元数据查询，语义对齐 metadata_store.query
func (r *DualPathRetriever) MetadataQuery(sql string, params ...any) ([]map[string]any, error) {
	if r.metadataStore == nil {
		return nil, fmt.Errorf("metadata_store 未配置")
	}
	return r.metadataStore.Query(sql, params...)
}

// SetPosteriorGraphGateLastDecision 记录最近一次后验图门控判定
func (r *DualPathRetriever) SetPosteriorGraphGateLastDecision(decision map[string]any) {
	r.posteriorGateDecision = decision
}

// PosteriorGraphGateLastDecision 返回最近一次后验图门控判定
func (r *DualPathRetriever) PosteriorGraphGateLastDecision() map[string]any {
	return r.posteriorGateDecision
}

// String 对齐 Python __repr__ 的展示内容
func (r *DualPathRetriever) String() string {
	return fmt.Sprintf("DualPathRetriever(strategy=%s, para_k=%d, rel_k=%d)",
		string(r.config.RetrievalStrategy), r.config.TopKParagraphs, r.config.TopKRelations)
}

// pprCacheKey PPR 缓存键，字段与 _build_ppr_cache_key 的元组一致
type pprCacheKey struct {
	GraphRevision      int64
	NumNodes           int
	NumEdges           int
	Alpha              float64
	LocalEnabled       bool
	LocalMaxNodes      int
	LocalHops          int
	LocalMinGraphNodes int
	EntityKey          string
}

// pprCacheEntry PPR 缓存项
type pprCacheEntry struct {
	ExpiresAt time.Time
	Scores    map[string]float64
}
