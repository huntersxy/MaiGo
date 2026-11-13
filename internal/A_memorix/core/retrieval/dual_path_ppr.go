package retrieval

// A_memorix PPR 重排序与本地图迭代，对齐 core/retrieval/dual_path.py 的 2677-2928 行

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

// errPPRTimeout PPR 计算超时
var errPPRTimeout = errors.New("ppr 计算超时")

// rerankWithPPR 对齐 _rerank_with_ppr：按段落命中图实体的 PPR 分数做正向增益
func (r *DualPathRetriever) rerankWithPPR(ctx context.Context, results []RetrievalResult, query string) []RetrievalResult {
	if r.graphStore == nil || r.ppr == nil {
		return results
	}
	_ = ctx
	entities := r.extractEntities(query)
	if len(entities) == 0 {
		zap.L().Debug("未识别到实体，跳过PPR重排序")
		return results
	}
	timeoutSeconds := r.config.PPRTimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 1.5
	}
	timeoutSeconds = math.Max(0.1, timeoutSeconds)
	pprScores, err := r.getCachedPPRScores(entities, time.Duration(timeoutSeconds*float64(time.Second)))
	if err != nil {
		if errors.Is(err, errPPRTimeout) {
			zap.L().Warn(fmt.Sprintf("metric.ppr_timeout_skip_count=1 timeout_s=%v entities=%d", timeoutSeconds, len(entities)))
		} else {
			zap.L().Warn(fmt.Sprintf("PPR 重排序失败，回退原排序: %v", err))
		}
		return results
	}

	pprScoresByName := make(map[string]float64, len(pprScores))
	for name, score := range pprScores {
		pprScoresByName[strings.ToLower(strings.TrimSpace(name))] = score
	}
	paragraphHashes := make([]string, 0, len(results))
	for _, item := range results {
		if item.ResultType == "paragraph" {
			paragraphHashes = append(paragraphHashes, item.HashValue)
		}
	}
	paragraphEntityMap, entityErr := r.metadataStore.GetParagraphEntitiesByHashes(paragraphHashes)
	if entityErr != nil {
		zap.L().Warn(fmt.Sprintf("PPR 重排序读取段落实体失败，回退原排序: %v", entityErr))
		return results
	}
	for index := range results {
		if results[index].ResultType != "paragraph" {
			continue
		}
		paraEntities := paragraphEntityMap[results[index].HashValue]
		if len(paraEntities) == 0 {
			continue
		}
		entityScores := []float64{}
		for _, entity := range paraEntities {
			entityName := strings.ToLower(strings.TrimSpace(stringFromMap(entity, "name")))
			if score, exists := pprScoresByName[entityName]; exists {
				entityScores = append(entityScores, score)
			}
		}
		if len(entityScores) == 0 {
			continue
		}
		sort.SliceStable(entityScores, func(left, right int) bool { return entityScores[left] > entityScores[right] })
		focusScores := entityScores
		if len(focusScores) > 2 {
			focusScores = focusScores[:2]
		}
		sum := 0.0
		for _, score := range focusScores {
			sum += score
		}
		pprSignal := sum / float64(len(focusScores))
		boostWeight := 0.06
		if len(focusScores) >= 2 {
			boostWeight = 0.12
		}
		boost := pprSignal * boostWeight
		if results[index].Metadata == nil {
			results[index].Metadata = map[string]any{}
		}
		results[index].Metadata["ppr_signal"] = dualPathRoundTo(pprSignal, 4)
		results[index].Metadata["ppr_focus_entity_count"] = len(focusScores)
		results[index].Metadata["ppr_boost"] = dualPathRoundTo(boost, 4)
		results[index].Score += boost
	}
	sort.SliceStable(results, func(left, right int) bool { return results[left].Score > results[right].Score })
	return results
}

// getCachedPPRScores 对齐 _get_cached_ppr_scores：短缓存加信号量限流
func (r *DualPathRetriever) getCachedPPRScores(entities map[string]float64, timeout time.Duration) (map[string]float64, error) {
	key := r.buildPPRCacheKey(entities)
	if scores, ok := r.loadPPRCache(key); ok {
		return scores, nil
	}
	r.acquirePPRSemaphore()
	defer r.releasePPRSemaphore()
	if scores, ok := r.loadPPRCache(key); ok {
		return scores, nil
	}

	type pprComputeResult struct {
		Scores map[string]float64
		Err    error
	}
	resultCh := make(chan pprComputeResult, 1)
	go func() {
		scores, err := r.computePPRScores(entities)
		resultCh <- pprComputeResult{Scores: scores, Err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-resultCh:
		if result.Err != nil {
			return nil, result.Err
		}
		r.storePPRCacheEntry(key, result.Scores)
		return result.Scores, nil
	case <-timer.C:
		return nil, errPPRTimeout
	}
}

// acquirePPRSemaphore 获取 PPR 并发令牌
func (r *DualPathRetriever) acquirePPRSemaphore() {
	if r.pprSemaphore == nil {
		return
	}
	r.pprSemaphore <- struct{}{}
}

// releasePPRSemaphore 释放 PPR 并发令牌
func (r *DualPathRetriever) releasePPRSemaphore() {
	if r.pprSemaphore == nil {
		return
	}
	select {
	case <-r.pprSemaphore:
	default:
	}
}

// loadPPRCache 读取未过期缓存，过期项立即移除
func (r *DualPathRetriever) loadPPRCache(key pprCacheKey) (map[string]float64, bool) {
	r.pprCacheMu.Lock()
	defer r.pprCacheMu.Unlock()
	cached, exists := r.pprCache[key]
	if !exists {
		return nil, false
	}
	if cached.ExpiresAt.After(time.Now()) {
		return cached.Scores, true
	}
	delete(r.pprCache, key)
	return nil, false
}

// storePPRCacheEntry 对齐 _store_ppr_cache_entry：容量满时淘汰最早过期项
func (r *DualPathRetriever) storePPRCacheEntry(key pprCacheKey, scores map[string]float64) {
	r.pprCacheMu.Lock()
	defer r.pprCacheMu.Unlock()
	if len(r.pprCache) >= r.pprCacheMaxEntries {
		var oldestKey pprCacheKey
		oldestTime := time.Time{}
		first := true
		for candidateKey, entry := range r.pprCache {
			if first || entry.ExpiresAt.Before(oldestTime) {
				oldestKey = candidateKey
				oldestTime = entry.ExpiresAt
				first = false
			}
		}
		if !first {
			delete(r.pprCache, oldestKey)
		}
	}
	copied := make(map[string]float64, len(scores))
	for name, score := range scores {
		copied[name] = score
	}
	r.pprCache[key] = pprCacheEntry{
		ExpiresAt: time.Now().Add(time.Duration(r.pprCacheTTLSeconds * float64(time.Second))),
		Scores:    copied,
	}
}

// computePPRScores 对齐 _compute_ppr_scores：优先本地子图，空结果回退全局
func (r *DualPathRetriever) computePPRScores(entities map[string]float64) (map[string]float64, error) {
	if r.ppr == nil {
		return map[string]float64{}, nil
	}
	if r.shouldUseLocalPPR() {
		scores := r.computeLocalPPRScores(entities)
		if len(scores) > 0 {
			return scores, nil
		}
	}
	normalize := true
	scores, err := r.ppr.Compute(entities, nil, nil, &normalize)
	if err != nil {
		return nil, err
	}
	return scores, nil
}

// shouldUseLocalPPR 对齐 _should_use_local_ppr
func (r *DualPathRetriever) shouldUseLocalPPR() bool {
	return r.graphStore != nil && r.config.PPRLocalEnabled && r.graphStore.NumNodes() >= r.config.PPRLocalMinGraphNodes
}

// resolvePPRSeedNodes 对齐 _resolve_ppr_seed_nodes：实体名映射到图节点并累加权重
func (r *DualPathRetriever) resolvePPRSeedNodes(entities map[string]float64) map[string]float64 {
	seeds := map[string]float64{}
	names := make([]string, 0, len(entities))
	for name := range entities {
		names = append(names, name)
	}
	sort.SliceStable(names, func(left, right int) bool { return strings.ToLower(names[left]) < strings.ToLower(names[right]) })
	for _, name := range names {
		nodeName, exists := r.graphStore.FindNode(name, true)
		if !exists || nodeName == "" {
			continue
		}
		weight := math.Max(0.0, entities[name])
		if weight <= 0.0 {
			continue
		}
		seeds[nodeName] += weight
	}
	return seeds
}

// collectLocalPPRNodes 对齐 _collect_local_ppr_nodes：按跳跃上限做 BFS
func (r *DualPathRetriever) collectLocalPPRNodes(seeds map[string]float64) []string {
	maxNodes := r.config.PPRLocalMaxNodes
	maxHops := r.config.PPRLocalHops
	orderedNodes := []string{}
	seen := map[string]struct{}{}
	type queueNode struct {
		Node  string
		Depth int
	}
	queue := make([]queueNode, 0, len(seeds))
	seedNames := make([]string, 0, len(seeds))
	for node := range seeds {
		seedNames = append(seedNames, node)
	}
	sort.SliceStable(seedNames, func(left, right int) bool {
		return strings.ToLower(seedNames[left]) < strings.ToLower(seedNames[right])
	})
	for _, node := range seedNames {
		queue = append(queue, queueNode{Node: node, Depth: 0})
	}

	for len(queue) > 0 && len(seen) < maxNodes {
		current := queue[0]
		queue = queue[1:]
		if _, exists := seen[current.Node]; exists {
			continue
		}
		seen[current.Node] = struct{}{}
		orderedNodes = append(orderedNodes, current.Node)
		if current.Depth >= maxHops {
			continue
		}
		neighbors := append([]string{}, r.graphStore.GetNeighbors(current.Node)...)
		neighbors = append(neighbors, r.graphStore.GetInNeighbors(current.Node)...)
		for _, neighbor := range neighbors {
			if _, exists := seen[neighbor]; exists {
				continue
			}
			if len(seen)+len(queue) < maxNodes {
				queue = append(queue, queueNode{Node: neighbor, Depth: current.Depth + 1})
			}
		}
	}
	return orderedNodes
}

// computeLocalPPRScores 对齐 _compute_local_ppr_scores：在局部子图上迭代
func (r *DualPathRetriever) computeLocalPPRScores(entities map[string]float64) map[string]float64 {
	seeds := r.resolvePPRSeedNodes(entities)
	if len(seeds) == 0 {
		return map[string]float64{}
	}
	nodes := r.collectLocalPPRNodes(seeds)
	if len(nodes) == 0 {
		return map[string]float64{}
	}
	nodeCount := len(nodes)
	nodeSet := make(map[string]struct{}, nodeCount)
	nodeToLocalIndex := make(map[string]int, nodeCount)
	for index, node := range nodes {
		nodeSet[node] = struct{}{}
		nodeToLocalIndex[node] = index
	}

	transition := make([][]float64, nodeCount)
	dangling := make([]bool, nodeCount)
	for sourceIndex, node := range nodes {
		row := make([]float64, nodeCount)
		totalEdgeWeight := 0.0
		type weightedEdge struct {
			Target int
			Weight float64
		}
		edges := []weightedEdge{}
		for _, neighbor := range r.graphStore.GetWeightedNeighbors(node) {
			if _, exists := nodeSet[neighbor.Node]; !exists {
				continue
			}
			if neighbor.Weight <= 0.0 {
				continue
			}
			edges = append(edges, weightedEdge{Target: nodeToLocalIndex[neighbor.Node], Weight: neighbor.Weight})
			totalEdgeWeight += neighbor.Weight
		}
		if totalEdgeWeight <= 0.0 {
			dangling[sourceIndex] = true
			transition[sourceIndex] = row
			continue
		}
		for _, edge := range edges {
			row[edge.Target] += edge.Weight / totalEdgeWeight
		}
		transition[sourceIndex] = row
	}

	totalSeedWeight := 0.0
	for _, node := range nodes {
		totalSeedWeight += seeds[node]
	}
	if totalSeedWeight <= 0.0 {
		return map[string]float64{}
	}
	personalization := make([]float64, nodeCount)
	for index, node := range nodes {
		personalization[index] = seeds[node] / totalSeedWeight
	}
	scores := make([]float64, nodeCount)
	copy(scores, personalization)
	alpha := r.config.PPRAlpha
	tolerance := r.ppr.config.Tol
	maxIterations := r.ppr.config.MaxIter
	minIterations := r.ppr.config.MinIterations

	for iteration := 0; iteration < maxIterations; iteration++ {
		nextScores := make([]float64, nodeCount)
		for target := 0; target < nodeCount; target++ {
			sum := 0.0
			for source := 0; source < nodeCount; source++ {
				if transition[source][target] != 0.0 {
					sum += transition[source][target] * scores[source]
				}
			}
			nextScores[target] = alpha*sum + (1.0-alpha)*personalization[target]
		}
		danglingMass := 0.0
		for index, isDangling := range dangling {
			if isDangling {
				danglingMass += scores[index]
			}
		}
		if danglingMass > 0.0 {
			for index := range nextScores {
				nextScores[index] += alpha * danglingMass * personalization[index]
			}
		}
		diff := 0.0
		for index := range nextScores {
			diff += math.Abs(nextScores[index] - scores[index])
		}
		scores = nextScores
		if iteration+1 >= minIterations && diff < tolerance {
			break
		}
	}

	totalScore := 0.0
	for _, score := range scores {
		totalScore += score
	}
	if totalScore > 0.0 {
		for index := range scores {
			scores[index] /= totalScore
		}
	}
	output := make(map[string]float64, nodeCount)
	for index, node := range nodes {
		output[node] = scores[index]
	}
	return output
}

// buildPPRCacheKey 对齐 _build_ppr_cache_key
func (r *DualPathRetriever) buildPPRCacheKey(entities map[string]float64) pprCacheKey {
	type entityPair struct {
		Name   string
		Weight float64
	}
	pairs := make([]entityPair, 0, len(entities))
	for name, weight := range entities {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		pairs = append(pairs, entityPair{Name: strings.ToLower(trimmed), Weight: dualPathRoundTo(weight, 6)})
	}
	sort.SliceStable(pairs, func(left, right int) bool {
		if pairs[left].Name != pairs[right].Name {
			return pairs[left].Name < pairs[right].Name
		}
		return pairs[left].Weight < pairs[right].Weight
	})
	parts := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		parts = append(parts, pair.Name+"\x1f"+fmt.Sprintf("%.6f", pair.Weight))
	}
	graphRevision := int64(0)
	numNodes := 0
	numEdges := 0
	if r.graphStore != nil {
		graphRevision = r.graphStore.GraphRevision()
		numNodes = r.graphStore.NumNodes()
		numEdges = r.graphStore.NumEdges()
	}
	return pprCacheKey{
		GraphRevision:      graphRevision,
		NumNodes:           numNodes,
		NumEdges:           numEdges,
		Alpha:              dualPathRoundTo(r.config.PPRAlpha, 6),
		LocalEnabled:       r.config.PPRLocalEnabled,
		LocalMaxNodes:      r.config.PPRLocalMaxNodes,
		LocalHops:          r.config.PPRLocalHops,
		LocalMinGraphNodes: r.config.PPRLocalMinGraphNodes,
		EntityKey:          strings.Join(parts, "\x1e"),
	}
}
