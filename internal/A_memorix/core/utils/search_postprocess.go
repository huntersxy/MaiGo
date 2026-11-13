package utils

// A_memorix 检索后处理，对齐 Python core/utils/search_postprocess.py
// path fallback 逻辑与 path_fallback_service.py 对应
// 为避免与并行移植的同名文件冲突，内部辅助实现统一使用 postprocess 前缀

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// RetrievalResult 检索结果，对齐 Python core/retrieval/dual_path.py 的 RetrievalResult
type RetrievalResult struct {
	HashValue  string
	Content    string
	Score      float64
	ResultType string
	Source     string
	Metadata   map[string]any
}

// ToDict 对齐 RetrievalResult.to_dict
func (r RetrievalResult) ToDict() map[string]any {
	metadata := r.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	return map[string]any{
		"hash":     r.HashValue,
		"content":  r.Content,
		"score":    r.Score,
		"type":     r.ResultType,
		"source":   r.Source,
		"metadata": metadata,
	}
}

// ApplySafeContentDedup 对齐 apply_safe_content_dedup，按哈希和内容去重
// Python 的 hash(content) 回退依赖进程随机种子，Go 侧改用内容本身作为回退键
func ApplySafeContentDedup(results []RetrievalResult) ([]RetrievalResult, int) {
	if len(results) == 0 {
		return []RetrievalResult{}, 0
	}

	uniqueResults := make([]RetrievalResult, 0, len(results))
	seenHashes := make(map[string]struct{}, len(results))
	seenContents := make([]string, 0, len(results))

	for _, item := range results {
		content := pyStrip(item.Content)
		if content == "" {
			continue
		}
		hashValue := pyStrip(item.HashValue)
		if hashValue == "" {
			hashValue = content
		}
		if _, ok := seenHashes[hashValue]; ok {
			continue
		}

		duplicate := false
		for _, seen := range seenContents {
			if strings.Contains(content, seen) || strings.Contains(seen, content) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}

		seenHashes[hashValue] = struct{}{}
		seenContents = append(seenContents, content)
		uniqueResults = append(uniqueResults, item)
	}

	if len(uniqueResults) == 0 {
		uniqueResults = append(uniqueResults, results[0])
	}
	removed := len(results) - len(uniqueResults)
	if removed < 0 {
		removed = 0
	}
	return uniqueResults, removed
}

// postprocessGraphNodeFinder 图存储的节点查询能力
type postprocessGraphNodeFinder interface {
	FindNode(name string, ignoreCase bool) (string, error)
}

// postprocessGraphPathFinder 图存储的路径查询能力
type postprocessGraphPathFinder interface {
	FindPaths(startNode string, endNode string, maxDepth int, maxPaths int) ([][]string, error)
}

// postprocessMetadataStore 元数据的关系查询能力，签名与 storage.Store 保持一致
type postprocessMetadataStore interface {
	GetRelations(subject string, predicate string, object string, includeInactive bool) ([]map[string]any, error)
}

// MaybeApplySmartPathFallback 对齐 maybe_apply_smart_path_fallback
// 返回合并后的结果、是否补充、补充数量与错误
// Python 侧通过异常传播存储错误，Go 侧显式返回 error
func MaybeApplySmartPathFallback(
	query string,
	results []RetrievalResult,
	graphStore any,
	metadataStore any,
	enabled bool,
	threshold float64,
	allowedRelationIDs []string,
	maxDepth int,
	maxPaths int,
) ([]RetrievalResult, bool, int, error) {
	if !enabled || pyStrip(query) == "" {
		return results, false, 0, nil
	}
	if graphStore == nil || metadataStore == nil {
		return results, false, 0, nil
	}

	maxScore := 0.0
	if len(results) > 0 {
		maxScore = results[0].Score
	}
	if maxScore >= threshold {
		return results, false, 0, nil
	}

	paths, err := postprocessFindPathsFromQuery(query, graphStore, metadataStore, allowedRelationIDs, maxDepth, maxPaths)
	if err != nil {
		return nil, false, 0, err
	}
	if len(paths) == 0 {
		return results, false, 0, nil
	}

	pathResults := postprocessToRetrievalResults(paths)
	if len(pathResults) == 0 {
		return results, false, 0, nil
	}

	merged := make([]RetrievalResult, 0, len(pathResults)+len(results))
	merged = append(merged, pathResults...)
	merged = append(merged, results...)
	return merged, true, len(pathResults), nil
}

// postprocessFindPathsFromQuery 对齐 find_paths_from_query
func postprocessFindPathsFromQuery(
	query string,
	graphStore any,
	metadataStore any,
	allowedRelationIDs []string,
	maxDepth int,
	maxPaths int,
) ([]map[string]any, error) {
	entities := postprocessExtractEntities(query, graphStore)
	if len(entities) != 2 {
		return []map[string]any{}, nil
	}
	return postprocessFindPathsBetweenEntities(entities[0], entities[1], graphStore, metadataStore, allowedRelationIDs, maxDepth, maxPaths)
}

// postprocessExtractEntities 对齐 extract_entities，使用 n-gram 匹配图节点
func postprocessExtractEntities(query string, graphStore any) []string {
	finder, ok := graphStore.(postprocessGraphNodeFinder)
	if !ok || finder == nil {
		return []string{}
	}
	text := pyStrip(query)
	if text == "" {
		return []string{}
	}

	tokens := strings.Fields(strings.NewReplacer("?", " ", "!", " ", ".", " ").Replace(text))
	if len(tokens) == 0 {
		return []string{}
	}

	foundEntities := make([]string, 0, 2)
	seenEntities := make(map[string]struct{})
	skipIndices := make(map[int]struct{})
	maxN := len(tokens)
	if maxN > 4 {
		maxN = 4
	}

	for size := maxN; size >= 1; size-- {
		for start := 0; start+size <= len(tokens); start++ {
			skipped := false
			for index := start; index < start+size; index++ {
				if _, ok := skipIndices[index]; ok {
					skipped = true
					break
				}
			}
			if skipped {
				continue
			}
			span := strings.Join(tokens[start:start+size], " ")
			matchedNode, err := finder.FindNode(span, true)
			if err != nil || matchedNode == "" {
				continue
			}
			if _, ok := seenEntities[matchedNode]; !ok {
				seenEntities[matchedNode] = struct{}{}
				foundEntities = append(foundEntities, matchedNode)
			}
			for index := start; index < start+size; index++ {
				skipIndices[index] = struct{}{}
			}
		}
	}
	return foundEntities
}

// postprocessFindPathsBetweenEntities 对齐 find_paths_between_entities
func postprocessFindPathsBetweenEntities(
	startNode string,
	endNode string,
	graphStore any,
	metadataStore any,
	allowedRelationIDs []string,
	maxDepth int,
	maxPaths int,
) ([]map[string]any, error) {
	pathFinder, ok := graphStore.(postprocessGraphPathFinder)
	if !ok || pathFinder == nil {
		return []map[string]any{}, nil
	}
	metadata, ok := metadataStore.(postprocessMetadataStore)
	if !ok || metadata == nil {
		return []map[string]any{}, nil
	}

	paths, err := pathFinder.FindPaths(startNode, endNode, maxDepth, maxPaths)
	if err != nil {
		// Python 侧捕获图查询异常并回退为空列表
		return []map[string]any{}, nil
	}
	if len(paths) == 0 {
		return []map[string]any{}, nil
	}

	var allowed map[string]struct{}
	if allowedRelationIDs != nil {
		allowed = make(map[string]struct{}, len(allowedRelationIDs))
		for _, value := range allowedRelationIDs {
			allowed[value] = struct{}{}
		}
	}

	type edgeInfo struct {
		predicate    string
		direction    string
		relationHash string
	}
	edgeCache := make(map[string]edgeInfo)
	formattedPaths := make([]map[string]any, 0, len(paths))

	for _, pathNodes := range paths {
		if len(pathNodes) < 2 {
			continue
		}

		pathDesc := make([]string, 0, len(pathNodes)-1)
		relationHashes := make([]string, 0, len(pathNodes)-1)
		pathAllowed := true
		for index := 0; index < len(pathNodes)-1; index++ {
			u := pathNodes[index]
			v := pathNodes[index+1]

			cacheKey := u + "\x00" + v
			if v < u {
				cacheKey = v + "\x00" + u
			}
			info, cached := edgeCache[cacheKey]
			if !cached {
				info = edgeInfo{predicate: "related", direction: "->"}
				relations, relErr := metadata.GetRelations(u, "", v, false)
				if relErr != nil {
					return nil, relErr
				}
				if len(relations) == 0 {
					relations, relErr = metadata.GetRelations(v, "", u, false)
					if relErr != nil {
						return nil, relErr
					}
					info.direction = "<-"
				}
				if allowed != nil {
					filtered := make([]map[string]any, 0, len(relations))
					for _, row := range relations {
						if _, ok := allowed[postprocessString(row["hash"])]; ok {
							filtered = append(filtered, row)
						}
					}
					relations = filtered
				}
				if len(relations) > 0 {
					best := relations[0]
					for _, row := range relations[1:] {
						if postprocessConfidence(row) > postprocessConfidence(best) {
							best = row
						}
					}
					info.predicate = postprocessStringDefault(best["predicate"], "related")
					info.relationHash = pyStrip(postprocessString(best["hash"]))
				}
				edgeCache[cacheKey] = info
			}

			if allowed != nil && info.relationHash == "" {
				pathAllowed = false
				break
			}
			if info.relationHash != "" {
				relationHashes = append(relationHashes, info.relationHash)
			}

			step := "<-[" + info.predicate + "]-"
			if info.direction == "->" {
				step = "-[" + info.predicate + "]->"
			}
			pathDesc = append(pathDesc, step)
		}

		if !pathAllowed {
			continue
		}

		fullPath := pathNodes[0]
		for index, step := range pathDesc {
			fullPath += " " + step + " " + pathNodes[index+1]
		}
		formattedPaths = append(formattedPaths, map[string]any{
			"nodes":           append([]string{}, pathNodes...),
			"description":     fullPath,
			"relation_hashes": relationHashes,
		})
	}
	return formattedPaths, nil
}

// postprocessToRetrievalResults 对齐 to_retrieval_results
func postprocessToRetrievalResults(paths []map[string]any) []RetrievalResult {
	converted := make([]RetrievalResult, 0, len(paths))
	for _, item := range paths {
		description := pyStrip(postprocessString(item["description"]))
		if description == "" {
			continue
		}
		sum := sha1.Sum([]byte(description))
		pathHash := "path_" + hex.EncodeToString(sum[:])
		converted = append(converted, RetrievalResult{
			HashValue:  pathHash,
			Content:    "[Indirect Relation] " + description,
			Score:      0.95,
			ResultType: "relation",
			Source:     "graph_path",
			Metadata: map[string]any{
				"source":          "graph_path",
				"is_indirect":     true,
				"relation_hashes": postprocessAnySlice(item["relation_hashes"]),
				"nodes":           postprocessAnySlice(item["nodes"]),
			},
		})
	}
	return converted
}

// postprocessAnySlice 把路径字段转换为 []any
func postprocessAnySlice(value any) []any {
	switch typed := value.(type) {
	case nil:
		return []any{}
	case []string:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = item
		}
		return items
	case []any:
		return append([]any{}, typed...)
	default:
		return []any{typed}
	}
}

// postprocessString 对齐 str(value or "")
func postprocessString(value any) string {
	if !isTruthy(value) {
		return ""
	}
	return pyStr(value)
}

// postprocessStringDefault 对齐 str(value or default)
func postprocessStringDefault(value any, defaultValue string) string {
	text := postprocessString(value)
	if text == "" {
		return defaultValue
	}
	return text
}

// postprocessConfidence 对齐 row.get("confidence", 1.0)
func postprocessConfidence(row map[string]any) float64 {
	value, ok := row["confidence"]
	if !ok {
		return 1.0
	}
	parsed, err := pyFloat64(value)
	if err != nil {
		return 1.0
	}
	return parsed
}

// sortStrings 稳定排序，保留给路径结果调试
func sortStrings(values []string) []string {
	sorted := append([]string{}, values...)
	sort.Strings(sorted)
	return sorted
}

// formatPathDebug 输出路径调试字符串，保留可读性
func formatPathDebug(nodes []string, steps []string) string {
	return fmt.Sprintf("%v %v", nodes, steps)
}
