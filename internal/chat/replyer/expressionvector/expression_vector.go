package expressionvector

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
)

// ExpressionVectorFilter TopK 检索时的会话范围，语义对齐 MatchExpressions 的 related + global
type ExpressionVectorFilter struct {
	SessionIDs    []string
	IncludeGlobal bool
	// AllowedIDs 精确候选集；非空时只保留这些表达方式，供聚类候选池与 scoped 过滤对齐
	AllowedIDs map[uint]struct{}
}

// ExpressionVectorHit 一次向量检索的候选命中，Vector 为候选向量副本供 MMR 去冗余
type ExpressionVectorHit struct {
	ID     uint
	Score  float32
	Vector []float32
}

// ExpressionVectorStore 表达方式向量索引；默认进程内内存实现，后续可换持久化/FAISS
type ExpressionVectorStore interface {
	Upsert(id uint, sessionID string, vector []float32)
	Remove(id uint)
	Len() int
	TopK(query []float32, filter ExpressionVectorFilter, k int) []uint
	// Scored 返回过滤范围内全部候选的余弦分数，对齐 expression_vector_index 的候选池检索
	Scored(query []float32, filter ExpressionVectorFilter) []ExpressionVectorHit
}

// ExpressionVectorMetadata 写入向量索引时随附的表达元数据，供 WebUI 聚类页读取
type ExpressionVectorMetadata struct {
	ID         uint
	SessionID  string
	Situation  string
	Style      string
	Count      int
	Checked    bool
	ModifiedBy string
}

type expressionVectorMetadataStore interface {
	UpsertExpression(meta ExpressionVectorMetadata, vector []float32)
}

// UpsertExpressionVector 优先写入带元数据的索引，缺省回退纯向量接口
func UpsertExpressionVector(store ExpressionVectorStore, meta ExpressionVectorMetadata, vector []float32) {
	if store == nil || meta.ID == 0 || len(vector) == 0 {
		return
	}
	if metadataStore, ok := store.(expressionVectorMetadataStore); ok {
		metadataStore.UpsertExpression(meta, vector)
		return
	}
	store.Upsert(meta.ID, meta.SessionID, vector)
}

type memoryVectorEntry struct {
	sessionID  string
	situation  string
	style      string
	count      int
	checked    bool
	modifiedBy string
	vector     []float32
}

// InMemoryExpressionVectorStore 基于 float32 余弦相似度的内存索引
type InMemoryExpressionVectorStore struct {
	mu           sync.RWMutex
	entries      map[uint]memoryVectorEntry
	clusterMu    sync.Mutex
	clusterState *expressionVectorClusterState
	clusterDirty bool
}

// NewInMemoryExpressionVectorStore 创建空索引
func NewInMemoryExpressionVectorStore() *InMemoryExpressionVectorStore {
	return &InMemoryExpressionVectorStore{
		entries:      make(map[uint]memoryVectorEntry),
		clusterDirty: true,
	}
}

func (s *InMemoryExpressionVectorStore) Upsert(id uint, sessionID string, vector []float32) {
	if s == nil || id == 0 || len(vector) == 0 {
		return
	}
	vec := append([]float32(nil), vector...)
	s.mu.Lock()
	s.entries[id] = memoryVectorEntry{
		sessionID: strings.TrimSpace(sessionID),
		vector:    vec,
	}
	s.mu.Unlock()
	s.invalidateClusters()
}

// UpsertExpression 写入向量并保留 WebUI 聚类所需的表达元数据
func (s *InMemoryExpressionVectorStore) UpsertExpression(meta ExpressionVectorMetadata, vector []float32) {
	if s == nil || meta.ID == 0 || len(vector) == 0 {
		return
	}
	vec := append([]float32(nil), vector...)
	s.mu.Lock()
	s.entries[meta.ID] = memoryVectorEntry{
		sessionID:  strings.TrimSpace(meta.SessionID),
		situation:  strings.TrimSpace(meta.Situation),
		style:      strings.TrimSpace(meta.Style),
		count:      meta.Count,
		checked:    meta.Checked,
		modifiedBy: strings.TrimSpace(meta.ModifiedBy),
		vector:     vec,
	}
	s.mu.Unlock()
	s.invalidateClusters()
}

func (s *InMemoryExpressionVectorStore) Remove(id uint) {
	if s == nil || id == 0 {
		return
	}
	s.mu.Lock()
	delete(s.entries, id)
	s.mu.Unlock()
	s.invalidateClusters()
}

func (s *InMemoryExpressionVectorStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	n := len(s.entries)
	s.mu.RUnlock()
	return n
}

// Replace 以磁盘载入的条目整体替换内存索引
func (s *InMemoryExpressionVectorStore) Replace(items []persistentExpressionVector) {
	if s == nil {
		return
	}
	entries := make(map[uint]memoryVectorEntry, len(items))
	for _, item := range items {
		if item.ID == 0 || len(item.Vector) == 0 {
			continue
		}
		entries[item.ID] = memoryVectorEntry{
			sessionID:  strings.TrimSpace(item.SessionID),
			situation:  strings.TrimSpace(item.Situation),
			style:      strings.TrimSpace(item.Style),
			count:      item.Count,
			checked:    item.Checked,
			modifiedBy: strings.TrimSpace(item.ModifiedBy),
			vector:     append([]float32(nil), item.Vector...),
		}
	}
	s.mu.Lock()
	s.entries = entries
	s.mu.Unlock()
	s.invalidateClusters()
}

// ReplaceWithClusters 载入条目并沿用索引持久化的簇划分与簇中心
//
// 对齐 Python：检索期直接用索引里的 cluster_id 与 cluster_centers_*，
// 不在查询时重跑 k-means，避免 WebUI 聚类页与检索召回分簇不一致。
func (s *InMemoryExpressionVectorStore) ReplaceWithClusters(items []persistentExpressionVector, centers [][]float32) {
	if s == nil {
		return
	}
	s.Replace(items)
	labels := make(map[uint]int, len(items))
	for _, item := range items {
		if item.ID == 0 {
			continue
		}
		labels[item.ID] = item.ClusterID
	}
	dimension := 0
	if len(centers) > 0 {
		dimension = len(centers[0])
	}
	s.clusterMu.Lock()
	if len(centers) > 0 && len(labels) > 0 {
		s.clusterState = &expressionVectorClusterState{
			labels: labels, centers: centers, dimension: dimension,
		}
		s.clusterDirty = false
	} else {
		s.clusterState = nil
		s.clusterDirty = true
	}
	s.clusterMu.Unlock()
}

// snapshotItems 导出可落盘条目，按 id 升序保证文件稳定
func (s *InMemoryExpressionVectorStore) snapshotItems() []persistentExpressionVector {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	items := make([]persistentExpressionVector, 0, len(s.entries))
	for id, entry := range s.entries {
		items = append(items, persistentExpressionVector{
			ID:         id,
			SessionID:  entry.sessionID,
			Situation:  entry.situation,
			Style:      entry.style,
			Count:      entry.count,
			Checked:    entry.checked,
			ModifiedBy: entry.modifiedBy,
			Vector:     append([]float32(nil), entry.vector...),
		})
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (s *InMemoryExpressionVectorStore) TopK(query []float32, filter ExpressionVectorFilter, k int) []uint {
	if k <= 0 {
		return nil
	}
	hits := s.Scored(query, filter)
	if len(hits) > k {
		hits = hits[:k]
	}
	out := make([]uint, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.ID)
	}
	return out
}

// Scored 计算全部候选的余弦相似度并按分数降序返回，供向量候选池与 MMR 使用
func (s *InMemoryExpressionVectorStore) Scored(query []float32, filter ExpressionVectorFilter) []ExpressionVectorHit {
	if s == nil || len(query) == 0 {
		return nil
	}
	queryNorm := vectorL2Norm(query)
	if queryNorm == 0 {
		return nil
	}

	s.mu.RLock()
	hits := make([]ExpressionVectorHit, 0, len(s.entries))
	for id, entry := range s.entries {
		if !expressionVectorAllowed(id, filter) {
			continue
		}
		if !expressionVectorSessionMatch(entry.sessionID, filter) {
			continue
		}
		if len(entry.vector) != len(query) {
			continue
		}
		denom := queryNorm * vectorL2Norm(entry.vector)
		if denom == 0 {
			continue
		}
		hits = append(hits, ExpressionVectorHit{
			ID:     id,
			Score:  dotProduct(query, entry.vector) / denom,
			Vector: append([]float32(nil), entry.vector...),
		})
	}
	s.mu.RUnlock()

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID > hits[j].ID
	})
	return hits
}

func expressionVectorAllowed(id uint, filter ExpressionVectorFilter) bool {
	if len(filter.AllowedIDs) == 0 {
		return true
	}
	_, ok := filter.AllowedIDs[id]
	return ok
}

func expressionVectorSessionMatch(sessionID string, filter ExpressionVectorFilter) bool {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return filter.IncludeGlobal
	}
	for _, related := range filter.SessionIDs {
		if sid == strings.TrimSpace(related) {
			return true
		}
	}
	return false
}

func dotProduct(a, b []float32) float32 {
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

func vectorL2Norm(v []float32) float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return float32(math.Sqrt(sum))
}

// noopExpressionVectorStore 占位，避免 nil 判断散落
type noopExpressionVectorStore struct{}

func (noopExpressionVectorStore) Upsert(uint, string, []float32) {}
func (noopExpressionVectorStore) Remove(uint)                    {}
func (noopExpressionVectorStore) Len() int                       { return 0 }
func (noopExpressionVectorStore) TopK([]float32, ExpressionVectorFilter, int) []uint {
	return nil
}
func (noopExpressionVectorStore) Scored([]float32, ExpressionVectorFilter) []ExpressionVectorHit {
	return nil
}

// IndexExpressionRecord 在嵌入客户端可用时为表达方式写入/更新向量；失败静默
func IndexExpressionRecord(ctx context.Context, reg Port, rec Record) {
	if reg == nil {
		reg = nil
	}
	if rec.ID == 0 {
		return
	}
	situation := strings.TrimSpace(rec.Situation)
	style := normalizeExpressionStyle(rec.Style)
	if situation == "" || style == "" {
		return
	}
	if !reg.Settings().UseVectorExpression {
		return
	}

	text := expressionEmbeddingText(situation, style)
	if text == "" {
		return
	}
	embedder := reg.Embedder()
	if embedder == nil {
		return
	}
	vec, err := embedder.EmbedText(ctx, text)
	if err != nil || len(vec) == 0 {
		return
	}
	modifiedBy := rec.ModifiedBy
	UpsertExpressionVector(reg.ExpressionVectors(), ExpressionVectorMetadata{
		ID:         rec.ID,
		SessionID:  rec.SessionID,
		Situation:  situation,
		Style:      style,
		Count:      rec.Count,
		Checked:    rec.Checked,
		ModifiedBy: modifiedBy,
	}, vec)
}

// RemoveExpressionVector 从内存索引删除表达方式
func RemoveExpressionVector(reg Port, id uint) {
	if reg == nil {
		reg = nil
	}
	reg.ExpressionVectors().Remove(id)
}

// selectExpressionsByVector 在 valid 候选集上构建向量候选池
//
// 对齐 expression_vector_index.select_candidates：聚类召回 + item/cluster 加权，
// 再用 MMR 去冗余并按原始分数排序；无聚类能力的 Store 回退纯余弦候选。
func selectExpressionsByVector(
	ctx context.Context,
	reg Port,
	valid []Record,
	query string,
	filter ExpressionVectorFilter,
	candidatePoolSize ...int,
) []Record {
	if reg == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	store := reg.ExpressionVectors()
	if store == nil || store.Len() == 0 {
		return nil
	}
	embedder := reg.Embedder()
	if embedder == nil {
		return nil
	}
	queryVec, err := embedder.EmbedText(ctx, query)
	if err != nil || len(queryVec) == 0 {
		return nil
	}

	validByID := make(map[uint]Record, len(valid))
	allowedIDs := make(map[uint]struct{}, len(valid))
	for _, rec := range valid {
		validByID[rec.ID] = rec
		allowedIDs[rec.ID] = struct{}{}
	}
	filter.AllowedIDs = allowedIDs
	pool := heuristicExpressionPoolSize
	if len(candidatePoolSize) > 0 && candidatePoolSize[0] > 0 {
		pool = candidatePoolSize[0]
	}
	if pool < MaxSelectedExpressions {
		pool = MaxSelectedExpressions
	}
	if pool > expressionVectorCandidateLimit {
		pool = expressionVectorCandidateLimit
	}
	var hits []ExpressionVectorHit
	if clusterStore, ok := store.(ExpressionVectorClusterStore); ok {
		hits = clusterStore.ClusterScored(queryVec, filter, expressionVectorClusterPoolSize, pool)
	} else {
		hits = store.Scored(queryVec, filter)
	}
	if len(hits) == 0 {
		return nil
	}
	scoped := make([]ExpressionVectorHit, 0, len(hits))
	for _, hit := range hits {
		if _, ok := validByID[hit.ID]; !ok {
			continue
		}
		scoped = append(scoped, hit)
	}
	// 对齐 select_candidates：当前 profile 范围内可用索引候选不足 10 条时跳过
	if len(scoped) < 10 {
		return nil
	}
	selectedHits := selectExpressionHitsByMMR(scoped, pool, ExpressionVectorDiversityLambda)
	out := make([]Record, 0, len(selectedHits))
	for _, hit := range selectedHits {
		rec, ok := validByID[hit.ID]
		if !ok {
			continue
		}
		out = append(out, rec)
	}
	return out
}

var _ ExpressionVectorStore = (*InMemoryExpressionVectorStore)(nil)
