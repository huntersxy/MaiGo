package memruntime

// 内核运行时门面，对齐 core/runtime/runtime_facade.py
// 向导入、调优等协作组件暴露受控的内核运行时能力

import (
	"context"
	"strings"

	"maibot-go/internal/A_memorix/core/embedding"
	"maibot-go/internal/A_memorix/core/retrieval"
	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// KernelRuntimeFacade 是内核的受控视图
type KernelRuntimeFacade struct {
	kernel           *Kernel
	config           map[string]any
	pluginConfig     map[string]any
	runtimeSelfCheck map[string]any
}

// NewKernelRuntimeFacade 构造门面并对齐 Python 的 config 别名
func NewKernelRuntimeFacade(kernel *Kernel) *KernelRuntimeFacade {
	facade := &KernelRuntimeFacade{
		kernel:           kernel,
		runtimeSelfCheck: map[string]any{},
	}
	if kernel != nil {
		facade.config = kernel.Config()
		facade.pluginConfig = kernel.Config()
	}
	return facade
}

// GetConfig 对齐 get_config
func (f *KernelRuntimeFacade) GetConfig(key string, defaultValue any) any {
	return f.kernel.Cfg(key, defaultValue)
}

// Config 返回插件配置引用
func (f *KernelRuntimeFacade) Config() map[string]any { return f.config }

// PluginConfig 返回插件配置引用，对齐 _plugin_config
func (f *KernelRuntimeFacade) PluginConfig() map[string]any { return f.pluginConfig }

// RuntimeSelfCheckReport 返回最近一次自检报告
func (f *KernelRuntimeFacade) RuntimeSelfCheckReport() map[string]any { return f.runtimeSelfCheck }

// SetRuntimeSelfCheckReport 记录自检报告
func (f *KernelRuntimeFacade) SetRuntimeSelfCheckReport(report map[string]any) {
	f.runtimeSelfCheck = report
}

// IsRuntimeReady 对齐 is_runtime_ready
func (f *KernelRuntimeFacade) IsRuntimeReady() bool { return f.kernel.IsRuntimeReady() }

// IsChatEnabled 对齐 is_chat_enabled
func (f *KernelRuntimeFacade) IsChatEnabled(streamID string, groupID string, userID string) bool {
	return f.kernel.IsChatEnabled(context.Background(), streamID, groupID, userID)
}

// ReinforceAccess 对齐 reinforce_access：元数据与图都就绪时才强化关系生命周期
func (f *KernelRuntimeFacade) ReinforceAccess(ctx context.Context, relationHashes []string) error {
	if f.kernel.MetadataStore() == nil || f.kernel.GraphStore() == nil {
		return nil
	}
	hashes := uniqueTrimmedStrings(relationHashes)
	if len(hashes) == 0 {
		return nil
	}
	if f.kernel.services.Maintenance == nil {
		return nil
	}
	return f.kernel.services.Maintenance.ApplyRelationLifecycleEvent(ctx, hashes, utils.LifecycleEventAccess)
}

// uniqueTrimmedStrings 对齐 list(dict.fromkeys(str(item or "").strip() ...)) 的保序去重
func uniqueTrimmedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

// ExecuteRequestWithDedup 对齐 execute_request_with_dedup
func (f *KernelRuntimeFacade) ExecuteRequestWithDedup(ctx context.Context, requestKey string, executor func() (map[string]any, error)) (bool, map[string]any, error) {
	return f.kernel.ExecuteRequestWithDedup(ctx, requestKey, executor)
}

// ApplyRetrievalTuningProfile 对齐 apply_retrieval_tuning_profile
func (f *KernelRuntimeFacade) ApplyRetrievalTuningProfile(ctx context.Context, profile map[string]any, validate bool) (map[string]any, error) {
	return f.kernel.ApplyRetrievalTuningProfile(ctx, profile, validate)
}

// VectorStore 对齐 vector_store 属性
func (f *KernelRuntimeFacade) VectorStore() *storage.VectorStore { return f.kernel.VectorStore() }

// ParagraphVectorStore 对齐 paragraph_vector_store 属性
func (f *KernelRuntimeFacade) ParagraphVectorStore() *storage.VectorStore {
	return f.kernel.ParagraphVectorStore()
}

// GraphVectorStore 对齐 graph_vector_store 属性
func (f *KernelRuntimeFacade) GraphVectorStore() *storage.VectorStore {
	return f.kernel.GraphVectorStore()
}

// GraphStore 对齐 graph_store 属性
func (f *KernelRuntimeFacade) GraphStore() *storage.GraphStore { return f.kernel.GraphStore() }

// MetadataStore 对齐 metadata_store 属性
func (f *KernelRuntimeFacade) MetadataStore() *storage.Store { return f.kernel.MetadataStore() }

// EmbeddingManager 对齐 embedding_manager 属性
func (f *KernelRuntimeFacade) EmbeddingManager() *embedding.EmbeddingAPIAdapter {
	return f.kernel.EmbeddingManager()
}

// SparseIndex 对齐 sparse_index 属性
func (f *KernelRuntimeFacade) SparseIndex() *retrieval.SparseBM25Index { return f.kernel.SparseIndex() }

// RelationWriteService 对齐 relation_write_service 属性
func (f *KernelRuntimeFacade) RelationWriteService() RelationWriteService {
	return f.kernel.RelationWriteService()
}

// IsEmbeddingDegraded 对齐 is_embedding_degraded
func (f *KernelRuntimeFacade) IsEmbeddingDegraded() bool { return f.kernel.IsEmbeddingDegraded() }

// DualVectorPoolsEnabled 对齐 _dual_vector_pools_enabled
func (f *KernelRuntimeFacade) DualVectorPoolsEnabled() bool { return f.kernel.DualVectorPoolsEnabled() }

// AllowMetadataOnlyWrite 对齐 allow_metadata_only_write
func (f *KernelRuntimeFacade) AllowMetadataOnlyWrite() bool { return f.kernel.AllowMetadataOnlyWrite() }

// PersistVectorStore 通过内核统一入口保存向量与指纹
func (f *KernelRuntimeFacade) PersistVectorStore(store *storage.VectorStore) {
	f.kernel.SaveVectorStore(store)
}

// IngestText 让派生写入复用内核的 external ID 幂等入口
func (f *KernelRuntimeFacade) IngestText(ctx context.Context, kwargs IngestTextParams) (map[string]any, error) {
	return f.kernel.IngestText(ctx, kwargs)
}

// WriteParagraphVectorOrEnqueue 对齐 write_paragraph_vector_or_enqueue
func (f *KernelRuntimeFacade) WriteParagraphVectorOrEnqueue(ctx context.Context, paragraphHash string, content string, contextText string) (map[string]any, error) {
	return f.kernel.WriteParagraphVectorOrEnqueue(ctx, paragraphHash, content, contextText)
}

// EnqueueParagraphVectorBackfill 对齐 enqueue_paragraph_vector_backfill
func (f *KernelRuntimeFacade) EnqueueParagraphVectorBackfill(paragraphHash string, errorText string) {
	f.kernel.EnqueueParagraphVectorBackfill(paragraphHash, errorText)
}

// RecordPersonEvidenceWritten 在人物证据提交后激活人物并合并画像刷新请求
func (f *KernelRuntimeFacade) RecordPersonEvidenceWritten(personIDs []string, reason string) {
	for _, personID := range uniqueTrimmedStrings(personIDs) {
		f.kernel.MarkPersonActive(personID)
		f.kernel.EnqueuePersonProfileRefresh(personID, reason)
	}
}
