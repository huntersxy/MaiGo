package memruntime

// 存储组件与检索组件初始化，对齐 lifecycle_orchestrator.py 的 initialize_storage_async
// 文件读写与迁移为同步调用，Python 的 asyncio.to_thread 在 Go 中不需要额外包装

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"MaiGo/internal/A_memorix/core/embedding"
	"MaiGo/internal/A_memorix/core/retrieval"
	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/utils"
)

// 生命周期初始化使用的默认值，对齐 Python 侧关键字默认值
const (
	lifecycleDefaultBatchSize            = 32
	lifecycleDefaultMaxConcurrent        = 5
	lifecycleDefaultEmbeddingDimension   = 1024
	lifecycleDefaultModelName            = "auto"
	lifecycleDefaultDimensionRequestMode = "explicit"
	lifecycleDefaultMinTrainThreshold    = 40
	lifecycleDefaultVectorPoolMode       = "dual"
	lifecycleDefaultSparseMatrixFormat   = "csr"
)

// lifecycleConfigInt 读取整数配置
func lifecycleConfigInt(plugin LifecyclePlugin, key string, defaultValue int) int {
	value, err := PyInt(plugin.GetConfig(key, defaultValue))
	if err != nil {
		return defaultValue
	}
	return value
}

// lifecycleConfigBool 读取布尔配置，缺失时返回默认值
func lifecycleConfigBool(plugin LifecyclePlugin, key string, defaultValue bool) bool {
	value := plugin.GetConfig(key, defaultValue)
	if value == nil {
		return defaultValue
	}
	return PyTruthy(value)
}

// lifecycleConfigString 读取字符串配置
func lifecycleConfigString(plugin LifecyclePlugin, key string, defaultValue string) string {
	value := plugin.GetConfig(key, defaultValue)
	if value == nil {
		return defaultValue
	}
	return PyStr(value)
}

// lifecycleConfigDict 读取字典配置，非字典返回空字典
func lifecycleConfigDict(plugin LifecyclePlugin, key string) map[string]any {
	value, ok := plugin.GetConfig(key, map[string]any{}).(map[string]any)
	if !ok || value == nil {
		return map[string]any{}
	}
	return value
}

// runFormatMigration 执行启动格式迁移
func runFormatMigration(deps *LifecycleDeps, dataDir string) error {
	if deps != nil && deps.RunFormatMigration != nil {
		return deps.RunFormatMigration(dataDir)
	}
	_, err := storage.RunStartupFormatMigration(dataDir)
	return err
}

// newLifecycleVectorStore 构造向量存储
func newLifecycleVectorStore(deps *LifecycleDeps, cfg storage.VectorStoreConfig) (*storage.VectorStore, error) {
	if deps != nil && deps.NewVectorStore != nil {
		return deps.NewVectorStore(cfg)
	}
	return storage.NewVectorStore(cfg)
}

// newLifecycleGraphStore 构造图存储
func newLifecycleGraphStore(deps *LifecycleDeps, format storage.SparseMatrixFormat, dataDir string) *storage.GraphStore {
	if deps != nil && deps.NewGraphStore != nil {
		return deps.NewGraphStore(format, dataDir)
	}
	return storage.NewGraphStore(format, dataDir)
}

// newLifecycleMetadataStore 构造元数据存储
func newLifecycleMetadataStore(deps *LifecycleDeps, dataDir string) *storage.Store {
	if deps != nil && deps.NewMetadataStore != nil {
		return deps.NewMetadataStore(dataDir, "metadata.db")
	}
	return storage.NewStore(dataDir, "metadata.db")
}

// newLifecycleSparseIndex 构造稀疏索引
func newLifecycleSparseIndex(deps *LifecycleDeps, store *storage.Store, cfg retrieval.SparseBM25Config) *retrieval.SparseBM25Index {
	if deps != nil && deps.NewSparseIndex != nil {
		return deps.NewSparseIndex(store, cfg)
	}
	return retrieval.NewSparseBM25Index(store, cfg, nil)
}

// newRelationWriteService 构造关系写入服务，未注入工厂时返回 nil
func newRelationWriteService(deps *LifecycleDeps, params RelationWriteParams) RelationWriteService {
	if deps == nil || deps.NewRelationWriteService == nil {
		return nil
	}
	return deps.NewRelationWriteService(params)
}

// InitializeStorageAsync 对齐 initialize_storage_async
func InitializeStorageAsync(ctx context.Context, plugin LifecyclePlugin, deps *LifecycleDeps) error {
	logger := deps.lifecycleLogger()
	dataDir := resolveLifecycleDataDir(plugin)

	logger.Info(fmt.Sprintf("A_Memorix 数据存储路径: %s", dataDir))
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	if err := runFormatMigration(deps, dataDir); err != nil {
		return err
	}

	adapterOptions := embedding.EmbeddingAPIAdapterOptions{
		BatchSize:            lifecycleConfigInt(plugin, "embedding.batch_size", lifecycleDefaultBatchSize),
		MaxConcurrent:        lifecycleConfigInt(plugin, "embedding.max_concurrent", lifecycleDefaultMaxConcurrent),
		DefaultDimension:     lifecycleConfigInt(plugin, "embedding.dimension", lifecycleDefaultEmbeddingDimension),
		EnableCache:          lifecycleConfigBool(plugin, "embedding.enable_cache", false),
		ModelName:            lifecycleConfigString(plugin, "embedding.model_name", lifecycleDefaultModelName),
		DimensionRequestMode: lifecycleConfigString(plugin, "embedding.dimension_request_mode", lifecycleDefaultDimensionRequestMode),
		RetryConfig:          lifecycleConfigDict(plugin, "embedding.retry"),
	}
	embeddingManager := embedding.CreateEmbeddingAPIAdapter(&adapterOptions)
	plugin.SetEmbeddingManager(embeddingManager)
	logger.Info("嵌入 API 适配器初始化完成")

	detectedDimension, detectErr := embeddingManager.DetectDimension(ctx)
	if detectErr != nil {
		logger.Warning(fmt.Sprintf("嵌入维度检测失败: %v，使用默认值", detectErr))
		detectedDimension = adapterOptions.DefaultDimension
	} else {
		logger.Info(fmt.Sprintf("嵌入维度: %d", detectedDimension))
	}

	quantizationText := strings.ToLower(strings.TrimSpace(PyStr(plugin.GetConfig("embedding.quantization_type", "int8"))))
	if quantizationText != "int8" {
		return fmt.Errorf("embedding.quantization_type 在 vNext 仅允许 int8(SQ8)。")
	}
	quantizationType := utils.QuantizationInt8
	minTrainThreshold := lifecycleConfigInt(plugin, "embedding.min_train_threshold", lifecycleDefaultMinTrainThreshold)

	vectorStore, err := newLifecycleVectorStore(deps, storage.VectorStoreConfig{
		Dimension:        detectedDimension,
		QuantizationType: quantizationType,
		DataDir:          filepath.Join(dataDir, "vectors"),
	})
	if err != nil {
		return err
	}
	vectorStore.SetMinTrainThreshold(minTrainThreshold)
	plugin.SetVectorStore(vectorStore)

	paragraphStore, err := newLifecycleVectorStore(deps, storage.VectorStoreConfig{
		Dimension:        detectedDimension,
		QuantizationType: quantizationType,
		DataDir:          filepath.Join(dataDir, "vectors", "paragraph"),
	})
	if err != nil {
		return err
	}
	paragraphStore.SetMinTrainThreshold(minTrainThreshold)
	plugin.SetParagraphVectorStore(paragraphStore)

	graphVectorStore, err := newLifecycleVectorStore(deps, storage.VectorStoreConfig{
		Dimension:        detectedDimension,
		QuantizationType: quantizationType,
		DataDir:          filepath.Join(dataDir, "vectors", "graph"),
	})
	if err != nil {
		return err
	}
	graphVectorStore.SetMinTrainThreshold(minTrainThreshold)
	plugin.SetGraphVectorStore(graphVectorStore)

	vectorPoolsConfig, _ := plugin.GetConfig("retrieval.vector_pools", map[string]any{}).(map[string]any)
	vectorPoolMode := lifecycleDefaultVectorPoolMode
	if vectorPoolsConfig != nil {
		if rawMode, exists := vectorPoolsConfig["mode"]; exists {
			vectorPoolMode = strings.ToLower(strings.TrimSpace(PyStr(rawMode)))
		}
	}
	dualVectorReady := vectorPoolMode == "dual" && DualVectorReady(dataDir, detectedDimension)
	if vectorPoolMode == "dual" && !dualVectorReady {
		logger.Warning("双池配置已开启，但 ready manifest 不可用，当前按单池检索与写入运行")
	}
	plugin.SetDualVectorPoolsReady(dualVectorReady)
	logger.Info(fmt.Sprintf(
		"向量存储初始化完成（维度: %d, 训练阈值: %d）",
		detectedDimension,
		vectorStore.MinTrainThreshold(),
	))

	matrixFormatText := lifecycleConfigString(plugin, "graph.sparse_matrix_format", lifecycleDefaultSparseMatrixFormat)
	matrixFormat := storage.SparseMatrixFormatCSR
	if matrixFormatText == "csc" {
		matrixFormat = storage.SparseMatrixFormatCSC
	}
	graphStore := newLifecycleGraphStore(deps, matrixFormat, filepath.Join(dataDir, "graph"))
	plugin.SetGraphStore(graphStore)
	logger.Debug("图存储初始化完成")

	metadataDir := filepath.Join(dataDir, "metadata")
	metadataStore := newLifecycleMetadataStore(deps, metadataDir)
	if err := metadataStore.Connect(metadataDir, true); err != nil {
		return err
	}
	plugin.SetMetadataStore(metadataStore)
	logger.Debug("元数据存储初始化完成")

	plugin.SetRelationWriteService(newRelationWriteService(deps, RelationWriteParams{
		MetadataStore:       metadataStore,
		GraphStore:          graphStore,
		VectorStore:         vectorStore,
		GraphVectorStore:    graphVectorStore,
		EmbeddingManager:    embeddingManager,
		UseTypedRelationIDs: dualVectorReady,
	}))
	logger.Info("关系写入服务初始化完成")

	sparseRaw, _ := plugin.GetConfig("retrieval.sparse", map[string]any{}).(map[string]any)
	if sparseRaw == nil {
		sparseRaw = map[string]any{}
	}
	sparseConfig, sparseErr := decodeSparseConfig(sparseRaw)
	if sparseErr != nil {
		logger.Warning(fmt.Sprintf("sparse 配置非法，回退默认配置: %v", sparseErr))
		sparseConfig = retrieval.NewSparseBM25Config()
	}
	sparseIndex := newLifecycleSparseIndex(deps, metadataStore, sparseConfig)
	plugin.SetSparseIndex(sparseIndex)
	logger.Info(fmt.Sprintf(
		"稀疏检索组件初始化完成: enabled=%v, lazy_load=%v, mode=%s, tokenizer=%s",
		sparseConfig.Enabled,
		sparseConfig.LazyLoad,
		sparseConfig.Mode,
		sparseConfig.TokenizerMode,
	))

	if sparseConfig.Enabled && !sparseConfig.LazyLoad {
		warmupSummary := sparseIndex.Warmup(nil, "")
		if PyTruthy(warmupSummary["ok"]) {
			logger.Info(fmt.Sprintf(
				"稀疏索引预热完成: backend=%v, docs=%v, paragraph_probe=%v, relation_probe=%v, duration_ms=%.2f",
				warmupSummary["backend"],
				warmupSummary["doc_count"],
				warmupSummary["paragraph_probe_count"],
				warmupSummary["relation_probe_count"],
				summaryFloat(warmupSummary, "duration_ms"),
			))
		} else {
			logger.Warning(fmt.Sprintf("稀疏索引预热失败，后续检索将按需重试: %v", summaryText(warmupSummary, "error")))
		}
	}

	if vectorStore.HasData() {
		if err := vectorStore.Load("", nil, nil, ""); err != nil {
			logger.Warning(fmt.Sprintf("加载向量数据失败: %v", err))
		} else {
			logger.Debug(fmt.Sprintf("向量数据已加载，共 %d 个向量", vectorStore.NumVectors()))
		}
	}
	if dualVectorReady {
		pools := []struct {
			name  string
			store *storage.VectorStore
		}{
			{"paragraph_vector_store", paragraphStore},
			{"graph_vector_store", graphVectorStore},
		}
		for _, pool := range pools {
			if pool.store == nil || !pool.store.HasData() {
				continue
			}
			if err := pool.store.Load("", nil, nil, ""); err != nil {
				logger.Warning(fmt.Sprintf("加载 %s 数据失败: %v", pool.name, err))
			} else {
				logger.Debug(fmt.Sprintf("%s 数据已加载，共 %d 个向量", pool.name, pool.store.NumVectors()))
			}
		}
	}

	warmupSummary := vectorStore.WarmupIndex(true)
	if PyTruthy(warmupSummary["ok"]) {
		logger.Info(fmt.Sprintf(
			"向量索引预热完成: trained=%v, index_ntotal=%v, fallback_ntotal=%v, bin_count=%v, duration_ms=%.2f",
			warmupSummary["trained"],
			warmupSummary["index_ntotal"],
			warmupSummary["fallback_ntotal"],
			warmupSummary["bin_count"],
			summaryFloat(warmupSummary, "duration_ms"),
		))
	} else {
		logger.Warning(fmt.Sprintf("向量索引预热失败，继续启用 sparse 降级路径: %v", summaryText(warmupSummary, "error")))
	}
	if dualVectorReady {
		pools := []struct {
			name  string
			store *storage.VectorStore
		}{
			{"paragraph_vector_store", paragraphStore},
			{"graph_vector_store", graphVectorStore},
		}
		for _, pool := range pools {
			if pool.store == nil {
				continue
			}
			pool.store.WarmupIndex(true)
		}
	}

	if graphStore.HasData() {
		if err := graphStore.Load(""); err != nil {
			logger.Warning(fmt.Sprintf("加载图数据失败: %v", err))
		} else {
			logger.Debug(fmt.Sprintf("图数据已加载，共 %d 个节点", graphStore.NumNodes()))
		}
	}

	logger.Info(fmt.Sprintf("知识库数据目录: %s", dataDir))
	return nil
}

// summaryFloat 读取汇总字段的浮点值
func summaryFloat(summary map[string]any, key string) float64 {
	value, err := PyFloat(summary[key])
	if err != nil {
		return 0
	}
	return value
}

// summaryText 读取汇总字段的文本值，缺失返回 unknown
func summaryText(summary map[string]any, key string) string {
	value, exists := summary[key]
	if !exists || value == nil {
		return "unknown"
	}
	return PyStr(value)
}
