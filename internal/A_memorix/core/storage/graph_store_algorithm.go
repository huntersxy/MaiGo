package storage

// A_memorix 图算法，对齐 Python graph_store.py 的 PageRank、衰减与关系哈希维护

import (
	"fmt"
	"math"
	"sort"

	"go.uber.org/zap"
)

// ComputePageRank 计算个性化 PageRank，参数与 Python 版本一一对应
func (g *GraphStore) ComputePageRank(personalization map[string]float64, alpha float64, maxIter int, tol float64) map[string]float64 {
	if g.adjacency == nil || len(g.nodes) == 0 {
		zap.L().Warn("图为空，无法计算PageRank")
		return map[string]float64{}
	}
	n := len(g.nodes)
	adjacency := g.adjacency

	// 出度按 float32 求和，与 scipy sum(axis=1) 的 dtype 保持一致
	outDegrees := make([]float32, n)
	for idx := 0; idx < n; idx++ {
		if idx < adjacency.size {
			outDegrees[idx] = adjacency.sumRow(idx)
		}
	}
	outDegreesInv := make([]float32, n)
	for idx, degree := range outDegrees {
		if degree != 0 {
			outDegreesInv[idx] = 1.0 / degree
		}
	}

	// 转移矩阵按目标行组织：M[target, source] = float32(weight * inv[source])
	transition := make([][]matrixEntry, n)
	for source := 0; source < adjacency.size; source++ {
		for _, entry := range adjacency.rows[source] {
			if int(entry.col) >= n {
				continue
			}
			value := entry.weight * outDegreesInv[source]
			transition[entry.col] = append(transition[entry.col], matrixEntry{col: int32(source), weight: value})
		}
	}

	var p []float64
	if personalization == nil {
		p = make([]float64, n)
		for idx := range p {
			p[idx] = 1.0 / float64(n)
		}
	} else {
		p = make([]float64, n)
		keys := make([]string, 0, len(personalization))
		for node := range personalization {
			keys = append(keys, node)
		}
		sort.Strings(keys)
		for _, node := range keys {
			canon := g.canonicalize(node)
			weight := personalization[node]
			if idx, ok := g.nodeToIdx[canon]; ok && !math.IsNaN(weight) && !math.IsInf(weight, 0) && weight > 0.0 {
				p[idx] += weight
			}
		}
		var sum float64
		for _, value := range p {
			sum += value
		}
		if sum == 0 {
			for idx := range p {
				p[idx] = 1.0 / float64(n)
			}
		} else {
			for idx := range p {
				p[idx] /= sum
			}
		}
	}
	pOrig := append([]float64(nil), p...)

	converged := false
	for iteration := 0; iteration < maxIter; iteration++ {
		pNew := make([]float64, n)
		for target := 0; target < n; target++ {
			var total float64
			for _, entry := range transition[target] {
				total += float64(entry.weight) * p[entry.col]
			}
			pNew[target] = alpha*total + (1-alpha)*pOrig[target]
		}
		var currentSum float64
		for _, value := range pNew {
			currentSum += value
		}
		if currentSum < 1.0 {
			diff := 1.0 - currentSum
			for idx := range pNew {
				pNew[idx] += diff * pOrig[idx]
			}
		}
		var delta float64
		for idx := range pNew {
			delta += math.Abs(pNew[idx] - p[idx])
		}
		if delta < tol {
			zap.L().Debug("PageRank 已收敛", zap.Int("iterations", iteration+1))
			p = pNew
			converged = true
			break
		}
		p = pNew
	}
	if !converged {
		zap.L().Warn("PageRank 未在限定迭代次数内收敛", zap.Int("max_iter", maxIter))
	}

	result := make(map[string]float64, n)
	for idx, name := range g.nodes {
		result[name] = p[idx]
	}
	return result
}

// GetSaliencyScores 获取节点显著性得分，未脏时复用缓存
func (g *GraphStore) GetSaliencyScores() map[string]float64 {
	if g.saliencyCache != nil && !g.adjacencyDirty {
		return g.saliencyCache
	}
	zap.L().Debug("正在计算节点显著性得分")
	scores := g.ComputePageRank(nil, DefaultPageRankAlpha, DefaultPageRankMaxIter, DefaultPageRankTolerance)
	g.saliencyCache = scores
	return scores
}

// ConnectSynonyms 按相似度矩阵上三角连接同义词节点，返回新增边数量
// Python 侧在 scipy 1.18 下该实现实际会抛 TypeError，这里按文档意图实现
func (g *GraphStore) ConnectSynonyms(similarityMatrix [][]float64, nodeList []string, threshold float64) (int, error) {
	if len(similarityMatrix) != len(nodeList) {
		return 0, fmt.Errorf("节点列表长度与相似度矩阵维度不匹配: %d vs %d", len(nodeList), len(similarityMatrix))
	}
	edges := make([]Edge, 0)
	weights := make([]float64, 0)
	for i := 0; i < len(nodeList); i++ {
		for j := i + 1; j < len(similarityMatrix[i]) && j < len(nodeList); j++ {
			value := similarityMatrix[i][j]
			if value >= threshold && value < 1.0 {
				edges = append(edges, Edge{Source: nodeList[i], Target: nodeList[j]})
				weights = append(weights, value)
			}
		}
	}
	if len(edges) == 0 {
		return 0, nil
	}
	count, err := g.AddEdges(edges, weights, nil)
	if err != nil {
		return 0, err
	}
	zap.L().Info("连接相似节点完成", zap.Int("count", count), zap.Float64("threshold", threshold))
	return count, nil
}

// Decay 对全图权重乘以衰减因子，minActiveWeight 仅对齐 Python 签名
func (g *GraphStore) Decay(factor float64, minActiveWeight float64) {
	_ = minActiveWeight
	if g.adjacency == nil || factor >= 1.0 || factor <= 0.0 {
		return
	}
	graphChanged := false
	if g.adjacency.numEdges() > 0 {
		for _, entries := range g.adjacency.rows {
			for _, entry := range entries {
				if entry.weight != 0.0 {
					graphChanged = true
				}
			}
		}
	}
	factor32 := float32(factor)
	for row := range g.adjacency.rows {
		for idx := range g.adjacency.rows[row] {
			g.adjacency.rows[row][idx].weight *= factor32
		}
	}
	g.adjacencyDirty = true
	if graphChanged {
		g.recordGraphChange(false)
	}
}

// PruneRelationHashes 按 (源, 目标, 哈希) 删除关系哈希，集合清空的边会被冻结
func (g *GraphStore) PruneRelationHashes(operations []RelationHashOperation) {
	if len(operations) == 0 {
		return
	}
	removal := make([]Edge, 0, len(operations))
	removalSeen := make(map[string]struct{}, len(operations))
	for _, operation := range operations {
		srcIdx, okSrc := g.nodeToIdx[g.canonicalize(operation.Source)]
		dstIdx, okDst := g.nodeToIdx[g.canonicalize(operation.Target)]
		if !okSrc || !okDst {
			continue
		}
		key := edgeKey{src: int32(srcIdx), dst: int32(dstIdx)}
		hashes, ok := g.edgeHashMap[key]
		if !ok {
			continue
		}
		delete(hashes, operation.Hash)
		if len(hashes) != 0 {
			continue
		}
		// 只有哈希集合被清空的边才进入移除列表，对齐 Python 的 edges_to_check_removal
		g.removeEdgeHash(key)
		removalKey := operation.Source + "\x00" + operation.Target
		if _, exists := removalSeen[removalKey]; exists {
			continue
		}
		removalSeen[removalKey] = struct{}{}
		removal = append(removal, Edge{Source: operation.Source, Target: operation.Target})
	}
	g.DeactivateEdges(removal)
	g.totalEdgesDeleted += int64(len(removal))
}

// RebuildEdgeHashMap 用关系三元组重建边关系哈希映射，返回成功写入的条目数
func (g *GraphStore) RebuildEdgeHashMap(triples []RelationTriple) int {
	g.edgeHashMap = map[edgeKey]map[string]struct{}{}
	g.edgeHashOrder = nil
	count := 0
	for _, triple := range triples {
		if triple.Hash == "" {
			continue
		}
		srcIdx, okSrc := g.nodeToIdx[g.canonicalize(triple.Subject)]
		dstIdx, okDst := g.nodeToIdx[g.canonicalize(triple.Object)]
		if !okSrc || !okDst {
			continue
		}
		g.addEdgeHash(edgeKey{src: int32(srcIdx), dst: int32(dstIdx)}, triple.Hash)
		count++
	}
	g.adjacencyDirty = true
	return count
}
