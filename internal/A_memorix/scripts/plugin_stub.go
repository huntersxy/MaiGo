package scripts

// 脚本侧最小插件对象，对齐 runtime_self_check.py 的 _PluginStub
// Python 的插件是鸭子类型，Go 版把生命周期实际访问的成员显式补齐

import (
	"sync"

	"maibot-go/internal/A_memorix/core/embedding"
	"maibot-go/internal/A_memorix/core/retrieval"
	memruntime "maibot-go/internal/A_memorix/core/runtime"
	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// scriptPlugin 脚本用插件对象
type scriptPlugin struct {
	config map[string]any

	initLock  sync.Mutex
	selfCheck utils.SelfCheckCache

	initialized          bool
	runtimeReady         bool
	dualVectorPoolsReady bool

	embeddingManager *embedding.EmbeddingAPIAdapter
	vectorStore      *storage.VectorStore
	paragraphStore   *storage.VectorStore
	graphVectorStore *storage.VectorStore
	graphStore       *storage.GraphStore
	metadataStore    *storage.Store
	sparseIndex      *retrieval.SparseBM25Index
	relationWriter   memruntime.RelationWriteService
}

// newScriptPlugin 构造脚本插件对象
func newScriptPlugin(config map[string]any) *scriptPlugin {
	if config == nil {
		config = map[string]any{}
	}
	return &scriptPlugin{config: config}
}

// Initialized 返回初始化标记
func (p *scriptPlugin) Initialized() bool { return p.initialized }

// SetInitialized 写入初始化标记
func (p *scriptPlugin) SetInitialized(value bool) { p.initialized = value }

// SetRuntimeReady 写入运行时就绪标记
func (p *scriptPlugin) SetRuntimeReady(ready bool) { p.runtimeReady = ready }

// CheckStorageReady 近似 check_storage_ready：元数据与向量存储都在视为就绪
func (p *scriptPlugin) CheckStorageReady() bool {
	return p.metadataStore != nil && p.vectorStore != nil
}

// InitLock 返回初始化锁
func (p *scriptPlugin) InitLock() *sync.Mutex { return &p.initLock }

// ValidateRuntimeConfig 脚本初始化不校验运行时配置，对齐 Python 直接调用 initialize_storage_async
func (p *scriptPlugin) ValidateRuntimeConfig() error { return nil }

// UpdatePluginConfig 脚本无需回写插件配置
func (p *scriptPlugin) UpdatePluginConfig() {}

// GetConfig 对齐 _PluginStub.get_config
func (p *scriptPlugin) GetConfig(key string, defaultValue any) any {
	return NestedGet(p.config, key, defaultValue)
}

// SetConfigValue 写入点分配置
func (p *scriptPlugin) SetConfigValue(key string, value any) {
	SetNested(p.config, key, value)
}

// SelfCheckCache 返回自检缓存
func (p *scriptPlugin) SelfCheckCache() *utils.SelfCheckCache { return &p.selfCheck }

// DualVectorPoolsReady 返回双池就绪标记
func (p *scriptPlugin) DualVectorPoolsReady() bool { return p.dualVectorPoolsReady }

// SetDualVectorPoolsReady 写入双池就绪标记
func (p *scriptPlugin) SetDualVectorPoolsReady(ready bool) { p.dualVectorPoolsReady = ready }

// EmbeddingManager 返回嵌入适配器
func (p *scriptPlugin) EmbeddingManager() *embedding.EmbeddingAPIAdapter { return p.embeddingManager }

// SetEmbeddingManager 写入嵌入适配器
func (p *scriptPlugin) SetEmbeddingManager(manager *embedding.EmbeddingAPIAdapter) {
	p.embeddingManager = manager
}

// VectorStore 返回单池向量存储
func (p *scriptPlugin) VectorStore() *storage.VectorStore { return p.vectorStore }

// SetVectorStore 写入单池向量存储
func (p *scriptPlugin) SetVectorStore(store *storage.VectorStore) { p.vectorStore = store }

// ParagraphVectorStore 返回段落池向量存储
func (p *scriptPlugin) ParagraphVectorStore() *storage.VectorStore { return p.paragraphStore }

// SetParagraphVectorStore 写入段落池向量存储
func (p *scriptPlugin) SetParagraphVectorStore(store *storage.VectorStore) { p.paragraphStore = store }

// GraphVectorStore 返回图谱池向量存储
func (p *scriptPlugin) GraphVectorStore() *storage.VectorStore { return p.graphVectorStore }

// SetGraphVectorStore 写入图谱池向量存储
func (p *scriptPlugin) SetGraphVectorStore(store *storage.VectorStore) { p.graphVectorStore = store }

// GraphStore 返回图存储
func (p *scriptPlugin) GraphStore() *storage.GraphStore { return p.graphStore }

// SetGraphStore 写入图存储
func (p *scriptPlugin) SetGraphStore(store *storage.GraphStore) { p.graphStore = store }

// MetadataStore 返回元数据存储
func (p *scriptPlugin) MetadataStore() *storage.Store { return p.metadataStore }

// SetMetadataStore 写入元数据存储
func (p *scriptPlugin) SetMetadataStore(store *storage.Store) { p.metadataStore = store }

// SparseIndex 返回稀疏索引
func (p *scriptPlugin) SparseIndex() *retrieval.SparseBM25Index { return p.sparseIndex }

// SetSparseIndex 写入稀疏索引
func (p *scriptPlugin) SetSparseIndex(index *retrieval.SparseBM25Index) { p.sparseIndex = index }

// RelationWriteService 返回关系写入服务
func (p *scriptPlugin) RelationWriteService() memruntime.RelationWriteService {
	return p.relationWriter
}

// SetRelationWriteService 写入关系写入服务
func (p *scriptPlugin) SetRelationWriteService(service memruntime.RelationWriteService) {
	p.relationWriter = service
}
