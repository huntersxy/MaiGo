package utils

// A_memorix 检索调优参数 schema 与规范化，对齐 Python core/utils/retrieval_tuning_manager.py
// 默认值、取值边界与 TOML 导出文案逐条对齐；配置读取同时兼容嵌套字典与点号扁平键

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// 调优目标函数取值，对齐 OBJECTIVES
const (
	RetrievalTuningObjectivePrecisionPriority = "precision_priority"
	RetrievalTuningObjectiveBalanced          = "balanced"
	RetrievalTuningObjectiveRecallPriority    = "recall_priority"
)

// 调优强度取值，对齐 INTENSITIES
const (
	RetrievalTuningIntensityQuick    = "quick"
	RetrievalTuningIntensityStandard = "standard"
	RetrievalTuningIntensityDeep     = "deep"
)

// 查询用例分类，对齐 CATEGORIES
const (
	RetrievalTuningCategoryQueryNL     = "query_nl"
	RetrievalTuningCategoryQueryKW     = "query_kw"
	RetrievalTuningCategorySPORelation = "spo_relation"
	RetrievalTuningCategorySPOSearch   = "spo_search"
)

// 验证阈值，对齐 MIN_VALIDATION_SCORE_DELTA 等常量
const (
	RetrievalTuningMinValidationScoreDelta = 0.02
	RetrievalTuningMaxPrecisionDrop        = 0.01
	RetrievalTuningMaxRecallDrop           = 0.01
	RetrievalTuningMaxEmptyRateIncrease    = 0.05
	RetrievalTuningMaxLatencyIncreaseMS    = 100.0
	RetrievalTuningMaxLatencyIncreaseRatio = 0.50
)

// RetrievalTuningIntensityRounds 对齐 INTENSITIES，返回强度对应默认轮次
func RetrievalTuningIntensityRounds(intensity string) (int, bool) {
	switch intensity {
	case RetrievalTuningIntensityQuick:
		return 8, true
	case RetrievalTuningIntensityStandard:
		return 20, true
	case RetrievalTuningIntensityDeep:
		return 32, true
	default:
		return 0, false
	}
}

// IsRetrievalTuningObjective 判断目标函数是否合法
func IsRetrievalTuningObjective(objective string) bool {
	switch objective {
	case RetrievalTuningObjectivePrecisionPriority, RetrievalTuningObjectiveBalanced, RetrievalTuningObjectiveRecallPriority:
		return true
	default:
		return false
	}
}

// IsRetrievalTuningCategory 判断用例分类是否合法
func IsRetrievalTuningCategory(category string) bool {
	switch category {
	case RetrievalTuningCategoryQueryNL, RetrievalTuningCategoryQueryKW,
		RetrievalTuningCategorySPORelation, RetrievalTuningCategorySPOSearch:
		return true
	default:
		return false
	}
}

// RetrievalTuningNonTunedInfluencers 对齐 NON_TUNED_RETRIEVAL_INFLUENCERS
func RetrievalTuningNonTunedInfluencers() []string {
	return []string{
		"embedding.*",
		"retrieval.sparse.tokenizer_mode",
		"retrieval.relation_vectorization.*",
		"retrieval.search.relation_intent.*",
		"retrieval.search.graph_recall.*",
		"retrieval.search.posterior_graph.*",
		"retrieval.aggregate.*",
		"filter.*",
		"episode.*",
		"person_profile.*",
		"integration.memory_query_default_limit",
		"integration.heuristic_memory_*",
	}
}

// RetrievalTuningRuntimeConfigInstanceKeys 对齐 _RUNTIME_CONFIG_INSTANCE_KEYS
func RetrievalTuningRuntimeConfigInstanceKeys() []string {
	return []string{
		"vector_store",
		"graph_store",
		"metadata_store",
		"embedding_manager",
		"sparse_index",
		"relation_write_service",
		"plugin_instance",
	}
}

// retrievalTuningNestedGet 对齐 _nested_get，按点号路径读取嵌套字典
func retrievalTuningNestedGet(data map[string]any, key string, defaultValue any) any {
	var current any = data
	for _, part := range strings.Split(key, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return defaultValue
		}
		value, exists := object[part]
		if !exists {
			return defaultValue
		}
		current = value
	}
	return current
}

// retrievalTuningNestedSet 对齐 _nested_set，按点号路径写入嵌套字典
func retrievalTuningNestedSet(data map[string]any, key string, value any) {
	parts := strings.Split(key, ".")
	current := data
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}

// retrievalTuningDeepMerge 对齐 _deep_merge，返回深合并后的新字典
func retrievalTuningDeepMerge(base map[string]any, patch map[string]any) map[string]any {
	out := retrievalTuningDeepCopy(base)
	for key, value := range patch {
		if patchObject, ok := value.(map[string]any); ok {
			if baseObject, ok := out[key].(map[string]any); ok {
				out[key] = retrievalTuningDeepMerge(baseObject, patchObject)
				continue
			}
		}
		out[key] = retrievalTuningDeepCopyValue(value)
	}
	return out
}

// retrievalTuningDeepCopy 深拷贝字典
func retrievalTuningDeepCopy(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = retrievalTuningDeepCopyValue(value)
	}
	return out
}

// retrievalTuningDeepCopyValue 深拷贝单个值，字典与切片递归复制
func retrievalTuningDeepCopyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return retrievalTuningDeepCopy(typed)
	case []any:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = retrievalTuningDeepCopyValue(item)
		}
		return items
	default:
		return value
	}
}

// retrievalTuningClampInt 对齐 _clamp_int
func retrievalTuningClampInt(value any, defaultValue int, minValue int, maxValue int) int {
	parsed, err := retrievalTuningToInt(value)
	if err != nil {
		parsed = defaultValue
	}
	return maxIntValue(minValue, minIntValue(maxValue, parsed))
}

// retrievalTuningClampFloat 对齐 _clamp_float
func retrievalTuningClampFloat(value any, defaultValue float64, minValue float64, maxValue float64) float64 {
	parsed, err := pyFloat64(value)
	if err != nil || math.IsNaN(parsed) {
		parsed = defaultValue
	}
	return math.Max(minValue, math.Min(maxValue, parsed))
}

// retrievalTuningToInt 对齐 Python int(value)
func retrievalTuningToInt(value any) (int, error) {
	switch typed := value.(type) {
	case bool:
		if typed {
			return 1, nil
		}
		return 0, nil
	case int:
		return typed, nil
	case int8:
		return int(typed), nil
	case int16:
		return int(typed), nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case uint:
		return int(typed), nil
	case uint8:
		return int(typed), nil
	case uint16:
		return int(typed), nil
	case uint32:
		return int(typed), nil
	case uint64:
		return int(typed), nil
	case float32:
		return retrievalTuningFloatToInt(float64(typed))
	case float64:
		return retrievalTuningFloatToInt(typed)
	case string:
		return retrievalTuningParseInt(typed)
	default:
		return 0, fmt.Errorf("int() argument must be a string or a number: %v", value)
	}
}

// retrievalTuningFloatToInt 对齐 int(float)，NaN 与无穷报错
func retrievalTuningFloatToInt(value float64) (int, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("cannot convert float to int: %v", value)
	}
	return int(math.Trunc(value)), nil
}

// retrievalTuningParseInt 对齐 int(str)，允许正负号、数字间下划线
func retrievalTuningParseInt(text string) (int, error) {
	trimmed := pyStrip(text)
	if trimmed == "" {
		return 0, fmt.Errorf("invalid literal for int(): ''")
	}
	sign := 1
	switch trimmed[0] {
	case '+':
		trimmed = trimmed[1:]
	case '-':
		sign = -1
		trimmed = trimmed[1:]
	}
	if trimmed == "" || strings.HasPrefix(trimmed, "_") || strings.HasSuffix(trimmed, "_") || strings.Contains(trimmed, "__") {
		return 0, fmt.Errorf("invalid literal for int(): %q", text)
	}
	digits := strings.ReplaceAll(trimmed, "_", "")
	for _, char := range digits {
		if char < '0' || char > '9' {
			return 0, fmt.Errorf("invalid literal for int(): %q", text)
		}
	}
	parsed, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, err
	}
	return sign * int(parsed), nil
}

// retrievalTuningCoerceBool 对齐 _coerce_bool
func retrievalTuningCoerceBool(value any, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case int:
		return typed != 0
	case int8:
		return typed != 0
	case int16:
		return typed != 0
	case int32:
		return typed != 0
	case int64:
		return typed != 0
	case uint:
		return typed != 0
	case uint8:
		return typed != 0
	case uint16:
		return typed != 0
	case uint32:
		return typed != 0
	case uint64:
		return typed != 0
	case float32:
		return typed != 0
	case float64:
		return typed != 0
	}
	text := pyLower(pyStrip(importValueText(value)))
	switch text {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return defaultValue
	}
}

// RetrievalTuningSafeJSONLoads 对齐 _safe_json_loads，失败返回 nil
// 与 SafeJSONLoads 的差异：这里允许顶层是数组，且返回任意 JSON 值
func RetrievalTuningSafeJSONLoads(text string) any {
	raw := pyStrip(text)
	if raw == "" {
		return nil
	}
	if strings.Contains(raw, "```") {
		raw = strings.ReplaceAll(raw, "```json", "```")
		for _, segment := range strings.Split(raw, "```") {
			segment = pyStrip(segment)
			if strings.HasPrefix(segment, "{") || strings.HasPrefix(segment, "[") {
				raw = segment
				break
			}
		}
	}
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err == nil {
		return payload
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(raw[start:end+1]), &payload); err == nil {
			return payload
		}
	}
	return nil
}

// retrievalTuningConfigValue 按嵌套路径再按点号扁平键读取配置
func retrievalTuningConfigValue(config map[string]any, key string, defaultValue any) any {
	if config == nil {
		return defaultValue
	}
	if value := retrievalTuningNestedGet(config, key, nil); value != nil {
		return value
	}
	if value, ok := config[key]; ok && value != nil {
		return value
	}
	return defaultValue
}

// RetrievalTuningProfileSnapshot 对齐 get_profile_snapshot，从插件配置提取参数快照
func RetrievalTuningProfileSnapshot(config map[string]any) map[string]any {
	get := func(key string, defaultValue any) any {
		return retrievalTuningConfigValue(config, key, defaultValue)
	}
	profile := map[string]any{
		"retrieval": map[string]any{
			"top_k_paragraphs":    get("retrieval.top_k_paragraphs", 20),
			"top_k_relations":     get("retrieval.top_k_relations", 10),
			"top_k_final":         get("retrieval.top_k_final", 10),
			"alpha":               get("retrieval.alpha", 0.5),
			"enable_ppr":          get("retrieval.enable_ppr", true),
			"ppr_alpha":           get("retrieval.ppr_alpha", 0.85),
			"ppr_timeout_seconds": get("retrieval.ppr_timeout_seconds", 1.5),
			"search": map[string]any{
				"smart_fallback": map[string]any{
					"enabled": get("retrieval.search.smart_fallback.enabled", true),
				},
			},
			"sparse": map[string]any{
				"enabled":              get("retrieval.sparse.enabled", true),
				"mode":                 get("retrieval.sparse.mode", "auto"),
				"candidate_k":          get("retrieval.sparse.candidate_k", 80),
				"relation_candidate_k": get("retrieval.sparse.relation_candidate_k", 60),
			},
			"fusion": map[string]any{
				"method":        get("retrieval.fusion.method", "weighted_rrf"),
				"rrf_k":         get("retrieval.fusion.rrf_k", 60),
				"vector_weight": get("retrieval.fusion.vector_weight", 0.7),
				"bm25_weight":   get("retrieval.fusion.bm25_weight", 0.3),
			},
			"vector_pools": map[string]any{
				"mode":                     get("retrieval.vector_pools.mode", "single"),
				"paragraph_top_k":          get("retrieval.vector_pools.paragraph_top_k", 20),
				"graph_top_k":              get("retrieval.vector_pools.graph_top_k", 40),
				"graph_expand_paragraph_k": get("retrieval.vector_pools.graph_expand_paragraph_k", 80),
				"relation_expand_per_hit":  get("retrieval.vector_pools.relation_expand_per_hit", 5),
				"entity_expand_per_hit":    get("retrieval.vector_pools.entity_expand_per_hit", 8),
				"relation_evidence_weight": get("retrieval.vector_pools.relation_evidence_weight", 1.0),
				"entity_evidence_weight":   get("retrieval.vector_pools.entity_evidence_weight", 0.55),
				"semantic_weight":          get("retrieval.vector_pools.semantic_weight", 0.65),
				"sparse_weight":            get("retrieval.vector_pools.sparse_weight", 0.2),
				"graph_weight":             get("retrieval.vector_pools.graph_weight", 0.15),
				"relation_intent": map[string]any{
					"graph_top_k":     get("retrieval.vector_pools.relation_intent.graph_top_k", 80),
					"semantic_weight": get("retrieval.vector_pools.relation_intent.semantic_weight", 0.45),
					"sparse_weight":   get("retrieval.vector_pools.relation_intent.sparse_weight", 0.15),
					"graph_weight":    get("retrieval.vector_pools.relation_intent.graph_weight", 0.4),
				},
			},
		},
		"threshold": map[string]any{
			"min_threshold": get("threshold.min_threshold", 0.29),
			"max_threshold": get("threshold.max_threshold", 0.95),
			"percentile":    get("threshold.percentile", 75.0),
			"min_results":   get("threshold.min_results", 4),
		},
	}
	return NormalizeRetrievalProfile(profile, profile)
}

// retrievalTuningPick 对齐 _normalize_profile 内的 pick 闭包
func retrievalTuningPick(raw map[string]any, base map[string]any, path string, defaultValue any) any {
	if value := retrievalTuningNestedGet(raw, path, nil); value != nil {
		return value
	}
	if value, ok := raw[path]; ok {
		return value
	}
	return retrievalTuningNestedGet(base, path, defaultValue)
}

// retrievalTuningNormalizeEnum 对齐枚举字段的大小写归一与回退
func retrievalTuningNormalizeEnum(value any, allowed []string, fallback string) string {
	text := pyLower(pyStrip(pyStr(value)))
	if text == "" {
		text = fallback
	}
	for _, item := range allowed {
		if text == item {
			return text
		}
	}
	return fallback
}

// NormalizeRetrievalProfile 对齐 _normalize_profile，输出固定结构的参数快照
// fallback 为 nil 时等价于从空配置读取快照
func NormalizeRetrievalProfile(profile map[string]any, fallback map[string]any) map[string]any {
	raw := retrievalTuningDeepCopy(profile)
	base := retrievalTuningDeepCopy(fallback)
	if fallback == nil {
		base = retrievalTuningDeepCopy(RetrievalTuningProfileSnapshot(nil))
	}

	pick := func(path string, defaultValue any) any {
		return retrievalTuningPick(raw, base, path, defaultValue)
	}

	fusionMethod := retrievalTuningNormalizeEnum(pick("retrieval.fusion.method", "weighted_rrf"), []string{"weighted_rrf", "alpha_legacy"}, "weighted_rrf")
	sparseMode := retrievalTuningNormalizeEnum(pick("retrieval.sparse.mode", "auto"), []string{"auto", "hybrid", "fallback_only"}, "auto")
	vectorPoolMode := retrievalTuningNormalizeEnum(pick("retrieval.vector_pools.mode", "single"), []string{"single", "dual"}, "single")

	vectorWeight := retrievalTuningClampFloat(pick("retrieval.fusion.vector_weight", 0.7), 0.7, 0.0, 1.0)
	bm25Weight := retrievalTuningClampFloat(pick("retrieval.fusion.bm25_weight", 0.3), 0.3, 0.0, 1.0)
	if total := vectorWeight + bm25Weight; total <= 1e-9 {
		vectorWeight, bm25Weight = 0.7, 0.3
	} else {
		vectorWeight, bm25Weight = vectorWeight/total, bm25Weight/total
	}

	semanticWeight := retrievalTuningClampFloat(pick("retrieval.vector_pools.semantic_weight", 0.65), 0.65, 0.0, 1.0)
	sparseWeight := retrievalTuningClampFloat(pick("retrieval.vector_pools.sparse_weight", 0.2), 0.2, 0.0, 1.0)
	graphWeight := retrievalTuningClampFloat(pick("retrieval.vector_pools.graph_weight", 0.15), 0.15, 0.0, 1.0)
	if total := semanticWeight + sparseWeight + graphWeight; total <= 1e-9 {
		semanticWeight, sparseWeight, graphWeight = 0.65, 0.2, 0.15
	} else {
		semanticWeight, sparseWeight, graphWeight = semanticWeight/total, sparseWeight/total, graphWeight/total
	}

	intentSemantic := retrievalTuningClampFloat(pick("retrieval.vector_pools.relation_intent.semantic_weight", 0.45), 0.45, 0.0, 1.0)
	intentSparse := retrievalTuningClampFloat(pick("retrieval.vector_pools.relation_intent.sparse_weight", 0.15), 0.15, 0.0, 1.0)
	intentGraph := retrievalTuningClampFloat(pick("retrieval.vector_pools.relation_intent.graph_weight", 0.4), 0.4, 0.0, 1.0)
	if total := intentSemantic + intentSparse + intentGraph; total <= 1e-9 {
		intentSemantic, intentSparse, intentGraph = 0.45, 0.15, 0.4
	} else {
		intentSemantic, intentSparse, intentGraph = intentSemantic/total, intentSparse/total, intentGraph/total
	}

	minThreshold := retrievalTuningClampFloat(pick("threshold.min_threshold", 0.29), 0.29, 0.0, 1.0)
	maxThreshold := retrievalTuningClampFloat(pick("threshold.max_threshold", 0.95), 0.95, 0.0, 1.0)
	if minThreshold >= maxThreshold {
		minThreshold, maxThreshold = 0.29, 0.95
	}

	return map[string]any{
		"retrieval": map[string]any{
			"top_k_paragraphs":    retrievalTuningClampInt(pick("retrieval.top_k_paragraphs", 20), 20, 10, 1200),
			"top_k_relations":     retrievalTuningClampInt(pick("retrieval.top_k_relations", 10), 10, 4, 512),
			"top_k_final":         retrievalTuningClampInt(pick("retrieval.top_k_final", 10), 10, 4, 512),
			"alpha":               retrievalTuningClampFloat(pick("retrieval.alpha", 0.5), 0.5, 0.0, 1.0),
			"enable_ppr":          retrievalTuningCoerceBool(pick("retrieval.enable_ppr", true), true),
			"ppr_alpha":           retrievalTuningClampFloat(pick("retrieval.ppr_alpha", 0.85), 0.85, 0.1, 0.99),
			"ppr_timeout_seconds": retrievalTuningClampFloat(pick("retrieval.ppr_timeout_seconds", 1.5), 1.5, 0.1, 10.0),
			"search": map[string]any{
				"smart_fallback": map[string]any{
					"enabled": retrievalTuningCoerceBool(pick("retrieval.search.smart_fallback.enabled", true), true),
				},
			},
			"sparse": map[string]any{
				"enabled":              retrievalTuningCoerceBool(pick("retrieval.sparse.enabled", true), true),
				"mode":                 sparseMode,
				"candidate_k":          retrievalTuningClampInt(pick("retrieval.sparse.candidate_k", 80), 80, 20, 2000),
				"relation_candidate_k": retrievalTuningClampInt(pick("retrieval.sparse.relation_candidate_k", 60), 60, 20, 2000),
			},
			"fusion": map[string]any{
				"method":        fusionMethod,
				"rrf_k":         retrievalTuningClampInt(pick("retrieval.fusion.rrf_k", 60), 60, 1, 500),
				"vector_weight": vectorWeight,
				"bm25_weight":   bm25Weight,
			},
			"vector_pools": map[string]any{
				"mode":                     vectorPoolMode,
				"paragraph_top_k":          retrievalTuningClampInt(pick("retrieval.vector_pools.paragraph_top_k", 20), 20, 4, 512),
				"graph_top_k":              retrievalTuningClampInt(pick("retrieval.vector_pools.graph_top_k", 40), 40, 4, 1000),
				"graph_expand_paragraph_k": retrievalTuningClampInt(pick("retrieval.vector_pools.graph_expand_paragraph_k", 80), 80, 4, 2000),
				"relation_expand_per_hit":  retrievalTuningClampInt(pick("retrieval.vector_pools.relation_expand_per_hit", 5), 5, 1, 50),
				"entity_expand_per_hit":    retrievalTuningClampInt(pick("retrieval.vector_pools.entity_expand_per_hit", 8), 8, 1, 80),
				"relation_evidence_weight": retrievalTuningClampFloat(pick("retrieval.vector_pools.relation_evidence_weight", 1.0), 1.0, 0.0, 3.0),
				"entity_evidence_weight":   retrievalTuningClampFloat(pick("retrieval.vector_pools.entity_evidence_weight", 0.55), 0.55, 0.0, 3.0),
				"semantic_weight":          semanticWeight,
				"sparse_weight":            sparseWeight,
				"graph_weight":             graphWeight,
				"relation_intent": map[string]any{
					"graph_top_k":     retrievalTuningClampInt(pick("retrieval.vector_pools.relation_intent.graph_top_k", 80), 80, 4, 1000),
					"semantic_weight": intentSemantic,
					"sparse_weight":   intentSparse,
					"graph_weight":    intentGraph,
				},
			},
		},
		"threshold": map[string]any{
			"min_threshold": minThreshold,
			"max_threshold": maxThreshold,
			"percentile":    retrievalTuningClampFloat(pick("threshold.percentile", 75.0), 75.0, 1.0, 99.0),
			"min_results":   retrievalTuningClampInt(pick("threshold.min_results", 4), 4, 1, 100),
		},
	}
}

// RetrievalTuningProfileInt 读取规范化参数中的整数项，缺失返回 0
func RetrievalTuningProfileInt(profile map[string]any, key string) int {
	value, err := retrievalTuningToInt(retrievalTuningNestedGet(profile, key, 0))
	if err != nil {
		return 0
	}
	return value
}

// RetrievalTuningProfileFloat 读取规范化参数中的浮点项，缺失返回 0
func RetrievalTuningProfileFloat(profile map[string]any, key string) float64 {
	value, err := pyFloat64(retrievalTuningNestedGet(profile, key, 0.0))
	if err != nil {
		return 0
	}
	return value
}

// RetrievalTuningProfileBool 读取规范化参数中的布尔项，缺失返回 false
func RetrievalTuningProfileBool(profile map[string]any, key string) bool {
	return retrievalTuningCoerceBool(retrievalTuningNestedGet(profile, key, false), false)
}

// RetrievalTuningProfileString 读取规范化参数中的字符串项，缺失返回空串
func RetrievalTuningProfileString(profile map[string]any, key string) string {
	value := retrievalTuningNestedGet(profile, key, "")
	if value == nil {
		return ""
	}
	return pyStr(value)
}

// retrievalTuningFormatFloat4 对齐 f"{value:.4f}"
