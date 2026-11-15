package memruntime

// 检索运行时统一初始化器，对齐 core/runtime/search_runtime_initializer.py
// Action、Tool 与 Command 共用同一套降级与配置解析语义

import (
	"fmt"
	"strings"

	"MaiGo/internal/A_memorix/core/embedding"
	"MaiGo/internal/A_memorix/core/retrieval"
	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/utils"
)

// DefaultSearchRuntimeLoggerName 对齐 _logger 的 logger 名
const DefaultSearchRuntimeLoggerName = "A_Memorix.SearchRuntimeInitializer"

// searchRuntimeRequiredComponents 必填组件键，对齐 _REQUIRED_COMPONENT_KEYS
var searchRuntimeRequiredComponents = []string{"metadata_store"}

// SearchRuntimeBundle 已解析的运行时组件与初始化结果，对齐 SearchRuntimeBundle
type SearchRuntimeBundle struct {
	VectorStore          *storage.VectorStore
	ParagraphVectorStore *storage.VectorStore
	GraphVectorStore     *storage.VectorStore
	LegacyVectorStore    retrieval.VectorSearcher
	GraphStore           *storage.GraphStore
	MetadataStore        *storage.Store
	EmbeddingManager     *embedding.EmbeddingAPIAdapter
	SparseIndex          *retrieval.SparseBM25Index
	Retriever            *retrieval.DualPathRetriever
	ThresholdFilter      *retrieval.DynamicThresholdFilter
	Error                string
}

// Ready 对齐 ready 属性：检索器与元数据核心都就绪
func (b SearchRuntimeBundle) Ready() bool {
	return b.Retriever != nil && b.MetadataStore != nil
}

// SearchRuntimeOptions 承载 Python 的关键字参数与插件实例探测
type SearchRuntimeOptions struct {
	LogPrefix             string
	DualVectorPoolsEnable func() bool
}

// configObjectOrEmpty 对齐 _safe_dict(_get_config_value(...) or {})
func configObjectOrEmpty(config map[string]any, key string) map[string]any {
	value := ConfigValue(config, key, nil)
	if !PyTruthy(value) {
		return map[string]any{}
	}
	return SafeDict(value)
}

// resolveDebugEnabled 对齐 _resolve_debug_enabled
func resolveDebugEnabled(pluginConfig map[string]any) bool {
	advanced := ConfigValue(pluginConfig, "advanced", nil)
	if object, ok := advanced.(map[string]any); ok {
		return PyTruthy(object["debug"])
	}
	return PyTruthy(ConfigValue(pluginConfig, "debug", false))
}

// resolveVectorPoolsReady 对齐 _resolve_vector_pools_ready
func resolveVectorPoolsReady(pluginConfig map[string]any, dualPoolsEnabled func() bool) bool {
	configured := ConfigValue(pluginConfig, "runtime.vector_pools_ready", nil)
	if configured != nil {
		return PyTruthy(configured)
	}
	if dualPoolsEnabled != nil {
		return dualPoolsEnabled()
	}
	components := GetRuntimeComponents()
	if components == nil {
		return false
	}
	return PyTruthy(components["vector_pools_ready"])
}

// runtimeComponentStore 从配置对象里取出向量库
func runtimeComponentStore(config map[string]any, key string) *storage.VectorStore {
	if config == nil {
		return nil
	}
	value, _ := config[key].(*storage.VectorStore)
	return value
}

// runtimeComponentSearcher 从配置对象里取出旧版只读向量视图，对齐 Python 的 Any 语义
func runtimeComponentSearcher(config map[string]any, key string) retrieval.VectorSearcher {
	if config == nil {
		return nil
	}
	return legacyVectorSearcherFromValue(config[key])
}

// resolveRuntimeComponents 对齐 _resolve_runtime_components
func resolveRuntimeComponents(pluginConfig map[string]any) SearchRuntimeBundle {
	bundle := SearchRuntimeBundle{
		VectorStore:          runtimeComponentStore(pluginConfig, "vector_store"),
		ParagraphVectorStore: runtimeComponentStore(pluginConfig, "paragraph_vector_store"),
		GraphVectorStore:     runtimeComponentStore(pluginConfig, "graph_vector_store"),
		LegacyVectorStore:    runtimeComponentSearcher(pluginConfig, "legacy_vector_store"),
	}
	if pluginConfig != nil {
		bundle.GraphStore, _ = pluginConfig["graph_store"].(*storage.GraphStore)
		bundle.MetadataStore, _ = pluginConfig["metadata_store"].(*storage.Store)
		bundle.EmbeddingManager, _ = pluginConfig["embedding_manager"].(*embedding.EmbeddingAPIAdapter)
		bundle.SparseIndex, _ = pluginConfig["sparse_index"].(*retrieval.SparseBM25Index)
	}

	missingRequired := false
	for _, key := range searchRuntimeRequiredComponents {
		if key == "metadata_store" && bundle.MetadataStore == nil {
			missingRequired = true
		}
	}
	if !missingRequired {
		if bundle.ParagraphVectorStore == nil {
			bundle.ParagraphVectorStore = bundle.VectorStore
		}
		if bundle.GraphVectorStore == nil {
			bundle.GraphVectorStore = bundle.VectorStore
		}
		return bundle
	}

	instances := GetRuntimeComponents()
	if len(instances) == 0 {
		return bundle
	}
	fillStore := func(target **storage.VectorStore, key string) {
		if *target != nil {
			return
		}
		*target, _ = instances[key].(*storage.VectorStore)
	}
	fillStore(&bundle.VectorStore, "vector_store")
	fillStore(&bundle.ParagraphVectorStore, "paragraph_vector_store")
	fillStore(&bundle.GraphVectorStore, "graph_vector_store")
	if bundle.LegacyVectorStore == nil {
		bundle.LegacyVectorStore = legacyVectorSearcherFromValue(instances["legacy_vector_store"])
	}
	if bundle.GraphStore == nil {
		bundle.GraphStore, _ = instances["graph_store"].(*storage.GraphStore)
	}
	if bundle.MetadataStore == nil {
		bundle.MetadataStore, _ = instances["metadata_store"].(*storage.Store)
	}
	if bundle.EmbeddingManager == nil {
		bundle.EmbeddingManager, _ = instances["embedding_manager"].(*embedding.EmbeddingAPIAdapter)
	}
	if bundle.SparseIndex == nil {
		bundle.SparseIndex, _ = instances["sparse_index"].(*retrieval.SparseBM25Index)
	}
	if bundle.ParagraphVectorStore == nil {
		bundle.ParagraphVectorStore = bundle.VectorStore
	}
	if bundle.GraphVectorStore == nil {
		bundle.GraphVectorStore = bundle.VectorStore
	}
	return bundle
}

// decodeSparseConfig 对齐 SparseBM25Config(**raw)，未知键按关键字参数报错
func decodeSparseConfig(raw map[string]any) (retrieval.SparseBM25Config, error) {
	config := retrieval.NewSparseBM25Config()
	setters := newSetters().
		boolean("enabled", &config.Enabled).
		text("backend", &config.Backend).
		boolean("lazy_load", &config.LazyLoad).
		text("mode", &config.Mode).
		text("tokenizer_mode", &config.TokenizerMode).
		text("jieba_user_dict", &config.JiebaUserDict).
		int("char_ngram_n", &config.CharNgramN).
		int("candidate_k", &config.CandidateK).
		int("max_doc_len", &config.MaxDocLen).
		boolean("enable_tokenized_shadow_index", &config.EnableTokenizedShadowIndex).
		boolean("enable_ngram_fallback_index", &config.EnableNgramFallbackIndex).
		boolean("enable_like_fallback", &config.EnableLikeFallback).
		boolean("enable_relation_sparse_fallback", &config.EnableRelationSparseFallback).
		int("relation_candidate_k", &config.RelationCandidateK).
		int("relation_max_doc_len", &config.RelationMaxDocLen).
		boolean("unload_on_disable", &config.UnloadOnDisable).
		boolean("shrink_memory_on_unload", &config.ShrinkMemoryOnUnload)
	if err := setters.apply("SparseBM25Config", raw); err != nil {
		return retrieval.NewSparseBM25Config(), err
	}
	config.Normalize()
	if err := config.Validate(); err != nil {
		return retrieval.NewSparseBM25Config(), err
	}
	return config, nil
}

// decodeFusionConfig 对齐 FusionConfig(**raw)
func decodeFusionConfig(raw map[string]any) (retrieval.FusionConfig, error) {
	config := retrieval.DefaultFusionConfig()
	setters := newSetters().
		text("method", &config.Method).
		int("rrf_k", &config.RRFK).
		float("vector_weight", &config.VectorWeight).
		float("bm25_weight", &config.BM25Weight).
		boolean("normalize_score", &config.NormalizeScore).
		text("normalize_method", &config.NormalizeMethod)
	if err := setters.apply("FusionConfig", raw); err != nil {
		return retrieval.DefaultFusionConfig(), err
	}
	config.Normalize()
	return config, nil
}

// decodeRelationIntentConfig 对齐 RelationIntentConfig(**raw)
func decodeRelationIntentConfig(raw map[string]any) (retrieval.RelationIntentConfig, error) {
	config := retrieval.DefaultRelationIntentConfig()
	setters := newSetters().
		boolean("enabled", &config.Enabled).
		float("alpha_override", &config.AlphaOverride).
		int("relation_candidate_multiplier", &config.RelationCandidateMultiplier).
		int("preserve_top_relations", &config.PreserveTopRelations).
		boolean("force_relation_sparse", &config.ForceRelationSparse).
		boolean("pair_predicate_rerank_enabled", &config.PairPredicateRerankEnabled).
		int("pair_predicate_limit", &config.PairPredicateLimit)
	if err := setters.apply("RelationIntentConfig", raw); err != nil {
		return retrieval.DefaultRelationIntentConfig(), err
	}
	config.Normalize()
	return config, nil
}

// decodeGraphRecallConfig 对齐 GraphRelationRecallConfig(**raw)
func decodeGraphRecallConfig(raw map[string]any) (retrieval.GraphRelationRecallConfig, error) {
	config := retrieval.DefaultGraphRelationRecallConfig()
	setters := newSetters().
		boolean("enabled", &config.Enabled).
		int("candidate_k", &config.CandidateK).
		int("max_hop", &config.MaxHop).
		boolean("allow_two_hop_pair", &config.AllowTwoHopPair).
		int("max_paths", &config.MaxPaths)
	if err := setters.apply("GraphRelationRecallConfig", raw); err != nil {
		return retrieval.DefaultGraphRelationRecallConfig(), err
	}
	config.Normalize()
	return config, nil
}

// decodePosteriorGraphConfig 对齐 PosteriorGraphConfig(**raw)
func decodePosteriorGraphConfig(raw map[string]any) (retrieval.PosteriorGraphConfig, error) {
	config := retrieval.DefaultPosteriorGraphConfig()
	setters := newSetters().
		boolean("enabled", &config.Enabled).
		float("drop_ratio", &config.DropRatio).
		int("min_core_results", &config.MinCoreResults).
		int("max_graph_slots", &config.MaxGraphSlots).
		int("gate_scan_top_k", &config.GateScanTopK).
		float("grounded_confidence_threshold", &config.GroundedConfidenceThreshold).
		float("incidental_confidence_threshold", &config.IncidentalConfidenceThreshold).
		float("min_query_token_coverage", &config.MinQueryTokenCoverage).
		float("incidental_query_relevance_threshold", &config.IncidentalQueryRelevanceThreshold).
		float("incidental_core_overlap_threshold", &config.IncidentalCoreOverlapThreshold).
		float("incidental_specificity_threshold", &config.IncidentalSpecificityThreshold).
		float("query_weight", &config.QueryWeight).
		float("novelty_weight", &config.NoveltyWeight).
		float("complementarity_weight", &config.ComplementarityWeight).
		float("specificity_weight", &config.SpecificityWeight).
		float("gap_fill_weight", &config.GapFillWeight).
		int("max_candidate_tokens", &config.MaxCandidateTokens)
	if err := setters.apply("PosteriorGraphConfig", raw); err != nil {
		return retrieval.DefaultPosteriorGraphConfig(), err
	}
	config.Normalize()
	return config, nil
}

// decodeVectorPoolsConfig 对齐 VectorPoolsConfig(**raw)
func decodeVectorPoolsConfig(raw map[string]any) (retrieval.VectorPoolsConfig, error) {
	config := retrieval.DefaultVectorPoolsConfig()
	setters := newSetters().
		text("mode", &config.Mode).
		int("paragraph_top_k", &config.ParagraphTopK).
		int("graph_top_k", &config.GraphTopK).
		int("graph_expand_paragraph_k", &config.GraphExpandParagraphK).
		int("relation_expand_per_hit", &config.RelationExpandPerHit).
		int("entity_expand_per_hit", &config.EntityExpandPerHit).
		float("relation_evidence_weight", &config.RelationEvidenceWeight).
		float("entity_evidence_weight", &config.EntityEvidenceWeight).
		float("semantic_weight", &config.SemanticWeight).
		float("sparse_weight", &config.SparseWeight).
		float("graph_weight", &config.GraphWeight).
		text("score_calibration_method", &config.ScoreCalibrationMethod).
		int("score_calibration_rrf_k", &config.ScoreCalibrationRRFK).
		int("relation_intent_graph_top_k", &config.RelationIntentGraphTopK).
		float("relation_intent_semantic_weight", &config.RelationIntentSemanticWeight).
		float("relation_intent_sparse_weight", &config.RelationIntentSparseWeight).
		float("relation_intent_graph_weight", &config.RelationIntentGraphWeight).
		boolean("return_relation_items", &config.ReturnRelationItems).
		custom("relation_intent", func(value any) error {
			config.RelationIntent = SafeDict(value)
			return nil
		})
	if err := setters.apply("VectorPoolsConfig", raw); err != nil {
		return retrieval.DefaultVectorPoolsConfig(), err
	}
	if err := config.Normalize(); err != nil {
		return retrieval.DefaultVectorPoolsConfig(), err
	}
	return config, nil
}

// configInt 对齐对 int 配置项的取值与报错
func configInt(pluginConfig map[string]any, key string, defaultValue int) int {
	value, err := PyInt(ConfigValue(pluginConfig, key, defaultValue))
	if err != nil {
		return defaultValue
	}
	return value
}

// configFloat 对齐对 float 配置项的取值与报错
func configFloat(pluginConfig map[string]any, key string, defaultValue float64) float64 {
	value, err := PyFloat(ConfigValue(pluginConfig, key, defaultValue))
	if err != nil {
		return defaultValue
	}
	return value
}

// configBool 对齐 bool(...) 的真值转换
func configBool(pluginConfig map[string]any, key string, defaultValue bool) bool {
	return PyTruthy(ConfigValue(pluginConfig, key, defaultValue))
}

// BuildSearchRuntime 对齐 build_search_runtime
func BuildSearchRuntime(pluginConfig map[string]any, logger utils.Logger, ownerTag string) SearchRuntimeBundle {
	return BuildSearchRuntimeWithOptions(pluginConfig, logger, ownerTag, SearchRuntimeOptions{})
}

// BuildSearchRuntimeWithOptions 支持日志前缀与插件实例双池探测
func BuildSearchRuntimeWithOptions(pluginConfig map[string]any, logger utils.Logger, ownerTag string, opts SearchRuntimeOptions) SearchRuntimeBundle {
	log := utils.OrLogger(logger)
	owner := strings.ToLower(strings.TrimSpace(ownerTag))
	if owner == "" {
		owner = "runtime"
	}
	prefix := strings.TrimSpace(opts.LogPrefix)
	prefixText := ""
	if prefix != "" {
		prefixText = prefix + " "
	}
	tag := fmt.Sprintf("%s[%s]", prefixText, owner)

	runtime := resolveRuntimeComponents(pluginConfig)
	if runtime.MetadataStore == nil {
		runtime.Error = "元数据核心未初始化"
		log.Warning(tag + " 元数据核心未初始化，无法构建检索运行时")
		return runtime
	}

	sparseRaw := configObjectOrEmpty(pluginConfig, "retrieval.sparse")
	fusionRaw := configObjectOrEmpty(pluginConfig, "retrieval.fusion")
	relationIntentRaw := configObjectOrEmpty(pluginConfig, "retrieval.search.relation_intent")
	graphRecallRaw := configObjectOrEmpty(pluginConfig, "retrieval.search.graph_recall")
	posteriorGraphRaw := configObjectOrEmpty(pluginConfig, "retrieval.search.posterior_graph")
	vectorPoolsRaw := configObjectOrEmpty(pluginConfig, "retrieval.vector_pools")

	rawMode := "dual"
	if value, ok := vectorPoolsRaw["mode"]; ok && PyTruthy(value) {
		rawMode = PyStr(value)
	}
	if strings.ToLower(strings.TrimSpace(rawMode)) == "dual" && !resolveVectorPoolsReady(pluginConfig, opts.DualVectorPoolsEnable) {
		copied := make(map[string]any, len(vectorPoolsRaw))
		for key, value := range vectorPoolsRaw {
			copied[key] = value
		}
		copied["mode"] = "single"
		vectorPoolsRaw = copied
		log.Debug(tag + " 双池向量尚未就绪，向量检索是否可用取决于单池加载状态")
	}

	sparseCfg, err := decodeSparseConfig(sparseRaw)
	if err != nil {
		log.Warning(fmt.Sprintf("%s sparse 配置非法，回退默认: %v", tag, err))
	}
	fusionCfg, err := decodeFusionConfig(fusionRaw)
	if err != nil {
		log.Warning(fmt.Sprintf("%s fusion 配置非法，回退默认: %v", tag, err))
	}
	relationIntentCfg, err := decodeRelationIntentConfig(relationIntentRaw)
	if err != nil {
		log.Warning(fmt.Sprintf("%s relation_intent 配置非法，回退默认: %v", tag, err))
	}
	graphRecallCfg, err := decodeGraphRecallConfig(graphRecallRaw)
	if err != nil {
		log.Warning(fmt.Sprintf("%s graph_recall 配置非法，回退默认: %v", tag, err))
	}
	posteriorGraphCfg, err := decodePosteriorGraphConfig(posteriorGraphRaw)
	if err != nil {
		log.Warning(fmt.Sprintf("%s posterior_graph 配置非法，回退默认: %v", tag, err))
	}
	vectorPoolsCfg, err := decodeVectorPoolsConfig(vectorPoolsRaw)
	if err != nil {
		log.Warning(fmt.Sprintf("%s vector_pools 配置非法，回退默认: %v", tag, err))
	}

	config := retrieval.DefaultDualPathRetrieverConfig()
	config.TopKParagraphs = configInt(pluginConfig, "retrieval.top_k_paragraphs", 20)
	config.TopKRelations = configInt(pluginConfig, "retrieval.top_k_relations", 10)
	config.TopKFinal = configInt(pluginConfig, "retrieval.top_k_final", 10)
	config.Alpha = configFloat(pluginConfig, "retrieval.alpha", 0.5)
	config.EnablePPR = configBool(pluginConfig, "retrieval.enable_ppr", true)
	config.PPRAlpha = configFloat(pluginConfig, "retrieval.ppr_alpha", 0.85)
	config.PPRTimeoutSeconds = configFloat(pluginConfig, "retrieval.ppr_timeout_seconds", 1.5)
	config.PPRConcurrencyLimit = configInt(pluginConfig, "retrieval.ppr_concurrency_limit", 4)
	config.PPRLocalEnabled = configBool(pluginConfig, "retrieval.ppr_local_enabled", true)
	config.PPRLocalMaxNodes = configInt(pluginConfig, "retrieval.ppr_local_max_nodes", 256)
	config.PPRLocalHops = configInt(pluginConfig, "retrieval.ppr_local_hops", 2)
	config.PPRLocalMinGraphNodes = configInt(pluginConfig, "retrieval.ppr_local_min_graph_nodes", 128)
	config.EnableParallel = configBool(pluginConfig, "retrieval.enable_parallel", true)
	config.RetrievalStrategy = retrieval.StrategyDualPath
	config.Debug = resolveDebugEnabled(pluginConfig)
	config.Sparse = sparseCfg
	config.Fusion = fusionCfg
	config.RelationIntent = relationIntentCfg
	config.VectorPools = vectorPoolsCfg
	config.GraphRecall = graphRecallCfg
	config.PosteriorGraph = posteriorGraphCfg

	paragraphStore := runtime.ParagraphVectorStore
	if paragraphStore == nil {
		paragraphStore = runtime.VectorStore
	}
	graphVectorStore := runtime.GraphVectorStore
	if graphVectorStore == nil {
		graphVectorStore = runtime.VectorStore
	}

	retriever, retrieverErr := retrieval.NewDualPathRetriever(retrieval.DualPathRetrieverOptions{
		VectorStore:          newVectorSearcher(runtime.VectorStore),
		ParagraphVectorStore: newVectorSearcher(paragraphStore),
		GraphVectorStore:     newVectorSearcher(graphVectorStore),
		LegacyVectorStore:    runtime.LegacyVectorStore,
		GraphStore:           retrieval.NewStorageGraphAdapter(runtime.GraphStore),
		MetadataStore:        newMetadataStore(runtime.MetadataStore),
		EmbeddingManager:     newEmbeddingEncoder(runtime.EmbeddingManager),
		SparseIndex:          newSparseIndex(runtime.SparseIndex),
		Config:               &config,
	})
	if retrieverErr != nil {
		runtime.Retriever = nil
		runtime.ThresholdFilter = nil
		runtime.Error = retrieverErr.Error()
		log.Error(fmt.Sprintf("%s 检索运行时初始化失败: %v", tag, retrieverErr))
		return runtime
	}
	retriever.SetRuntimeSparseOnly(runtime.VectorStore == nil || runtime.EmbeddingManager == nil)

	thresholdConfig := retrieval.DefaultThresholdConfig()
	thresholdConfig.Method = retrieval.ThresholdAdaptive
	thresholdConfig.MinThreshold = configFloat(pluginConfig, "threshold.min_threshold", 0.29)
	thresholdConfig.MaxThreshold = configFloat(pluginConfig, "threshold.max_threshold", 0.95)
	thresholdConfig.Percentile = configFloat(pluginConfig, "threshold.percentile", 75.0)
	thresholdConfig.StdMultiplier = configFloat(pluginConfig, "threshold.std_multiplier", 1.5)
	thresholdConfig.MinResults = configInt(pluginConfig, "threshold.min_results", 4)
	thresholdFilter, thresholdErr := retrieval.NewDynamicThresholdFilter(thresholdConfig)
	if thresholdErr != nil {
		runtime.Retriever = nil
		runtime.ThresholdFilter = nil
		runtime.Error = thresholdErr.Error()
		log.Error(fmt.Sprintf("%s 检索运行时初始化失败: %v", tag, thresholdErr))
		return runtime
	}

	runtime.Retriever = retriever
	runtime.ThresholdFilter = thresholdFilter
	runtime.Error = ""
	log.Info(tag + " 检索运行时就绪")
	return runtime
}

// SearchRuntimeInitializer 函数式初始化器的兼容包装类
type SearchRuntimeInitializer struct{}

// BuildSearchRuntime 对齐静态方法 build_search_runtime
func (SearchRuntimeInitializer) BuildSearchRuntime(pluginConfig map[string]any, logger utils.Logger, ownerTag string, logPrefix string) SearchRuntimeBundle {
	return BuildSearchRuntimeWithOptions(pluginConfig, logger, ownerTag, SearchRuntimeOptions{LogPrefix: logPrefix})
}
