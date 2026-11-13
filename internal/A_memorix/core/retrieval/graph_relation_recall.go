package retrieval

// A_memorix 面向关系查询的图辅助候选关系召回
// 对齐 core/retrieval/graph_relation_recall.py

import (
	"fmt"
	"sort"
	"strings"
)

// GraphRelationRecallConfig 受控图关系召回配置
type GraphRelationRecallConfig struct {
	Enabled         bool
	CandidateK      int
	MaxHop          int
	AllowTwoHopPair bool
	MaxPaths        int
}

// DefaultGraphRelationRecallConfig 与 Python 默认值一致
func DefaultGraphRelationRecallConfig() GraphRelationRecallConfig {
	return GraphRelationRecallConfig{Enabled: true, CandidateK: 24, MaxHop: 1, AllowTwoHopPair: true, MaxPaths: 4}
}

// Normalize 对齐 __post_init__ 的下限归一
func (c *GraphRelationRecallConfig) Normalize() {
	c.CandidateK = maxInt(c.CandidateK, 1)
	c.MaxHop = maxInt(c.MaxHop, 1)
	c.MaxPaths = maxInt(c.MaxPaths, 1)
}

// GraphRelationCandidate 进入检索器融合前，由图结构生成的关系候选
type GraphRelationCandidate struct {
	HashValue                string
	Subject                  string
	Predicate                string
	Object                   string
	Confidence               float64
	GraphSeedEntities        []string
	GraphHops                int
	GraphCandidateType       string
	SupportingParagraphCount int
}

// ToPayload 对齐 to_payload 的字段与 content 拼接
func (c GraphRelationCandidate) ToPayload() map[string]any {
	return map[string]any{
		"hash":                       c.HashValue,
		"content":                    c.Subject + " " + c.Predicate + " " + c.Object,
		"subject":                    c.Subject,
		"predicate":                  c.Predicate,
		"object":                     c.Object,
		"confidence":                 c.Confidence,
		"graph_seed_entities":        append([]string(nil), c.GraphSeedEntities...),
		"graph_hops":                 c.GraphHops,
		"graph_candidate_type":       c.GraphCandidateType,
		"supporting_paragraph_count": c.SupportingParagraphCount,
	}
}

// RelationRecallGraph 召回所需的图能力；storage.GraphStore 应满足该接口
type RelationRecallGraph interface {
	FindNode(name string, ignoreCase bool) (string, bool)
	GetRelationHashesForEdge(source, target string) []string
	FindPaths(start, end string, maxDepth int, maxPaths int) [][]string
	GetIncidentRelationHashes(node string, limit int) []string
}

// RelationRecallMetadata 召回所需的元数据能力；storage.Store 应满足该接口
type RelationRecallMetadata interface {
	GetRelation(hashValue string, includeInactive bool) (map[string]any, error)
	GetParagraphsByRelation(relationHash string) ([]map[string]any, error)
}

// GraphRelationRecallService 按受控策略从实体图中收集关系候选
type GraphRelationRecallService struct {
	graphStore    RelationRecallGraph
	metadataStore RelationRecallMetadata
	config        GraphRelationRecallConfig
}

// NewGraphRelationRecallService 构造服务；nil 图或元数据在 recall 时直接返回空
func NewGraphRelationRecallService(graphStore RelationRecallGraph, metadataStore RelationRecallMetadata, config GraphRelationRecallConfig) *GraphRelationRecallService {
	config.Normalize()
	return &GraphRelationRecallService{graphStore: graphStore, metadataStore: metadataStore, config: config}
}

// Recall 对齐 recall：根据种子实体数量走直接对、二跳或单跳召回
func (s *GraphRelationRecallService) Recall(seedEntities []string, allowedRelationIds map[string]struct{}) []GraphRelationCandidate {
	if !s.config.Enabled || s.graphStore == nil || s.metadataStore == nil {
		return []GraphRelationCandidate{}
	}
	seeds := s.normalizeSeedEntities(seedEntities)
	if len(seeds) == 0 {
		return []GraphRelationCandidate{}
	}
	if allowedRelationIds != nil && len(allowedRelationIds) == 0 {
		return []GraphRelationCandidate{}
	}

	seenHashes := map[string]struct{}{}
	candidates := make([]GraphRelationCandidate, 0, s.config.CandidateK)
	if len(seeds) >= 2 {
		s.collectDirectPairCandidates(seeds[0], seeds[1], seenHashes, allowedRelationIds, &candidates)
		if len(candidates) < 3 && s.config.AllowTwoHopPair && len(candidates) < s.config.CandidateK {
			s.collectTwoHopPairCandidates(seeds[0], seeds[1], seenHashes, allowedRelationIds, &candidates)
		}
	} else {
		s.collectOneHopSeedCandidates(seeds[0], seenHashes, allowedRelationIds, &candidates)
	}
	if len(candidates) > s.config.CandidateK {
		candidates = candidates[:s.config.CandidateK]
	}
	return candidates
}

// normalizeSeedEntities 对齐 _normalize_seed_entities：最多两个、图内解析、小写去重
func (s *GraphRelationRecallService) normalizeSeedEntities(seedEntities []string) []string {
	limit := len(seedEntities)
	if limit > 2 {
		limit = 2
	}
	output := make([]string, 0, 2)
	seen := map[string]struct{}{}
	for index := 0; index < limit; index++ {
		raw := seedEntities[index]
		resolved, ok := s.graphStore.FindNode(raw, true)
		if !ok || resolved == "" {
			continue
		}
		canonical := strings.ToLower(strings.TrimSpace(resolved))
		if canonical == "" {
			continue
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		output = append(output, resolved)
	}
	return output
}

// collectDirectPairCandidates 对齐 _collect_direct_pair_candidates
func (s *GraphRelationRecallService) collectDirectPairCandidates(seedA, seedB string, seenHashes map[string]struct{}, allowed map[string]struct{}, out *[]GraphRelationCandidate) {
	relationHashes := make([]string, 0)
	relationHashes = append(relationHashes, s.graphStore.GetRelationHashesForEdge(seedA, seedB)...)
	relationHashes = append(relationHashes, s.graphStore.GetRelationHashesForEdge(seedB, seedA)...)
	s.appendRelationHashes(relationHashes, seenHashes, allowed, out, "direct_pair", 1, []string{seedA, seedB})
}

// collectTwoHopPairCandidates 对齐 _collect_two_hop_pair_candidates
func (s *GraphRelationRecallService) collectTwoHopPairCandidates(seedA, seedB string, seenHashes map[string]struct{}, allowed map[string]struct{}, out *[]GraphRelationCandidate) {
	paths := s.graphStore.FindPaths(seedA, seedB, 2, s.config.MaxPaths)
	for _, pathNodes := range paths {
		if len(*out) >= s.config.CandidateK {
			break
		}
		if len(pathNodes) != 3 {
			continue
		}
		for index := 0; index < len(pathNodes)-1; index++ {
			if len(*out) >= s.config.CandidateK {
				break
			}
			left := pathNodes[index]
			right := pathNodes[index+1]
			relationHashes := make([]string, 0)
			relationHashes = append(relationHashes, s.graphStore.GetRelationHashesForEdge(left, right)...)
			relationHashes = append(relationHashes, s.graphStore.GetRelationHashesForEdge(right, left)...)
			s.appendRelationHashes(relationHashes, seenHashes, allowed, out, "two_hop_pair", 2, []string{seedA, seedB})
		}
	}
}

// collectOneHopSeedCandidates 对齐 _collect_one_hop_seed_candidates
// limit 为 0 表示不限制：Python 在允许集合非空时传 None
func (s *GraphRelationRecallService) collectOneHopSeedCandidates(seed string, seenHashes map[string]struct{}, allowed map[string]struct{}, out *[]GraphRelationCandidate) {
	limit := s.config.CandidateK
	if allowed != nil {
		limit = 0
	}
	relationHashes := s.graphStore.GetIncidentRelationHashes(seed, limit)
	hops := s.config.MaxHop
	if hops > 1 {
		hops = 1
	}
	s.appendRelationHashes(relationHashes, seenHashes, allowed, out, "one_hop_seed", hops, []string{seed})
}

// appendRelationHashes 对齐 _append_relation_hashes，按 hash 排序保证确定性
func (s *GraphRelationRecallService) appendRelationHashes(relationHashes []string, seenHashes map[string]struct{}, allowed map[string]struct{}, out *[]GraphRelationCandidate, candidateType string, graphHops int, seedEntities []string) {
	unique := map[string]struct{}{}
	for _, raw := range relationHashes {
		trimmed := strings.TrimSpace(raw)
		if trimmed != "" {
			unique[raw] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(unique))
	for hashValue := range unique {
		ordered = append(ordered, hashValue)
	}
	sort.Strings(ordered)

	for _, relationHash := range ordered {
		if len(*out) >= s.config.CandidateK {
			break
		}
		if _, exists := seenHashes[relationHash]; exists {
			continue
		}
		if allowed != nil {
			if _, ok := allowed[relationHash]; !ok {
				continue
			}
		}
		candidate, ok := s.buildCandidate(relationHash, candidateType, graphHops, seedEntities)
		if !ok {
			continue
		}
		seenHashes[relationHash] = struct{}{}
		*out = append(*out, candidate)
	}
}

// buildCandidate 对齐 _build_candidate：关系不存在或查询失败即跳过
func (s *GraphRelationRecallService) buildCandidate(relationHash string, candidateType string, graphHops int, seedEntities []string) (GraphRelationCandidate, bool) {
	relation, err := s.metadataStore.GetRelation(relationHash, false)
	if err != nil || relation == nil {
		return GraphRelationCandidate{}, false
	}
	paragraphs, err := s.metadataStore.GetParagraphsByRelation(relationHash)
	if err != nil {
		paragraphs = nil
	}
// 对齐 Python float(relation.get("confidence", 1.0) or 1.0)：0/None 回落 1.0
		confidence := 1.0
		if value, ok := numericValue(relation["confidence"]); ok && value != 0 {
			confidence = value
		}
	seeds := make([]string, len(seedEntities))
	copy(seeds, seedEntities)
	return GraphRelationCandidate{
		HashValue:                relationHash,
		Subject:                  stringValue(relation["subject"]),
		Predicate:                stringValue(relation["predicate"]),
		Object:                   stringValue(relation["object"]),
		Confidence:               confidence,
		GraphSeedEntities:        seeds,
		GraphHops:                graphHops,
		GraphCandidateType:       candidateType,
		SupportingParagraphCount: len(paragraphs),
	}, true
}

// stringValue 将元数据值转为字符串；nil 视为空串，对齐 Python str(value or "")
func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}
