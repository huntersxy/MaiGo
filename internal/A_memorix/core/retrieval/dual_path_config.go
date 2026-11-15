package retrieval

// A_memorix 双路检索器配置与常量，对齐 core/retrieval/dual_path.py 的 dataclass

import (
	"fmt"
	"math"
	"strings"
)

// 图证据可信度常量，与 Python 模块级常量一一对应
const (
	relationBothEntitiesGroundingFactor  = 0.78
	relationSingleEntityGroundingFactor  = 0.55
	relationPredicateOnlyGroundingFactor = 0.4
	relationUngroundedFactor             = 0.3
	entityUngroundedFactor               = 0.4
	graphReliabilityGroundingWeight      = 0.65
	graphReliabilityAgreementWeight      = 0.10
	graphReliabilitySupportWeight        = 0.25
	graphReliabilitySupportTarget        = 2
	graphReliabilityWeightFloor          = 0.15
	graphReliabilityCurveExponent        = 3.0
)

// graphReliabilityEstimate 当前查询命中的图证据可信度估计
type graphReliabilityEstimate struct {
	Score                 float64
	GroundingQuality      float64
	ChannelAgreement      float64
	SupportCoverage       float64
	EvidenceCount         int
	GroundedRelationCount int
	RelationCount         int
}

// DualPathRetrieverConfig 双路检索器配置
type DualPathRetrieverConfig struct {
	TopKParagraphs        int
	TopKRelations         int
	TopKFinal             int
	Alpha                 float64
	EnablePPR             bool
	PPRAlpha              float64
	PPRTimeoutSeconds     float64
	PPRConcurrencyLimit   int
	PPRLocalEnabled       bool
	PPRLocalMaxNodes      int
	PPRLocalHops          int
	PPRLocalMinGraphNodes int
	EnableParallel        bool
	RetrievalStrategy     RetrievalStrategy
	Debug                 bool

	Sparse         SparseBM25Config
	Fusion         FusionConfig
	RelationIntent RelationIntentConfig
	VectorPools    VectorPoolsConfig
	GraphRecall    GraphRelationRecallConfig
	PosteriorGraph PosteriorGraphConfig
}

// DefaultDualPathRetrieverConfig 返回与 Python dataclass 默认值一致的配置
func DefaultDualPathRetrieverConfig() DualPathRetrieverConfig {
	return DualPathRetrieverConfig{
		TopKParagraphs:        20,
		TopKRelations:         10,
		TopKFinal:             10,
		Alpha:                 0.5,
		EnablePPR:             true,
		PPRAlpha:              0.85,
		PPRTimeoutSeconds:     1.5,
		PPRConcurrencyLimit:   4,
		PPRLocalEnabled:       true,
		PPRLocalMaxNodes:      256,
		PPRLocalHops:          2,
		PPRLocalMinGraphNodes: 128,
		EnableParallel:        true,
		RetrievalStrategy:     StrategyDualPath,
		Debug:                 false,
		Sparse:                NewSparseBM25Config(),
		Fusion:                DefaultFusionConfig(),
		RelationIntent:        DefaultRelationIntentConfig(),
		VectorPools:           DefaultVectorPoolsConfig(),
		GraphRecall:           DefaultGraphRelationRecallConfig(),
		PosteriorGraph:        DefaultPosteriorGraphConfig(),
	}
}

// Normalize 对齐 __post_init__：校验取值并归一化子配置
func (c *DualPathRetrieverConfig) Normalize() error {
	if c.Alpha < 0 || c.Alpha > 1 {
		return fmt.Errorf("alpha必须在[0, 1]之间: %v", c.Alpha)
	}
	if c.TopKParagraphs <= 0 {
		return fmt.Errorf("top_k_paragraphs必须大于0: %v", c.TopKParagraphs)
	}
	if c.TopKRelations <= 0 {
		return fmt.Errorf("top_k_relations必须大于0: %v", c.TopKRelations)
	}
	if c.TopKFinal <= 0 {
		return fmt.Errorf("top_k_final必须大于0: %v", c.TopKFinal)
	}
	if c.PPRTimeoutSeconds <= 0 {
		return fmt.Errorf("ppr_timeout_seconds必须大于0: %v", c.PPRTimeoutSeconds)
	}
	c.PPRLocalMaxNodes = max(16, c.PPRLocalMaxNodes)
	c.PPRLocalHops = max(1, c.PPRLocalHops)
	c.PPRLocalMinGraphNodes = max(0, c.PPRLocalMinGraphNodes)

	c.Sparse.Normalize()
	if err := c.Sparse.Validate(); err != nil {
		return err
	}
	c.Fusion.Normalize()
	c.RelationIntent.Normalize()
	if err := c.VectorPools.Normalize(); err != nil {
		return err
	}
	c.GraphRecall.Normalize()
	c.PosteriorGraph.Normalize()
	return nil
}

// TemporalQueryOptions 时序查询选项
type TemporalQueryOptions struct {
	TimeFrom             *float64
	TimeTo               *float64
	Person               string
	Source               string
	AllowCreatedFallback bool
	CandidateMultiplier  int
	MaxScan              int
}

// DefaultTemporalQueryOptions 返回与 Python dataclass 默认值一致的时序选项
func DefaultTemporalQueryOptions() TemporalQueryOptions {
	return TemporalQueryOptions{
		AllowCreatedFallback: true,
		CandidateMultiplier:  8,
		MaxScan:              1000,
	}
}

// RelationIntentConfig 关系意图增强配置
type RelationIntentConfig struct {
	Enabled                     bool
	AlphaOverride               float64
	RelationCandidateMultiplier int
	PreserveTopRelations        int
	ForceRelationSparse         bool
	PairPredicateRerankEnabled  bool
	PairPredicateLimit          int
}

// DefaultRelationIntentConfig 返回与 Python dataclass 默认值一致的配置
func DefaultRelationIntentConfig() RelationIntentConfig {
	return RelationIntentConfig{
		Enabled:                     true,
		AlphaOverride:               0.35,
		RelationCandidateMultiplier: 4,
		PreserveTopRelations:        3,
		ForceRelationSparse:         true,
		PairPredicateRerankEnabled:  true,
		PairPredicateLimit:          3,
	}
}

// Normalize 对齐 __post_init__ 的钳制与布尔化
func (c *RelationIntentConfig) Normalize() {
	c.AlphaOverride = math.Min(1.0, math.Max(0.0, c.AlphaOverride))
	c.RelationCandidateMultiplier = max(1, c.RelationCandidateMultiplier)
	c.PreserveTopRelations = max(0, c.PreserveTopRelations)
	c.PairPredicateLimit = max(1, c.PairPredicateLimit)
}

// VectorPoolsConfig 双向量池检索配置
type VectorPoolsConfig struct {
	Mode                         string
	ParagraphTopK                int
	GraphTopK                    int
	GraphExpandParagraphK        int
	RelationExpandPerHit         int
	EntityExpandPerHit           int
	RelationEvidenceWeight       float64
	EntityEvidenceWeight         float64
	SemanticWeight               float64
	SparseWeight                 float64
	GraphWeight                  float64
	ScoreCalibrationMethod       string
	ScoreCalibrationRRFK         int
	RelationIntentGraphTopK      int
	RelationIntentSemanticWeight float64
	RelationIntentSparseWeight   float64
	RelationIntentGraphWeight    float64
	ReturnRelationItems          bool
	RelationIntent               map[string]any
}

// DefaultVectorPoolsConfig 返回与 Python dataclass 默认值一致的配置
func DefaultVectorPoolsConfig() VectorPoolsConfig {
	return VectorPoolsConfig{
		Mode:                         "dual",
		ParagraphTopK:                20,
		GraphTopK:                    40,
		GraphExpandParagraphK:        80,
		RelationExpandPerHit:         5,
		EntityExpandPerHit:           8,
		RelationEvidenceWeight:       1.0,
		EntityEvidenceWeight:         0.55,
		SemanticWeight:               0.65,
		SparseWeight:                 0.20,
		GraphWeight:                  0.15,
		ScoreCalibrationMethod:       "none",
		ScoreCalibrationRRFK:         60,
		RelationIntentGraphTopK:      80,
		RelationIntentSemanticWeight: 0.45,
		RelationIntentSparseWeight:   0.15,
		RelationIntentGraphWeight:    0.40,
		ReturnRelationItems:          false,
	}
}

// Normalize 对齐 __post_init__：模式回落、计数与权重下限、relation_intent 覆盖
func (c *VectorPoolsConfig) Normalize() error {
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Mode == "" {
		c.Mode = "single"
	}
	if c.Mode != "single" && c.Mode != "dual" {
		c.Mode = "single"
	}
	c.ParagraphTopK = max(1, c.ParagraphTopK)
	c.GraphTopK = max(1, c.GraphTopK)
	c.GraphExpandParagraphK = max(1, c.GraphExpandParagraphK)
	c.RelationExpandPerHit = max(1, c.RelationExpandPerHit)
	c.EntityExpandPerHit = max(1, c.EntityExpandPerHit)
	c.RelationEvidenceWeight = math.Max(0.0, c.RelationEvidenceWeight)
	c.EntityEvidenceWeight = math.Max(0.0, c.EntityEvidenceWeight)
	c.SemanticWeight = math.Max(0.0, c.SemanticWeight)
	c.SparseWeight = math.Max(0.0, c.SparseWeight)
	c.GraphWeight = math.Max(0.0, c.GraphWeight)

	method, err := NormalizeCalibrationMethod(c.ScoreCalibrationMethod)
	if err != nil {
		return err
	}
	c.ScoreCalibrationMethod = method
	c.ScoreCalibrationRRFK = max(1, c.ScoreCalibrationRRFK)

	overrides := c.RelationIntent
	if overrides != nil {
		if value, exists := overrides["graph_top_k"]; exists {
			if number, ok := numericValue(value); ok {
				c.RelationIntentGraphTopK = int(number)
			}
		}
		if value, exists := overrides["semantic_weight"]; exists {
			if number, ok := numericValue(value); ok {
				c.RelationIntentSemanticWeight = number
			}
		}
		if value, exists := overrides["sparse_weight"]; exists {
			if number, ok := numericValue(value); ok {
				c.RelationIntentSparseWeight = number
			}
		}
		if value, exists := overrides["graph_weight"]; exists {
			if number, ok := numericValue(value); ok {
				c.RelationIntentGraphWeight = number
			}
		}
		if value, exists := overrides["return_relation_items"]; exists {
			c.ReturnRelationItems = dualPathBool(value, c.ReturnRelationItems)
		}
	}
	c.RelationIntentGraphTopK = max(1, c.RelationIntentGraphTopK)
	c.RelationIntentSemanticWeight = math.Max(0.0, c.RelationIntentSemanticWeight)
	c.RelationIntentSparseWeight = math.Max(0.0, c.RelationIntentSparseWeight)
	c.RelationIntentGraphWeight = math.Max(0.0, c.RelationIntentGraphWeight)
	return nil
}

// FusionConfig 融合配置
type FusionConfig struct {
	Method          string
	RRFK            int
	VectorWeight    float64
	BM25Weight      float64
	NormalizeScore  bool
	NormalizeMethod string
}

// DefaultFusionConfig 返回与 Python dataclass 默认值一致的配置
func DefaultFusionConfig() FusionConfig {
	return FusionConfig{
		Method:          "weighted_rrf",
		RRFK:            60,
		VectorWeight:    0.7,
		BM25Weight:      0.3,
		NormalizeScore:  true,
		NormalizeMethod: "minmax",
	}
}

// Normalize 对齐 __post_init__：默认方法、权重和为 0 时回落、和不为 1 时归一
func (c *FusionConfig) Normalize() {
	c.Method = strings.ToLower(strings.TrimSpace(c.Method))
	if c.Method == "" {
		c.Method = "weighted_rrf"
	}
	c.NormalizeMethod = strings.ToLower(strings.TrimSpace(c.NormalizeMethod))
	if c.NormalizeMethod == "" {
		c.NormalizeMethod = "minmax"
	}
	c.RRFK = max(1, c.RRFK)
	c.VectorWeight = math.Max(0.0, c.VectorWeight)
	c.BM25Weight = math.Max(0.0, c.BM25Weight)
	sum := c.VectorWeight + c.BM25Weight
	if sum <= 0 {
		c.VectorWeight = 0.7
		c.BM25Weight = 0.3
	} else if math.Abs(sum-1.0) > 1e-8 {
		c.VectorWeight /= sum
		c.BM25Weight /= sum
	}
}

// dualPathBool 把配置值转成布尔，缺省时返回默认值，对齐 Python bool(...)
func dualPathBool(value any, fallback bool) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return strings.TrimSpace(typed) != ""
	case float64:
		return typed != 0
	case float32:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case int32:
		return typed != 0
	}
	return true
}
