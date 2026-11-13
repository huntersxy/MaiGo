package storage

// A_memorix 存储格式迁移入口，对齐 Python core/storage/format_migration.py
// 启动阶段把历史 pickle 元数据转换为 JSON，并补齐图边到关系哈希的映射表

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

// FormatMigrationVersion 对应 Python FORMAT_MIGRATION_VERSION
const FormatMigrationVersion = "pickle_to_json_v1"

// ErrPickleUnsupported 表示旧存储 pickle 使用了纯 Go 受限解析器无法对等处理的载荷
// 例如 GLOBAL/REDUCE/NEWOBJ 等可加载任意对象的操作码；此时必须显式失败，不伪造迁移成功
var ErrPickleUnsupported = errors.New("不支持的 pickle 载荷")

// migrationTableDDL 对齐 _ensure_migration_table 的建表语句
const migrationTableDDL = "CREATE TABLE IF NOT EXISTS storage_format_migrations (" +
	"version TEXT PRIMARY KEY, " +
	"applied_at REAL NOT NULL, " +
	"summary_json TEXT NOT NULL)"

// insertMigrationRecordSQL 对齐完成记录写入语句
const insertMigrationRecordSQL = "INSERT INTO storage_format_migrations (version, applied_at, summary_json) " +
	"VALUES (?, ?, ?) ON CONFLICT(version) DO NOTHING"

// metadataTables 参与 pickle 转 JSON 的业务表，顺序与 Python 一致
var metadataTables = []string{"paragraphs", "entities", "relations", "deleted_relations"}

// vectorRelativeDirs 三个向量元数据目录，顺序与 Python 一致
var vectorRelativeDirs = []string{
	"vectors",
	filepath.Join("vectors", "paragraph"),
	filepath.Join("vectors", "graph"),
}

// FormatMigrationSummary 对齐 run_startup_format_migration 的返回字典
type FormatMigrationSummary struct {
	Version    string                 `json:"version"`
	DataDir    string                 `json:"data_dir"`
	StartedAt  float64                `json:"started_at"`
	SQLite     SQLiteMigrationSummary `json:"sqlite"`
	Vectors    []VectorMigrationEntry `json:"vectors"`
	Graph      GraphMigrationEntry    `json:"graph"`
	FinishedAt float64                `json:"finished_at"`
}

// SQLiteMigrationSummary 对应 summary["sqlite"]
// 迁移成功时携带 tables；未执行迁移时只携带 updated 与 reason
type SQLiteMigrationSummary struct {
	Tables  map[string]SQLiteTableMigration `json:"tables,omitempty"`
	Updated int                             `json:"updated"`
	Reason  string                          `json:"reason,omitempty"`
}

// SQLiteTableMigration 对应 _migrate_sqlite_metadata 的单表统计
type SQLiteTableMigration struct {
	Scanned int `json:"scanned"`
	Updated int `json:"updated"`
}

// VectorMigrationEntry 同时承载检测结果与迁移结果
// 检测结果使用 action/reason/path/error，迁移结果使用 migrated/recovered/backup
type VectorMigrationEntry struct {
	Action    string  `json:"action,omitempty"`
	Reason    string  `json:"reason,omitempty"`
	Path      string  `json:"path,omitempty"`
	Error     *string `json:"error,omitempty"`
	Migrated  *bool   `json:"migrated,omitempty"`
	Recovered *bool   `json:"recovered,omitempty"`
	Backup    string  `json:"backup,omitempty"`
}

// GraphMigrationEntry 对应图元数据通道的检测结果与迁移结果
type GraphMigrationEntry struct {
	Action          string  `json:"action,omitempty"`
	Reason          string  `json:"reason,omitempty"`
	Error           *string `json:"error,omitempty"`
	Migrated        *bool   `json:"migrated,omitempty"`
	Recovered       *bool   `json:"recovered,omitempty"`
	Backup          string  `json:"backup,omitempty"`
	EdgeHashMapRows *int    `json:"edge_hash_map_rows,omitempty"`
}

// StartupFormatWork 对应 _detect_startup_format_work 的返回字典
type StartupFormatWork struct {
	RecordExists   bool
	SQLiteRequired bool
	Vectors        []VectorMigrationEntry
	Graph          GraphMigrationEntry
	Required       bool
}

// boolPtr 构造可选布尔，用于区分字段缺失与字段为 false
func boolPtr(value bool) *bool { return &value }

// RunStartupFormatMigration 仅在存在历史格式输入时执行一次启动转换
// 全部数据库写入与 Python 一样落在同一个隐式事务里：成功才提交，出错回滚后返回原始错误
func RunStartupFormatMigration(dataDir string) (summary *FormatMigrationSummary, err error) {
	dataDir = filepath.Clean(dataDir)
	summary = &FormatMigrationSummary{
		Version:   FormatMigrationVersion,
		DataDir:   dataDir,
		StartedAt: nowTimestamp(),
		Vectors:   []VectorMigrationEntry{},
	}

	dbPath := MetadataDBPath(dataDir)
	var session *MigrationDB
	if pathExists(dbPath) {
		session, err = ConnectMetadataDB(dbPath)
		if err != nil {
			return nil, fmt.Errorf("连接元数据库失败: %w", err)
		}
		// 与 Python 的 try/except/finally 对应：提交或回滚之后再关闭连接
		// defer 为后进先出，因此关闭连接要最先注册
		defer func() { _ = session.Close() }()
		defer func() {
			if err != nil {
				_ = session.Rollback()
				return
			}
			if commitErr := session.Commit(); commitErr != nil {
				_ = session.Rollback()
				summary = nil
				err = commitErr
			}
		}()
	}

	var conn MigrationConn
	if session != nil {
		conn = session
	}
	if err = runStartupFormatMigration(summary, dataDir, conn); err != nil {
		summary = nil
		return nil, err
	}
	return summary, nil
}

// runStartupFormatMigration 对齐 run_startup_format_migration 的主体
func runStartupFormatMigration(summary *FormatMigrationSummary, dataDir string, conn MigrationConn) error {
	work, err := DetectStartupFormatWork(dataDir, conn)
	if err != nil {
		return err
	}

	if !work.Required {
		summary.SQLite = SQLiteMigrationSummary{Reason: sqliteSkipReason(work, conn)}
		summary.Vectors = append(summary.Vectors, work.Vectors...)
		summary.Graph = work.Graph
		summary.FinishedAt = nowTimestamp()
		zap.L().Debug("A_Memorix 无需执行存储格式迁移",
			zap.String("reason", summary.SQLite.Reason),
			zap.Float64("duration", summary.FinishedAt-summary.StartedAt))
		return nil
	}

	switch {
	case conn != nil && work.SQLiteRequired:
		sqliteResult, migrateErr := MigrateSQLiteMetadata(conn)
		if migrateErr != nil {
			return migrateErr
		}
		summary.SQLite = sqliteResult
	case conn != nil:
		summary.SQLite = SQLiteMigrationSummary{Reason: "not_required"}
	default:
		summary.SQLite = SQLiteMigrationSummary{Reason: "metadata_db_missing"}
	}

	for _, item := range work.Vectors {
		if item.Action == "none" {
			summary.Vectors = append(summary.Vectors, item)
			continue
		}
		migrated, migrateErr := MigrateVectorMetadataDir(item.Path)
		if migrateErr != nil {
			return migrateErr
		}
		summary.Vectors = append(summary.Vectors, migrated)
	}

	if work.Graph.Action == "none" {
		summary.Graph = work.Graph
	} else {
		graph, migrateErr := MigrateGraphMetadata(dataDir, conn)
		if migrateErr != nil {
			return migrateErr
		}
		summary.Graph = graph
	}

	summary.FinishedAt = nowTimestamp()

	// 当前格式已损坏时不能落完成记录，否则后续启动会误判为已迁移
	hasBlockingIssue := summary.Graph.Reason == "current_format_corrupt"
	for _, item := range summary.Vectors {
		if item.Reason == "current_format_corrupt" {
			hasBlockingIssue = true
		}
	}
	if conn != nil && !hasBlockingIssue {
		if err := ensureMigrationTable(conn); err != nil {
			return err
		}
		if err := insertMigrationRecord(conn, summary); err != nil {
			return err
		}
		zap.L().Info("A_Memorix 存储格式迁移检查完成",
			zap.Bool("sqlite_updated", summary.SQLite.Reason == "" || summary.SQLite.Reason == "migrated"),
			zap.Float64("duration", summary.FinishedAt-summary.StartedAt))
	}
	return nil
}

// sqliteSkipReason 对齐无待迁移数据时的 sqlite.reason 取值
func sqliteSkipReason(work StartupFormatWork, conn MigrationConn) string {
	if conn == nil {
		return "metadata_db_missing"
	}
	if work.RecordExists {
		return "already_applied"
	}
	return "not_required"
}

// DetectStartupFormatWork 对齐 _detect_startup_format_work
func DetectStartupFormatWork(dataDir string, conn MigrationConn) (StartupFormatWork, error) {
	work := StartupFormatWork{Vectors: []VectorMigrationEntry{}}

	recordExists := false
	if conn != nil {
		exists, err := migrationRecordExists(conn)
		if err != nil {
			return work, err
		}
		recordExists = exists
	}
	work.RecordExists = recordExists

	// 完成记录与 SQLite 转换在同一事务提交；恢复旧数据库时也会恢复其迁移状态
	if conn != nil && !recordExists {
		required, err := sqliteLegacyMetadataExists(conn)
		if err != nil {
			return work, err
		}
		work.SQLiteRequired = required
	}

	for _, relative := range vectorRelativeDirs {
		item, err := vectorFormatWork(filepath.Join(dataDir, relative), recordExists)
		if err != nil {
			return work, err
		}
		work.Vectors = append(work.Vectors, item)
	}

	graph, err := graphFormatWork(dataDir, conn, recordExists)
	if err != nil {
		return work, err
	}
	work.Graph = graph

	work.Required = work.SQLiteRequired || graph.Action != "none"
	for _, item := range work.Vectors {
		if item.Action != "none" {
			work.Required = true
		}
	}
	return work, nil
}

// MetadataDBPath 对齐 _metadata_db_path
func MetadataDBPath(dataDir string) string {
	return filepath.Join(dataDir, "metadata", "metadata.db")
}

// sqliteTableExists 对齐 _sqlite_table_exists
func sqliteTableExists(conn MigrationConn, table string) (bool, error) {
	var found int
	err := conn.QueryRow(
		"SELECT 1 FROM sqlite_master WHERE type='table' AND name=? LIMIT 1", table,
	).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// sqliteColumnExists 对齐 _sqlite_column_exists
func sqliteColumnExists(conn MigrationConn, table string, column string) (bool, error) {
	exists, err := sqliteTableExists(conn, table)
	if err != nil || !exists {
		return false, err
	}
	rows, err := conn.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()

	columns, err := rows.Columns()
	if err != nil {
		return false, err
	}
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return false, err
		}
		if len(values) < 2 {
			continue
		}
		if fmt.Sprint(normalizeSQLiteValue(values[1])) == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

// ensureMigrationTable 对齐 _ensure_migration_table
func ensureMigrationTable(conn MigrationConn) error {
	_, err := conn.Exec(migrationTableDDL)
	return err
}

// migrationRecordExists 对齐 _migration_record_exists
func migrationRecordExists(conn MigrationConn) (bool, error) {
	exists, err := sqliteTableExists(conn, "storage_format_migrations")
	if err != nil || !exists {
		return false, err
	}
	var found int
	err = conn.QueryRow(
		"SELECT 1 FROM storage_format_migrations WHERE version = ? LIMIT 1", FormatMigrationVersion,
	).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// sqliteLegacyMetadataExists 对齐 _sqlite_legacy_metadata_exists
func sqliteLegacyMetadataExists(conn MigrationConn) (bool, error) {
	for _, table := range metadataTables {
		exists, err := sqliteColumnExists(conn, table, "metadata")
		if err != nil {
			return false, err
		}
		if !exists {
			continue
		}
		var found int
		err = conn.QueryRow(fmt.Sprintf(
			"SELECT 1 FROM %s WHERE metadata IS NOT NULL AND typeof(metadata) = 'blob' LIMIT 1", table,
		)).Scan(&found)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	return false, nil
}

// insertMigrationRecord 对齐 ON CONFLICT(version) DO NOTHING 的完成记录写入
func insertMigrationRecord(conn MigrationConn, summary *FormatMigrationSummary) error {
	text, err := migrationSummaryJSON(summary)
	if err != nil {
		return err
	}
	_, err = conn.Exec(insertMigrationRecordSQL, FormatMigrationVersion, nowTimestamp(), text)
	return err
}

// pathExists 对齐 pathlib.Path.exists
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// pythonSuffix 对齐 pathlib.Path.suffix：仅最后一个点之后的部分，点名以点开头时没有后缀
func pythonSuffix(path string) string {
	name := filepath.Base(path)
	if name == "." || name == ".." {
		return ""
	}
	index := strings.LastIndexByte(name, '.')
	if index <= 0 {
		return ""
	}
	return name[index:]
}

// withPathSuffix 对齐 pathlib.Path.with_suffix
func withPathSuffix(path string, suffix string) string {
	name := filepath.Base(path)
	base := name
	if ext := pythonSuffix(path); ext != "" {
		base = strings.TrimSuffix(name, ext)
	}
	return filepath.Join(filepath.Dir(path), base+suffix)
}

// backupLegacyFile 对齐 _backup_legacy_file：改名成功返回备份路径
func backupLegacyFile(path string) (string, error) {
	backupPath := withPathSuffix(path, pythonSuffix(path)+".bak")
	if pathExists(backupPath) {
		backupPath = withPathSuffix(path, fmt.Sprintf("%s.bak.%d", pythonSuffix(path), time.Now().UnixNano()))
	}
	if err := os.Rename(path, backupPath); err != nil {
		return "", err
	}
	return backupPath, nil
}

// legacyBackupCandidates 对齐 _legacy_backup_candidates：按修改时间倒序，最新的在最前
func legacyBackupCandidates(path string) []string {
	candidates := make([]string, 0, 4)
	direct := withPathSuffix(path, pythonSuffix(path)+".bak")
	if pathExists(direct) {
		candidates = append(candidates, direct)
	}

	dir := filepath.Dir(path)
	prefix := filepath.Base(path) + ".bak."
	entries, err := os.ReadDir(dir)
	if err == nil {
		matches := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			matches = append(matches, filepath.Join(dir, entry.Name()))
		}
		// Python 的 glob 顺序不确定，这里固定字典序后再按修改时间稳定排序
		sort.Strings(matches)
		candidates = append(candidates, matches...)
	}
	sort.SliceStable(candidates, func(i int, j int) bool {
		return fileModTime(candidates[i]).After(fileModTime(candidates[j]))
	})
	return candidates
}

// fileModTime 读取修改时间，读取失败时返回零值以保证排序稳定
func fileModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}
