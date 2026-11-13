package scripts

// A_Memorix vNext 发布迁移入口，对齐 src/A_memorix/scripts/release_vnext_migrate.py
// 子命令 preflight/migrate/verify，复用 core/storage 的 schema 迁移与图重建能力

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// vnextReleaseSubcommands 合法子命令
var vnextReleaseSubcommands = map[string]struct{}{
	"preflight": {},
	"migrate":   {},
	"verify":    {},
}

// RunReleaseVNextMigrate 执行 release_vnext_migrate.py 等价流程，返回进程退出码
func RunReleaseVNextMigrate(ctx context.Context, argv []string) int {
	parser := newScriptParser(ctx, "release_vnext_migrate.py", "A_Memorix vNext release migration tool")
	parser.AddString([]string{"--config"}, DefaultConfigPath(), "config.toml 路径（默认 config/a_memorix.toml）")
	parser.AddString([]string{"--data-dir"}, "", "可选数据目录覆盖，默认从 config.storage.data_dir 解析")
	parser.AddString([]string{"--json-out"}, "", "可选 JSON 报告输出路径")
	parser.AddPositional("command", "preflight | migrate | verify")
	parser.AddBool([]string{"--strict"}, "存在 error 级检查时返回 1")
	parser.AddBool([]string{"--dry-run"}, "migrate 子命令只打印计划变更")
	parser.AddBool([]string{"--verify-after"}, "migrate 成功后自动执行 verify")
	args, code, handled := parser.Parse(argv)
	if handled {
		return code
	}

	command := strings.TrimSpace(args.String("command"))
	if _, ok := vnextReleaseSubcommands[command]; !ok {
		scriptErrorf(ctx, "release_vnext_migrate.py: error: argument command: invalid choice: %q\n", command)
		parser.WriteHelp(ScriptStderr(ctx))
		return 2
	}
	dryRun := args.Bool("dry-run")
	verifyAfter := args.Bool("verify-after")
	strict := args.Bool("strict")
	if command == "preflight" && (dryRun || verifyAfter) {
		scriptErrorf(ctx, "release_vnext_migrate.py: error: unrecognized arguments for preflight\n")
		return 2
	}
	if command == "migrate" && strict {
		scriptErrorf(ctx, "release_vnext_migrate.py: error: unrecognized arguments for migrate\n")
		return 2
	}
	if command == "verify" && (dryRun || verifyAfter) {
		scriptErrorf(ctx, "release_vnext_migrate.py: error: unrecognized arguments for verify\n")
		return 2
	}

	configPath := ResolveRepoPath(args.String("config"), DefaultConfigPath())
	if !pathIsFile(configPath) {
		scriptPrintf(ctx, "❌ config not found: %s\n", configPath)
		return 2
	}
	configDoc, err := LoadTOML(configPath)
	if err != nil {
		scriptPrintf(ctx, "❌ failed to read config: %v\n", err)
		return 1
	}
	dataDir := resolveReleaseDataDir(configDoc, args.String("data-dir"))
	jsonOut := args.String("json-out")

	switch command {
	case "preflight":
		report := releasePreflight(configPath, dataDir)
		printReleaseReport(ctx, "vNext Preflight", report)
		if err := writeReleaseJSONIfNeeded(ctx, jsonOut, report); err != nil {
			scriptPrintf(ctx, "❌ failed to write json: %v\n", err)
			return 1
		}
		if strict && releaseHasError(report.Checks) {
			return 1
		}
		return 0
	case "migrate":
		payload, err := runReleaseMigrate(ctx, configPath, dataDir, dryRun)
		if err != nil {
			scriptPrintf(ctx, "❌ migrate failed: %v\n", err)
			return 1
		}
		scriptPrintln(ctx, "=== vNext Migrate ===")
		printIndentedJSON(ctx, payload)
		var verifyReport *releaseReport
		if verifyAfter && !dryRun {
			report := releaseVerify(configPath, dataDir)
			printReleaseReport(ctx, "vNext Verify (after migrate)", report)
			verifyReport = &report
			payload["verify_after"] = report.ToMap()
		}
		if err := writeReleaseJSONIfNeeded(ctx, jsonOut, payload); err != nil {
			scriptPrintf(ctx, "❌ failed to write json: %v\n", err)
			return 1
		}
		if verifyReport != nil && releaseHasError(verifyReport.Checks) {
			return 1
		}
		return 0
	case "verify":
		report := releaseVerify(configPath, dataDir)
		printReleaseReport(ctx, "vNext Verify", report)
		if err := writeReleaseJSONIfNeeded(ctx, jsonOut, report); err != nil {
			scriptPrintf(ctx, "❌ failed to write json: %v\n", err)
			return 1
		}
		if strict && releaseHasError(report.Checks) {
			return 1
		}
		return 0
	}
	return 2
}

// resolveReleaseDataDir 对齐 _resolve_data_dir
func resolveReleaseDataDir(configDoc map[string]any, explicit string) string {
	if explicit != "" {
		return ResolveRepoPath(explicit, DefaultDataDir())
	}
	raw := strings.TrimSpace(pyString(NestedGet(configDoc, "storage.data_dir", "./data")))
	if raw == "" {
		raw = "./data"
	}
	return ResolveRepoPath(raw, DefaultDataDir())
}

// runReleaseMigrate 对齐 _migrate_impl
func runReleaseMigrate(ctx context.Context, configPath, dataDir string, dryRun bool) (map[string]any, error) {
	_ = ctx
	configDoc, err := LoadTOML(configPath)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"config_path": configPath,
		"data_dir":    dataDir,
		"dry_run":     dryRun,
		"steps":       map[string]any{},
	}
	steps := result["steps"].(map[string]any)

	configChanges := migrateReleaseConfig(configDoc)
	steps["config"] = map[string]any{"changed": len(configChanges) > 0, "changes": configChanges}
	if len(configChanges) > 0 && !dryRun {
		if err := writeReleaseTOML(configPath, configDoc); err != nil {
			return nil, err
		}
	}

	vectorsDir := filepath.Join(dataDir, "vectors")
	if err := os.MkdirAll(vectorsDir, 0o755); err != nil {
		return nil, err
	}
	npyPath := filepath.Join(vectorsDir, "vectors.npy")
	binPath := filepath.Join(vectorsDir, "vectors.bin")
	idsBinPath := filepath.Join(vectorsDir, "vectors_ids.bin")
	if pathIsFile(npyPath) && !(pathIsFile(binPath) && pathIsFile(idsBinPath)) {
		if dryRun {
			steps["vector"] = map[string]any{"migrated": false, "reason": "dry_run"}
		} else {
			dim := guessReleaseVectorDimension(configDoc, vectorsDir)
			vectorStore, err := storage.NewVectorStore(storage.VectorStoreConfig{
				Dimension:        max(1, dim),
				QuantizationType: utils.QuantizationInt8,
				DataDir:          vectorsDir,
			})
			if err != nil {
				return nil, err
			}
			vectorResult, err := vectorStore.MigrateLegacyNpy(vectorsDir)
			if err != nil {
				return nil, err
			}
			steps["vector"] = vectorResult
		}
	} else {
		steps["vector"] = map[string]any{"migrated": false, "reason": "not_required"}
	}

	metadataDir := filepath.Join(dataDir, "metadata")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		return nil, err
	}
	metadataDB := filepath.Join(metadataDir, "metadata.db")
	var triples []storage.Triple
	relationCount := 0
	metadataResult := map[string]any{"migrated": false, "reason": "not_required"}
	if pathIsFile(metadataDB) {
		store := storage.NewStore(metadataDir, "")
		if err := store.Connect(metadataDir, false); err != nil {
			return nil, err
		}
		if dryRun {
			metadataResult = map[string]any{"migrated": false, "reason": "dry_run"}
		} else {
			metadataResult, err = runReleaseLegacyMigration(store)
			if err != nil {
				_ = store.Close()
				return nil, err
			}
		}
		relationCount, err = store.CountRelations(false, false)
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		if relationCount > 0 {
			triples, err = store.GetAllTriples()
			if err != nil {
				_ = store.Close()
				return nil, err
			}
		}
		_ = store.Close()
	}
	steps["metadata"] = metadataResult

	graphDir := filepath.Join(dataDir, "graph")
	if err := os.MkdirAll(graphDir, 0o755); err != nil {
		return nil, err
	}
	graphFormat := pyString(NestedGet(configDoc, "graph.sparse_matrix_format", "csr"))
	if graphFormat == "" {
		graphFormat = "csr"
	}
	graphStore := storage.NewGraphStore(storage.SparseMatrixFormat(graphFormat), graphDir)
	graphStep := map[string]any{
		"rebuilt":                         false,
		"mapped_hashes":                   0,
		"relation_count":                  relationCount,
		"topology_rebuilt_from_relations": false,
	}
	if relationCount > 0 {
		if dryRun {
			graphStep["reason"] = "dry_run"
		} else {
			if graphStore.HasData() {
				if err := graphStore.Load(""); err != nil {
					return nil, err
				}
			}
			mapped := graphStore.RebuildEdgeHashMap(releaseRelationTriples(triples))
			if mapped <= 0 || !graphStore.HasEdgeHashMap() {
				nodeSet := make(map[string]struct{})
				edges := make([]storage.Edge, 0, len(triples))
				hashes := make([]string, 0, len(triples))
				for _, triple := range triples {
					nodeSet[triple.Subject] = struct{}{}
					nodeSet[triple.Object] = struct{}{}
					edges = append(edges, storage.Edge{Source: triple.Subject, Target: triple.Object})
					hashes = append(hashes, triple.Hash)
				}
				nodes := make([]string, 0, len(nodeSet))
				for node := range nodeSet {
					nodes = append(nodes, node)
				}
				sort.Strings(nodes)
				graphStore.Clear()
				if len(nodes) > 0 {
					graphStore.AddNodes(nodes, nil)
				}
				if len(edges) > 0 {
					mapped, err = graphStore.AddEdges(edges, nil, hashes)
					if err != nil {
						return nil, err
					}
				} else {
					mapped = 0
				}
				graphStep["topology_rebuilt_from_relations"] = true
				graphStep["rebuilt_nodes"] = len(nodes)
				graphStep["rebuilt_edges"] = graphStore.NumEdges()
			}
			if err := graphStore.Save(""); err != nil {
				return nil, err
			}
			graphStep["rebuilt"] = true
			graphStep["mapped_hashes"] = mapped
		}
	} else {
		graphStep["reason"] = "no_relations"
	}
	steps["graph"] = graphStep
	return result, nil
}

// runReleaseLegacyMigration 组合 Go 侧可用的 schema 迁移步骤，对齐 run_legacy_migration_for_vnext
func runReleaseLegacyMigration(store *storage.Store) (map[string]any, error) {
	if err := store.EnsureSchema(); err != nil {
		return nil, err
	}
	conn, err := store.Conn()
	if err != nil {
		return nil, err
	}
	aliasResult, err := store.RebuildRelationHashAliases(conn)
	if err != nil {
		return nil, err
	}
	knowledgeResult, err := store.NormalizeParagraphKnowledgeTypes(conn)
	if err != nil {
		return nil, err
	}
	factResult, err := store.BackfillPersonFactClaims(conn)
	if err != nil {
		return nil, err
	}
	if err := store.SetSchemaVersion(storage.SchemaVersion); err != nil {
		return nil, err
	}
	return map[string]any{
		"schema_version": storage.SchemaVersion,
		"alias_result": map[string]any{
			"inserted":       aliasResult.Inserted,
			"conflict_count": aliasResult.ConflictCount,
			"conflicts":      aliasResult.Conflicts,
		},
		"knowledge_type_result": map[string]any{
			"normalized":     knowledgeResult.Normalized,
			"invalid_before": knowledgeResult.InvalidBefore,
			"normalized_to":  knowledgeResult.NormalizedTo,
		},
		"fact_result": factResult,
	}, nil
}

// migrateReleaseConfig 对齐 _migrate_config
func migrateReleaseConfig(configDoc map[string]any) map[string]any {
	changes := map[string]any{}

	routing := ensureReleaseTable(configDoc, "routing")
	modeRaw := strings.ToLower(strings.TrimSpace(pyString(configDefault(routing, "tool_search_mode", "forward"))))
	modeNew := modeRaw
	if modeRaw == "legacy" || (modeRaw != "forward" && modeRaw != "disabled") {
		modeNew = "forward"
	}
	if modeNew != modeRaw {
		routing["tool_search_mode"] = modeNew
		changes["routing.tool_search_mode"] = map[string]any{"old": modeRaw, "new": modeNew}
	}

	summary := ensureReleaseTable(configDoc, "summarization")
	summaryModel, hasModel := summary["model_name"]
	normalized := []any{"auto"}
	switch {
	case !hasModel:
		summary["model_name"] = normalized
		changes["summarization.model_name"] = map[string]any{"old": "<missing>", "new": normalized}
	case isStringValue(summaryModel):
		text := strings.TrimSpace(asStringValue(summaryModel))
		if text == "" {
			text = "auto"
		}
		normalized = []any{text}
		summary["model_name"] = normalized
		changes["summarization.model_name"] = map[string]any{"old": summaryModel, "new": normalized}
	case !isStringList(summaryModel):
		summary["model_name"] = normalized
		changes["summarization.model_name"] = map[string]any{"old": pyTypeName(summaryModel), "new": normalized}
	case summaryListHasNonString(summaryModel):
		items := make([]any, 0)
		for _, item := range toStringItems(summaryModel) {
			if text := strings.TrimSpace(item); text != "" {
				items = append(items, text)
			}
		}
		if len(items) == 0 {
			items = []any{"auto"}
		}
		summary["model_name"] = items
		changes["summarization.model_name"] = map[string]any{"old": summaryModel, "new": items}
	}

	defaultKnowledgeType := strings.ToLower(strings.TrimSpace(pyString(configDefault(summary, "default_knowledge_type", "narrative"))))
	if !isAllowedKnowledgeType(defaultKnowledgeType) {
		summary["default_knowledge_type"] = "narrative"
		changes["summarization.default_knowledge_type"] = map[string]any{"old": defaultKnowledgeType, "new": "narrative"}
	}

	embedding := ensureReleaseTable(configDoc, "embedding")
	quantization := strings.ToLower(strings.TrimSpace(pyString(configDefault(embedding, "quantization_type", "int8"))))
	if quantization != "int8" {
		embedding["quantization_type"] = "int8"
		changes["embedding.quantization_type"] = map[string]any{"old": quantization, "new": "int8"}
	}
	return changes
}

// ensureReleaseTable 对齐 _ensure_table
func ensureReleaseTable(doc map[string]any, key string) map[string]any {
	if existing, ok := doc[key].(map[string]any); ok {
		return existing
	}
	created := map[string]any{}
	doc[key] = created
	return created
}

// writeReleaseTOML 写回 TOML，Go 侧会重排格式并丢弃注释
func writeReleaseTOML(path string, doc map[string]any) error {
	raw, err := toml.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// guessReleaseVectorDimension 对齐 _guess_vector_dimension
func guessReleaseVectorDimension(configDoc map[string]any, vectorsDir string) int {
	metaPath := filepath.Join(vectorsDir, "vectors_metadata.json")
	if pathIsFile(metaPath) {
		payload := map[string]any{}
		if err := readReleaseJSON(metaPath, &payload); err == nil {
			if dim := utils.PyIntOrZero(payload["dimension"]); dim > 0 {
				return dim
			}
		}
	}
	if dim := utils.PyIntOrZero(NestedGet(configDoc, "embedding.dimension", 1024)); dim > 0 {
		return dim
	}
	return 1024
}

// graphEdgeHashMapSize 对齐 _graph_edge_hash_map_size
func graphEdgeHashMapSize(dataDir string) (int, error) {
	metadataDB := filepath.Join(dataDir, "metadata", "metadata.db")
	if pathIsFile(metadataDB) {
		db, err := openReleaseSQLite(metadataDB)
		if err == nil {
			if releaseTableExists(db, "graph_edge_relation_map") {
				var count int
				if err := db.QueryRow("SELECT COUNT(*) FROM graph_edge_relation_map").Scan(&count); err == nil && count > 0 {
					_ = db.Close()
					return count, nil
				}
			}
			_ = db.Close()
		}
	}
	graphMetaPath := filepath.Join(dataDir, "graph", "graph_metadata.json")
	if !pathIsFile(graphMetaPath) {
		return 0, nil
	}
	payload := map[string]any{}
	if err := readReleaseJSON(graphMetaPath, &payload); err != nil {
		return 0, err
	}
	if edgeMap, ok := payload["edge_hash_map"].(map[string]any); ok {
		return len(edgeMap), nil
	}
	return 0, nil
}

// collectReleaseAliasConflicts 对齐 _collect_hash_alias_conflicts
func collectReleaseAliasConflicts(db *sql.DB) map[string][]string {
	hashes := make([]string, 0)
	if releaseTableExists(db, "relations") {
		hashes = append(hashes, releaseQueryHashes(db, "SELECT hash FROM relations")...)
	}
	if releaseTableExists(db, "deleted_relations") {
		hashes = append(hashes, releaseQueryHashes(db, "SELECT hash FROM deleted_relations")...)
	}
	aliasMap := map[string]string{}
	conflicts := map[string]map[string]struct{}{}
	for _, hash := range hashes {
		if len(hash) != 64 {
			continue
		}
		alias := hash[:32]
		old, exists := aliasMap[alias]
		if !exists {
			aliasMap[alias] = hash
			continue
		}
		if old != hash {
			if conflicts[alias] == nil {
				conflicts[alias] = map[string]struct{}{}
			}
			conflicts[alias][old] = struct{}{}
			conflicts[alias][hash] = struct{}{}
		}
	}
	out := map[string][]string{}
	for alias, values := range conflicts {
		items := make([]string, 0, len(values))
		for value := range values {
			items = append(items, value)
		}
		sort.Strings(items)
		out[alias] = items
	}
	return out
}

// collectReleaseInvalidKnowledgeTypes 对齐 _collect_invalid_knowledge_types
func collectReleaseInvalidKnowledgeTypes(db *sql.DB) []string {
	if !releaseTableExists(db, "paragraphs") || !releaseColumnExists(db, "paragraphs", "knowledge_type") {
		return nil
	}
	rows, err := db.Query("SELECT DISTINCT knowledge_type FROM paragraphs")
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	allowed := map[string]struct{}{}
	for _, item := range storage.AllowedKnowledgeTypeValues() {
		allowed[item] = struct{}{}
	}
	seen := map[string]struct{}{}
	for rows.Next() {
		var raw sql.NullString
		if err := rows.Scan(&raw); err != nil {
			return nil
		}
		text := ""
		if raw.Valid {
			text = raw.String
		}
		value := strings.ToLower(strings.TrimSpace(text))
		if _, ok := allowed[value]; ok {
			continue
		}
		seen[text] = struct{}{}
	}
	items := make([]string, 0, len(seen))
	for item := range seen {
		items = append(items, item)
	}
	sort.Strings(items)
	return items
}

// releaseRelationTriples 转换 triples，供图哈希映射重建
func releaseRelationTriples(triples []storage.Triple) []storage.RelationTriple {
	out := make([]storage.RelationTriple, 0, len(triples))
	for _, triple := range triples {
		out = append(out, storage.RelationTriple{
			Subject:   triple.Subject,
			Predicate: triple.Predicate,
			Object:    triple.Object,
			Hash:      triple.Hash,
		})
	}
	return out
}
