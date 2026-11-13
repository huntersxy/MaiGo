package retrieval

// A_memorix 双路检索器依赖的能力接口与存储适配器
// 外部依赖按 Python 构造参数注入，storage 侧差异集中在这里抹平

import (
	"context"
	"sort"

	"maibot-go/internal/A_memorix/core/storage"
)

// VectorSearcher 向量召回能力，storage.VectorStore 直接满足
type VectorSearcher interface {
	Search(query []float32, k int, filterDeleted bool, allowedIDs []string) ([]string, []float64, error)
	Dimension() int
	NumVectors() int
}

// EmbeddingEncoder 查询向量编码能力，embedding.EmbeddingManager 直接满足
type EmbeddingEncoder interface {
	EncodeOne(ctx context.Context, text string, batchSize int, showProgress bool, normalize bool) ([]float32, error)
	GetEmbeddingDimension() int
}

// DualPathSparseIndex 稀疏召回能力，*SparseBM25Index 直接满足
type DualPathSparseIndex interface {
	Search(query string, k int, allowedIDs []string) []map[string]any
	SearchRelations(query string, k int, allowedIDs []string) []map[string]any
	Stats() map[string]any
}

// DualPathGraphStore 双路检索所需的图能力集合
// 同时覆盖 PersonalizedPageRank、关系图召回与本地 PPR 的用图需求
type DualPathGraphStore interface {
	ComputePagerank(personalization map[string]float64, alpha float64, maxIter int, tol float64) (map[string]float64, error)
	GetNeighbors(node string) []string
	GetNodes() []string
	NodeRevision() uint64
	NumNodes() int
	NumEdges() int

	GraphRevision() int64
	FindNode(name string, ignoreCase bool) (string, bool)
	GetRelationHashesForEdge(source, target string) []string
	FindPaths(start, end string, maxDepth int, maxPaths int) [][]string
	GetIncidentRelationHashes(node string, limit int) []string
	GetInNeighbors(node string) []string
	GetWeightedNeighbors(node string) []storage.WeightedNeighbor
}

// DualPathMetadataStore 双路检索所需的元数据能力，*storage.Store 直接满足
type DualPathMetadataStore interface {
	GetParagraph(hashValue string) (map[string]any, error)
	GetParagraphsByHashes(hashValues []string) (map[string]map[string]any, error)
	GetEntitiesByHashes(hashValues []string) (map[string]map[string]any, error)
	GetRelation(hashValue string, includeInactive bool) (map[string]any, error)
	GetRelations(subject, predicate, object string, includeInactive bool) ([]map[string]any, error)
	GetRelationsByHashes(hashValues []string, includeInactive bool) (map[string]map[string]any, error)
	GetParagraphsByRelation(relationHash string) ([]map[string]any, error)
	GetParagraphsByRelationHashes(relationHashes []string) (map[string][]map[string]any, error)
	GetParagraphHashesByRelationHashes(relationHashes []string) (map[string][]string, error)
	GetParagraphEntitiesByHashes(paragraphHashes []string) (map[string][]map[string]any, error)
	GetParagraphsByEntityHashes(entityHashes []string) (map[string][]map[string]any, error)
	GetParagraphHashesByEntityHashes(entityHashes []string) (map[string][]string, error)
	QueryParagraphsTemporal(startTS *float64, endTS *float64, person string, source string, limit int, allowCreatedFallback bool, allowedHashes []string) ([]map[string]any, error)
	GetStatistics() (map[string]int, error)
	Query(sql string, params ...any) ([]map[string]any, error)
}

// storageGraphAdapter 把 *storage.GraphStore 适配为 DualPathGraphStore
// 差异点：PageRank 方法大小写、版本号类型、边哈希返回集合、FindPaths 多一个扩展上限参数
type storageGraphAdapter struct {
	store *storage.GraphStore
}

// NewStorageGraphAdapter 构造 storage 图适配器，nil 时返回 nil
func NewStorageGraphAdapter(store *storage.GraphStore) DualPathGraphStore {
	if store == nil {
		return nil
	}
	return &storageGraphAdapter{store: store}
}

// ComputePagerank 包装 storage 的无错误返回实现
func (a *storageGraphAdapter) ComputePagerank(personalization map[string]float64, alpha float64, maxIter int, tol float64) (map[string]float64, error) {
	return a.store.ComputePageRank(personalization, alpha, maxIter, tol), nil
}

// GetNeighbors 返回出邻居
func (a *storageGraphAdapter) GetNeighbors(node string) []string { return a.store.GetNeighbors(node) }

// GetNodes 返回节点快照
func (a *storageGraphAdapter) GetNodes() []string { return a.store.GetNodes() }

// NodeRevision 把 int64 版本号适配为 uint64
func (a *storageGraphAdapter) NodeRevision() uint64 { return uint64(a.store.NodeRevision()) }

// NumNodes 返回节点数
func (a *storageGraphAdapter) NumNodes() int { return a.store.NumNodes() }

// NumEdges 返回边数
func (a *storageGraphAdapter) NumEdges() int { return a.store.NumEdges() }

// GraphRevision 返回图版本号
func (a *storageGraphAdapter) GraphRevision() int64 { return a.store.GraphRevision() }

// FindNode 按名称查节点
func (a *storageGraphAdapter) FindNode(name string, ignoreCase bool) (string, bool) {
	return a.store.FindNode(name, ignoreCase)
}

// GetRelationHashesForEdge 把边哈希集合排序成切片，保证图召回顺序可复现
func (a *storageGraphAdapter) GetRelationHashesForEdge(source, target string) []string {
	hashes := a.store.GetRelationHashesForEdge(source, target)
	result := make([]string, 0, len(hashes))
	for hashValue := range hashes {
		result = append(result, hashValue)
	}
	sort.Strings(result)
	return result
}

// FindPaths 使用 storage 默认的搜索扩展上限
func (a *storageGraphAdapter) FindPaths(start, end string, maxDepth int, maxPaths int) [][]string {
	return a.store.FindPaths(start, end, maxDepth, maxPaths, storage.DefaultFindPathsMaxExpansions)
}

// GetIncidentRelationHashes 返回节点关联关系哈希
// 召回层约定 limit 为 0 表示不限制（对齐 Python 传 None），
// 而 storage.GraphStore 把 >=0 的 limit 按 max(1, limit) 截断，这里转换后再下传
func (a *storageGraphAdapter) GetIncidentRelationHashes(node string, limit int) []string {
	storeLimit := limit
	if limit == 0 {
		storeLimit = -1
	}
	return a.store.GetIncidentRelationHashes(node, storeLimit)
}

// GetInNeighbors 返回入邻居
func (a *storageGraphAdapter) GetInNeighbors(node string) []string {
	return a.store.GetInNeighbors(node)
}

// GetWeightedNeighbors 返回带权出邻居
func (a *storageGraphAdapter) GetWeightedNeighbors(node string) []storage.WeightedNeighbor {
	return a.store.GetWeightedNeighbors(node)
}
