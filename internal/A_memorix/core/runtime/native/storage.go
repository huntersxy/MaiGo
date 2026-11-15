package native

// 存储与检索对象构造，替代 runtime 包中服务缺失时静默返回零值的转发器
// 每个构造失败都返回真实错误，由生命周期服务按 Python 语义决定降级范围

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"MaiGo/internal/A_memorix/core/embedding"
	imagememory "MaiGo/internal/A_memorix/core/image"
	"MaiGo/internal/A_memorix/core/retrieval"
	memruntime "MaiGo/internal/A_memorix/core/runtime"
	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/utils"
	"MaiGo/internal/A_memorix/core/write"
)

// 初始化默认值，对齐 Python lifecycle 关键字默认值
const (
	nativeDefaultBatchSize          = 32
	nativeDefaultMaxConcurrent      = 5
	nativeDefaultDimension          = 1024
	nativeDefaultModelName          = "auto"
	nativeDefaultDimensionMode      = "explicit"
	nativeDefaultMinTrainThreshold  = 40
	nativeDefaultSparseMatrixFormat = "csr"
	nativeDefaultVectorPoolMode     = "dual"
)

// createEmbeddingAdapter 按生命周期服务传入的参数构造嵌入适配器
func (r *Runtime) createEmbeddingAdapter(params map[string]any) (any, error) {
	options := &embedding.EmbeddingAPIAdapterOptions{
		BatchSize:            cfgInt(params, "batch_size", nativeDefaultBatchSize),
		MaxConcurrent:        cfgInt(params, "max_concurrent", nativeDefaultMaxConcurrent),
		DefaultDimension:     cfgInt(params, "default_dimension", nativeDefaultDimension),
		EnableCache:          cfgTruthy(params["enable_cache"]),
		ModelName:            cfgString(params, "model_name", nativeDefaultModelName),
		DimensionRequestMode: cfgString(params, "dimension_request_mode", nativeDefaultDimensionMode),
		RetryConfig:          cfgMap(params, "retry_config"),
		ConfigSource:         r.embeddingConfigSource,
	}
	return embedding.CreateEmbeddingAPIAdapter(options), nil
}

// openMetadataStore 打开元数据 SQLite
func (r *Runtime) openMetadataStore() error {
	dir := filepath.Join(r.kernel.DataDir(), "metadata")
	store := storage.NewStore(dir, "metadata.db")
	if err := store.Connect(dir, true); err != nil {
		return fmt.Errorf("打开元数据存储失败: %w", err)
	}
	r.metadataStore = store
	r.kernel.SetMetadataStore(store)
	if r.base != nil {
		r.base.SetMeta(newMetadataPort(store))
	}
	return nil
}

// ensureGraphStore 构造并装载图存储，失败返回真实错误由生命周期关闭图谱通道
func (r *Runtime) ensureGraphStore(matrixFormat string) error {
	format := storage.SparseMatrixFormatCSR
	if strings.ToLower(strings.TrimSpace(matrixFormat)) == "csc" {
		format = storage.SparseMatrixFormatCSC
	}
	dir := filepath.Join(r.kernel.DataDir(), "graph")
	store := storage.NewGraphStore(format, dir)
	if store.HasData() {
		if err := store.Load(dir); err != nil {
			return fmt.Errorf("加载图存储失败: %w", err)
		}
		r.logger.Info(fmt.Sprintf("[native] 图存储已加载: nodes=%d, edges=%d", store.NumNodes(), store.NumEdges()))
	}
	r.graphStore = store
	r.kernel.SetGraphStore(store)
	if r.base != nil {
		r.base.SetGraph(newGraphPort(store))
	}
	return nil
}

// ensureSparseIndex 按配置构造稀疏索引，配置非法时回退默认值并记录降级
func (r *Runtime) ensureSparseIndex(sparseConfig map[string]any) error {
	if r.metadataStore == nil {
		return errors.New("稀疏索引缺少元数据存储")
	}
	config := decodeSparseConfig(sparseConfig)
	segmenter := retrieval.SegmenterForSparseConfig(config)
	if _, ok := segmenter.(*retrieval.JiebaSegmenter); !ok {
		r.markDegraded("jieba 分词器不可用，稀疏索引索引与查询统一退化为字符 n-gram")
	}
	// Python metadata_fts.py:229-239 的 _tokenize_paragraph_for_fts 对 tokenized shadow 索引
	// 始终用 jieba.cut_for_search（无 jieba 时才逐字符回退），与查询 tokenizer 配置无关
	shadowSegmenter, shadowErr := retrieval.DefaultJiebaSegmenter()
	if shadowErr != nil || shadowSegmenter == nil {
		r.metadataStore.SetSegmenter(nil)
	} else {
		r.metadataStore.SetSegmenter(shadowSegmenter)
	}
	index := retrieval.NewSparseBM25Index(r.metadataStore, config, segmenter)
	r.sparseIndex = index
	r.sparseEnabled = config.Enabled
	r.kernel.SetSparseIndex(index)
	r.logger.Info(fmt.Sprintf("[native] 稀疏索引已构造: enabled=%v, lazy_load=%v, mode=%s, tokenizer=%s", config.Enabled, config.LazyLoad, config.Mode, config.TokenizerMode))
	return nil
}

// paragraphPoolStore 返回段落向量写入目标，对齐 Python _paragraph_store
// 单池模式写主向量库，双池就绪后写段落池；缺失时回落到主库
func (r *Runtime) paragraphPoolStore() *storage.VectorStore {
	if r.kernel.DualVectorPoolsEnabled() {
		if r.paragraphVectorStore != nil {
			return r.paragraphVectorStore
		}
	}
	return r.vectorStore
}

// graphPoolStore 返回实体/关系向量写入目标，对齐 Python _graph_vector_store
func (r *Runtime) graphPoolStore() *storage.VectorStore {
	if r.kernel.DualVectorPoolsEnabled() {
		if r.graphVectorStore != nil {
			return r.graphVectorStore
		}
	}
	return r.vectorStore
}

// persistBundleVectorStores 对齐 Python _persist(force_vectors=True)
// 依次持久化向量库、向量空间输入、关系图、稀疏索引与图片向量
func (r *Runtime) persistBundleVectorStores() {
	if r.kernel.DualVectorPoolsEnabled() {
		r.kernel.SaveVectorStore(r.paragraphPoolStore())
		r.kernel.SaveVectorStore(r.graphPoolStore())
	} else {
		r.kernel.SaveVectorStore(r.vectorStore)
	}
	if err := r.kernel.SaveVectorSpaceInputs(); err != nil {
		r.logger.Warning(fmt.Sprintf("[native] 持久化向量空间输入映射失败: %v", err))
	}
	if r.graphStore != nil {
		if err := r.graphStore.Save(r.graphStore.DataDir()); err != nil {
			r.logger.Warning(fmt.Sprintf("[native] 持久化关系图失败: %v", err))
		}
	}
	if r.sparseIndex != nil && r.sparseEnabled {
		r.sparseIndex.EnsureLoaded()
	}
}

// sparseWarmup 预热稀疏索引，失败返回错误由生命周期关闭稀疏通道
func (r *Runtime) sparseWarmup() (map[string]any, error) {
	if r.sparseIndex == nil {
		return nil, errors.New("稀疏索引尚未构造")
	}
	summary := r.sparseIndex.Warmup(nil, "")
	if !memruntime.PyTruthy(summary["ok"]) {
		return summary, fmt.Errorf("稀疏索引预热失败: %v", summary["error"])
	}
	return summary, nil
}

// makeVectorStores 构造三套向量存储，对齐 Python _make_vector_store 与双池探测
func (r *Runtime) makeVectorStores(dimension int) error {
	quantization := strings.ToLower(strings.TrimSpace(memruntime.PyStr(r.kernel.Cfg("embedding.quantization_type", "int8"))))
	if quantization != "" && quantization != "int8" {
		return fmt.Errorf("embedding.quantization_type 在 vNext 仅允许 int8(SQ8)")
	}
	minTrain := cfgInt(r.kernel.Config(), "embedding.min_train_threshold", nativeDefaultMinTrainThreshold)
	root := filepath.Join(r.kernel.DataDir(), "vectors")
	makeStore := func(dir string) (*storage.VectorStore, error) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建向量目录失败 %s: %w", dir, err)
		}
		store, err := storage.NewVectorStore(storage.VectorStoreConfig{
			Dimension:        dimension,
			QuantizationType: utils.QuantizationInt8,
			DataDir:          dir,
		})
		if err != nil {
			return nil, fmt.Errorf("构造向量存储失败 %s: %w", dir, err)
		}
		store.SetMinTrainThreshold(minTrain)
		return store, nil
	}
	main, err := makeStore(root)
	if err != nil {
		return err
	}
	paragraph, err := makeStore(filepath.Join(root, "paragraph"))
	if err != nil {
		return err
	}
	graphVector, err := makeStore(filepath.Join(root, "graph"))
	if err != nil {
		return err
	}
	r.vectorStore = main
	r.paragraphVectorStore = paragraph
	r.graphVectorStore = graphVector
	r.kernel.SetVectorStore(main)
	r.kernel.SetParagraphVectorStore(paragraph)
	r.kernel.SetGraphVectorStore(graphVector)
	dualReady := memruntime.DualVectorReady(r.kernel.DataDir(), dimension)
	r.kernel.SetDualVectorPoolsReady(dualReady)
	if !dualReady {
		r.markDegraded("双池向量 manifest 不可用，按单池运行")
	}
	return nil
}

// loadVectorStoresForRuntime 装载已有向量数据并预热索引，失败返回真实错误
func (r *Runtime) loadVectorStoresForRuntime() error {
	load := func(name string, store *storage.VectorStore) error {
		if store == nil {
			return nil
		}
		if store.HasData() {
			if err := store.Load("", nil, nil, ""); err != nil {
				return fmt.Errorf("加载 %s 失败: %w", name, err)
			}
			r.logger.Info(fmt.Sprintf("[native] %s 已加载: %d 个向量", name, store.NumVectors()))
		}
		if summary := store.WarmupIndex(true); !memruntime.PyTruthy(summary["ok"]) {
			r.markDegraded(fmt.Sprintf("%s 索引预热失败: %v", name, summary["error"]))
		}
		return nil
	}
	if err := load("主向量库", r.vectorStore); err != nil {
		return err
	}
	if err := load("段向量库", r.paragraphVectorStore); err != nil {
		return err
	}
	return load("图向量库", r.graphVectorStore)
}

// markVectorStoresHealthy 写入向量健康状态，对齐 _set_vector_health 的 healthy 补丁
// 恢复流程留下旧只读视图时保持 recovering/degraded，不能覆盖为 healthy
func (r *Runtime) markVectorStoresHealthy() {
	if r.legacyVectorView != nil {
		return
	}
	r.kernel.SetVectorHealthField("state", "healthy")
	r.kernel.SetVectorHealthField("error_code", "")
	r.kernel.SetVectorHealthField("reason", "")
	r.kernel.SetVectorHealthField("trusted_coverage", 1.0)
	r.kernel.SetVectorHealthField("recovery_stage", "idle")
	r.kernel.SetVectorHealthField("operation_id", "")
	r.kernel.SetVectorHealthField("copy_progress", map[string]any{})
}

// disableVectorChannel 关闭向量通道并记录真实原因
func (r *Runtime) disableVectorChannel(cause error) {
	reason := "unknown"
	code := "vector_store_unavailable"
	if cause != nil {
		reason = cause.Error()
		if strings.Contains(strings.ToLower(reason), "integrity") {
			code = "vector_store_integrity"
		}
	}
	_ = r.kernel.SetRuntimeCapability("vector_read", false)
	_ = r.kernel.SetRuntimeCapability("vector_write", false)
	r.kernel.SetVectorHealthField("state", "unavailable")
	r.kernel.SetVectorHealthField("error_code", code)
	r.kernel.SetVectorHealthField("reason", reason)
	r.kernel.SetVectorHealthField("trusted_coverage", 0.0)
	r.kernel.SetVectorHealthField("recovery_stage", "idle")
	r.markDegraded("向量通道已关闭: " + reason)
	r.logger.Warning(fmt.Sprintf("[native] 向量通道已关闭，元数据与稀疏通道继续运行: %s", reason))
}

// storedVectorDimension 读取历史向量维度，对齐 _stored_vector_dimension
// 双池配置下优先读 ready manifest，否则读向量目录的元数据文件
func (r *Runtime) storedVectorDimension() (int, bool, error) {
	root := r.vectorSpaceRoot()
	if r.dualVectorPoolsConfigured() {
		manifestPath := filepath.Join(root, "dual_ready.json")
		raw, err := os.ReadFile(manifestPath)
		switch {
		case err == nil:
			var manifest map[string]any
			if err := json.Unmarshal(raw, &manifest); err != nil {
				r.logger.Warning(fmt.Sprintf("[native] 读取双池 ready manifest 失败: %v", err))
			} else if dimension, ok := pyPositiveInt(manifest["dimension"]); ok {
				return dimension, true, nil
			}
		case !os.IsNotExist(err):
			r.logger.Warning(fmt.Sprintf("[native] 读取双池 ready manifest 失败: %v", err))
		}
	}
	metaPath := filepath.Join(root, "vectors_metadata.json")
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		r.logger.Warning(fmt.Sprintf("[native] 读取向量元数据失败，将回退到 runtime self-check: %v", err))
		return 0, false, nil
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		r.logger.Warning(fmt.Sprintf("[native] 读取向量元数据失败，将回退到 runtime self-check: %v", err))
		return 0, false, nil
	}
	dimension, ok := pyPositiveInt(meta["dimension"])
	if !ok {
		return 0, false, nil
	}
	return dimension, true, nil
}

// vectorSpaceRoot 返回当前活动向量空间的向量根目录
func (r *Runtime) vectorSpaceRoot() string {
	if space := r.kernel.ActiveVectorSpaceID(); space != "" {
		return filepath.Join(r.kernel.DataDir(), "vectors", "spaces", space)
	}
	return filepath.Join(r.kernel.DataDir(), "vectors")
}

// dualVectorPoolsConfigured 按配置判断是否启用双池，对齐 _dual_vector_pools_config_enabled
func (r *Runtime) dualVectorPoolsConfigured() bool {
	mode := strings.ToLower(strings.TrimSpace(memruntime.PyStr(r.kernel.Cfg("retrieval.vector_pools.mode", "dual"))))
	if mode == "" {
		mode = "dual"
	}
	return mode == "dual"
}

// pyPositiveInt 解析正整数，非数字或非正数返回 false
func pyPositiveInt(value any) (int, bool) {
	parsed, err := memruntime.PyInt(value)
	if err != nil || parsed <= 0 {
		return 0, false
	}
	return parsed, true
}

// readActiveVectorSpace 读取向量空间目录中的活动空间
func (r *Runtime) readActiveVectorSpace() (string, error) {
	path := filepath.Join(r.kernel.DataDir(), "vectors", "active.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 对齐 Python：缺文件时活动空间为空串并写回内核状态
			r.kernel.SetActiveVectorSpaceID("")
			return "", nil
		}
		return "", err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("解析活动向量空间失败: %w", err)
	}
	space, _ := payload["space_id"].(string)
	space = strings.TrimSpace(space)
	// 对齐 Python runtime_lifecycle：读盘后写回 self._active_vector_space_id
	r.kernel.SetActiveVectorSpaceID(space)
	return space, nil
}

// readVectorSpaceInputs 读取向量空间输入映射
func (r *Runtime) readVectorSpaceInputs() (map[string]string, error) {
	path := r.kernel.VectorSpaceInputsPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("解析向量空间输入映射失败: %w", err)
	}
	out := make(map[string]string, len(payload))
	for key, value := range payload {
		if text, ok := value.(string); ok {
			out[key] = text
		}
	}
	r.kernel.SetVectorSpaceInputs(out)
	return out, nil
}

// ensureImageRuntime 构造图片记忆运行时，对齐 Python _ensure_image_runtime
func (r *Runtime) ensureImageRuntime(imageConfig map[string]any) error {
	if !memruntime.PyTruthy(imageConfig["enabled"]) {
		return nil
	}
	if r.metadataStore == nil {
		return errors.New("图片记忆运行时缺少元数据存储")
	}
	maxBytes := cfgInt(imageConfig, "max_bytes", 10*1024*1024)
	maxPixels := cfgInt(imageConfig, "max_pixels", 40*1000*1000)
	assetStore, err := imagememory.NewImageAssetStore(filepath.Join(r.kernel.DataDir(), "images", "assets"), maxBytes, maxPixels)
	if err != nil {
		return fmt.Errorf("构造图片资产存储失败: %w", err)
	}
	probeRetry := 60.0
	if raw, ok := imageConfig["probe_retry_seconds"]; ok {
		parsed, parseErr := memruntime.PyFloat(raw)
		if parseErr != nil {
			return fmt.Errorf("图片配置项 probe_retry_seconds 无法转换为浮点数: %w", parseErr)
		}
		if parsed < 0 {
			parsed = 0
		}
		probeRetry = parsed
	}
	embedder, err := imagememory.NewHostImageEmbedderWithOptions(imagememory.HostImageEmbedderOptions{
		ClientProvider:    currentImageEmbeddingClient,
		TaskName:          cfgString(imageConfig, "task_name", "image_embedding"),
		PreprocessVersion: cfgString(imageConfig, "preprocess_version", "identity_v1"),
		ProbeRetrySeconds: &probeRetry,
		Logger:            r.logger,
	})
	if err != nil {
		return fmt.Errorf("构造图片嵌入器失败: %w", err)
	}
	imageRuntime, err := imagememory.NewImageMemoryRuntime(imagememory.ImageMemoryRuntimeConfig{
		MetadataStore: r.metadataStore,
		AssetStore:    assetStore,
		Embedder:      embedder,
		VectorRoot:    filepath.Join(r.kernel.DataDir(), "images", "vectors"),
		Config:        imageConfig,
		PersistVectorStore: func(store *storage.VectorStore, fingerprint map[string]any) error {
			return store.Save(store.DataDir(), fingerprint)
		},
		Logger: r.logger,
	})
	if err != nil {
		return fmt.Errorf("构造图片记忆运行时失败: %w", err)
	}
	r.imageRuntime = imageRuntime
	r.kernel.SetImageMemoryRuntime(imageRuntime)
	return nil
}

// recoverImageAssets 在初始化阶段核对已提交的图片资产
func (r *Runtime) recoverImageAssets() error {
	if r.imageRuntime == nil {
		return nil
	}
	return r.imageRuntime.RecoverAssets(context.Background())
}

// reconcileRelationGraphProjectionJobs 对齐 Python _reconcile_relation_graph_projection_jobs
// 作业表、租约与权威态 CAS 都在元数据存储内，这里只负责持有图投影锁并转调维护服务
func (r *Runtime) reconcileRelationGraphProjectionJobs(resetLeases bool, batchSize int) error {
	service := r.kernel.Services().Maintenance
	if service == nil {
		return errors.New("关系图投影作业对账缺少维护服务")
	}
	reconciler, ok := service.(interface {
		ReconcileRelationGraphProjectionJobs(context.Context, bool, int) (map[string]int, error)
	})
	if !ok {
		return errors.New("维护服务不支持关系图投影作业对账")
	}
	r.graphProjectionMu.Lock()
	defer r.graphProjectionMu.Unlock()
	result, err := reconciler.ReconcileRelationGraphProjectionJobs(context.Background(), resetLeases, batchSize)
	if err != nil {
		return err
	}
	message := fmt.Sprintf("[native] 关系图投影作业对账完成: claimed=%d completed=%d saved_batches=%d",
		result["claimed"], result["completed"], result["saved_batches"])
	// 周期性对账在无事可做时每轮都会走到这里，空转降为 Debug，避免日志刷屏
	if result["claimed"] == 0 && result["completed"] == 0 && result["saved_batches"] == 0 {
		r.logger.Debug(message)
	} else {
		r.logger.Info(message)
	}
	return nil
}

// refreshRelationWriteService 按当前存储与嵌入通道重建关系写入服务
func (r *Runtime) refreshRelationWriteService() {
	if r.metadataStore == nil || r.vectorStore == nil {
		r.relationWriteService = nil
		r.kernel.SetRelationWriteService(nil)
		return
	}
	var metadata write.RelationMetadataStore = r.metadataStore
	var graph write.RelationGraphStore
	if r.graphStore != nil {
		graph = r.graphStore
	}
	var vectors write.RelationVectorStore = r.vectorStore
	var graphVectors write.RelationVectorStore
	if r.graphVectorStore != nil {
		graphVectors = r.graphVectorStore
	}
	var embedder write.TextEmbedder
	if r.embeddingManager != nil {
		embedder = &embeddingTextAdapter{inner: r.embeddingManager}
	}
	config := r.kernel.Config()
	service := write.NewRelationWriteService(metadata, graph, vectors, embedder, write.RelationWriteConfig{
		GraphVectorStore:       graphVectors,
		UseTypedRelationIDs:    r.kernel.DualVectorPoolsEnabled(),
		EmbeddingBatchSize:     cfgInt(config, "embedding.batch_size", 32),
		EmbeddingMaxConcurrent: cfgInt(config, "embedding.max_concurrent", 5),
		RecordEmbeddingInput: func(source string, hashValue string, text string) {
			r.kernel.RecordVectorSpaceInput(source, hashValue, text)
		},
	})
	r.relationWriteService = &relationWriteAdapter{inner: service}
	r.kernel.SetRelationWriteService(r.relationWriteService)
	if embedder == nil {
		r.markDegraded("嵌入管理器未初始化，关系写入仅落元数据与图边")
	}
}

// vectorRebuildStatus 读取向量重建状态，对齐 _vector_rebuild_status
func (r *Runtime) vectorRebuildStatus() map[string]any {
	status, err := r.kernel.VectorRebuildStatus()
	if err != nil {
		r.logger.Warning(fmt.Sprintf("[native] 读取向量重建状态失败: %v", err))
		return map[string]any{}
	}
	return status
}

// vectorPoolsStatus 计算向量池状态，对齐 _vector_pools_status
func (r *Runtime) vectorPoolsStatus() map[string]any {
	configured := nativeDefaultVectorPoolMode
	if raw, ok := r.kernel.Cfg("retrieval.vector_pools", nil).(map[string]any); ok {
		if mode := strings.ToLower(strings.TrimSpace(memruntime.PyStr(raw["mode"]))); mode != "" {
			configured = mode
		}
	}
	dualReady := r.kernel.DualVectorPoolsEnabled()
	effective := configured
	if configured == "dual" && !dualReady {
		effective = "single"
	}
	return map[string]any{
		"configured_mode": configured,
		"effective_mode":  effective,
		"ready":           dualReady,
		"single_pool":     r.kernel.VectorStoreSnapshot(r.vectorStore),
		"paragraph_pool":  r.kernel.VectorStoreSnapshot(r.paragraphVectorStore),
		"graph_pool":      r.kernel.VectorStoreSnapshot(r.graphVectorStore),
		"ready_manifest":  r.kernel.DualVectorReadyManifestPath(),
		"auto_migration":  r.migrationStatusSnapshot(),
	}
}

// closeRuntime 持久化并释放存储，对齐 _close_runtime 的收尾段
func (r *Runtime) closeRuntime() error {
	if r.closed {
		return nil
	}
	r.closed = true
	var failures []error
	// 稀疏索引持有独立的 SQLite 连接，必须先卸载，否则 metadata.db 在 Windows 上无法删除
	if index := r.sparseIndex; index != nil {
		index.Unload()
		r.sparseIndex = nil
	}
	// 对齐 Python _persist：只要向量库可能写过数据就必须提交元数据
	// 首次运行的向量库还没有元数据文件时 HasData 为 false，仅按它判断会丢掉整轮新增向量
	saveVectorStore := func(name string, store *storage.VectorStore, dir string) {
		if store == nil || (!store.HasData() && store.NumVectors() == 0) {
			return
		}
		if err := store.Save(dir, nil); err != nil {
			failures = append(failures, fmt.Errorf("保存%s失败: %w", name, err))
		}
	}
	saveVectorStore("主向量库", r.vectorStore, filepath.Join(r.kernel.DataDir(), "vectors"))
	saveVectorStore("段向量库", r.paragraphVectorStore, filepath.Join(r.kernel.DataDir(), "vectors", "paragraph"))
	saveVectorStore("图向量库", r.graphVectorStore, filepath.Join(r.kernel.DataDir(), "vectors", "graph"))
	// 对齐 Python _persist 中 _save_inputs 的位置：向量提交后立即持久化输入映射
	if err := r.kernel.SaveVectorSpaceInputs(); err != nil {
		failures = append(failures, fmt.Errorf("保存向量空间输入映射失败: %w", err))
	}
	if r.graphStore != nil && (r.graphStore.HasData() || r.graphStore.NumNodes() > 0) {
		if err := r.graphStore.Save(filepath.Join(r.kernel.DataDir(), "graph")); err != nil {
			failures = append(failures, fmt.Errorf("保存图存储失败: %w", err))
		}
	}
	if r.metadataStore != nil {
		if err := r.metadataStore.Close(); err != nil {
			failures = append(failures, fmt.Errorf("关闭元数据存储失败: %w", err))
		}
		r.metadataStore = nil
	}
	r.vectorStore = nil
	r.paragraphVectorStore = nil
	r.graphVectorStore = nil
	r.graphStore = nil
	r.embeddingManager = nil
	r.imageRuntime = nil
	r.sparseIndex = nil
	r.sparseEnabled = false
	r.retriever = nil
	r.thresholdFilter = nil
	r.runtimeBundle = memruntime.SearchRuntimeBundle{}
	r.profileMu.Lock()
	r.personProfile = nil
	r.profileMu.Unlock()
	r.stopping.Store(false)

	r.kernel.SetVectorStore(nil)
	r.kernel.SetParagraphVectorStore(nil)
	r.kernel.SetGraphVectorStore(nil)
	r.kernel.SetGraphStore(nil)
	r.kernel.SetMetadataStore(nil)
	r.kernel.SetEmbeddingManager(nil)
	r.kernel.SetImageMemoryRuntime(nil)
	r.kernel.SetSparseIndex(nil)
	r.kernel.SetRetriever(nil)
	r.kernel.SetThresholdFilter(nil)
	r.kernel.SetRuntimeBundle(memruntime.SearchRuntimeBundle{})
	r.kernel.SetRelationWriteService(nil)
	r.kernel.SetDualVectorPoolsReady(false)
	r.kernel.SetInitialized(false)
	for _, channel := range []string{"metadata", "sparse", "graph", "vector_read", "vector_write", "embedding"} {
		_ = r.kernel.SetRuntimeCapability(channel, false)
	}
	r.kernel.SetVectorHealthField("state", "not_initialized")
	r.kernel.SetVectorHealthField("error_code", "")
	r.kernel.SetVectorHealthField("reason", "")
	r.kernel.SetVectorHealthField("trusted_coverage", 0.0)
	if r.writerLock != nil && r.writerLock.Held() {
		if err := r.writerLock.Release(); err != nil {
			failures = append(failures, fmt.Errorf("释放数据目录写者锁失败: %w", err))
		}
	}
	return errors.Join(failures...)
}

// cfgInt 读取整数配置，非法值回退默认
func cfgInt(source map[string]any, key string, defaultValue int) int {
	if source == nil {
		return defaultValue
	}
	value, err := memruntime.PyInt(memruntime.ConfigValue(source, key, defaultValue))
	if err != nil || value < 1 {
		return defaultValue
	}
	return value
}

// cfgString 读取字符串配置
func cfgString(source map[string]any, key string, defaultValue string) string {
	if source == nil {
		return defaultValue
	}
	value := memruntime.PyStr(memruntime.ConfigValue(source, key, defaultValue))
	if strings.TrimSpace(value) == "" {
		return defaultValue
	}
	return value
}

// cfgMap 读取字典配置
func cfgMap(source map[string]any, key string) map[string]any {
	if source == nil {
		return map[string]any{}
	}
	out := memruntime.SafeDict(memruntime.ConfigValue(source, key, nil))
	if out == nil {
		return map[string]any{}
	}
	return out
}

// cfgTruthy 对齐 bool(...) 的真值转换
func cfgTruthy(value any) bool { return memruntime.PyTruthy(value) }

// decodeSparseConfig 把配置字典还原为 SparseBM25Config，未知键忽略并记录降级
func decodeSparseConfig(raw map[string]any) retrieval.SparseBM25Config {
	config := retrieval.NewSparseBM25Config()
	if raw == nil {
		return config
	}
	setBool := func(key string, target *bool) {
		if value, ok := raw[key]; ok {
			*target = memruntime.PyTruthy(value)
		}
	}
	setInt := func(key string, target *int) {
		if value, ok := raw[key]; ok {
			if parsed, err := memruntime.PyInt(value); err == nil {
				*target = parsed
			}
		}
	}
	setText := func(key string, target *string) {
		if value, ok := raw[key]; ok {
			text := strings.TrimSpace(memruntime.PyStr(value))
			if text != "" {
				*target = text
			}
		}
	}
	setBool("enabled", &config.Enabled)
	setText("backend", &config.Backend)
	setBool("lazy_load", &config.LazyLoad)
	setText("mode", &config.Mode)
	setText("tokenizer_mode", &config.TokenizerMode)
	setText("jieba_user_dict", &config.JiebaUserDict)
	setInt("char_ngram_n", &config.CharNgramN)
	setInt("candidate_k", &config.CandidateK)
	setInt("max_doc_len", &config.MaxDocLen)
	setBool("enable_tokenized_shadow_index", &config.EnableTokenizedShadowIndex)
	setBool("enable_ngram_fallback_index", &config.EnableNgramFallbackIndex)
	setBool("enable_like_fallback", &config.EnableLikeFallback)
	setBool("enable_relation_sparse_fallback", &config.EnableRelationSparseFallback)
	setInt("relation_candidate_k", &config.RelationCandidateK)
	setInt("relation_max_doc_len", &config.RelationMaxDocLen)
	setBool("unload_on_disable", &config.UnloadOnDisable)
	setBool("shrink_memory_on_unload", &config.ShrinkMemoryOnUnload)
	config.Normalize()
	return config
}

// ensureContext 返回可用上下文，原生装配全部使用后台上下文
func ensureContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
