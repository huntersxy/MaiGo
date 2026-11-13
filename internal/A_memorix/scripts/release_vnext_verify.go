package scripts

// vNext 发布迁移的报告、校验与 SQLite 检查辅助，对齐 release_vnext_migrate.py 的 verify 部分

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"

	"maibot-go/internal/A_memorix/core/storage"
)

// releaseCheck 单项检查，对齐 CheckItem
type releaseCheck struct {
	Code    string         `json:"code"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// releaseReport 检查报告，对齐 _preflight_impl/_verify_impl 的返回结构
type releaseReport struct {
	OK     bool           `json:"ok"`
	Checks []releaseCheck `json:"checks"`
	Facts  map[string]any `json:"facts"`
}

// ToMap 转成 JSON 友好的字典
func (r releaseReport) ToMap() map[string]any {
	checks := make([]any, 0, len(r.Checks))
	for _, check := range r.Checks {
		item := map[string]any{
			"code":    check.Code,
			"level":   check.Level,
			"message": check.Message,
		}
		if len(check.Details) > 0 {
			item["details"] = check.Details
		}
		checks = append(checks, item)
	}
	facts := r.Facts
	if facts == nil {
		facts = map[string]any{}
	}
	return map[string]any{"ok": r.OK, "checks": checks, "facts": facts}
}

// releaseHasError 判断是否存在 error 级检查
func releaseHasError(checks []releaseCheck) bool {
	for _, check := range checks {
		if check.Level == "error" {
			return true
		}
	}
	return false
}

// releaseVerify 对齐 _verify_impl
func releaseVerify(configPath, dataDir string) releaseReport {
	checks := make([]releaseCheck, 0)
	facts := map[string]any{
		"config_path": configPath,
		"data_dir":    dataDir,
	}
	if !pathIsFile(configPath) {
		checks = append(checks, releaseCheck{Code: "CFG-00", Level: "error", Message: fmt.Sprintf("config not found: %s", configPath)})
		return releaseReport{OK: false, Checks: checks, Facts: facts}
	}
	configDoc, err := LoadTOML(configPath)
	if err != nil {
		checks = append(checks, releaseCheck{Code: "CFG-00", Level: "error", Message: fmt.Sprintf("config not found: %s", configPath)})
		return releaseReport{OK: false, Checks: checks, Facts: facts}
	}

	mode := strings.ToLower(strings.TrimSpace(pyString(NestedGet(configDoc, "routing.tool_search_mode", "forward"))))
	if mode != "forward" && mode != "disabled" {
		checks = append(checks, releaseCheck{Code: "CP-04", Level: "error", Message: fmt.Sprintf("invalid routing.tool_search_mode: %s", mode)})
	}
	summaryModel := NestedGet(configDoc, "summarization.model_name", []any{"auto"})
	if !isStringList(summaryModel) {
		checks = append(checks, releaseCheck{Code: "CP-11", Level: "error", Message: "summarization.model_name must be List[str]"})
	}
	summaryKnowledgeType := strings.ToLower(strings.TrimSpace(pyString(NestedGet(configDoc, "summarization.default_knowledge_type", "narrative"))))
	if !isAllowedKnowledgeType(summaryKnowledgeType) {
		checks = append(checks, releaseCheck{Code: "CP-13", Level: "error", Message: fmt.Sprintf("invalid summarization.default_knowledge_type: %s", summaryKnowledgeType)})
	}
	quantization := strings.ToLower(strings.TrimSpace(pyString(NestedGet(configDoc, "embedding.quantization_type", "int8"))))
	if quantization != "int8" {
		checks = append(checks, releaseCheck{Code: "UG-07", Level: "error", Message: "embedding.quantization_type must be int8"})
	}

	vectorsDir := filepath.Join(dataDir, "vectors")
	npyPath := filepath.Join(vectorsDir, "vectors.npy")
	binPath := filepath.Join(vectorsDir, "vectors.bin")
	idsBinPath := filepath.Join(vectorsDir, "vectors_ids.bin")
	if pathIsFile(npyPath) && !(pathIsFile(binPath) && pathIsFile(idsBinPath)) {
		checks = append(checks, releaseCheck{Code: "CP-07", Level: "error", Message: "legacy vectors.npy still exists without bin migration"})
	}

	metadataDir := filepath.Join(dataDir, "metadata")
	store := storage.NewStore(metadataDir, "")
	if err := store.Connect(metadataDir, true); err != nil {
		checks = append(checks, releaseCheck{Code: "CP-08", Level: "error", Message: fmt.Sprintf("metadata strict connect failed: %v", err)})
	} else {
		verifyReleaseMetadata(store, metadataDir, configDoc, &checks, facts)
	}
	if err := store.Close(); err != nil {
		checks = append(checks, releaseCheck{Code: "CP-09", Level: "warning", Message: fmt.Sprintf("metadata close failed: %v", err)})
	}

	return releaseReport{OK: !releaseHasError(checks), Checks: checks, Facts: facts}
}

// verifyReleaseMetadata 执行严格 schema 校验与图完整性检查
func verifyReleaseMetadata(store *storage.Store, metadataDir string, configDoc map[string]any, checks *[]releaseCheck, facts map[string]any) {
	schemaVersion, err := store.SchemaVersion()
	if err != nil {
		*checks = append(*checks, releaseCheck{Code: "CP-08", Level: "error", Message: fmt.Sprintf("metadata strict connect failed: %v", err)})
		return
	}
	facts["schema_version"] = schemaVersion
	if schemaVersion != storage.SchemaVersion {
		*checks = append(*checks, releaseCheck{Code: "CP-08", Level: "error", Message: fmt.Sprintf("schema version mismatch: %d", schemaVersion)})
	}
	relationCount, err := store.CountRelations(false, false)
	if err != nil {
		*checks = append(*checks, releaseCheck{Code: "CP-08", Level: "error", Message: fmt.Sprintf("metadata strict connect failed: %v", err)})
		return
	}
	facts["relations_count"] = relationCount

	metadataDB := filepath.Join(metadataDir, "metadata.db")
	db, err := openReleaseSQLite(metadataDB)
	if err != nil {
		*checks = append(*checks, releaseCheck{Code: "CP-08", Level: "error", Message: fmt.Sprintf("metadata strict connect failed: %v", err)})
		return
	}
	hasParagraphBackfill := releaseTableExists(db, "paragraph_vector_backfill")
	facts["paragraph_vector_backfill_exists"] = hasParagraphBackfill
	if !hasParagraphBackfill {
		*checks = append(*checks, releaseCheck{Code: "CP-14", Level: "error", Message: "paragraph_vector_backfill table missing after migration"})
	}
	hasFeedbackTasks := releaseTableExists(db, "memory_feedback_tasks")
	facts["memory_feedback_tasks_exists"] = hasFeedbackTasks
	if !hasFeedbackTasks {
		*checks = append(*checks, releaseCheck{Code: "CP-15", Level: "error", Message: "memory_feedback_tasks table missing after migration"})
	}
	hasFeedbackLogs := releaseTableExists(db, "memory_feedback_action_logs")
	facts["memory_feedback_action_logs_exists"] = hasFeedbackLogs
	if !hasFeedbackLogs {
		*checks = append(*checks, releaseCheck{Code: "CP-16", Level: "error", Message: "memory_feedback_action_logs table missing after migration"})
	}
	hasRollbackStatus := releaseColumnExists(db, "memory_feedback_tasks", "rollback_status")
	facts["memory_feedback_tasks_rollback_status_exists"] = hasRollbackStatus
	if !hasRollbackStatus {
		*checks = append(*checks, releaseCheck{Code: "CP-17", Level: "error", Message: "memory_feedback_tasks.rollback_status missing after migration"})
	}
	hasRollbackPlan := releaseColumnExists(db, "memory_feedback_tasks", "rollback_plan_json")
	facts["memory_feedback_tasks_rollback_plan_exists"] = hasRollbackPlan
	if !hasRollbackPlan {
		*checks = append(*checks, releaseCheck{Code: "CP-18", Level: "error", Message: "memory_feedback_tasks.rollback_plan_json missing after migration"})
	}
	conflicts := collectReleaseAliasConflicts(db)
	invalidTypes := collectReleaseInvalidKnowledgeTypes(db)
	_ = db.Close()

	if len(conflicts) > 0 {
		aliases := sortedMapKeys(conflicts)
		*checks = append(*checks, releaseCheck{
			Code: "CP-05", Level: "error", Message: "alias conflicts still exist after migration",
			Details: map[string]any{"aliases": firstStrings(aliases, 20), "total": len(aliases)},
		})
	}
	if len(invalidTypes) > 0 {
		*checks = append(*checks, releaseCheck{
			Code: "CP-12", Level: "error", Message: "invalid paragraph knowledge_type values remain after migration",
			Details: map[string]any{"values": firstStrings(invalidTypes, 20), "total": len(invalidTypes)},
		})
	}

	if relationCount > 0 {
		graphDir := filepath.Join(dataDirOfMetadata(metadataDir), "graph")
		if !pathIsFile(filepath.Join(graphDir, "graph_metadata.json")) {
			*checks = append(*checks, releaseCheck{Code: "CP-06", Level: "error", Message: "graph metadata missing while relations exist"})
			return
		}
		matrixFormat := pyString(NestedGet(configDoc, "graph.sparse_matrix_format", "csr"))
		if matrixFormat == "" {
			matrixFormat = "csr"
		}
		graphStore := storage.NewGraphStore(storage.SparseMatrixFormat(matrixFormat), graphDir)
		if err := graphStore.Load(""); err != nil {
			*checks = append(*checks, releaseCheck{Code: "CP-08", Level: "error", Message: fmt.Sprintf("metadata strict connect failed: %v", err)})
			return
		}
		if !graphStore.HasEdgeHashMap() {
			*checks = append(*checks, releaseCheck{Code: "CP-06", Level: "error", Message: "edge_hash_map is empty"})
		}
	}
}

// dataDirOfMetadata 从 metadata 目录回溯数据根目录
func dataDirOfMetadata(metadataDir string) string { return filepath.Dir(metadataDir) }

// printReleaseReport 对齐 _print_report
func printReleaseReport(ctx context.Context, title string, report releaseReport) {
	scriptPrintf(ctx, "=== %s ===\n", title)
	scriptPrintf(ctx, "ok: %s\n", pythonBool(report.OK))
	if len(report.Facts) > 0 {
		scriptPrintln(ctx, "facts:")
		keys := make([]string, 0, len(report.Facts))
		for key := range report.Facts {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			scriptPrintf(ctx, "  - %s: %s\n", key, releasePythonValue(report.Facts[key]))
		}
	}
	if len(report.Checks) > 0 {
		scriptPrintln(ctx, "checks:")
		for _, item := range report.Checks {
			scriptPrintf(ctx, "  - [%s] %s: %s\n", item.Level, item.Code, item.Message)
		}
	} else {
		scriptPrintln(ctx, "checks: none")
	}
}

// printIndentedJSON 打印 2 空格缩进的 JSON
func printIndentedJSON(ctx context.Context, value any) {
	raw, err := marshalReleaseJSONIndent(value)
	if err != nil {
		scriptPrintf(ctx, "❌ failed to encode json: %v\n", err)
		return
	}
	scriptPrintln(ctx, raw)
}

// writeReleaseJSONIfNeeded 对齐 _write_json_if_needed
func writeReleaseJSONIfNeeded(ctx context.Context, path string, payload any) error {
	if path == "" {
		return nil
	}
	out, err := filepath.Abs(ExpandUser(path))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	raw, err := marshalReleaseJSONIndent(payload)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, []byte(raw), 0o644); err != nil {
		return err
	}
	scriptPrintf(ctx, "json_out: %s\n", out)
	return nil
}

// marshalReleaseJSONIndent 对齐 json.dumps 的键排序与缩进
func marshalReleaseJSONIndent(value any) (string, error) {
	raw, err := sonic.Config{EscapeHTML: false, SortMapKeys: true}.Froze().MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// openReleaseSQLite 打开 SQLite 连接，仅用于只读检查
func openReleaseSQLite(path string) (*sql.DB, error) {
	return sql.Open("sqlite", path)
}

// releaseTableExists 对齐 _sqlite_table_exists
func releaseTableExists(db *sql.DB, table string) bool {
	var name string
	if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name = ?", table).Scan(&name); err != nil {
		return false
	}
	return name != ""
}

// releaseColumnExists 对齐 _sqlite_column_exists
func releaseColumnExists(db *sql.DB, table, column string) bool {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return false
		}
		if name == column {
			return true
		}
	}
	return false
}

// releaseSchemaVersion 读取 schema_migrations 最大版本
func releaseSchemaVersion(db *sql.DB) int {
	var version sql.NullInt64
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil || !version.Valid {
		return 0
	}
	return int(version.Int64)
}

// releaseRelationCount 统计 relations 行数
func releaseRelationCount(db *sql.DB) int {
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM relations").Scan(&count); err != nil {
		return 0
	}
	return count
}

// releaseQueryHashes 读取单列 hash
func releaseQueryHashes(db *sql.DB, query string) []string {
	rows, err := db.Query(query)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	out := make([]string, 0)
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return out
		}
		out = append(out, hash)
	}
	return out
}

// readReleaseJSON 读取 JSON 对象
func readReleaseJSON(path string, dest any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return UnmarshalJSON(raw, dest)
}

// isStringValue 判断 TOML 字符串
func isStringValue(value any) bool {
	_, ok := value.(string)
	return ok
}

// asStringValue 取字符串值
func asStringValue(value any) string {
	text, _ := value.(string)
	return text
}

// isStringList 判断 list[str]
func isStringList(value any) bool {
	switch items := value.(type) {
	case []any:
		for _, item := range items {
			if _, ok := item.(string); !ok {
				return false
			}
		}
		return true
	case []string:
		return true
	default:
		return false
	}
}

// summaryListHasNonString 判断列表中是否含非字符串元素
func summaryListHasNonString(value any) bool {
	switch items := value.(type) {
	case []any:
		for _, item := range items {
			if _, ok := item.(string); !ok {
				return true
			}
		}
		return false
	case []string:
		return false
	default:
		return true
	}
}

// toStringItems 把 TOML 数组转成字符串切片
func toStringItems(value any) []string {
	switch items := value.(type) {
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			out = append(out, pyString(item))
		}
		return out
	case []string:
		return items
	default:
		return nil
	}
}

// configDefault 读取 map 值，缺失时返回默认值
func configDefault(table map[string]any, key string, fallback any) any {
	if value, ok := table[key]; ok {
		return value
	}
	return fallback
}

// isAllowedKnowledgeType 判断 knowledge_type 是否合法
func isAllowedKnowledgeType(value string) bool {
	for _, allowed := range storage.AllowedKnowledgeTypeValues() {
		if value == allowed {
			return true
		}
	}
	return false
}

// pyTypeName 对齐 type(value).__name__ 的常用类型
func pyTypeName(value any) string {
	switch value.(type) {
	case nil:
		return "NoneType"
	case string:
		return "str"
	case bool:
		return "bool"
	case int, int64:
		return "int"
	case float64:
		return "float"
	case []any, []string:
		return "list"
	case map[string]any:
		return "dict"
	default:
		return fmt.Sprintf("%T", value)
	}
}

// sortedMapKeys 返回排序后的键
func sortedMapKeys(values map[string][]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// firstStrings 取前 limit 项
func firstStrings(items []string, limit int) []string {
	if len(items) > limit {
		return items[:limit]
	}
	return items
}

// pythonBool 输出 True/False
func pythonBool(value bool) string {
	if value {
		return "True"
	}
	return "False"
}

// releasePythonValue 对齐 str(value) 的常用输出
func releasePythonValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return releasePythonRepr(value)
}

// releasePythonRepr 对齐 repr(value) 的常用输出
func releasePythonRepr(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case string:
		return "'" + strings.ReplaceAll(strings.ReplaceAll(typed, "\\", "\\\\"), "'", "\\'") + "'"
	case bool:
		return pythonBool(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			items = append(items, releasePythonRepr(item))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case []string:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			items = append(items, releasePythonRepr(item))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case map[string][]string:
		keys := sortedMapKeys(typed)
		items := make([]string, 0, len(keys))
		for _, key := range keys {
			items = append(items, releasePythonRepr(key)+": "+releasePythonRepr(typed[key]))
		}
		return "{" + strings.Join(items, ", ") + "}"
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		items := make([]string, 0, len(keys))
		for _, key := range keys {
			items = append(items, releasePythonRepr(key)+": "+releasePythonRepr(typed[key]))
		}
		return "{" + strings.Join(items, ", ") + "}"
	default:
		return fmt.Sprint(value)
	}
}
