package retrieval

// Episode 混合检索服务，对齐 A_memorix core/utils/episode_retrieval_service.py
// 元数据存储与双路检索器通过依赖注入，便于运行时装配与测试

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"maibot-go/internal/A_memorix/core/utils"
)

// EpisodeRetrievalStoreDeps Episode 检索所需的元数据能力
type EpisodeRetrievalStoreDeps struct {
	QueryEpisodes                   func(query string, timeFrom, timeTo *float64, person, source string, limit int, allowedEpisodeIDs []string) ([]map[string]any, error)
	GetEpisodeRowsByParagraphHashes func(paragraphHashes []string, source string) ([]map[string]any, error)
	GetEpisodeRowsByRelationHashes  func(relationHashes []string, source string) ([]map[string]any, error)
	GetEpisodeParagraphs            func(episodeID string, limit int) ([]map[string]any, error)
}

// EpisodeEvidenceRetriever 双路检索器的最小接口
type EpisodeEvidenceRetriever interface {
	Retrieve(ctx context.Context, query string, opts *RetrieveOptions) ([]RetrievalResult, error)
}

// EpisodeRetrievalService 结合词法记录与证据投影的 Episode 混合检索
type EpisodeRetrievalService struct {
	Deps      EpisodeRetrievalStoreDeps
	Retriever EpisodeEvidenceRetriever
	Logger    utils.Logger
}

// NewEpisodeRetrievalService 构造 Episode 混合检索服务
func NewEpisodeRetrievalService(deps EpisodeRetrievalStoreDeps, retriever EpisodeEvidenceRetriever, logger utils.Logger) *EpisodeRetrievalService {
	return &EpisodeRetrievalService{Deps: deps, Retriever: retriever, Logger: logger}
}

const episodeRRFK = 60.0

var episodeBranchWeights = map[string]float64{
	"lexical":            1.0,
	"paragraph_evidence": 1.0,
	"relation_evidence":  0.85,
}

var episodeRelationQueryMarkers = []string{
	"关系", "关联", "联系", "相关", "相互", "之间", "依赖", "属于", "包含", "影响", "导致", "为什么", "怎么", "如何",
}

// Query 执行 Episode 混合检索
func (s *EpisodeRetrievalService) Query(
	ctx context.Context,
	query string,
	topK int,
	timeFrom, timeTo *float64,
	person, source string,
	includeParagraphs bool,
	scope *RetrievalScope,
) ([]map[string]any, error) {
	cleanQuery := strings.TrimSpace(query)
	safeTopK := topK
	if safeTopK < 1 {
		safeTopK = 1
	}
	candidateK := 20
	if safeTopK*4 > candidateK {
		candidateK = safeTopK * 4
	}
	if s.Deps.QueryEpisodes == nil {
		return nil, fmt.Errorf("episode metadata store unavailable")
	}
	limit := safeTopK
	if cleanQuery != "" {
		limit = candidateK
	}
	var allowed []string
	if scope != nil && len(scope.EpisodeIDs) > 0 {
		allowed = make([]string, 0, len(scope.EpisodeIDs))
		for episodeID := range scope.EpisodeIDs {
			allowed = append(allowed, episodeID)
		}
		sort.Strings(allowed)
	}
	lexical, err := s.Deps.QueryEpisodes(cleanQuery, timeFrom, timeTo, person, source, limit, allowed)
	if err != nil {
		return nil, err
	}
	branches := map[string][]map[string]any{"lexical": lexical}
	logger := utils.OrLogger(s.Logger)

	if cleanQuery != "" && s.Retriever != nil {
		temporal := &TemporalQueryOptions{
			TimeFrom: timeFrom,
			TimeTo:   timeTo,
			Person:   person,
			Source:   source,
		}
		results, retrieveErr := s.Retriever.Retrieve(ctx, cleanQuery, &RetrieveOptions{
			TopK:     &candidateK,
			Temporal: temporal,
			Scope:    scope,
		})
		if retrieveErr != nil {
			logger.Warning(fmt.Sprintf("episode evidence retrieval failed, fallback to lexical only: %v", retrieveErr))
		} else {
			paragraphRankMap := make(map[string]int)
			relationRankMap := make(map[string]int)
			for index, item := range results {
				rank := index + 1
				hashValue := strings.TrimSpace(item.HashValue)
				if hashValue == "" {
					continue
				}
				switch strings.ToLower(strings.TrimSpace(item.ResultType)) {
				case "paragraph":
					if _, exists := paragraphRankMap[hashValue]; !exists {
						paragraphRankMap[hashValue] = rank
					}
				case "relation":
					if _, exists := relationRankMap[hashValue]; !exists {
						relationRankMap[hashValue] = rank
					}
				}
			}
			if len(paragraphRankMap) > 0 && s.Deps.GetEpisodeRowsByParagraphHashes != nil {
				paragraphRows, err := s.Deps.GetEpisodeRowsByParagraphHashes(episodeSortedKeys(paragraphRankMap), source)
				if err != nil {
					return nil, err
				}
				paragraphRows = filterEpisodeRowsByScope(paragraphRows, scope)
				if len(paragraphRows) > 0 {
					branches["paragraph_evidence"] = rankEpisodeProjectedRows(paragraphRows, paragraphRankMap, "matched_paragraph_hashes")
				}
			}
			if len(relationRankMap) > 0 && episodeShouldProjectRelationEvidence(cleanQuery) && s.Deps.GetEpisodeRowsByRelationHashes != nil {
				relationRows, err := s.Deps.GetEpisodeRowsByRelationHashes(episodeSortedKeys(relationRankMap), source)
				if err != nil {
					return nil, err
				}
				relationRows = filterEpisodeRowsByScope(relationRows, scope)
				if len(relationRows) > 0 {
					branches["relation_evidence"] = rankEpisodeProjectedRows(relationRows, relationRankMap, "matched_relation_hashes")
				}
			}
		}
	}

	fused := fuseEpisodeBranches(branches, safeTopK)
	if includeParagraphs && s.Deps.GetEpisodeParagraphs != nil {
		for _, item := range fused {
			// 对齐 Python str(item.get("episode_id") or "")：不做 strip
			episodeID := utils.PyStr(item["episode_id"])
			paragraphs, err := s.Deps.GetEpisodeParagraphs(episodeID, 50)
			if err != nil {
				return nil, err
			}
			if paragraphs == nil {
				paragraphs = []map[string]any{}
			}
			item["paragraphs"] = paragraphs
		}
	}
	return fused, nil
}

func episodeSortedKeys(ranks map[string]int) []string {
	keys := make([]string, 0, len(ranks))
	for key := range ranks {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func filterEpisodeRowsByScope(rows []map[string]any, scope *RetrievalScope) []map[string]any {
	if scope == nil {
		return rows
	}
	allowed := make(map[string]struct{}, len(scope.EpisodeIDs))
	for item := range scope.EpisodeIDs {
		allowed[item] = struct{}{}
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if _, ok := allowed[strings.TrimSpace(utils.PyStr(row["episode_id"]))]; ok {
			out = append(out, row)
		}
	}
	return out
}

func episodeFirstSupportRank(item map[string]any, rankMap map[string]int, supportKey string) int {
	sentinel := 1000000000
	best := sentinel
	for _, raw := range utils.ArgumentTokens(item[supportKey]) {
		hashValue := strings.TrimSpace(raw)
		if rank, ok := rankMap[hashValue]; ok && rank < best {
			best = rank
		}
	}
	return best
}

func rankEpisodeProjectedRows(rows []map[string]any, rankMap map[string]int, supportKey string) []map[string]any {
	ranked := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		copied := make(map[string]any, len(row))
		for key, value := range row {
			copied[key] = value
		}
		ranked = append(ranked, copied)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		leftRank := episodeFirstSupportRank(ranked[i], rankMap, supportKey)
		rightRank := episodeFirstSupportRank(ranked[j], rankMap, supportKey)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		leftMatched := utils.PyIntOrZero(ranked[i]["matched_paragraph_count"])
		rightMatched := utils.PyIntOrZero(ranked[j]["matched_paragraph_count"])
		if leftMatched != rightMatched {
			return leftMatched > rightMatched
		}
		leftUpdated := utils.PyFloatOrZero(ranked[i]["updated_at"])
		rightUpdated := utils.PyFloatOrZero(ranked[j]["updated_at"])
		if leftUpdated != rightUpdated {
			return leftUpdated > rightUpdated
		}
		return strings.TrimSpace(utils.PyStr(ranked[i]["episode_id"])) < strings.TrimSpace(utils.PyStr(ranked[j]["episode_id"]))
	})
	return ranked
}

func fuseEpisodeBranches(branches map[string][]map[string]any, topK int) []map[string]any {
	bucket := make(map[string]map[string]any)
	for _, branchName := range []string{"lexical", "paragraph_evidence", "relation_evidence"} {
		rows, ok := branches[branchName]
		if !ok {
			continue
		}
		weight := episodeBranchWeights[branchName]
		if weight <= 0 {
			continue
		}
		for index, row := range rows {
			rank := index + 1
			episodeID := strings.TrimSpace(utils.PyStr(row["episode_id"]))
			if episodeID == "" {
				continue
			}
			payload, exists := bucket[episodeID]
			if !exists {
				payload = make(map[string]any, len(row)+1)
				for key, value := range row {
					payload[key] = value
				}
				delete(payload, "matched_paragraph_hashes")
				delete(payload, "matched_relation_hashes")
				delete(payload, "matched_paragraph_count")
				delete(payload, "matched_relation_count")
				payload["_fusion_score"] = 0.0
				bucket[episodeID] = payload
			}
			score, _ := payload["_fusion_score"].(float64)
			payload["_fusion_score"] = score + weight/(episodeRRFK+float64(rank))
		}
	}
	out := make([]map[string]any, 0, len(bucket))
	for _, item := range bucket {
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		leftScore, _ := out[i]["_fusion_score"].(float64)
		rightScore, _ := out[j]["_fusion_score"].(float64)
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		leftUpdated := utils.PyFloatOrZero(out[i]["updated_at"])
		rightUpdated := utils.PyFloatOrZero(out[j]["updated_at"])
		if leftUpdated != rightUpdated {
			return leftUpdated > rightUpdated
		}
		return strings.TrimSpace(utils.PyStr(out[i]["episode_id"])) < strings.TrimSpace(utils.PyStr(out[j]["episode_id"]))
	})
	for _, item := range out {
		delete(item, "_fusion_score")
	}
	if topK < 1 {
		topK = 1
	}
	if len(out) > topK {
		out = out[:topK]
	}
	return out
}

func episodeShouldProjectRelationEvidence(query string) bool {
	clean := strings.TrimSpace(query)
	if clean == "" {
		return false
	}
	for _, marker := range episodeRelationQueryMarkers {
		if strings.Contains(clean, marker) {
			return true
		}
	}
	return false
}
