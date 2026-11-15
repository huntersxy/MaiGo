package expressionvector

import (
	"math"
	"sort"
)

// 对齐 expression_vector_index 的聚类召回常量
const (
	expressionVectorClusterPoolSize = 16
	expressionVectorCandidateLimit  = 50
	expressionVectorItemWeight      = 0.875
	expressionVectorClusterWeight   = 0.125
	expressionVectorClusterMax      = 80
)

// ExpressionVectorClusterStore 可选聚类召回能力；未实现时回退纯余弦候选
type ExpressionVectorClusterStore interface {
	ClusterScored(query []float32, filter ExpressionVectorFilter, clusterPoolSize, limit int) []ExpressionVectorHit
}

type expressionVectorClusterState struct {
	labels    map[uint]int
	centers   [][]float32
	dimension int
}

type expressionVectorClusterSample struct {
	id     uint
	vector []float32
}

// ClusterScored 对齐 expression_vector_index.select_candidates 的聚类召回与加权打分
// 先按簇中心相似度选簇，再按 item/cluster 加权分数排序，MMR 由调用方执行
func (s *InMemoryExpressionVectorStore) ClusterScored(query []float32, filter ExpressionVectorFilter, clusterPoolSize, limit int) []ExpressionVectorHit {
	if s == nil || len(query) == 0 {
		return nil
	}
	queryNorm := vectorL2Norm(query)
	if queryNorm == 0 {
		return nil
	}

	s.mu.RLock()
	allEntries := make([]expressionVectorClusterSample, 0, len(s.entries))
	entries := make([]expressionVectorClusterSample, 0, len(s.entries))
	for id, entry := range s.entries {
		if len(entry.vector) != len(query) {
			continue
		}
		allEntries = append(allEntries, expressionVectorClusterSample{id: id, vector: entry.vector})
		if !expressionVectorAllowed(id, filter) || !expressionVectorSessionMatch(entry.sessionID, filter) {
			continue
		}
		entries = append(entries, expressionVectorClusterSample{id: id, vector: entry.vector})
	}
	s.mu.RUnlock()
	if len(entries) == 0 {
		return nil
	}

	state := s.clusterStateFor(allEntries)
	if state == nil || len(state.centers) == 0 {
		return nil
	}

	clusterScores := make([]float32, len(state.centers))
	for index, center := range state.centers {
		denom := vectorL2Norm(center) * queryNorm
		if denom == 0 {
			continue
		}
		clusterScores[index] = dotProduct(center, query) / denom
	}
	orderedClusters := make([]int, len(clusterScores))
	for index := range orderedClusters {
		orderedClusters[index] = index
	}
	sort.Slice(orderedClusters, func(i, j int) bool {
		left, right := clusterScores[orderedClusters[i]], clusterScores[orderedClusters[j]]
		if left != right {
			return left > right
		}
		return orderedClusters[i] < orderedClusters[j]
	})

	byCluster := make(map[int][]expressionVectorClusterSample, len(state.centers))
	for _, entry := range entries {
		clusterID, ok := state.labels[entry.id]
		if !ok {
			continue
		}
		byCluster[clusterID] = append(byCluster[clusterID], entry)
	}

	effectiveClusterPool := clusterPoolSize
	if effectiveClusterPool < 1 {
		effectiveClusterPool = 1
	}
	effectiveLimit := limit
	if effectiveLimit < 1 {
		effectiveLimit = 1
	}
	if effectiveLimit > expressionVectorCandidateLimit {
		effectiveLimit = expressionVectorCandidateLimit
	}

	selectedClusters := 0
	pool := make([]expressionVectorClusterSample, 0, effectiveLimit)
	for _, clusterID := range orderedClusters {
		members := byCluster[clusterID]
		if len(members) == 0 {
			continue
		}
		selectedClusters++
		pool = append(pool, members...)
		if selectedClusters >= effectiveClusterPool && len(pool) >= effectiveLimit {
			break
		}
	}
	if len(pool) == 0 {
		return nil
	}

	hits := make([]ExpressionVectorHit, 0, len(pool))
	for _, entry := range pool {
		clusterID := state.labels[entry.id]
		itemSimilarity := float32(0)
		if denom := vectorL2Norm(entry.vector) * queryNorm; denom != 0 {
			itemSimilarity = dotProduct(entry.vector, query) / denom
		}
		score := itemSimilarity*expressionVectorItemWeight + clusterScores[clusterID]*expressionVectorClusterWeight
		hits = append(hits, ExpressionVectorHit{
			ID:     entry.id,
			Score:  score,
			Vector: append([]float32(nil), entry.vector...),
		})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID > hits[j].ID
	})
	return hits
}

func (s *InMemoryExpressionVectorStore) clusterStateFor(entries []expressionVectorClusterSample) *expressionVectorClusterState {
	s.clusterMu.Lock()
	defer s.clusterMu.Unlock()
	dimension := 0
	if len(entries) > 0 {
		dimension = len(entries[0].vector)
	}
	if !s.clusterDirty && s.clusterState != nil && len(s.clusterState.labels) == len(entries) && s.clusterState.dimension == dimension {
		return s.clusterState
	}
	s.clusterState = buildExpressionVectorClusters(entries)
	s.clusterDirty = false
	return s.clusterState
}

// clusterLabels 返回当前索引条目的簇标签，持久化层写入 WebUI 聚类摘要
func (s *InMemoryExpressionVectorStore) clusterLabels() map[uint]int {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	entries := make([]expressionVectorClusterSample, 0, len(s.entries))
	for id, entry := range s.entries {
		if len(entry.vector) == 0 {
			continue
		}
		entries = append(entries, expressionVectorClusterSample{id: id, vector: entry.vector})
	}
	s.mu.RUnlock()
	state := s.clusterStateFor(entries)
	if state == nil {
		return nil
	}
	labels := make(map[uint]int, len(state.labels))
	for id, label := range state.labels {
		labels[id] = label
	}
	return labels
}

func (s *InMemoryExpressionVectorStore) invalidateClusters() {
	if s == nil {
		return
	}
	s.clusterMu.Lock()
	s.clusterDirty = true
	s.clusterMu.Unlock()
}

// buildExpressionVectorClusters 在归一化向量上执行确定性 cosine k-means
func buildExpressionVectorClusters(entries []expressionVectorClusterSample) *expressionVectorClusterState {
	if len(entries) == 0 {
		return nil
	}
	// embedding 后端切换会产生不同维度的向量，聚类只使用首个维度，避免点积越界
	dimension := len(entries[0].vector)
	filtered := make([]expressionVectorClusterSample, 0, len(entries))
	for _, entry := range entries {
		if len(entry.vector) == dimension {
			filtered = append(filtered, entry)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	entries = filtered
	vectors := make([][]float32, len(entries))
	for index, entry := range entries {
		vectors[index] = normalizeVectorForClustering(entry.vector)
	}
	clusterCount := chooseExpressionVectorClusterCount(len(entries), 0)
	centers := initializeClusterCenters(vectors, clusterCount)
	labels := make([]int, len(vectors))
	for iteration := 0; iteration < 100; iteration++ {
		nextLabels := assignClusterLabels(vectors, centers)
		repairEmptyClusterLabels(nextLabels, vectors, centers)
		nextCenters := buildClusterCenters(vectors, nextLabels, centers)
		converged := true
		for index := range labels {
			if labels[index] != nextLabels[index] {
				converged = false
				break
			}
		}
		labels = nextLabels
		centers = nextCenters
		if converged {
			break
		}
	}
	state := &expressionVectorClusterState{
		labels:    make(map[uint]int, len(entries)),
		centers:   centers,
		dimension: len(entries[0].vector),
	}
	for index, entry := range entries {
		state.labels[entry.id] = labels[index]
	}
	return state
}

// initializeClusterCenters 对齐 Python _run_kmeans 的 k-means++ 初始化
// 使用 numpy PCG64 seed 20260621，首中心 rng.integers(n)，后续按距离概率 rng.choice
func initializeClusterCenters(vectors [][]float32, clusterCount int) [][]float32 {
	indices := initializeClusterCenterIndices(vectors, clusterCount)
	centers := make([][]float32, 0, len(indices))
	for _, index := range indices {
		centers = append(centers, append([]float32(nil), vectors[index]...))
	}
	return centers
}

func initializeClusterCenterIndices(vectors [][]float32, clusterCount int) []int {
	if len(vectors) == 0 || clusterCount <= 0 {
		return nil
	}
	if clusterCount > len(vectors) {
		clusterCount = len(vectors)
	}
	if clusterCount <= 1 {
		return []int{0}
	}
	rng := newExpressionVectorPCG64(20260621)
	first := int(rng.bounded32(uint32(len(vectors) - 1)))
	indices := make([]int, 0, clusterCount)
	indices = append(indices, first)
	closestDistances := make([]float32, len(vectors))
	for index, vector := range vectors {
		closestDistances[index] = max(1-dotProduct(vector, vectors[first]), 0)
	}
	closestDistances[first] = 0
	selected := make([]bool, len(vectors))
	selected[first] = true
	for len(indices) < clusterCount {
		totalDistance := float32(0)
		for _, distance := range closestDistances {
			totalDistance += distance
		}
		var nextIndex int
		if totalDistance <= 0 {
			nextIndex = firstUnselectedVector(selected)
		} else {
			probabilities := make([]float32, len(closestDistances))
			for index, distance := range closestDistances {
				probabilities[index] = distance / totalDistance
			}
			nextIndex = expressionVectorChoice(rng, probabilities)
			if selected[nextIndex] {
				nextIndex = firstUnselectedVector(selected)
			}
		}
		indices = append(indices, nextIndex)
		selected[nextIndex] = true
		for index, vector := range vectors {
			distance := max(1-dotProduct(vector, vectors[nextIndex]), 0)
			if distance < closestDistances[index] {
				closestDistances[index] = distance
			}
		}
		for index := range selected {
			if selected[index] {
				closestDistances[index] = 0
			}
		}
	}
	return indices
}

func firstUnselectedVector(selected []bool) int {
	for index, value := range selected {
		if !value {
			return index
		}
	}
	return 0
}

// expressionVectorChoice 对齐 numpy rng.choice(n, p=probabilities)
// cdf 按 float32 顺序累加并归一化，u 为 float64
func expressionVectorChoice(rng *expressionVectorPCG64, probabilities []float32) int {
	cdf := make([]float32, len(probabilities))
	var sum float32
	for index, probability := range probabilities {
		sum += probability
		cdf[index] = sum
	}
	last := cdf[len(cdf)-1]
	for index := range cdf {
		cdf[index] /= last
	}
	u := rng.random()
	for index, value := range cdf {
		if u < float64(value) {
			return index
		}
	}
	return len(cdf) - 1
}

func assignClusterLabels(vectors, centers [][]float32) []int {
	labels := make([]int, len(vectors))
	for index, vector := range vectors {
		bestCluster := 0
		bestScore := float32(math.Inf(-1))
		for clusterID, center := range centers {
			score := dotProduct(vector, center)
			if score > bestScore {
				bestScore = score
				bestCluster = clusterID
			}
		}
		labels[index] = bestCluster
	}
	return labels
}

func repairEmptyClusterLabels(labels []int, vectors, centers [][]float32) {
	counts := make([]int, len(centers))
	for _, label := range labels {
		if label >= 0 && label < len(counts) {
			counts[label]++
		}
	}
	for clusterID := range centers {
		if counts[clusterID] > 0 {
			continue
		}
		donorIndex := -1
		donorScore := float32(math.Inf(1))
		for index, label := range labels {
			if counts[label] <= 1 {
				continue
			}
			score := dotProduct(vectors[index], centers[label])
			if score < donorScore {
				donorScore = score
				donorIndex = index
			}
		}
		if donorIndex < 0 {
			continue
		}
		counts[labels[donorIndex]]--
		labels[donorIndex] = clusterID
		counts[clusterID]++
	}
}

func buildClusterCenters(vectors [][]float32, labels []int, previous [][]float32) [][]float32 {
	centers := make([][]float32, len(previous))
	for clusterID := range centers {
		center := make([]float32, len(vectors[0]))
		members := 0
		for index, label := range labels {
			if label != clusterID {
				continue
			}
			members++
			for dimension, value := range vectors[index] {
				center[dimension] += value
			}
		}
		if members == 0 {
			centers[clusterID] = append([]float32(nil), previous[clusterID]...)
			continue
		}
		for dimension := range center {
			center[dimension] /= float32(members)
		}
		centers[clusterID] = normalizeVectorCenter(center)
	}
	return centers
}

// normalizeVectorForClustering 对齐 Python l2_normalize 的逐行 float32 范数
// numpy 对 axis=1 的范数使用 float32 顺序平方和，不同于 BLAS 单向量 sdot
func normalizeVectorForClustering(vector []float32) []float32 {
	norm := pythonRowL2Norm(vector)
	if norm == 0 {
		return append([]float32(nil), vector...)
	}
	out := make([]float32, len(vector))
	for index, value := range vector {
		out[index] = value / norm
	}
	return out
}

// normalizeVectorCenter 对齐 Python _build_cluster_centers_from_labels 的单向量范数
// np.linalg.norm 对 1D float32 走 BLAS sdot，乘积为 float32、累加为 float64
func normalizeVectorCenter(vector []float32) []float32 {
	norm := pythonCenterL2Norm(vector)
	if norm == 0 {
		return append([]float32(nil), vector...)
	}
	out := make([]float32, len(vector))
	for index, value := range vector {
		out[index] = value / norm
	}
	return out
}

func pythonRowL2Norm(vector []float32) float32 {
	var sum float32
	for _, value := range vector {
		sum += value * value
	}
	return float32(math.Sqrt(float64(sum)))
}

func pythonCenterL2Norm(vector []float32) float32 {
	var sum float64
	for _, value := range vector {
		sum += float64(value * value)
	}
	return float32(math.Sqrt(float64(float32(sum))))
}

var _ ExpressionVectorClusterStore = (*InMemoryExpressionVectorStore)(nil)
