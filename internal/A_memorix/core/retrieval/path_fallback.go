package retrieval

// 路径回退辅助，对齐 A_memorix/core/utils/path_fallback_service.py
// Python 侧通过 getattr 调用 GraphStore / MetadataStore，Go 侧用最小接口收口

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strings"
)

// PathFallbackGraph 是路径回退需要的图存储子集
type PathFallbackGraph interface {
	FindNode(name string, ignoreCase bool) (string, bool)
	FindPaths(startNode, endNode string, maxDepth int, maxPaths int) [][]string
}

// PathFallbackMetadata 是路径回退需要的元数据存储子集
type PathFallbackMetadata interface {
	GetRelations(subject, predicate, object string, includeInactive bool) ([]map[string]any, error)
}

// ExtractEntities 从查询中提取至多两个图节点，对齐 extract_entities
func ExtractEntities(query string, graphStore PathFallbackGraph) []string {
	if graphStore == nil {
		return []string{}
	}
	text := strings.TrimSpace(query)
	if text == "" {
		return []string{}
	}
	replacer := strings.NewReplacer("?", " ", "!", " ", ".", " ")
	tokens := strings.Fields(replacer.Replace(text))
	if len(tokens) == 0 {
		return []string{}
	}

	found := map[string]struct{}{}
	ordered := []string{}
	skip := map[int]struct{}{}
	maxN := len(tokens)
	if maxN > 4 {
		maxN = 4
	}
	for size := maxN; size >= 1; size-- {
		for start := 0; start+size <= len(tokens); start++ {
			skipped := false
			for index := start; index < start+size; index++ {
				if _, exists := skip[index]; exists {
					skipped = true
					break
				}
			}
			if skipped {
				continue
			}
			span := strings.Join(tokens[start:start+size], " ")
			matched, ok := graphStore.FindNode(span, true)
			if !ok || matched == "" {
				continue
			}
			if _, exists := found[matched]; !exists {
				found[matched] = struct{}{}
				ordered = append(ordered, matched)
			}
			for index := start; index < start+size; index++ {
				skip[index] = struct{}{}
			}
		}
	}
	return ordered
}

// FindPathsBetweenEntities 查找并补充两个节点之间的间接路径信息
func FindPathsBetweenEntities(
	startNode string,
	endNode string,
	graphStore PathFallbackGraph,
	metadataStore PathFallbackMetadata,
	allowedRelationIDs []string,
	maxDepth int,
	maxPaths int,
) []map[string]any {
	if graphStore == nil || metadataStore == nil {
		return []map[string]any{}
	}
	if maxDepth <= 0 {
		maxDepth = 3
	}
	if maxPaths <= 0 {
		maxPaths = 5
	}
	paths := graphStore.FindPaths(startNode, endNode, maxDepth, maxPaths)
	if len(paths) == 0 {
		return []map[string]any{}
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
	edgeCache := map[string]edgeInfo{}
	formatted := make([]map[string]any, 0, len(paths))

	for _, pathNodes := range paths {
		if len(pathNodes) < 2 {
			continue
		}
		pathDesc := make([]string, 0, len(pathNodes)-1)
		relationHashes := []string{}
		pathAllowed := true
		for index := 0; index+1 < len(pathNodes); index++ {
			u := pathNodes[index]
			v := pathNodes[index+1]
			cacheKeyParts := []string{u, v}
			sort.Strings(cacheKeyParts)
			cacheKey := strings.Join(cacheKeyParts, "\x00")

			info, cached := edgeCache[cacheKey]
			if !cached {
				info = edgeInfo{predicate: "related", direction: "->"}
				relations, err := metadataStore.GetRelations(u, "", v, false)
				if err != nil {
					return []map[string]any{}
				}
				if len(relations) == 0 {
					relations, err = metadataStore.GetRelations(v, "", u, false)
					if err != nil {
						return []map[string]any{}
					}
					info.direction = "<-"
				}
				if allowed != nil {
					filtered := make([]map[string]any, 0, len(relations))
					for _, row := range relations {
						if _, ok := allowed[stringValue(row["hash"])]; ok {
							filtered = append(filtered, row)
						}
					}
					relations = filtered
				}
				if len(relations) > 0 {
					best := relations[0]
					bestConfidence := pathConfidence(best)
					for _, row := range relations[1:] {
						if confidence := pathConfidence(row); confidence > bestConfidence {
							best = row
							bestConfidence = confidence
						}
					}
					predicate := stringValue(best["predicate"])
					if predicate == "" {
						predicate = "related"
					}
					info.predicate = predicate
					info.relationHash = strings.TrimSpace(stringValue(best["hash"]))
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
			if info.direction == "<-" {
				pathDesc = append(pathDesc, "<-["+info.predicate+"]-")
			} else {
				pathDesc = append(pathDesc, "-["+info.predicate+"]->")
			}
		}
		if !pathAllowed {
			continue
		}
		var builder strings.Builder
		builder.WriteString(pathNodes[0])
		for index, step := range pathDesc {
			builder.WriteString(" ")
			builder.WriteString(step)
			builder.WriteString(" ")
			builder.WriteString(pathNodes[index+1])
		}
		formatted = append(formatted, map[string]any{
			"nodes":           append([]string{}, pathNodes...),
			"description":     builder.String(),
			"relation_hashes": relationHashes,
		})
	}
	return formatted
}

func pathConfidence(row map[string]any) float64 {
	if value, exists := row["confidence"]; exists && value != nil {
		if number, ok := numericValue(value); ok {
			return number
		}
	}
	return 1.0
}

// FindPathsFromQuery 从查询中提取实体并解析间接路径
func FindPathsFromQuery(
	query string,
	graphStore PathFallbackGraph,
	metadataStore PathFallbackMetadata,
	allowedRelationIDs []string,
	maxDepth int,
	maxPaths int,
) []map[string]any {
	entities := ExtractEntities(query, graphStore)
	if len(entities) != 2 {
		return []map[string]any{}
	}
	return FindPathsBetweenEntities(entities[0], entities[1], graphStore, metadataStore, allowedRelationIDs, maxDepth, maxPaths)
}

// ToRetrievalResults 将路径结果转换为统一检索链路使用的结果，对齐 to_retrieval_results
func ToRetrievalResults(paths []map[string]any) []RetrievalResult {
	converted := make([]RetrievalResult, 0, len(paths))
	for _, item := range paths {
		description := strings.TrimSpace(stringValue(item["description"]))
		if description == "" {
			continue
		}
		digest := sha1.Sum([]byte(description))
		pathHash := "path_" + hex.EncodeToString(digest[:])
		relationHashes := []string{}
		switch values := item["relation_hashes"].(type) {
		case []string:
			relationHashes = append(relationHashes, values...)
		case []any:
			for _, value := range values {
				relationHashes = append(relationHashes, stringValue(value))
			}
		}
		nodes := []string{}
		switch values := item["nodes"].(type) {
		case []string:
			nodes = append(nodes, values...)
		case []any:
			for _, value := range values {
				nodes = append(nodes, stringValue(value))
			}
		}
		converted = append(converted, RetrievalResult{
			HashValue:  pathHash,
			Content:    "[Indirect Relation] " + description,
			Score:      0.95,
			ResultType: "relation",
			Source:     "graph_path",
			Metadata: map[string]any{
				"source":          "graph_path",
				"is_indirect":     true,
				"relation_hashes": relationHashes,
				"nodes":           nodes,
			},
		})
	}
	return converted
}
