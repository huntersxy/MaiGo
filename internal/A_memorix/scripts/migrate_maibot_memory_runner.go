package scripts

// migrate_maibot_memory 的目标库装配、窗口提交、向量补齐与严格校验

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"maibot-go/internal/A_memorix/core/embedding"
	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
	"maibot-go/internal/A_memorix/core/write"
)

// migrateRunner 对齐 MigrationRunner
type migrateRunner struct {
	options *migrateOptions
	logger  *ScriptLogger
	stdout  io.Writer

	sourceDBPath string
	targetDir    string
	stateFile    string
	badRowsFile  string
	reportFile   string

	source    *migrateSourceDB
	selection *migrateSelection

	vectorStore   *storage.VectorStore
	graphStore    *storage.GraphStore
	metadataStore *storage.Store
	embedManager  *embedding.EmbeddingAPIAdapter
	relationWrite *write.RelationWriteService

	pluginConfig map[string]any
	embedWorkers int
	dimension    int
	filterFP     string
	sourceFP     map[string]any
	sourceFPHash string
	state        map[string]any
	stats        map[string]any
	startedAt    time.Time
	failed       bool
	failReason   string
}

// newMigrateRunner 构造迁移执行器
func newMigrateRunner(options *migrateOptions, logger *ScriptLogger, stdout io.Writer) *migrateRunner {
	if stdout == nil {
		stdout = os.Stdout
	}
	return &migrateRunner{
		options:      options,
		logger:       logger,
		stdout:       stdout,
		sourceDBPath: ResolveRepoPath(options.sourceDB, DefaultDBPath()),
		targetDir:    ResolveRepoPath(options.targetDataDir, DefaultDataDir()),
		startedAt:    time.Now(),
		stats: map[string]any{
			"source_matched_total": 0, "scanned_rows": 0, "valid_rows": 0, "migrated_rows": 0,
			"skipped_existing_rows": 0, "bad_rows": 0, "coerced_list_fields": 0,
			"paragraph_vectors_added": 0, "entity_vectors_added": 0, "relations_written": 0,
			"relation_vectors_written": 0, "relation_vectors_failed": 0, "relation_vectors_skipped": 0,
			"graph_edges_written": 0, "windows_committed": 0, "last_committed_id": 0,
			"verify_sample_size": 0, "verify_paragraph_missing": 0, "verify_vector_missing": 0,
			"verify_relation_missing": 0, "verify_edge_missing": 0, "verify_bad_rows": 0,
			"verify_passed": false,
		},
	}
}

// run 对齐 MigrationRunner.run
func (r *migrateRunner) run(ctx context.Context) int {
	defer r.close()
	r.stateFile = filepath.Join(r.targetDir, migrationStateDirName, migrationStateFileName)
	r.badRowsFile = filepath.Join(r.targetDir, migrationStateDirName, migrationBadRowsFileName)
	r.reportFile = filepath.Join(r.targetDir, migrationStateDirName, migrationReportFileName)
	if err := r.preparePaths(); err != nil {
		return r.fail(err)
	}
	source, err := openMigrateSourceDB(r.sourceDBPath)
	if err != nil {
		return r.fail(err)
	}
	r.source = source
	if selection, err := buildSelection(source, r.options, r.logger); err != nil {
		return r.fail(err)
	} else {
		r.selection = selection
	}
	r.filterFP = pySha1(selectionFingerprint(r.selection))
	fingerprint, hashValue, err := buildSourceFingerprint(r.sourceDBPath)
	if err != nil {
		return r.fail(err)
	}
	r.sourceFP, r.sourceFPHash = fingerprint, hashValue

	preview, err := source.preview(r.selection, r.options.previewLimit)
	if err != nil {
		return r.fail(err)
	}
	r.stats["source_matched_total"] = preview.total
	r.printPreview(preview)

	if preview.total <= 0 {
		r.logger.Info("筛选后无数据，退出。")
		r.stats["verify_passed"] = true
		if r.options.verifyOnly {
			if err := r.loadConfig(); err != nil {
				return r.fail(err)
			}
			if err := r.initStores(ctx, false); err != nil {
				return r.fail(err)
			}
			if err := r.verify(ctx, true); err != nil {
				return r.fail(err)
			}
		}
		return r.finalize()
	}
	if r.options.verifyOnly {
		if err := r.loadConfig(); err != nil {
			return r.fail(err)
		}
		if err := r.initStores(ctx, false); err != nil {
			return r.fail(err)
		}
		if err := r.verify(ctx, true); err != nil {
			return r.fail(err)
		}
		return r.finalize()
	}
	if r.options.dryRun {
		r.logger.Info("dry-run 模式：仅预览，不写入。")
		return r.finalize()
	}
	if !r.options.yes && !r.confirm() {
		r.logger.Info("用户取消执行。")
		return r.finalize()
	}
	if err := r.loadConfig(); err != nil {
		return r.fail(err)
	}
	if err := r.initStores(ctx, true); err != nil {
		return r.fail(err)
	}
	if err := r.loadOrInitState(); err != nil {
		return r.fail(err)
	}
	startAfter := r.resolveStartAfterID()
	if err := r.migrate(ctx, startAfter); err != nil {
		return r.fail(err)
	}
	if err := r.verify(ctx, true); err != nil {
		return r.fail(err)
	}
	return r.finalize()
}

// fail 记录失败并返回最终退出码
func (r *migrateRunner) fail(err error) int {
	r.failed = true
	r.failReason = err.Error()
	r.logger.Error(fmt.Sprintf("迁移失败: %v", err))
	return r.finalize()
}

// preparePaths 对齐 _prepare_paths
func (r *migrateRunner) preparePaths() error {
	stateDir := filepath.Join(r.targetDir, migrationStateDirName)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	if r.options.resetState {
		for _, path := range []string{r.stateFile, r.badRowsFile} {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

// loadConfig 对齐 _load_plugin_config，读取失败只用默认值
func (r *migrateRunner) loadConfig() error {
	config := map[string]any{}
	if path := DefaultConfigPath(); path != "" {
		if _, err := os.Stat(path); err == nil {
			loaded, loadErr := LoadTOML(path)
			if loadErr != nil {
				r.logger.Warning(fmt.Sprintf("读取 A_Memorix 配置失败，继续使用默认配置: %v", loadErr))
			} else {
				config = loaded
			}
		}
	}
	r.pluginConfig = config
	return nil
}

// initStores 对齐 _init_target_stores
func (r *migrateRunner) initStores(ctx context.Context, requireEmbedding bool) error {
	workers := utils.PyIntOrZero(NestedGet(r.pluginConfig, "embedding.max_concurrent", 5))
	if r.options.embedWorkers != nil {
		workers = *r.options.embedWorkers
	}
	r.embedWorkers = max(1, workers)
	batchSize := max(1, utils.PyIntOrZero(NestedGet(r.pluginConfig, "embedding.batch_size", 32)))
	defaultDim := max(1, utils.PyIntOrZero(NestedGet(r.pluginConfig, "embedding.dimension", 1024)))
	retryConfig, _ := NestedGet(r.pluginConfig, "embedding.retry", map[string]any{}).(map[string]any)

	r.dimension = r.readExistingDimension(defaultDim)
	if requireEmbedding {
		r.embedManager = embedding.CreateEmbeddingAPIAdapter(&embedding.EmbeddingAPIAdapterOptions{
			BatchSize:            batchSize,
			MaxConcurrent:        r.embedWorkers,
			DefaultDimension:     defaultDim,
			ModelName:            utils.PyStr(NestedGet(r.pluginConfig, "embedding.model_name", "auto")),
			DimensionRequestMode: utils.PyStr(NestedGet(r.pluginConfig, "embedding.dimension_request_mode", "explicit")),
			RetryConfig:          retryConfig,
		})
		if !r.hasVectorMetadata() {
			if detected, err := r.embedManager.DetectDimension(ctx); err == nil && detected > 0 {
				r.dimension = detected
			} else if err != nil {
				r.logger.Warning(fmt.Sprintf("嵌入维度探测失败，回退配置维度: %v", err))
			}
		}
	}
	quantization := strings.ToLower(strings.TrimSpace(utils.PyStr(NestedGet(r.pluginConfig, "embedding.quantization_type", "int8"))))
	if quantization != "" && quantization != "none" && quantization != "int8" {
		return fmt.Errorf("embedding.quantization_type 在 vNext 仅允许 int8(SQ8)。 请先执行 scripts/release_vnext_migrate.py migrate。")
	}
	vectorStore, err := storage.NewVectorStore(storage.VectorStoreConfig{
		Dimension:        r.dimension,
		QuantizationType: utils.QuantizationInt8,
		DataDir:          filepath.Join(r.targetDir, "vectors"),
	})
	if err != nil {
		return err
	}
	r.vectorStore = vectorStore
	matrixFormat := storage.SparseMatrixFormatCSR
	if strings.ToLower(strings.TrimSpace(utils.PyStr(NestedGet(r.pluginConfig, "graph.sparse_matrix_format", "csr")))) == "csc" {
		matrixFormat = storage.SparseMatrixFormatCSC
	}
	r.graphStore = storage.NewGraphStore(matrixFormat, filepath.Join(r.targetDir, "graph"))
	metadataDir := filepath.Join(r.targetDir, "metadata")
	r.metadataStore = storage.NewStore(metadataDir, "")
	if err := r.metadataStore.Connect(metadataDir, true); err != nil {
		return err
	}
	if requireEmbedding && r.embedManager != nil {
		r.relationWrite = write.NewRelationWriteService(r.metadataStore, r.graphStore, r.vectorStore, relationEmbedder{adapter: r.embedManager}, write.RelationWriteConfig{})
	}
	if r.vectorStore.HasData() {
		if err := r.vectorStore.Load("", nil, nil, ""); err != nil {
			return err
		}
	}
	if r.graphStore.HasData() {
		if err := r.graphStore.Load(""); err != nil {
			return err
		}
	}
	r.logger.Info(fmt.Sprintf("目标存储初始化完成: dim=%d, quant=%s, graph_fmt=%s, embed_workers=%d", r.vectorStore.Dimension(), quantization, matrixFormat, r.embedWorkers))
	return nil
}

// hasVectorMetadata 判断已有 vectors_metadata.json
func (r *migrateRunner) hasVectorMetadata() bool {
	_, err := os.Stat(filepath.Join(r.targetDir, "vectors", "vectors_metadata.json"))
	return err == nil
}

// readExistingDimension 对齐 _read_existing_vector_dimension
func (r *migrateRunner) readExistingDimension(fallback int) int {
	path := filepath.Join(r.targetDir, "vectors", "vectors_metadata.json")
	content, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	var payload map[string]any
	if err := UnmarshalJSON(content, &payload); err != nil {
		return fallback
	}
	value := utils.PyIntOrZero(payload["dimension"])
	if value <= 0 {
		return fallback
	}
	return value
}

// shouldWriteRelationVectors 对齐 _should_write_relation_vectors
func (r *migrateRunner) shouldWriteRelationVectors() bool {
	relationConfig, ok := NestedGet(r.pluginConfig, "retrieval.relation_vectorization", nil).(map[string]any)
	if !ok {
		return false
	}
	return isTruthy(relationConfig["enabled"]) && ConfigBool(relationConfig, "write_on_import", true)
}

// close 对齐 _close
func (r *migrateRunner) close() {
	if r.metadataStore != nil {
		if err := r.metadataStore.Close(); err != nil {
			r.logger.Warning(fmt.Sprintf("关闭 A_Memorix 迁移目标存储失败: %v", err))
		}
	}
	if r.source != nil {
		_ = r.source.Close()
	}
}

// loadOrInitState 对齐 _load_or_init_state
func (r *migrateRunner) loadOrInitState() error {
	if r.options.startID != nil {
		r.logger.Info("检测到 --start-id，已按用户指定起点覆盖断点状态。")
		r.state = r.newState(*r.options.startID - 1)
		return nil
	}
	if r.options.noResume {
		r.state = r.newState(0)
		return nil
	}
	content, err := os.ReadFile(r.stateFile)
	if err != nil {
		r.state = r.newState(0)
		return nil
	}
	var loaded map[string]any
	if err := UnmarshalJSON(content, &loaded); err != nil {
		r.state = r.newState(0)
		return nil
	}
	if utils.PyStr(loaded["filter_fingerprint"]) != r.filterFP || utils.PyStr(loaded["source_db_fingerprint"]) != r.sourceFPHash {
		if r.options.dryRun || r.options.verifyOnly {
			r.logger.Info("检测到断点与当前筛选不一致；当前为只读模式，将忽略旧断点。")
			r.state = r.newState(0)
			return nil
		}
		return fmt.Errorf("检测到筛选条件或源库指纹变化，已拒绝继续续传。请使用 --reset-state 或调整参数后重试。")
	}
	r.state = loaded
	if stored, ok := loaded["stats"].(map[string]any); ok {
		for key, value := range stored {
			if _, exists := r.stats[key]; !exists {
				continue
			}
			switch value.(type) {
			case float64, int, bool:
				r.stats[key] = value
			}
		}
	}
	return nil
}

// newState 对齐 _new_state
func (r *migrateRunner) newState(lastCommittedID int) map[string]any {
	return map[string]any{
		"version": 1, "updated_at": unixNow(), "last_committed_id": lastCommittedID,
		"filter_fingerprint": r.filterFP, "source_db_fingerprint": r.sourceFPHash,
		"source_db_meta": r.sourceFP, "stats": copyStats(r.stats),
	}
}

// flushState 对齐 _flush_state
func (r *migrateRunner) flushState(lastCommittedID int64) error {
	r.stats["last_committed_id"] = lastCommittedID
	r.state = map[string]any{
		"version": 1, "updated_at": unixNow(), "last_committed_id": lastCommittedID,
		"filter_fingerprint": r.filterFP, "source_db_fingerprint": r.sourceFPHash,
		"source_db_meta": r.sourceFP, "stats": copyStats(r.stats),
	}
	content, err := MarshalJSONIndent(r.state)
	if err != nil {
		return err
	}
	return writeFileAtomic(r.stateFile, []byte(content))
}

// resolveStartAfterID 对齐 _resolve_start_after_id
func (r *migrateRunner) resolveStartAfterID() int64 {
	if r.options.startID != nil {
		return int64(*r.options.startID) - 1
	}
	if r.options.noResume {
		return 0
	}
	value := utils.PyIntOrZero(r.state["last_committed_id"])
	if value < 0 {
		return 0
	}
	return int64(value)
}
