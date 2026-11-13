package scripts

// 关系向量一次性回填，对齐 src/A_memorix/scripts/backfill_relation_vectors.py
// 复用 core/write 的 RelationWriteService，不重写关系向量写入内核

import (
	"context"
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

// RunBackfillRelationVectors 执行 backfill_relation_vectors.py 等价流程，返回进程退出码
func RunBackfillRelationVectors(ctx context.Context, argv []string) int {
	parser := newScriptParser(ctx, "backfill_relation_vectors.py", "关系向量一次性回填")
	parser.AddString([]string{"--config"}, DefaultConfigPath(), "配置文件路径（默认 config/a_memorix.toml）")
	parser.AddString([]string{"--data-dir"}, DefaultDataDir(), "数据目录（默认 data/a-memorix）")
	parser.AddString([]string{"--states"}, "none,failed,pending", "待处理状态列表，逗号分隔")
	parser.AddInt([]string{"--limit"}, 50000, "最大处理数量")
	parser.AddInt([]string{"--concurrency"}, 8, "并发数")
	parser.AddInt([]string{"--max-retry"}, 0, "最大重试次数过滤")
	parser.AddBool([]string{"--include-ready-missing"}, "额外纳入 vector_state=ready 但向量缺失的关系")
	parser.AddBool([]string{"--dry-run"}, "仅统计候选，不写入")
	args, code, handled := parser.Parse(argv)
	if handled {
		return code
	}

	configPath := ResolveRepoPath(args.String("config"), DefaultConfigPath())
	if _, err := os.Stat(configPath); err != nil {
		scriptPrintf(ctx, "❌ 配置文件不存在: %s\n", configPath)
		return 2
	}
	config, err := LoadTOML(configPath)
	if err != nil {
		scriptErrorf(ctx, "❌ 加载配置失败: %v\n", err)
		return 1
	}
	dataDir := ResolveRepoPath(args.String("data-dir"), DefaultDataDir())
	if _, err := os.Stat(dataDir); err != nil {
		scriptPrintf(ctx, "❌ 数据目录不存在: %s\n", dataDir)
		return 2
	}

	scriptPrintf(ctx, "data_dir: %s\n", dataDir)
	scriptPrintf(ctx, "config: %s\n", configPath)

	vectorStore, err := buildBackfillVectorStore(dataDir, configSection(config, "embedding"))
	if err != nil {
		scriptErrorf(ctx, "❌ %v\n", err)
		return 1
	}
	graphStore, err := buildBackfillGraphStore(dataDir, configSection(config, "graph"))
	if err != nil {
		scriptErrorf(ctx, "❌ %v\n", err)
		return 1
	}
	metadataDir := filepath.Join(dataDir, "metadata")
	metadataStore := storage.NewStore(metadataDir, "")
	if err := metadataStore.Connect(metadataDir, true); err != nil {
		scriptErrorf(ctx, "❌ 初始化元数据存储失败: %v\n", err)
		return 1
	}
	defer func() { _ = metadataStore.Close() }()

	embeddingManager := buildBackfillEmbeddingManager(configSection(config, "embedding"))
	service := write.NewRelationWriteService(
		metadataStore, graphStore, vectorStore,
		relationEmbedder{adapter: embeddingManager},
		write.RelationWriteConfig{},
	)

	states := parseBackfillStates(args.String("states"))
	if len(states) == 0 {
		states = []string{"none", "failed", "pending"}
	}
	retrievalConfig := configSection(config, "retrieval")
	relationVectorization, _ := retrievalConfig["relation_vectorization"].(map[string]any)
	maxRetry := utils.PyIntOrZero(NestedGet(relationVectorization, "max_retry", 3))
	if args.Has("max-retry") {
		maxRetry = args.Int("max-retry")
	}
	maxRetry = scriptMaxInt(1, maxRetry)
	limit := scriptMaxInt(1, args.Int("limit"))

	rows, err := metadataStore.ListRelationsByVectorState(states, limit, &maxRetry)
	if err != nil {
		scriptErrorf(ctx, "❌ 查询关系失败: %v\n", err)
		return 1
	}
	addedReadyMissing := 0
	if args.Bool("include-ready-missing") {
		readyRows, err := metadataStore.ListRelationsByVectorState([]string{"ready"}, limit, &maxRetry)
		if err != nil {
			scriptErrorf(ctx, "❌ 查询 ready 关系失败: %v\n", err)
			return 1
		}
		readyMissing := make([]map[string]any, 0)
		for _, row := range readyRows {
			if !vectorStore.Contains(hashTextFromRow(row)) {
				readyMissing = append(readyMissing, row)
			}
		}
		addedReadyMissing = len(readyMissing)
		if len(readyMissing) > 0 {
			rows = mergeRelationRows(rows, readyMissing, limit)
		}
	}
	scriptPrintf(ctx, "candidates: %d (states=%s, max_retry=%d)\n", len(rows), pyStringListRepr(states), maxRetry)
	if args.Bool("include-ready-missing") {
		scriptPrintf(ctx, "ready_missing_candidates_added: %d\n", addedReadyMissing)
	}
	if len(rows) == 0 {
		return 0
	}
	if args.Bool("dry-run") {
		scriptPrintln(ctx, "dry_run=true，未执行写入。")
		return 0
	}

	started := time.Now()
	stat := processRelationBackfillRows(ctx, service, rows, args.Int("concurrency"))
	elapsed := float64(time.Since(started).Microseconds()) / 1000.0

	if err := vectorStore.Save("", nil); err != nil {
		scriptErrorf(ctx, "❌ 保存向量存储失败: %v\n", err)
		return 1
	}
	if err := graphStore.Save(""); err != nil {
		scriptErrorf(ctx, "❌ 保存图存储失败: %v\n", err)
		return 1
	}
	stateStats, err := metadataStore.CountRelationsByVectorState()
	if err != nil {
		scriptErrorf(ctx, "❌ 统计关系向量状态失败: %v\n", err)
		return 1
	}
	output := map[string]any{
		"processed":   len(rows),
		"success":     stat.success,
		"failed":      stat.failed,
		"skipped":     stat.skipped,
		"elapsed_ms":  elapsed,
		"state_stats": stateStats,
	}
	payload, err := MarshalJSONIndent(output)
	if err != nil {
		scriptErrorf(ctx, "❌ 输出结果失败: %v\n", err)
		return 1
	}
	scriptPrintln(ctx, payload)
	if stat.failed == 0 {
		return 0
	}
	return 1
}

// relationBackfillStat 并发处理统计
type relationBackfillStat struct {
	success int
	failed  int
	skipped int
}

// processRelationBackfillRows 对齐 _process_rows
func processRelationBackfillRows(ctx context.Context, service *write.RelationWriteService, rows []map[string]any, concurrency int) relationBackfillStat {
	stat := relationBackfillStat{}
	limit := scriptMaxInt(1, concurrency)
	semaphore := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, row := range rows {
		row := row
		wg.Add(1)
		go func() {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			result, err := service.EnsureRelationVector(
				ctx,
				strings.TrimSpace(utils.PyStr(row["hash"])),
				utils.PyStr(row["subject"]),
				utils.PyStr(row["predicate"]),
				utils.PyStr(row["object"]),
				memruntimeRelationErrorMaxLen,
				false,
				nil,
			)
			mu.Lock()
			defer mu.Unlock()
			if err == nil && result.VectorState == "ready" {
				if result.VectorWritten {
					stat.success++
				} else {
					stat.skipped++
				}
				return
			}
			stat.failed++
		}()
	}
	wg.Wait()
	return stat
}

// memruntimeRelationErrorMaxLen 对齐 ERROR_MAX_LEN
const memruntimeRelationErrorMaxLen = 500

// buildBackfillVectorStore 对齐 _build_vector_store
func buildBackfillVectorStore(dataDir string, embConfig map[string]any) (*storage.VectorStore, error) {
	quantization := strings.ToLower(utils.PyStr(embConfig["quantization_type"]))
	if quantization == "" {
		quantization = "int8"
	}
	if quantization != "int8" {
		return nil, fmt.Errorf("embedding.quantization_type 在 vNext 仅允许 int8(SQ8)。 请先执行 scripts/release_vnext_migrate.py migrate。")
	}
	dimension := utils.PyIntOrZero(NestedGet(embConfig, "dimension", 1024))
	store, err := storage.NewVectorStore(storage.VectorStoreConfig{
		Dimension:        scriptMaxInt(1, dimension),
		QuantizationType: utils.QuantizationInt8,
		DataDir:          filepath.Join(dataDir, "vectors"),
	})
	if err != nil {
		return nil, err
	}
	if store.HasData() {
		if err := store.Load("", nil, nil, ""); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// buildBackfillGraphStore 对齐 _build_graph_store
func buildBackfillGraphStore(dataDir string, graphConfig map[string]any) (*storage.GraphStore, error) {
	format := strings.ToLower(utils.PyStr(NestedGet(graphConfig, "sparse_matrix_format", "csr")))
	matrixFormat := storage.SparseMatrixFormatCSR
	if format == "csc" {
		matrixFormat = storage.SparseMatrixFormatCSC
	}
	store := storage.NewGraphStore(matrixFormat, filepath.Join(dataDir, "graph"))
	if store.HasData() {
		if err := store.Load(""); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// buildBackfillEmbeddingManager 对齐 _build_embedding_manager
func buildBackfillEmbeddingManager(embConfig map[string]any) *embedding.EmbeddingAPIAdapter {
	retryConfig, _ := embConfig["retry"].(map[string]any)
	return embedding.CreateEmbeddingAPIAdapter(&embedding.EmbeddingAPIAdapterOptions{
		BatchSize:            utils.PyIntOrZero(NestedGet(embConfig, "batch_size", 32)),
		MaxConcurrent:        utils.PyIntOrZero(NestedGet(embConfig, "max_concurrent", 5)),
		DefaultDimension:     utils.PyIntOrZero(NestedGet(embConfig, "dimension", 1024)),
		ModelName:            utils.PyStr(NestedGet(embConfig, "model_name", "auto")),
		DimensionRequestMode: utils.PyStr(NestedGet(embConfig, "dimension_request_mode", "explicit")),
		RetryConfig:          retryConfig,
	})
}

// configSection 读取配置中的 dict 段，非 dict 视为空
func configSection(config map[string]any, key string) map[string]any {
	if section, ok := config[key].(map[string]any); ok {
		return section
	}
	return map[string]any{}
}

// parseBackfillStates 对齐 states 逗号拆分
func parseBackfillStates(raw string) []string {
	out := make([]string, 0)
	for _, item := range strings.Split(raw, ",") {
		if token := strings.TrimSpace(item); token != "" {
			out = append(out, token)
		}
	}
	return out
}

// hashTextFromRow 读取关系行 hash
func hashTextFromRow(row map[string]any) string {
	return strings.TrimSpace(utils.PyStr(row["hash"]))
}

// mergeRelationRows 保序去重并截断，对齐 Python dict.setdefault 逻辑
func mergeRelationRows(rows []map[string]any, extra []map[string]any, limit int) []map[string]any {
	seen := make(map[string]struct{}, len(rows))
	merged := make([]map[string]any, 0, len(rows)+len(extra))
	appendRow := func(row map[string]any) {
		hashValue := hashTextFromRow(row)
		if _, exists := seen[hashValue]; exists {
			return
		}
		seen[hashValue] = struct{}{}
		merged = append(merged, row)
	}
	for _, row := range rows {
		appendRow(row)
	}
	for _, row := range extra {
		appendRow(row)
	}
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

// pyStringListRepr 对齐 Python list[str] 的 repr
func pyStringListRepr(values []string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, singleQuoteKey(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// scriptMaxInt 返回较大值
func scriptMaxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}
