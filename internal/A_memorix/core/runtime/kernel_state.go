package memruntime

// 内核共享状态快照，对齐 SDKMemoryKernel 的 _vector_health 与 _embedding_degraded 字段

import (
	"context"

	"MaiGo/internal/A_memorix/core/utils"

	"MaiGo/internal/A_memorix/core/embedding"
	"MaiGo/internal/A_memorix/core/retrieval"
	"MaiGo/internal/A_memorix/core/storage"
)

// copyAnyMap 深拷贝一层字典，避免调用方改写内核状态
func copyAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

// copyVectorHealth 复制向量健康状态并单独深拷贝 copy_progress，对齐 _vector_health_snapshot
func copyVectorHealth(source map[string]any) map[string]any {
	out := copyAnyMap(source)
	if progress, ok := source["copy_progress"].(map[string]any); ok {
		copied := make(map[string]any, len(progress))
		for key, value := range progress {
			copied[key] = value
		}
		out["copy_progress"] = copied
	}
	return out
}

// VectorHealth 返回向量健康状态副本，对齐 _vector_health
func (k *Kernel) VectorHealth() map[string]any {
	k.stateMu.RLock()
	defer k.stateMu.RUnlock()
	return copyVectorHealth(k.vectorHealth)
}

// SetVectorHealthField 写入单个向量健康字段，对齐 _set_vector_health 的补丁语义
func (k *Kernel) SetVectorHealthField(key string, value any) {
	k.stateMu.Lock()
	defer k.stateMu.Unlock()
	if k.vectorHealth == nil {
		k.vectorHealth = map[string]any{}
	}
	k.vectorHealth[key] = value
}

// applyVectorHealthPatch 在一次写锁内应用整批补丁，避免调用方看到半更新状态
// ApplyVectorHealthPatch 直接合并健康状态，供服务端口注入避免经 VectorRecovery 回环
func (k *Kernel) ApplyVectorHealthPatch(patch map[string]any) {
	k.applyVectorHealthPatch(patch)
}

func (k *Kernel) applyVectorHealthPatch(patch map[string]any) {
	k.stateMu.Lock()
	defer k.stateMu.Unlock()
	if k.vectorHealth == nil {
		k.vectorHealth = map[string]any{}
	}
	for key, value := range patch {
		k.vectorHealth[key] = value
	}
}

// EmbeddingDegraded 返回嵌入降级状态副本，对齐 _embedding_degraded
func (k *Kernel) EmbeddingDegraded() map[string]any {
	k.stateMu.RLock()
	defer k.stateMu.RUnlock()
	return copyAnyMap(k.embeddingDegraded)
}

// SetEmbeddingDegradedField 写入单个降级状态字段
func (k *Kernel) SetEmbeddingDegradedField(key string, value any) {
	k.stateMu.Lock()
	defer k.stateMu.Unlock()
	if k.embeddingDegraded == nil {
		k.embeddingDegraded = map[string]any{}
	}
	k.embeddingDegraded[key] = value
}

// VectorHealthSnapshot 对齐 _vector_health_snapshot
func (k *Kernel) VectorHealthSnapshot() map[string]any {
	if k.services.VectorRecovery == nil {
		k.stateMu.RLock()
		defer k.stateMu.RUnlock()
		return copyVectorHealth(k.vectorHealth)
	}
	return k.services.VectorRecovery.VectorHealthSnapshot(k.context())
}

// context 返回内核内部调用使用的上下文，Python 版靠隐式事件循环，Go 版显式传空上下文
func (k *Kernel) context() context.Context { return context.Background() }

// DualVectorPoolsEnabled 对齐 _dual_vector_pools_enabled，服务缺失时回落到本地标记
func (k *Kernel) DualVectorPoolsEnabled() bool {
	if k.services.DualVectorState != nil {
		return k.services.DualVectorState.DualVectorPoolsEnabled(k.context())
	}
	k.stateMu.RLock()
	defer k.stateMu.RUnlock()
	return k.dualVectorPoolsReady
}

// SetDualVectorPoolsReady 回写双池就绪标记，对齐 _dual_vector_pools_ready
func (k *Kernel) SetDualVectorPoolsReady(value bool) {
	k.stateMu.Lock()
	k.dualVectorPoolsReady = value
	k.stateMu.Unlock()
}

// DualVectorPoolsReady 返回双池就绪标记
func (k *Kernel) DualVectorPoolsReady() bool {
	k.stateMu.RLock()
	defer k.stateMu.RUnlock()
	return k.dualVectorPoolsReady
}

// dualVectorPoolsConfigEnabled 对齐 _dual_vector_pools_config_enabled
func (k *Kernel) dualVectorPoolsConfigEnabled() bool {
	if k.services.DualVectorState != nil {
		return k.services.DualVectorState.DualVectorPoolsConfigEnabled(k.context())
	}
	return false
}

// IsEmbeddingDegraded 对齐 _is_embedding_degraded
func (k *Kernel) IsEmbeddingDegraded() bool {
	if k.services.EmbeddingState != nil {
		return k.services.EmbeddingState.IsEmbeddingDegraded(k.context())
	}
	k.stateMu.RLock()
	defer k.stateMu.RUnlock()
	return PyTruthy(k.embeddingDegraded["active"])
}

// AllowMetadataOnlyWrite 对齐 _allow_metadata_only_write
func (k *Kernel) AllowMetadataOnlyWrite() bool {
	if k.services.EmbeddingState != nil {
		return k.services.EmbeddingState.AllowMetadataOnlyWrite(k.context())
	}
	return false
}

// VectorStore 对齐 vector_store 属性
func (k *Kernel) VectorStore() *storage.VectorStore { return k.vectorStore }

// SetVectorStore 写入单池向量存储
func (k *Kernel) SetVectorStore(store *storage.VectorStore) { k.vectorStore = store }

// GraphStore 对齐 graph_store 属性
func (k *Kernel) GraphStore() *storage.GraphStore { return k.graphStore }

// SetGraphStore 写入图存储
func (k *Kernel) SetGraphStore(store *storage.GraphStore) { k.graphStore = store }

// MetadataStore 对齐 metadata_store 属性
func (k *Kernel) MetadataStore() *storage.Store { return k.metadataStore }

// SetMetadataStore 写入元数据存储
func (k *Kernel) SetMetadataStore(store *storage.Store) { k.metadataStore = store }

// EmbeddingManager 对齐 embedding_manager 属性
func (k *Kernel) EmbeddingManager() *embedding.EmbeddingAPIAdapter { return k.embeddingManager }

// SetEmbeddingManager 写入嵌入管理器
func (k *Kernel) SetEmbeddingManager(manager *embedding.EmbeddingAPIAdapter) {
	k.embeddingManager = manager
}

// SparseIndex 对齐 sparse_index 属性
func (k *Kernel) SparseIndex() *retrieval.SparseBM25Index { return k.sparseIndex }

// SetSparseIndex 写入稀疏索引
func (k *Kernel) SetSparseIndex(index *retrieval.SparseBM25Index) { k.sparseIndex = index }

// Retriever 对齐 retriever 属性
func (k *Kernel) Retriever() *retrieval.DualPathRetriever { return k.retriever }

// ThresholdFilter 对齐 threshold_filter 属性
func (k *Kernel) ThresholdFilter() *retrieval.DynamicThresholdFilter { return k.thresholdFilter }

// SetRetriever 写入双路检索器，供 native 装配在初始化后回填
func (k *Kernel) SetRetriever(retriever *retrieval.DualPathRetriever) { k.retriever = retriever }

// SetThresholdFilter 写入动态阈值过滤器
func (k *Kernel) SetThresholdFilter(filter *retrieval.DynamicThresholdFilter) {
	k.thresholdFilter = filter
}

// RuntimeBundle 返回检索运行时装配结果
func (k *Kernel) RuntimeBundle() SearchRuntimeBundle { return k.runtimeBundle }

// SetRuntimeBundle 写入检索运行时装配结果
func (k *Kernel) SetRuntimeBundle(bundle SearchRuntimeBundle) { k.runtimeBundle = bundle }

// RelationWriteService 对齐 relation_write_service 属性
func (k *Kernel) RelationWriteService() RelationWriteService { return k.relationWriteService }

// SetRelationWriteService 写入关系写入服务
func (k *Kernel) SetRelationWriteService(service RelationWriteService) {
	k.relationWriteService = service
}

// SetFeedbackIntegrationConfig 注入反馈纠错集成配置
func (k *Kernel) SetFeedbackIntegrationConfig(cfg *utils.FeedbackIntegrationConfig) {
	k.feedbackIntegration = cfg
}

// FeedbackIntegrationConfig 返回反馈纠错集成配置
func (k *Kernel) FeedbackIntegrationConfig() *utils.FeedbackIntegrationConfig {
	return k.feedbackIntegration
}

// configGetter 暴露给策略函数的配置读取回调
func (k *Kernel) configGetter() utils.ConfigGetter {
	return func(key string, defaultValue any) any { return k.Cfg(key, defaultValue) }
}
