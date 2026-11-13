package storage

// A_memorix 图查询与路径算法，对齐 Python graph_store.py 的邻居/路径/哈希关系语义

import (
	"sort"
	"strconv"
	"strings"
)

// GetNeighbors 返回节点的出邻居
// 与 Python 一致：CSR 格式直接返回存储项列索引，其他格式过滤显式 0
func (g *GraphStore) GetNeighbors(node string) []string {
	idx, ok := g.nodeToIdx[g.canonicalize(node)]
	if !ok || g.adjacency == nil {
		return []string{}
	}
	indices := g.rowNeighborIndices(g.adjacency, idx, g.matrixFormat != SparseMatrixFormatCSR)
	result := make([]string, 0, len(indices))
	for _, col := range indices {
		result = append(result, g.nodes[col])
	}
	return result
}

// GetWeightedNeighbors 返回节点的出邻居及权重，过滤权重为 0 的存储项
func (g *GraphStore) GetWeightedNeighbors(node string) []WeightedNeighbor {
	idx, ok := g.nodeToIdx[g.canonicalize(node)]
	if !ok || g.adjacency == nil {
		return []WeightedNeighbor{}
	}
	result := make([]WeightedNeighbor, 0, len(g.adjacency.rows[idx]))
	for _, entry := range g.adjacency.rows[idx] {
		if entry.weight == 0 {
			continue
		}
		result = append(result, WeightedNeighbor{Node: g.nodes[entry.col], Weight: float64(entry.weight)})
	}
	return result
}

// GetInNeighbors 返回节点的入邻居
func (g *GraphStore) GetInNeighbors(node string) []string {
	idx, ok := g.nodeToIdx[g.canonicalize(node)]
	if !ok || g.adjacency == nil {
		return []string{}
	}
	g.ensureAdjacencyT()
	if g.adjacencyT == nil {
		return []string{}
	}
	indices := g.rowNeighborIndices(g.adjacencyT, idx, false)
	result := make([]string, 0, len(indices))
	for _, row := range indices {
		result = append(result, g.nodes[row])
	}
	return result
}

// rowNeighborIndices 返回指定行的邻居索引，filterZeros 为真时跳过显式 0
func (g *GraphStore) rowNeighborIndices(matrix *sparseMatrix, row int, filterZeros bool) []int32 {
	if matrix == nil || row < 0 || row >= matrix.size {
		return nil
	}
	entries := matrix.rows[row]
	if !filterZeros {
		indices := make([]int32, 0, len(entries))
		for _, entry := range entries {
			indices = append(indices, entry.col)
		}
		return indices
	}
	indices := make([]int32, 0, len(entries))
	for _, entry := range entries {
		if entry.weight != 0 {
			indices = append(indices, entry.col)
		}
	}
	return indices
}

// ensureAdjacencyT 确保转置缓存可用，并清除脏标记
// 与 Python 相同：该操作会清除 _adjacency_dirty，可能让 saliency 缓存变旧
func (g *GraphStore) ensureAdjacencyT() {
	if g.adjacency == nil {
		g.adjacencyT = nil
		return
	}
	if g.adjacencyDirty || g.adjacencyT == nil {
		g.adjacencyT = g.adjacency.transpose()
		g.adjacencyDirty = false
	}
}

// HasEdgeHashMap 判断是否存在边关系哈希映射
func (g *GraphStore) HasEdgeHashMap() bool {
	return len(g.edgeHashMap) > 0
}

// GetRelationHashesForEdge 返回边关系哈希副本，边不存在时返回空集合
func (g *GraphStore) GetRelationHashesForEdge(source, target string) map[string]struct{} {
	srcIdx, okSrc := g.nodeToIdx[g.canonicalize(source)]
	dstIdx, okDst := g.nodeToIdx[g.canonicalize(target)]
	if !okSrc || !okDst {
		return map[string]struct{}{}
	}
	hashes, ok := g.edgeHashMap[edgeKey{src: int32(srcIdx), dst: int32(dstIdx)}]
	if !ok {
		return map[string]struct{}{}
	}
	copied := make(map[string]struct{}, len(hashes))
	for hash := range hashes {
		copied[hash] = struct{}{}
	}
	return copied
}

// GetIncidentRelationHashes 返回与节点关联的关系哈希，按 (源名小写, 目标名小写, 哈希) 排序去重
// limit 小于 0 表示不限制，否则按 max(1, limit) 截断
func (g *GraphStore) GetIncidentRelationHashes(node string, limit int) []string {
	idx, ok := g.nodeToIdx[g.canonicalize(node)]
	if !ok || len(g.edgeHashMap) == 0 {
		return []string{}
	}
	limitVal := -1
	if limit >= 0 {
		limitVal = limit
		if limitVal < 1 {
			limitVal = 1
		}
	}

	type incidentEntry struct {
		source string
		target string
		hash   string
	}
	collected := make([]incidentEntry, 0)
	for _, key := range g.edgeHashOrder {
		if int(key.src) != idx && int(key.dst) != idx {
			continue
		}
		hashes, ok := g.edgeHashMap[key]
		if !ok {
			continue
		}
		sourceName := ""
		targetName := ""
		if key.src >= 0 && int(key.src) < len(g.nodes) {
			sourceName = g.nodes[key.src]
		}
		if key.dst >= 0 && int(key.dst) < len(g.nodes) {
			targetName = g.nodes[key.dst]
		}
		for hash := range hashes {
			text := strings.TrimSpace(hash)
			if text == "" {
				continue
			}
			collected = append(collected, incidentEntry{source: sourceName, target: targetName, hash: text})
		}
	}
	sort.SliceStable(collected, func(i, j int) bool {
		srcI := strings.ToLower(collected[i].source)
		srcJ := strings.ToLower(collected[j].source)
		if srcI != srcJ {
			return srcI < srcJ
		}
		tgtI := strings.ToLower(collected[i].target)
		tgtJ := strings.ToLower(collected[j].target)
		if tgtI != tgtJ {
			return tgtI < tgtJ
		}
		return collected[i].hash < collected[j].hash
	})
	out := make([]string, 0, len(collected))
	seen := make(map[string]struct{}, len(collected))
	for _, entry := range collected {
		if _, ok := seen[entry.hash]; ok {
			continue
		}
		seen[entry.hash] = struct{}{}
		out = append(out, entry.hash)
		if limitVal >= 0 && len(out) >= limitVal {
			break
		}
	}
	return out
}

// EdgeContainsRelationHash 判断边是否包含指定关系哈希，空哈希直接返回 false
func (g *GraphStore) EdgeContainsRelationHash(source, target, hashValue string) bool {
	if hashValue == "" {
		return false
	}
	_, ok := g.GetRelationHashesForEdge(source, target)[hashValue]
	return ok
}

// IterEdgeHashEntries 按插入顺序返回边关系哈希条目，跳过空集合与越界索引
func (g *GraphStore) IterEdgeHashEntries() []EdgeHashEntry {
	out := make([]EdgeHashEntry, 0, len(g.edgeHashMap))
	for _, key := range g.edgeHashOrder {
		hashes, ok := g.edgeHashMap[key]
		if !ok || len(hashes) == 0 {
			continue
		}
		if key.src < 0 || key.dst < 0 || int(key.src) >= len(g.nodes) || int(key.dst) >= len(g.nodes) {
			continue
		}
		copied := make(map[string]struct{}, len(hashes))
		for hash := range hashes {
			copied[hash] = struct{}{}
		}
		out = append(out, EdgeHashEntry{Source: g.nodes[key.src], Target: g.nodes[key.dst], Hashes: copied})
	}
	return out
}

// DeactivateEdges 冻结边（权重置 0 并删除存储项），返回实际冻结数量
func (g *GraphStore) DeactivateEdges(edges []Edge) int {
	if len(edges) == 0 || g.adjacency == nil {
		return 0
	}
	deactivated := 0
	_ = g.BatchUpdate(func() error {
		for _, edge := range edges {
			srcIdx, okSrc := g.nodeToIdx[g.canonicalize(edge.Source)]
			dstIdx, okDst := g.nodeToIdx[g.canonicalize(edge.Target)]
			if !okSrc || !okDst {
				continue
			}
			if g.adjacency.get(srcIdx, dstIdx) == 0 {
				continue
			}
			g.adjacency.set(srcIdx, dstIdx, 0)
			deactivated++
		}
		return nil
	})
	g.adjacencyDirty = true
	if deactivated > 0 {
		g.recordGraphChange(false)
	}
	return deactivated
}

// FindPaths 以 BFS 查找有向边与入边均可通行的路径
func (g *GraphStore) FindPaths(startNode, endNode string, maxDepth int, maxPaths int, maxExpansions int) [][]string {
	startIdx, okStart := g.nodeToIdx[g.canonicalize(startNode)]
	endIdx, okEnd := g.nodeToIdx[g.canonicalize(endNode)]
	if !okStart || !okEnd || g.adjacency == nil {
		return [][]string{}
	}
	g.ensureAdjacencyT()

	type pathState struct {
		node int
		path []int
	}
	foundPaths := make([][]string, 0)
	uniquePaths := make(map[string]struct{})
	queue := []pathState{{node: startIdx, path: []int{startIdx}}}
	head := 0
	expansions := 0

	for head < len(queue) {
		current := queue[head]
		head++
		path := current.path
		if len(path) > maxDepth+1 {
			continue
		}
		if current.node == endIdx {
			names := make([]string, len(path))
			for i, idx := range path {
				names[i] = g.nodes[idx]
			}
			key := pathKey(path)
			if _, exists := uniquePaths[key]; !exists {
				uniquePaths[key] = struct{}{}
				foundPaths = append(foundPaths, names)
			}
			if len(foundPaths) >= maxPaths {
				break
			}
			continue
		}
		if expansions >= maxExpansions {
			break
		}
		expansions++

		neighbors := g.rowNeighborIndices(g.adjacency, current.node, g.matrixFormat != SparseMatrixFormatCSR)
		if g.adjacencyT != nil {
			neighbors = append(neighbors, g.rowNeighborIndices(g.adjacencyT, current.node, false)...)
		}

		seenInPath := make(map[int]struct{}, len(path))
		for _, idx := range path {
			seenInPath[idx] = struct{}{}
		}
		queued := make(map[int]struct{}, len(neighbors))
		for _, neighbor := range neighbors {
			next := int(neighbor)
			if _, exists := seenInPath[next]; exists {
				continue
			}
			if _, exists := queued[next]; exists {
				continue
			}
			queued[next] = struct{}{}
			nextPath := make([]int, len(path)+1)
			copy(nextPath, path)
			nextPath[len(path)] = next
			queue = append(queue, pathState{node: next, path: nextPath})
		}
	}
	return foundPaths
}

// pathKey 构造路径去重键
func pathKey(path []int) string {
	var builder strings.Builder
	for _, idx := range path {
		builder.WriteString(strconv.Itoa(idx))
		builder.WriteByte(',')
	}
	return builder.String()
}

// GetLowWeightEdges 返回权重低于阈值的边，包含显式 0
func (g *GraphStore) GetLowWeightEdges(threshold float64) []Edge {
	if g.adjacency == nil {
		return []Edge{}
	}
	result := make([]Edge, 0)
	g.adjacency.forEach(func(row int, col int, weight float32) {
		// 与 Python 的 "weight >= threshold 才跳过" 保持一致，NaN 也会被视为低权重
		if !(float64(weight) >= threshold) {
			result = append(result, Edge{Source: g.nodes[row], Target: g.nodes[col]})
		}
	})
	return result
}

// GetIsolatedNodes 返回活跃度为 0 的节点
// includeInactive 为真时排除仍存在关系哈希（冻结边）的端点
func (g *GraphStore) GetIsolatedNodes(includeInactive bool) []string {
	if g.adjacency == nil {
		return append([]string{}, g.nodes...)
	}
	outDegrees := make([]float32, len(g.nodes))
	for idx := range g.nodes {
		if idx < g.adjacency.size {
			outDegrees[idx] = g.adjacency.sumRow(idx)
		}
	}
	inDegrees := g.adjacency.columnSums()
	isolated := make([]int, 0)
	for idx := range g.nodes {
		var inDegree float32
		if idx < len(inDegrees) {
			inDegree = inDegrees[idx]
		}
		if outDegrees[idx]+inDegree == 0 {
			isolated = append(isolated, idx)
		}
	}
	if len(isolated) == 0 {
		return []string{}
	}
	if includeInactive && len(g.edgeHashMap) > 0 {
		frozen := make(map[int32]struct{})
		for key, hashes := range g.edgeHashMap {
			if len(hashes) == 0 {
				continue
			}
			frozen[key.src] = struct{}{}
			frozen[key.dst] = struct{}{}
		}
		filtered := isolated[:0]
		for _, idx := range isolated {
			if _, ok := frozen[int32(idx)]; !ok {
				filtered = append(filtered, idx)
			}
		}
		isolated = filtered
	}
	result := make([]string, 0, len(isolated))
	for _, idx := range isolated {
		result = append(result, g.nodes[idx])
	}
	return result
}
