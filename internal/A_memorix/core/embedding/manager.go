package embedding

// 嵌入管理器，对齐 A_memorix/core/embedding/manager.py
// 负责模型加载、缓存与批量生成；本地模型加载通过 ModelLoader 接口注入

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
)

var managerLogger = zap.L().Named("A_Memorix.EmbeddingManager")

// ErrLocalModelUnsupported Go 侧没有 sentence-transformers 对等实现
// Python 在导入失败时抛 ImportError("sentence-transformers 未安装，请安装: pip install sentence-transformers")
var ErrLocalModelUnsupported = errors.New(
	"sentence-transformers 未安装，请安装: pip install sentence-transformers；Go 侧不支持本地模型加载，请改用 EmbeddingAPIAdapter 或通过 ModelLoader 注入")

// TextEncoder 本地嵌入模型的编码能力，对齐 SentenceTransformer.encode 被实际使用的参数
type TextEncoder interface {
	Encode(ctx context.Context, texts []string, batchSize int, showProgress bool, normalize bool) ([][]float32, error)
}

// ModelLoader 加载本地嵌入模型；Go 无法对等实现，需宿主注入
// 对齐 SentenceTransformer(model_path, cache_folder=config.cache_dir)
type ModelLoader interface {
	LoadModel(ctx context.Context, config EmbeddingModelConfig) (TextEncoder, error)
}

// EmbeddingManager 嵌入管理器，对应 Python EmbeddingManager
type EmbeddingManager struct {
	config      EmbeddingModelConfig
	cacheDir    string
	enableCache bool
	numWorkers  int

	loader ModelLoader

	modelMu sync.RWMutex
	model   TextEncoder

	cacheMu        sync.Mutex
	embeddingCache map[string][]float32
	cacheHits      int
	cacheMisses    int

	totalEncoded atomic.Int64
}

// NewEmbeddingManager 构造管理器；未注入 ModelLoader 时返回 ErrLocalModelUnsupported
// 对齐 Python 在 sentence-transformers 缺失时构造即抛 ImportError 的行为
func NewEmbeddingManager(config EmbeddingModelConfig, cacheDir string, enableCache bool, numWorkers int) (*EmbeddingManager, error) {
	return NewEmbeddingManagerWithLoader(config, cacheDir, enableCache, numWorkers, nil)
}

// NewEmbeddingManagerWithLoader 构造管理器并注入本地模型加载器
func NewEmbeddingManagerWithLoader(
	config EmbeddingModelConfig,
	cacheDir string,
	enableCache bool,
	numWorkers int,
	loader ModelLoader,
) (*EmbeddingManager, error) {
	if loader == nil {
		return nil, ErrLocalModelUnsupported
	}
	manager := &EmbeddingManager{
		config:         config,
		cacheDir:       cacheDir,
		enableCache:    enableCache,
		numWorkers:     numWorkers,
		loader:         loader,
		embeddingCache: make(map[string][]float32),
	}
	if manager.numWorkers < 1 {
		manager.numWorkers = 1
	}
	managerLogger.Info(fmt.Sprintf(
		"EmbeddingManager 初始化: model=%s, dim=%d, workers=%d",
		config.ModelName,
		config.Dimension,
		numWorkers,
	))
	return manager, nil
}

// Config 返回模型配置副本
func (m *EmbeddingManager) Config() EmbeddingModelConfig {
	return m.config
}

// CacheDir 返回缓存目录，空串表示未指定
func (m *EmbeddingManager) CacheDir() string {
	return m.cacheDir
}

// EnableCache 是否启用内存缓存
func (m *EmbeddingManager) EnableCache() bool {
	return m.enableCache
}

// NumWorkers 返回工作线程数
func (m *EmbeddingManager) NumWorkers() int {
	return m.numWorkers
}

// LoadModel 懒加载模型，对齐 load_model 的双重检查
func (m *EmbeddingManager) LoadModel(ctx context.Context) error {
	if m.IsModelLoaded() {
		return nil
	}
	m.modelMu.Lock()
	defer m.modelMu.Unlock()
	if m.model != nil {
		return nil
	}
	managerLogger.Info(fmt.Sprintf("正在加载模型: %s", m.config.ModelName))
	model, err := m.loader.LoadModel(ctx, m.config)
	if err != nil {
		managerLogger.Error(fmt.Sprintf("模型加载失败: %v", err))
		return err
	}
	m.model = model
	managerLogger.Info(fmt.Sprintf("模型加载成功: %s", m.config.ModelName))
	return nil
}

// IsModelLoaded 模型是否已加载
func (m *EmbeddingManager) IsModelLoaded() bool {
	m.modelMu.RLock()
	defer m.modelMu.RUnlock()
	return m.model != nil
}

// ensureModel 返回已加载的编码器，必要时先加载
func (m *EmbeddingManager) ensureModel(ctx context.Context) (TextEncoder, error) {
	if err := m.LoadModel(ctx); err != nil {
		return nil, err
	}
	m.modelMu.RLock()
	defer m.modelMu.RUnlock()
	return m.model, nil
}

// Encode 生成文本嵌入，对齐 EmbeddingManager.encode
// 单条文本请用 EncodeOne，对应 Python 传 str 时返回 1D 数组
func (m *EmbeddingManager) Encode(
	ctx context.Context,
	texts []string,
	batchSize int,
	showProgress bool,
	normalize bool,
) ([][]float32, error) {
	encoder, err := m.ensureModel(ctx)
	if err != nil {
		return nil, err
	}
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	if batchSize <= 0 {
		batchSize = m.config.BatchSize
	}
	// 对齐 normalize_embeddings=normalize and config.normalization
	embeddings, err := encoder.Encode(ctx, texts, batchSize, showProgress, normalize && m.config.Normalization)
	if err != nil {
		managerLogger.Error(fmt.Sprintf("生成嵌入失败: %v", err))
		return nil, err
	}
	if len(embeddings) != len(texts) {
		return nil, fmt.Errorf("编码器返回 %d 条向量，与输入 %d 条文本不一致", len(embeddings), len(texts))
	}
	m.totalEncoded.Add(int64(len(texts)))
	return embeddings, nil
}

// EncodeOne 生成单条文本嵌入，对齐 Python 单字符串输入返回 1D 数组的分支
func (m *EmbeddingManager) EncodeOne(
	ctx context.Context,
	text string,
	batchSize int,
	showProgress bool,
	normalize bool,
) ([]float32, error) {
	embeddings, err := m.Encode(ctx, []string{text}, batchSize, showProgress, normalize)
	if err != nil {
		return nil, err
	}
	return embeddings[0], nil
}

// EncodeBatch 批量生成嵌入（多线程优化），对齐 encode_batch
// numWorkers <= 0 表示沿用初始化时的 num_workers
func (m *EmbeddingManager) EncodeBatch(
	ctx context.Context,
	texts []string,
	batchSize int,
	numWorkers int,
	showProgress bool,
) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	workers := numWorkers
	if workers <= 0 {
		workers = m.numWorkers
	}
	if workers == 1 {
		return m.Encode(ctx, texts, batchSize, showProgress, true)
	}
	managerLogger.Info(fmt.Sprintf("使用 %d 个线程生成 %d 个嵌入", workers, len(texts)))
	if batchSize <= 0 {
		batchSize = m.config.BatchSize
	}
	batches := splitTextBatches(texts, batchSize)
	type batchResult struct {
		embeddings [][]float32
		err        error
	}
	results := make([]batchResult, len(batches))
	sem := make(chan struct{}, workers)
	var waitGroup sync.WaitGroup
	for index, batch := range batches {
		waitGroup.Add(1)
		sem <- struct{}{}
		go func(index int, batch []string) {
			defer func() {
				<-sem
				waitGroup.Done()
			}()
			embeddings, err := m.Encode(ctx, batch, batchSize, false, true)
			results[index] = batchResult{embeddings: embeddings, err: err}
		}(index, batch)
	}
	waitGroup.Wait()

	total := 0
	for index := range results {
		if results[index].err != nil {
			managerLogger.Error(fmt.Sprintf("批次 %d 生成嵌入失败: %v", index, results[index].err))
			return nil, results[index].err
		}
		total += len(results[index].embeddings)
	}
	merged := make([][]float32, 0, total)
	for index := range results {
		merged = append(merged, results[index].embeddings...)
	}
	return merged, nil
}

// EncodeWithCache 生成嵌入（带进程内缓存），对齐 encode_with_cache
func (m *EmbeddingManager) EncodeWithCache(
	ctx context.Context,
	texts []string,
	batchSize int,
	showProgress bool,
) ([][]float32, error) {
	if !m.enableCache {
		return m.Encode(ctx, texts, batchSize, showProgress, true)
	}
	type indexedEmbedding struct {
		index     int
		embedding []float32
	}
	cached := make([]indexedEmbedding, 0, len(texts))
	uncachedTexts := make([]string, 0, len(texts))
	uncachedIndices := make([]int, 0, len(texts))
	for index, text := range texts {
		cacheKey := m.cacheKey(text)
		m.cacheMu.Lock()
		embedding, ok := m.embeddingCache[cacheKey]
		if ok {
			m.cacheHits++
		} else {
			m.cacheMisses++
		}
		m.cacheMu.Unlock()
		if ok {
			cached = append(cached, indexedEmbedding{index: index, embedding: cloneVector(embedding)})
			continue
		}
		uncachedTexts = append(uncachedTexts, text)
		uncachedIndices = append(uncachedIndices, index)
	}

	if len(uncachedTexts) > 0 {
		newEmbeddings, err := m.Encode(ctx, uncachedTexts, batchSize, showProgress, true)
		if err != nil {
			return nil, err
		}
		m.cacheMu.Lock()
		for index, text := range uncachedTexts {
			m.embeddingCache[m.cacheKey(text)] = cloneVector(newEmbeddings[index])
		}
		m.cacheMu.Unlock()
		for index, absoluteIndex := range uncachedIndices {
			cached = append(cached, indexedEmbedding{index: absoluteIndex, embedding: cloneVector(newEmbeddings[index])})
		}
	}

	sort.Slice(cached, func(i, j int) bool { return cached[i].index < cached[j].index })
	finalEmbeddings := make([][]float32, len(cached))
	for index := range cached {
		finalEmbeddings[index] = cached[index].embedding
	}
	return finalEmbeddings, nil
}

// ClearCache 清空缓存，对齐 clear_cache
func (m *EmbeddingManager) ClearCache() {
	m.cacheMu.Lock()
	count := len(m.embeddingCache)
	m.embeddingCache = make(map[string][]float32)
	m.cacheMu.Unlock()
	managerLogger.Info(fmt.Sprintf("已清空缓存: %d 条", count))
}

// CacheHitRate 缓存命中率，对齐 cache_hit_rate 属性
func (m *EmbeddingManager) CacheHitRate() float64 {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	total := m.cacheHits + m.cacheMisses
	if total == 0 {
		return 0.0
	}
	return float64(m.cacheHits) / float64(total)
}

// CacheSize 当前缓存条数
func (m *EmbeddingManager) CacheSize() int {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	return len(m.embeddingCache)
}

// GetModelInfo 获取模型信息，对齐 get_model_info
func (m *EmbeddingManager) GetModelInfo() map[string]any {
	m.cacheMu.Lock()
	cacheSize := len(m.embeddingCache)
	cacheHits := m.cacheHits
	cacheMisses := m.cacheMisses
	m.cacheMu.Unlock()
	return map[string]any{
		"model_name":     m.config.ModelName,
		"dimension":      m.config.Dimension,
		"max_seq_length": m.config.MaxSeqLength,
		"batch_size":     m.config.BatchSize,
		"normalization":  m.config.Normalization,
		"pooling":        m.config.Pooling,
		"model_loaded":   m.IsModelLoaded(),
		"cache_enabled":  m.enableCache,
		"cache_size":     cacheSize,
		"total_encoded":  int(m.totalEncoded.Load()),
		"cache_hits":     cacheHits,
		"cache_misses":   cacheMisses,
	}
}

// GetEmbeddingDimension 获取嵌入维度，对齐 get_embedding_dimension
func (m *EmbeddingManager) GetEmbeddingDimension() int {
	return m.config.Dimension
}

// cacheKey 生成缓存键（SHA256），对齐 _get_cache_key
func (m *EmbeddingManager) cacheKey(text string) string {
	return sha256Hex([]byte(text))
}

// String 对齐 __repr__
func (m *EmbeddingManager) String() string {
	return fmt.Sprintf(
		"EmbeddingManager(model=%s, dim=%d, loaded=%t, cache=%d)",
		m.config.ModelName,
		m.config.Dimension,
		m.IsModelLoaded(),
		m.CacheSize(),
	)
}

// CreateEmbeddingManagerFromConfig 从自定义配置创建管理器，对齐 create_embedding_manager_from_config
// 未注入加载器时与 Python 缺少 sentence-transformers 一致，直接返回 ErrLocalModelUnsupported
func CreateEmbeddingManagerFromConfig(
	modelName string,
	modelPath string,
	dimension int,
	cacheDir string,
	enableCache bool,
	numWorkers int,
	opt *EmbeddingModelConfigOptions,
) (*EmbeddingManager, error) {
	config := GetCustomConfig(modelName, modelPath, dimension, cacheDir, opt)
	return NewEmbeddingManager(config, cacheDir, enableCache, numWorkers)
}

// CreateEmbeddingManagerFromConfigWithLoader 从自定义配置创建管理器并注入加载器
func CreateEmbeddingManagerFromConfigWithLoader(
	modelName string,
	modelPath string,
	dimension int,
	cacheDir string,
	enableCache bool,
	numWorkers int,
	opt *EmbeddingModelConfigOptions,
	loader ModelLoader,
) (*EmbeddingManager, error) {
	config := GetCustomConfig(modelName, modelPath, dimension, cacheDir, opt)
	return NewEmbeddingManagerWithLoader(config, cacheDir, enableCache, numWorkers, loader)
}

// splitTextBatches 按批次大小切分文本，对齐列表推导式切片
func splitTextBatches(texts []string, batchSize int) [][]string {
	if batchSize <= 0 {
		batchSize = 1
	}
	batches := make([][]string, 0, (len(texts)+batchSize-1)/batchSize)
	for start := 0; start < len(texts); start += batchSize {
		end := start + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batches = append(batches, texts[start:end])
	}
	return batches
}
