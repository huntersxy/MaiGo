package retrieval

// A_memorix Personalized PageRank 封装，对齐 core/retrieval/pagerank.py
// 真正的图迭代由 GraphStore.ComputePagerank 完成，这里负责配置、归一化、统计与查询实体匹配

import (
	"fmt"
	"sort"
	"strings"
)

// PageRankConfig PageRank 配置
type PageRankConfig struct {
	Alpha         float64
	MaxIter       int
	Tol           float64
	Normalize     bool
	MinIterations int
}

// DefaultPageRankConfig 与 Python 默认值一致
func DefaultPageRankConfig() PageRankConfig {
	return PageRankConfig{Alpha: 0.85, MaxIter: 100, Tol: 1e-6, Normalize: true, MinIterations: 20}
}

// Validate 对齐 __post_init__ 的校验与错误文案
func (c PageRankConfig) Validate() error {
	if c.Alpha < 0 || c.Alpha >= 1 {
		return fmt.Errorf("alpha必须在[0, 1)之间: %v", c.Alpha)
	}
	if c.MaxIter <= 0 {
		return fmt.Errorf("max_iter必须大于0: %v", c.MaxIter)
	}
	if c.Tol <= 0 {
		return fmt.Errorf("tol必须大于0: %v", c.Tol)
	}
	if c.MinIterations < 0 {
		return fmt.Errorf("min_iterations必须大于等于0: %v", c.MinIterations)
	}
	if c.MinIterations >= c.MaxIter {
		return fmt.Errorf("min_iterations必须小于max_iter")
	}
	return nil
}

// PageRankGraph 图能力集合；storage.GraphStore 应满足该接口
type PageRankGraph interface {
	ComputePagerank(personalization map[string]float64, alpha float64, maxIter int, tol float64) (map[string]float64, error)
	GetNeighbors(node string) []string
	GetNodes() []string
	NodeRevision() uint64
	NumNodes() int
	NumEdges() int
}

// QueryEntityMatcher 查询实体匹配器；utils.AhoCorasick 应满足该接口
type QueryEntityMatcher interface {
	AddPattern(pattern string)
	Build()
	FindAll(text string) map[string]int
}

// PersonalizedPageRank 个性化 PageRank 计算器
type PersonalizedPageRank struct {
	graphStore PageRankGraph
	config     PageRankConfig

	totalComputations  int
	totalIterations    int
	convergenceHistory []int

	matcher        QueryEntityMatcher
	matcherFactory func() QueryEntityMatcher
	matcherNodes   int
	matcherRev     uint64
	matcherBuilt   bool
}

// NewPersonalizedPageRank 构造计算器并校验配置
func NewPersonalizedPageRank(graphStore PageRankGraph, config PageRankConfig) (*PersonalizedPageRank, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &PersonalizedPageRank{graphStore: graphStore, config: config}, nil
}

// SetMatcherFactory 注入匹配器工厂；未注入时查询实体匹配返回空
func (p *PersonalizedPageRank) SetMatcherFactory(factory func() QueryEntityMatcher) {
	p.matcherFactory = factory
	p.matcher = nil
	p.matcherBuilt = false
}

// Compute 对齐 compute；nil 覆盖值表示使用配置
func (p *PersonalizedPageRank) Compute(personalization map[string]float64, alpha *float64, maxIter *int, normalize *bool) (map[string]float64, error) {
	usedAlpha := p.config.Alpha
	if alpha != nil {
		usedAlpha = *alpha
	}
	usedMaxIter := p.config.MaxIter
	if maxIter != nil {
		usedMaxIter = *maxIter
	}
	usedNormalize := p.config.Normalize
	if normalize != nil {
		usedNormalize = *normalize
	}
	scores, err := p.graphStore.ComputePagerank(personalization, usedAlpha, usedMaxIter, p.config.Tol)
	if err != nil {
		return nil, err
	}
	if usedNormalize && len(scores) > 0 {
		total := 0.0
		for _, score := range scores {
			total += score
		}
		if total > 0 {
			normalized := make(map[string]float64, len(scores))
			for node, score := range scores {
				normalized[node] = score / total
			}
			scores = normalized
		}
	}
	p.totalComputations++
	return scores, nil
}

// ComputeBatch 对齐 compute_batch
func (p *PersonalizedPageRank) ComputeBatch(personalizationList []map[string]float64, normalize bool) ([]map[string]float64, error) {
	results := make([]map[string]float64, 0, len(personalizationList))
	for _, personalization := range personalizationList {
		scores, err := p.Compute(personalization, nil, nil, &normalize)
		if err != nil {
			return nil, err
		}
		results = append(results, scores)
	}
	return results, nil
}

// ComputeForEntities 对齐 compute_for_entities
func (p *PersonalizedPageRank) ComputeForEntities(entities []string, weights []float64, normalize bool) (map[string]float64, error) {
	if len(entities) == 0 {
		return p.Compute(nil, nil, nil, &normalize)
	}
	if weights == nil {
		weights = make([]float64, len(entities))
		for index := range weights {
			weights[index] = 1.0
		}
	}
	if len(weights) != len(entities) {
		return nil, fmt.Errorf("权重数量与实体数量不匹配: %d vs %d", len(weights), len(entities))
	}
	personalization := make(map[string]float64, len(entities))
	for index, entity := range entities {
		personalization[entity] = weights[index]
	}
	return p.Compute(personalization, nil, nil, &normalize)
}

// ComputeForQuery 对齐 compute_for_query；未注入匹配器时退化为空实体
func (p *PersonalizedPageRank) ComputeForQuery(query string, extractor func(string) []string, normalize bool) (map[string]float64, error) {
	var entities []string
	if extractor != nil {
		entities = extractor(query)
	} else {
		entities = p.extractEntitiesFromQuery(query)
	}
	if len(entities) == 0 {
		return p.Compute(nil, nil, nil, &normalize)
	}
	return p.ComputeForEntities(entities, nil, normalize)
}

// RankNodes 对齐 rank_nodes：按分数降序，topK<0 表示不限制
func (p *PersonalizedPageRank) RankNodes(scores map[string]float64, topK int, minScore float64) []PageRankNodeScore {
	filtered := make([]PageRankNodeScore, 0, len(scores))
	for node, score := range scores {
		if score >= minScore {
			filtered = append(filtered, PageRankNodeScore{Node: node, Score: score})
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].Score > filtered[j].Score })
	if topK >= 0 && len(filtered) > topK {
		filtered = filtered[:topK]
	}
	return filtered
}

// PageRankNodeScore 排序后的节点分数
type PageRankNodeScore struct {
	Node  string
	Score float64
}

// GetPersonalizationVector 对齐 get_personalization_vector
func (p *PersonalizedPageRank) GetPersonalizationVector(nodes []string, method string) (map[string]float64, error) {
	if len(nodes) == 0 {
		return map[string]float64{}, nil
	}
	switch method {
	case "uniform":
		weight := 1.0 / float64(len(nodes))
		output := make(map[string]float64, len(nodes))
		for _, node := range nodes {
			output[node] = weight
		}
		return output, nil
	case "degree":
		degrees := make(map[string]float64, len(nodes))
		total := 0.0
		for _, node := range nodes {
			degree := float64(len(p.graphStore.GetNeighbors(node)))
			degrees[node] = degree
			total += degree
		}
		if total > 0 {
			for node := range degrees {
				degrees[node] /= total
			}
			return degrees, nil
		}
		weight := 1.0 / float64(len(nodes))
		for _, node := range nodes {
			degrees[node] = weight
		}
		return degrees, nil
	case "inverse_degree":
		inverse := make(map[string]float64, len(nodes))
		total := 0.0
		for _, node := range nodes {
			degree := float64(len(p.graphStore.GetNeighbors(node)))
			value := 1.0 / (degree + 1.0)
			inverse[node] = value
			total += value
		}
		if total > 0 {
			for node := range inverse {
				inverse[node] /= total
			}
			return inverse, nil
		}
		weight := 1.0 / float64(len(nodes))
		for _, node := range nodes {
			inverse[node] = weight
		}
		return inverse, nil
	}
	return nil, fmt.Errorf("不支持的个性化向量生成方法: %s", method)
}

// CompareScores 对齐 compare_scores
func (p *PersonalizedPageRank) CompareScores(scores1, scores2 map[string]float64) map[string]any {
	common := map[string]any{}
	onlyIn1 := map[string]float64{}
	onlyIn2 := map[string]float64{}
	for node, score1 := range scores1 {
		if score2, ok := scores2[node]; ok {
			common[node] = [2]float64{score1, score2}
		} else {
			onlyIn1[node] = score1
		}
	}
	for node, score2 := range scores2 {
		if _, ok := scores1[node]; !ok {
			onlyIn2[node] = score2
		}
	}
	return map[string]any{"common_nodes": common, "only_in_1": onlyIn1, "only_in_2": onlyIn2}
}

// GetStatistics 对齐 get_statistics
func (p *PersonalizedPageRank) GetStatistics() map[string]any {
	avgIterations := 0.0
	if p.totalComputations > 0 {
		avgIterations = float64(p.totalIterations) / float64(p.totalComputations)
	}
	return map[string]any{
		"config": map[string]any{
			"alpha": p.config.Alpha, "max_iter": p.config.MaxIter, "tol": p.config.Tol,
			"normalize": p.config.Normalize, "min_iterations": p.config.MinIterations,
		},
		"statistics": map[string]any{
			"total_computations": p.totalComputations, "total_iterations": p.totalIterations,
			"avg_iterations": avgIterations, "convergence_history": append([]int(nil), p.convergenceHistory...),
		},
		"graph": map[string]any{"num_nodes": p.graphStore.NumNodes(), "num_edges": p.graphStore.NumEdges()},
	}
}

// ResetStatistics 重置统计
func (p *PersonalizedPageRank) ResetStatistics() {
	p.totalComputations = 0
	p.totalIterations = 0
	p.convergenceHistory = nil
}

// NumComputations 对齐 num_computations 属性
func (p *PersonalizedPageRank) NumComputations() int { return p.totalComputations }

// AvgIterations 对齐 avg_iterations 属性
func (p *PersonalizedPageRank) AvgIterations() float64 {
	if p.totalComputations == 0 {
		return 0.0
	}
	return float64(p.totalIterations) / float64(p.totalComputations)
}

// String 对齐 __repr__
func (p *PersonalizedPageRank) String() string {
	return fmt.Sprintf("PersonalizedPageRank(alpha=%v, computations=%d)", p.config.Alpha, p.totalComputations)
}

// CreatePageRankFromGraph 对齐 create_ppr_from_graph
func CreatePageRankFromGraph(graphStore PageRankGraph, alpha float64, maxIter int) (*PersonalizedPageRank, error) {
	return NewPersonalizedPageRank(graphStore, PageRankConfig{Alpha: alpha, MaxIter: maxIter, Tol: 1e-6, Normalize: true, MinIterations: 20})
}

// extractEntitiesFromQuery 对齐 _extract_entities_from_query；按节点 revision 重建匹配器
func (p *PersonalizedPageRank) extractEntitiesFromQuery(query string) []string {
	nodes := p.graphStore.GetNodes()
	if len(nodes) == 0 {
		return []string{}
	}
	revision := p.graphStore.NodeRevision()
	if p.matcher == nil || p.matcherRev != revision {
		if p.matcherFactory == nil {
			return []string{}
		}
		p.matcher = p.matcherFactory()
		for _, node := range nodes {
			p.matcher.AddPattern(lowerText(node))
		}
		p.matcher.Build()
		p.matcherNodes = len(nodes)
		p.matcherRev = revision
		p.matcherBuilt = true
	}
	nodeMap := make(map[string]string, len(nodes))
	for _, node := range nodes {
		nodeMap[lowerText(node)] = node
	}
	stats := p.matcher.FindAll(lowerText(query))
	entities := make([]string, 0, len(stats))
	for lower := range stats {
		if original, ok := nodeMap[lower]; ok {
			entities = append(entities, original)
		}
	}
	return entities
}

// lowerText 对齐 Python str.lower 的 Unicode 小写语义
func lowerText(value string) string {
	return strings.ToLower(value)
}
