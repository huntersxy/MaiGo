package scripts

// 脚本导入器，对齐 src/A_memorix/scripts/process_knowledge.py 的 AutoImporter
// 本文件先覆盖 JSON 载荷导入路径（import_lpmm_json 复用）；文本文件导入在 process_knowledge 补全

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"maibot-go/internal/A_memorix/core/embedding"
	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
	"maibot-go/internal/A_memorix/core/write"
)

// AutoImporterOptions 对齐 AutoImporter.__init__ 的关键字参数
type AutoImporterOptions struct {
	// Force 强制重新导入，文本导入路径使用
	Force bool
	// ClearManifest 启动时清空导入清单
	ClearManifest bool
	// TargetType 目标导入策略，默认 auto
	TargetType string
	// Concurrency 并发数
	Concurrency int
	// ChatLog 聊天记录导入模式
	ChatLog bool
	// ChatReferenceTime 聊天记录相对时间参考点
	ChatReferenceTime string
	// DataDir 数据目录，空时回退默认数据目录
	DataDir string
	// Logger 日志器，空时静默
	Logger utils.Logger
}

// AutoImporter 脚本导入器
type AutoImporter struct {
	logger utils.Logger

	vectorStore          *storage.VectorStore
	graphStore           *storage.GraphStore
	metadataStore        *storage.Store
	embeddingManager     *embedding.EmbeddingAPIAdapter
	relationWriteService *write.RelationWriteService
	pluginConfig         map[string]any

	manifest map[string]any

	dataDir      string
	importDir    string
	rawDir       string
	processedDir string
	manifestPath string

	force             bool
	clearManifest     bool
	chatLog           bool
	targetType        string
	chatReferenceTime string
	concurrencyLimit  int

	storageLock *sync.Mutex
}

// NewAutoImporter 构造导入器，对齐 AutoImporter.__init__
func NewAutoImporter(options AutoImporterOptions) (*AutoImporter, error) {
	strategy, err := storage.ParseImportStrategyOrDefault(options.TargetType, storage.ImportAuto)
	if err != nil {
		return nil, err
	}
	logger := utils.OrLogger(options.Logger)
	if options.ChatLog && strategy != storage.ImportAuto && strategy != storage.ImportNarrative {
		logger.Warning(fmt.Sprintf("chat_log 模式已启用，target_type=%s 将被覆盖为 narrative", options.TargetType))
	}
	targetType := string(strategy)
	if options.ChatLog {
		targetType = string(storage.ImportNarrative)
	}
	dataDir := ResolveRepoPath(options.DataDir, DefaultDataDir())
	importDir := filepath.Join(dataDir, "imports")
	return &AutoImporter{
		logger:            logger,
		manifest:          map[string]any{},
		dataDir:           dataDir,
		importDir:         importDir,
		rawDir:            filepath.Join(importDir, "source", "raw"),
		processedDir:      filepath.Join(importDir, "processed"),
		manifestPath:      filepath.Join(importDir, "manifest.json"),
		force:             options.Force,
		clearManifest:     options.ClearManifest,
		chatLog:           options.ChatLog,
		targetType:        targetType,
		chatReferenceTime: strings.TrimSpace(options.ChatReferenceTime),
		concurrencyLimit:  options.Concurrency,
	}, nil
}

// DataDir 返回解析后的数据目录
func (a *AutoImporter) DataDir() string { return a.dataDir }

// ManifestPath 返回导入清单路径
func (a *AutoImporter) ManifestPath() string { return a.manifestPath }

// TargetType 返回生效的导入策略
func (a *AutoImporter) TargetType() string { return a.targetType }

// Manifest 返回导入清单快照
func (a *AutoImporter) Manifest() map[string]any {
	copied := make(map[string]any, len(a.manifest))
	for key, value := range a.manifest {
		copied[key] = value
	}
	return copied
}

// Initialize 初始化目录、清单与存储，对齐 AutoImporter.initialize
func (a *AutoImporter) Initialize(ctx context.Context) bool {
	a.logger.Info(fmt.Sprintf("正在初始化... (并发数: %d)", a.concurrencyLimit))
	a.storageLock = &sync.Mutex{}

	if err := os.MkdirAll(a.rawDir, 0o777); err != nil {
		a.logger.Error(fmt.Sprintf("创建原始导入目录失败: %v", err))
		return false
	}
	if err := os.MkdirAll(a.processedDir, 0o777); err != nil {
		a.logger.Error(fmt.Sprintf("创建已处理目录失败: %v", err))
		return false
	}

	legacyManifestPath := filepath.Join(a.dataDir, "import_manifest.json")
	if pathIsFile(legacyManifestPath) && !pathExists(a.manifestPath) {
		if err := os.Rename(legacyManifestPath, a.manifestPath); err != nil {
			a.logger.Warning(fmt.Sprintf("旧导入清单迁移失败，保留原文件继续执行: %s, error=%v", legacyManifestPath, err))
		} else {
			a.logger.Info(fmt.Sprintf("已迁移旧导入清单: %s -> %s", legacyManifestPath, a.manifestPath))
		}
	}

	if a.clearManifest {
		a.logger.Info("🧹 清理 Mainfest")
		a.manifest = map[string]any{}
		if err := a.saveManifest(); err != nil {
			a.logger.Error(fmt.Sprintf("清理导入清单失败: %v", err))
			return false
		}
	} else if pathExists(a.manifestPath) {
		a.manifest = map[string]any{}
		if raw, err := os.ReadFile(a.manifestPath); err == nil {
			loaded := map[string]any{}
			if UnmarshalJSON(raw, &loaded) == nil {
				a.manifest = loaded
			}
		}
	}

	config, err := LoadTOML(DefaultConfigPath())
	if err != nil {
		a.logger.Error(fmt.Sprintf("加载 A_Memorix 配置失败: %v", err))
		return false
	}
	a.pluginConfig = config

	if err := a.initStores(ctx); err != nil {
		a.logger.Error(fmt.Sprintf("初始化存储失败: %v", err))
		return false
	}
	return true
}

// initStores 使用与主运行时一致的存储初始化流程，对齐 _init_stores
func (a *AutoImporter) initStores(ctx context.Context) error {
	retryConfig, _ := NestedGet(a.pluginConfig, "embedding.retry", map[string]any{}).(map[string]any)
	a.embeddingManager = embedding.CreateEmbeddingAPIAdapter(&embedding.EmbeddingAPIAdapterOptions{
		BatchSize:            utils.PyIntOrZero(NestedGet(a.pluginConfig, "embedding.batch_size", 32)),
		DefaultDimension:     utils.PyIntOrZero(NestedGet(a.pluginConfig, "embedding.dimension", 384)),
		ModelName:            utils.PyStr(NestedGet(a.pluginConfig, "embedding.model_name", "auto")),
		DimensionRequestMode: utils.PyStr(NestedGet(a.pluginConfig, "embedding.dimension_request_mode", "explicit")),
		RetryConfig:          retryConfig,
	})

	dimension, err := a.embeddingManager.DetectDimension(ctx)
	if err != nil {
		dimension = a.embeddingManager.GetRequestedDimension()
	}

	quantization := strings.ToLower(strings.TrimSpace(utils.PyStr(NestedGet(a.pluginConfig, "embedding.quantization_type", "int8"))))
	if quantization == "" || quantization == "none" {
		quantization = "int8"
	}
	if quantization != "int8" {
		return errors.New("embedding.quantization_type 在 vNext 仅允许 int8(SQ8)。 请先执行 scripts/release_vnext_migrate.py migrate。")
	}

	a.vectorStore, err = storage.NewVectorStore(storage.VectorStoreConfig{
		Dimension:        dimension,
		QuantizationType: utils.QuantizationInt8,
		DataDir:          filepath.Join(a.dataDir, "vectors"),
	})
	if err != nil {
		return err
	}

	matrixFormat := storage.SparseMatrixFormatCSR
	if utils.PyStr(NestedGet(a.pluginConfig, "graph.sparse_matrix_format", "csr")) == "csc" {
		matrixFormat = storage.SparseMatrixFormatCSC
	}
	a.graphStore = storage.NewGraphStore(matrixFormat, filepath.Join(a.dataDir, "graph"))

	metadataDir := filepath.Join(a.dataDir, "metadata")
	a.metadataStore = storage.NewStore(metadataDir, "")
	if err := a.metadataStore.Connect(metadataDir, true); err != nil {
		return err
	}

	a.relationWriteService = write.NewRelationWriteService(
		a.metadataStore,
		a.graphStore,
		a.vectorStore,
		relationEmbedder{adapter: a.embeddingManager},
		write.RelationWriteConfig{},
	)

	if a.vectorStore.HasData() {
		if err := a.vectorStore.Load("", nil, nil, ""); err != nil {
			return err
		}
	}
	if a.graphStore.HasData() {
		if err := a.graphStore.Load(""); err != nil {
			return err
		}
	}
	return nil
}

// ShouldWriteRelationVectors 对齐 _should_write_relation_vectors
func (a *AutoImporter) ShouldWriteRelationVectors() bool {
	retrievalConfig, ok := a.pluginConfig["retrieval"].(map[string]any)
	if !ok {
		return false
	}
	relationConfig, ok := retrievalConfig["relation_vectorization"].(map[string]any)
	if !ok {
		return false
	}
	return isTruthy(relationConfig["enabled"]) && ConfigBool(relationConfig, "write_on_import", true)
}

// GetFileHash 对齐 get_file_hash：md5 十六进制
func (a *AutoImporter) GetFileHash(content string) string {
	sum := md5.Sum([]byte(content))
	return hex.EncodeToString(sum[:])
}

// ImportJSONData 导入已转换的 JSON 载荷，对齐 import_json_data
func (a *AutoImporter) ImportJSONData(ctx context.Context, data map[string]any, filename string, progress func(int)) error {
	if a.storageLock == nil {
		return errors.New("Importer is not initialized. Call initialize() first.")
	}
	a.storageLock.Lock()
	defer a.storageLock.Unlock()

	if err := a.importToDB(ctx, data, progress); err != nil {
		return err
	}

	encoded, err := utils.PyJSONMarshal(data)
	if err != nil {
		return err
	}
	if a.manifest == nil {
		a.manifest = map[string]any{}
	}
	a.manifest[filename] = map[string]any{
		"hash":      a.GetFileHash(encoded),
		"timestamp": float64(time.Now().UnixNano()) / 1e9,
		"imported":  true,
	}
	if err := a.saveManifest(); err != nil {
		return err
	}
	if err := a.vectorStore.Save("", nil); err != nil {
		return err
	}
	return a.graphStore.Save("")
}

// importToDB 把载荷写入元数据/图/向量，对齐 _import_to_db
func (a *AutoImporter) importToDB(ctx context.Context, data map[string]any, progress func(int)) error {
	warningCount := 0
	warn := func(message string) {
		warningCount++
		a.logger.Warning(message)
	}
	advance := func() {
		if progress != nil {
			progress(1)
		}
	}

	err := a.graphStore.BatchUpdate(func() error {
		paragraphItems := anySlice(data["paragraphs"])
		for paragraphIndex, item := range paragraphItems {
			paragraph, normalizeErr := utils.NormalizeParagraphImportItem(item, "script", resolveScriptKnowledgeType)
			if normalizeErr != nil {
				warn(fmt.Sprintf("脚本导入跳过段落[%d]：%v (code=%s)", paragraphIndex, normalizeErr, importPayloadErrorCode(normalizeErr)))
				advance()
				continue
			}
			if utils.IsProbableHashToken(paragraph.Content) {
				warn(fmt.Sprintf("脚本导入跳过段落[%d]：内容疑似哈希值", paragraphIndex))
				advance()
				continue
			}

			hashValue, err := a.metadataStore.AddParagraph(
				paragraph.Content,
				nil,
				paragraph.Source,
				nil,
				paragraph.KnowledgeType,
				paragraph.TimeMeta,
			)
			if err != nil {
				return err
			}

			if !a.vectorStore.Contains(hashValue) {
				encoded, encodeErr := a.embeddingManager.EncodeOne(ctx, paragraph.Content, nil)
				if encodeErr != nil {
					a.logger.Error(fmt.Sprintf("  Vector fail: %v", encodeErr))
				} else if _, addErr := a.vectorStore.Add([][]float32{encoded}, []string{hashValue}); addErr != nil {
					a.logger.Error(fmt.Sprintf("  Vector fail: %v", addErr))
				}
			}

			for _, entity := range paragraph.Entities {
				entityName, ok := utils.NormalizeEntityImportItem(entity)
				if !ok {
					warn(fmt.Sprintf("脚本导入跳过段落[%d]中的实体：无效名称或疑似哈希值", paragraphIndex))
					continue
				}
				if _, err := a.addEntityWithVector(ctx, entityName, hashValue); err != nil {
					return err
				}
			}

			for _, relation := range paragraph.Relations {
				normalized, ok := utils.NormalizeRelationImportItem(relation)
				if !ok {
					warn(fmt.Sprintf("脚本导入跳过段落[%d]中的关系：字段无效或疑似哈希值", paragraphIndex))
					continue
				}
				if err := a.writeRelation(ctx, normalized, 1.0, map[string]any{}, hashValue); err != nil {
					return err
				}
			}
			advance()
		}

		for entityIndex, item := range anySlice(data["entities"]) {
			entityName, ok := utils.NormalizeEntityImportItem(item)
			if !ok {
				warn(fmt.Sprintf("脚本导入跳过顶层实体[%d]：无效名称或疑似哈希值", entityIndex))
				continue
			}
			if _, err := a.addEntityWithVector(ctx, entityName, ""); err != nil {
				return err
			}
		}

		for relationIndex, rawRelation := range anySlice(data["relations"]) {
			normalized, ok := utils.NormalizeRelationImportItem(rawRelation)
			if !ok {
				warn(fmt.Sprintf("脚本导入跳过顶层关系[%d]：字段无效或疑似哈希值", relationIndex))
				continue
			}
			confidence, metadata := relationWriteOptions(rawRelation)
			if err := a.writeRelation(ctx, normalized, confidence, metadata, ""); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if warningCount > 0 {
		a.logger.Warning(fmt.Sprintf("脚本导入完成，跳过异常项 %d 条", warningCount))
	}
	return nil
}

// writeRelation 写入一条关系，对齐 _import_to_db 的关系分支
func (a *AutoImporter) writeRelation(ctx context.Context, relation map[string]string, confidence float64, metadata map[string]any, sourceParagraph string) error {
	subject := relation["subject"]
	predicate := relation["predicate"]
	object := relation["object"]

	if _, err := a.addEntityWithVector(ctx, subject, sourceParagraph); err != nil {
		return err
	}
	if _, err := a.addEntityWithVector(ctx, object, sourceParagraph); err != nil {
		return err
	}

	writeVector := a.ShouldWriteRelationVectors()
	if a.relationWriteService != nil {
		_, err := a.relationWriteService.UpsertRelationWithVector(
			ctx, subject, predicate, object, confidence, sourceParagraph, metadata, writeVector,
		)
		return err
	}
	relationHash, err := a.metadataStore.AddRelation(subject, predicate, object, nil, confidence, sourceParagraph, metadata)
	if err != nil {
		return err
	}
	_, err = a.graphStore.AddEdges([]storage.Edge{{Source: subject, Target: object}}, nil, []string{relationHash})
	return err
}

// addEntityWithVector 写入实体及其向量，对齐 _add_entity_with_vector
func (a *AutoImporter) addEntityWithVector(ctx context.Context, name string, sourceParagraph string) (string, error) {
	entityName := strings.TrimSpace(name)
	if entityName == "" {
		return "", nil
	}
	if utils.IsProbableHashToken(entityName) {
		a.logger.Warning(fmt.Sprintf("脚本导入跳过疑似哈希实体: %s", truncateText(entityName, 32)))
		return "", nil
	}

	hashValue, err := a.metadataStore.AddEntity(entityName, nil, sourceParagraph, nil)
	if err != nil {
		return "", err
	}
	a.graphStore.AddNodes([]string{entityName}, nil)

	encoded, encodeErr := a.embeddingManager.EncodeOne(ctx, entityName, nil)
	if encodeErr != nil {
		a.logger.Warning(fmt.Sprintf("实体向量写入失败: entity=%s, hash=%s, error=%v", entityName, hashValue, encodeErr))
		return hashValue, nil
	}
	if _, addErr := a.vectorStore.Add([][]float32{encoded}, []string{hashValue}); addErr != nil {
		a.logger.Warning(fmt.Sprintf("实体向量写入失败: entity=%s, hash=%s, error=%v", entityName, hashValue, addErr))
	}
	return hashValue, nil
}

// Close 关闭元数据存储，对齐 close
func (a *AutoImporter) Close() error {
	if a.metadataStore == nil {
		return nil
	}
	return a.metadataStore.Close()
}

// saveManifest 落盘导入清单，对齐 _save_manifest
func (a *AutoImporter) saveManifest() error {
	if err := os.MkdirAll(a.importDir, 0o777); err != nil {
		return err
	}
	text, err := MarshalJSONIndent(a.manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(a.manifestPath, []byte(text), 0o666)
}

// resolveScriptKnowledgeType 对齐 normalize_paragraph_import_item 的默认解析器
func resolveScriptKnowledgeType(value any, content string) (string, error) {
	knowledgeType, err := storage.ResolveStoredKnowledgeType(value, content, false, nil)
	if err != nil {
		return "", err
	}
	return string(knowledgeType), nil
}

// importPayloadErrorCode 读取导入校验异常的错误码
func importPayloadErrorCode(err error) string {
	var payload *utils.ImportPayloadValidationError
	if errors.As(err, &payload) {
		return payload.Code
	}
	return "unknown"
}

// anySlice 把 JSON 数组归一为 []any
func anySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

// relationWriteOptions 读取顶层关系的置信度与元数据
func relationWriteOptions(rawRelation any) (float64, map[string]any) {
	confidence := 1.0
	metadata := map[string]any{}
	table, ok := rawRelation.(map[string]any)
	if !ok {
		return confidence, metadata
	}
	if value, exists := table["confidence"]; exists && value != nil {
		confidence = utils.PyFloatOrZero(value)
	}
	if value, ok := table["metadata"].(map[string]any); ok {
		metadata = value
	}
	return confidence, metadata
}

// pathIsFile 判断路径是否为普通文件
func pathIsFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// pathExists 判断路径是否存在
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// relationEmbedder 把嵌入适配器适配成 write.TextEmbedder
type relationEmbedder struct {
	adapter *embedding.EmbeddingAPIAdapter
}

// Encode 批量编码关系文本
func (e relationEmbedder) Encode(ctx context.Context, texts []string, batchSize int, showProgress bool, normalize bool) ([][]float32, error) {
	return e.adapter.Encode(ctx, texts, encodeOptionsForBatch(batchSize))
}

// EncodeOne 单条编码关系文本
func (e relationEmbedder) EncodeOne(ctx context.Context, text string, batchSize int, showProgress bool, normalize bool) ([]float32, error) {
	return e.adapter.EncodeOne(ctx, text, encodeOptionsForBatch(batchSize))
}

// encodeOptionsForBatch 构造编码选项
func encodeOptionsForBatch(batchSize int) *embedding.EncodeOptions {
	options := &embedding.EncodeOptions{}
	if batchSize > 0 {
		options.BatchSize = batchSize
	}
	return options
}
