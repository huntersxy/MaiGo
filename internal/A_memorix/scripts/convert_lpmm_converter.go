package scripts

// LPMM parquet/graph 转换器主体，对齐 LPMMConverter 的初始化与 run

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"MaiGo/internal/A_memorix/core/embedding"
	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/utils"
)

// LPMMParquetConverter 将 LPMM parquet 与图文件写入 A_memorix 双池
type LPMMParquetConverter struct {
	inputDir  string
	outputDir string

	dimension         int
	batchSize         int
	allowUnsafePickle bool

	embeddingFingerprint map[string]any

	vectorDir          string
	paragraphVectorDir string
	graphVectorDir     string
	graphDir           string
	metadataDir        string

	paragraphVectorStore *storage.VectorStore
	graphVectorStore     *storage.VectorStore
	graphStore           *storage.GraphStore
	metadataStore        *storage.Store

	vectorStats map[string]int
	idMapping   map[string]string

	logger *ScriptLogger
}

// newLPMMParquetConverter 构造转换器，对齐 LPMMConverter.__init__
func newLPMMParquetConverter(inputDir string, outputDir string, dimension int, batchSize int, allowUnsafePickle bool, logger *ScriptLogger) *LPMMParquetConverter {
	if logger == nil {
		logger = NewScriptLogger(lpmmConverterLoggerName)
	}
	if batchSize < 1 {
		batchSize = 1
	}
	vectorDir := filepath.Join(outputDir, "vectors")
	return &LPMMParquetConverter{
		inputDir:           inputDir,
		outputDir:          outputDir,
		dimension:          dimension,
		batchSize:          batchSize,
		allowUnsafePickle:  allowUnsafePickle,
		vectorDir:          vectorDir,
		paragraphVectorDir: filepath.Join(vectorDir, "paragraph"),
		graphVectorDir:     filepath.Join(vectorDir, "graph"),
		graphDir:           filepath.Join(outputDir, "graph"),
		metadataDir:        filepath.Join(outputDir, "metadata"),
		vectorStats:        map[string]int{"paragraph": 0, "entity": 0, "relation": 0},
		idMapping:          map[string]string{},
		logger:             logger,
	}
}

// Run 对齐 LPMMConverter.run
func (c *LPMMParquetConverter) Run(ctx context.Context) error {
	_ = ctx
	if err := c.validateInput(); err != nil {
		return err
	}
	if err := c.initializeStores(); err != nil {
		return err
	}
	defer func() {
		if c.metadataStore != nil {
			_ = c.metadataStore.Close()
			c.metadataStore = nil
		}
	}()

	if err := c.convertVectors(); err != nil {
		return err
	}
	if err := c.convertGraph(); err != nil {
		return err
	}
	if err := c.paragraphVectorStore.Save("", c.embeddingFingerprint); err != nil {
		return err
	}
	if err := c.graphVectorStore.Save("", c.embeddingFingerprint); err != nil {
		return err
	}
	if err := c.graphStore.Save(""); err != nil {
		return err
	}
	if err := c.metadataStore.Close(); err != nil {
		return err
	}
	c.metadataStore = nil
	if err := c.writeDualReadyManifest(); err != nil {
		return err
	}
	c.logger.Info("所有转换成功完成。")
	return nil
}

// validateInput 对齐 _validate_input
func (c *LPMMParquetConverter) validateInput() error {
	return validateLPMMInput(c.inputDir)
}

// initializeStores 对齐 initialize_stores
func (c *LPMMParquetConverter) initializeStores() error {
	c.logger.Infof("正在初始化存储于 %s...", c.outputDir)

	if info, err := os.Stat(c.outputDir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("输出路径不是目录: %s", c.outputDir)
		}
		entries, err := os.ReadDir(c.outputDir)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return fmt.Errorf("输出目录必须为空，已拒绝覆盖: %s", c.outputDir)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(c.outputDir, 0o777); err != nil {
		return err
	}
	if c.embeddingFingerprint == nil {
		fingerprint, err := c.loadEmbeddingFingerprint()
		if err != nil {
			return err
		}
		c.embeddingFingerprint = fingerprint
	}

	vectorStore, err := storage.NewVectorStore(storage.VectorStoreConfig{
		Dimension:        c.dimension,
		QuantizationType: utils.QuantizationInt8,
		DataDir:          c.paragraphVectorDir,
	})
	if err != nil {
		return err
	}
	c.paragraphVectorStore = vectorStore

	vectorStore, err = storage.NewVectorStore(storage.VectorStoreConfig{
		Dimension:        c.dimension,
		QuantizationType: utils.QuantizationInt8,
		DataDir:          c.graphVectorDir,
	})
	if err != nil {
		return err
	}
	c.graphVectorStore = vectorStore

	c.graphStore = storage.NewGraphStore(storage.SparseMatrixFormatCSR, c.graphDir)
	c.metadataStore = storage.NewStore(c.metadataDir, "")
	if err := c.metadataStore.Connect(c.metadataDir, true); err != nil {
		c.metadataStore = nil
		return err
	}
	return nil
}

// loadEmbeddingFingerprint 对齐 _load_embedding_fingerprint
func (c *LPMMParquetConverter) loadEmbeddingFingerprint() (map[string]any, error) {
	configPath := DefaultConfigPath()
	if !pathExists(configPath) {
		return nil, fmt.Errorf("A_Memorix 配置不存在，无法确定 Embedding 指纹: %s", configPath)
	}
	config, err := LoadTOML(configPath)
	if err != nil {
		return nil, err
	}
	retryConfig, _ := NestedGet(config, "embedding.retry", map[string]any{}).(map[string]any)
	adapter := embedding.CreateEmbeddingAPIAdapter(&embedding.EmbeddingAPIAdapterOptions{
		BatchSize:            utils.PyIntOrZero(NestedGet(config, "embedding.batch_size", 32)),
		MaxConcurrent:        utils.PyIntOrZero(NestedGet(config, "embedding.max_concurrent", 5)),
		DefaultDimension:     c.dimension,
		ModelName:            utils.PyStr(NestedGet(config, "embedding.model_name", "auto")),
		DimensionRequestMode: utils.PyStr(NestedGet(config, "embedding.dimension_request_mode", "explicit")),
		RetryConfig:          retryConfig,
	})
	dimension := c.dimension
	fingerprint, err := adapter.GetEmbeddingFingerprint(&embedding.EmbeddingFingerprintOptions{Dimension: &dimension})
	if err != nil {
		return nil, err
	}
	if fingerprint == nil || strings.TrimSpace(utils.PyStr(fingerprint["hash"])) == "" {
		return nil, fmt.Errorf("无法确定当前 Embedding 指纹，拒绝生成不可加载的双池")
	}
	return fingerprint, nil
}
