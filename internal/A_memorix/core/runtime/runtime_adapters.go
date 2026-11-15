package memruntime

// 运行时组件到检索器接口的空值安全适配器
// 说明：Go 的 typed nil 放进接口后不等于 nil，这里统一归一

import (
	"context"

	"MaiGo/internal/A_memorix/core/embedding"
	"MaiGo/internal/A_memorix/core/retrieval"
	"MaiGo/internal/A_memorix/core/storage"
)

// newVectorSearcher 把向量库包装成接口，nil 时返回 nil 接口
func newVectorSearcher(store *storage.VectorStore) retrieval.VectorSearcher {
	if store == nil {
		return nil
	}
	return store
}

// legacyVectorSearcher 把只读旧向量视图适配成检索器接口
// 视图的 Dimension 字段与 VectorSearcher.Dimension 方法同名，不能直接实现接口
type legacyVectorSearcher struct {
	view *storage.ReadOnlyVectorStoreView
}

// Search 转发只读视图检索并把 float32 分数转成检索器接口的 float64
func (a legacyVectorSearcher) Search(query []float32, k int, filterDeleted bool, allowedIDs []string) ([]string, []float64, error) {
	ids, scores, err := a.view.Search(query, k, filterDeleted, allowedIDs)
	if err != nil {
		return ids, nil, err
	}
	converted := make([]float64, len(scores))
	for index, score := range scores {
		converted[index] = float64(score)
	}
	return ids, converted, nil
}

// Dimension 返回只读视图的向量维度
func (a legacyVectorSearcher) Dimension() int { return a.view.Dimension }

// NumVectors 返回只读视图的可信向量数
func (a legacyVectorSearcher) NumVectors() int { return a.view.NumVectors() }

// NewLegacyVectorSearcher 把旧只读向量视图包装成检索器接口，nil 时返回 nil 接口
func NewLegacyVectorSearcher(view *storage.ReadOnlyVectorStoreView) retrieval.VectorSearcher {
	if view == nil {
		return nil
	}
	return legacyVectorSearcher{view: view}
}

// legacyVectorSearcherFromValue 按 Python Any 语义解析旧视图配置值
func legacyVectorSearcherFromValue(value any) retrieval.VectorSearcher {
	if view, ok := value.(*storage.ReadOnlyVectorStoreView); ok {
		return NewLegacyVectorSearcher(view)
	}
	searcher, _ := value.(retrieval.VectorSearcher)
	return searcher
}

// newMetadataStore 把元数据库包装成接口，nil 时返回 nil 接口
func newMetadataStore(store *storage.Store) retrieval.DualPathMetadataStore {
	if store == nil {
		return nil
	}
	return store
}

// embeddingEncoderAdapter 把嵌入适配器适配成检索器接口
type embeddingEncoderAdapter struct {
	manager *embedding.EmbeddingAPIAdapter
}

// EncodeOne 对齐 EmbeddingEncoder 的批量参数签名，内部固定走单条编码
func (a embeddingEncoderAdapter) EncodeOne(ctx context.Context, text string, batchSize int, showProgress bool, normalize bool) ([]float32, error) {
	return a.manager.EncodeOne(ctx, text, nil)
}

// GetEmbeddingDimension 转发嵌入维度
func (a embeddingEncoderAdapter) GetEmbeddingDimension() int {
	return a.manager.GetEmbeddingDimension()
}

// newEmbeddingEncoder 把嵌入适配器包装成接口，nil 时返回 nil 接口
func newEmbeddingEncoder(manager *embedding.EmbeddingAPIAdapter) retrieval.EmbeddingEncoder {
	if manager == nil {
		return nil
	}
	return embeddingEncoderAdapter{manager: manager}
}

// newSparseIndex 把稀疏索引包装成接口，nil 时返回 nil 接口
func newSparseIndex(index *retrieval.SparseBM25Index) retrieval.DualPathSparseIndex {
	if index == nil {
		return nil
	}
	return index
}
