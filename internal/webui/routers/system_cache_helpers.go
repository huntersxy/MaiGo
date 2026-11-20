package webuiapi

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/glebarez/go-sqlite"
)

// 本文件对齐 ../MaiBot/src/webui/routers/system.py 中本地缓存相关的文件系统逻辑

// localCacheStatsCacheTTL 对齐 Python _LOCAL_CACHE_STATS_CACHE_TTL_SECONDS
const localCacheStatsCacheTTL = 120 * time.Second

var (
	localCacheStatsMu      sync.Mutex
	localCacheStatsCache   *localCacheStatsResponse
	localCacheStatsExpiry  time.Time
	databaseStatsCache     *databaseStorageStats
	databaseStatsExpiry    time.Time
)

type localCacheStatsResponse struct {
	Directories []cacheDirectoryStats `json:"directories"`
	Database    databaseStorageStats  `json:"database"`
}

var cacheImageExtensions = map[string]bool{
	".avif": true, ".bmp": true, ".gif": true, ".jpeg": true,
	".jpg": true, ".png": true, ".webp": true,
}

type systemCachePaths struct {
	Root       string
	Data       string
	Images     string
	Emoji      string
	Thumbnails string
	Logs       string
	Database   string
}

func resolveSystemCachePaths(instanceDir string) (systemCachePaths, error) {
	root := strings.TrimSpace(instanceDir)
	if root == "" {
		return systemCachePaths{}, errors.New("需要设置 MAIBOT_INSTANCE_DIR 指向 MaiBot 实例根目录")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return systemCachePaths{}, err
	}
	data := filepath.Join(abs, "data")
	return systemCachePaths{
		Root: abs, Data: data,
		Images: filepath.Join(data, "images"), Emoji: filepath.Join(data, "emoji"),
		Thumbnails: filepath.Join(data, "emoji_thumbnails"), Logs: filepath.Join(abs, "logs"),
		Database: filepath.Join(data, "MaiBot.db"),
	}, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

type pathSummary struct {
	FileCount int
	TotalSize int64
	Modified  float64
}

func summarizePath(path string) pathSummary {
	info, err := os.Stat(path)
	if err != nil {
		return pathSummary{}
	}
	if info.Mode().IsRegular() {
		return pathSummary{FileCount: 1, TotalSize: info.Size(), Modified: float64(info.ModTime().UnixNano()) / 1e9}
	}
	summary := pathSummary{}
	_ = filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		fileInfo, err := entry.Info()
		if err != nil || !fileInfo.Mode().IsRegular() {
			return nil
		}
		summary.FileCount++
		summary.TotalSize += fileInfo.Size()
		if modified := float64(fileInfo.ModTime().UnixNano()) / 1e9; modified > summary.Modified {
			summary.Modified = modified
		}
		return nil
	})
	return summary
}

func listFiles(root string) []string {
	files := []string{}
	_ = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if !entry.IsDir() && entry.Type().IsRegular() {
			files = append(files, current)
		}
		return nil
	})
	return files
}

func isCacheImageFile(path string) bool {
	return cacheImageExtensions[strings.ToLower(filepath.Ext(path))]
}

// resolveWithin 把 relative 限制在 root 内（含符号链接解析），返回绝对路径
func resolveWithin(root, relative string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootAbs = filepath.Clean(rootAbs)
	target := filepath.Clean(filepath.Join(rootAbs, relative))
	if target != rootAbs && !strings.HasPrefix(target, rootAbs+string(os.PathSeparator)) {
		return "", errors.New("路径越界")
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		if os.IsNotExist(err) {
			return target, nil
		}
		return "", err
	}
	resolved = filepath.Clean(resolved)
	if resolved != rootAbs && !strings.HasPrefix(resolved, rootAbs+string(os.PathSeparator)) {
		return "", errors.New("路径越界")
	}
	return resolved, nil
}

func isPathWithin(path, parent string) bool {
	resolvedPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	resolvedParent, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	resolvedPath, resolvedParent = filepath.Clean(resolvedPath), filepath.Clean(resolvedParent)
	return resolvedPath == resolvedParent || strings.HasPrefix(resolvedPath, resolvedParent+string(os.PathSeparator))
}

type cacheDirectoryStats struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Path      string `json:"path"`
	Exists    bool   `json:"exists"`
	FileCount int    `json:"file_count"`
	TotalSize int64  `json:"total_size"`
	DBRecords int64  `json:"db_records"`
}

type databaseFileStats struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Size   int64  `json:"size"`
}

// databaseTableStats 对齐 Python DatabaseTableStats
type databaseTableStats struct {
	Name              string  `json:"name"`
	Rows              int64   `json:"rows"`
	Size              int64   `json:"size"`
	SizeSource        string  `json:"size_source"`
	Label             string  `json:"label"`
	Category          string  `json:"category"`
	Description       string  `json:"description"`
	CleanupSupported  bool    `json:"cleanup_supported"`
	CleanupDateColumn *string `json:"cleanup_date_column"`
}

type databaseStorageStats struct {
	Files         []databaseFileStats  `json:"files"`
	Tables        []databaseTableStats `json:"tables"`
	TotalSize     int64                `json:"total_size"`
	PageSize      int64                `json:"page_size"`
	PageCount     int64                `json:"page_count"`
	FreelistCount int64                `json:"freelist_count"`
	FreeSize      int64                `json:"free_size"`
	Backend       string               `json:"backend"`
}

// databaseCleanupMeta 对齐 Python _DATABASE_CLEANUP_TABLES
type databaseCleanupMeta struct {
	Label       string
	Category    string
	Description string
	DateColumn  string
}

var databaseCleanupTables = map[string]databaseCleanupMeta{
	"llm_usage": {
		Label: "LLM 调用记录", Category: "调用与统计",
		Description: "模型调用、Token、耗时和费用记录。", DateColumn: "timestamp",
	},
	"tool_records": {
		Label: "工具调用记录", Category: "调用与统计",
		Description: "内置工具和插件工具的调用过程记录。", DateColumn: "timestamp",
	},
	"mai_messages": {
		Label: "消息记录", Category: "聊天历史",
		Description: "收到的聊天消息与消息元数据。", DateColumn: "timestamp",
	},
	"chat_history": {
		Label: "聊天摘要历史", Category: "聊天历史",
		Description: "聊天片段摘要、主题和关键词记录。", DateColumn: "end_timestamp",
	},
	"online_time": {
		Label: "在线时长记录", Category: "运行统计",
		Description: "运行在线时长统计记录。", DateColumn: "timestamp",
	},
	"statistics_message_hourly": {
		Label: "消息小时统计", Category: "统计缓存",
		Description: "按小时聚合的消息统计缓存。", DateColumn: "bucket_time",
	},
	"statistics_tool_hourly": {
		Label: "工具小时统计", Category: "统计缓存",
		Description: "按小时聚合的工具调用统计缓存。", DateColumn: "bucket_time",
	},
	"statistics_model_hourly": {
		Label: "模型小时统计", Category: "统计缓存",
		Description: "按小时聚合的模型调用统计缓存。", DateColumn: "bucket_time",
	},
}

var (
	errDatabaseCleanupEmptyTables = errors.New("请至少选择一个要清理的数据库表")
	errDatabaseCleanupNeedDays    = errors.New("按时间清理时必须设置保留天数")
)

func buildDatabaseStorage(paths systemCachePaths) databaseStorageStats {
	return buildDatabaseStorageOpts(paths, true)
}

// buildDatabaseStorageOpts 对齐 Python _build_database_stats(include_tables=...)
func buildDatabaseStorageOpts(paths systemCachePaths, includeTables bool) databaseStorageStats {
	stats := databaseStorageStats{Tables: []databaseTableStats{}, Backend: "sqlite"}
	for _, candidate := range []string{paths.Database, paths.Database + "-wal", paths.Database + "-shm"} {
		fileStat := databaseFileStats{Path: candidate}
		if info, err := os.Stat(candidate); err == nil {
			fileStat.Exists = true
			fileStat.Size = info.Size()
		}
		stats.Files = append(stats.Files, fileStat)
		stats.TotalSize += fileStat.Size
	}
	if pageSize, pageCount, freelistCount, err := readSQLitePageStats(paths.Database); err == nil {
		stats.PageSize = pageSize
		stats.PageCount = pageCount
		stats.FreelistCount = freelistCount
		stats.FreeSize = pageSize * freelistCount
	}
	if includeTables {
		if tables, err := listDatabaseTableStats(paths.Database); err == nil {
			stats.Tables = tables
		}
	}
	return stats
}

func buildLocalCacheStatsResponse(paths systemCachePaths) localCacheStatsResponse {
	return localCacheStatsResponse{
		Directories: []cacheDirectoryStats{
			buildDirectoryStats("images", "图片缓存", paths.Images),
			buildDirectoryStats("emoji", "表情包缓存", paths.Emoji, paths.Thumbnails),
			buildDirectoryStats("logs", "日志文件", paths.Logs),
		},
		Database: buildDatabaseStorageOpts(paths, false),
	}
}

func getCachedLocalCacheStats() (localCacheStatsResponse, bool) {
	localCacheStatsMu.Lock()
	defer localCacheStatsMu.Unlock()
	if localCacheStatsCache == nil || time.Now().After(localCacheStatsExpiry) {
		return localCacheStatsResponse{}, false
	}
	return *localCacheStatsCache, true
}

func storeLocalCacheStats(response localCacheStatsResponse) localCacheStatsResponse {
	localCacheStatsMu.Lock()
	defer localCacheStatsMu.Unlock()
	copied := response
	localCacheStatsCache = &copied
	localCacheStatsExpiry = time.Now().Add(localCacheStatsCacheTTL)
	return response
}

func getCachedDatabaseStats() (databaseStorageStats, bool) {
	localCacheStatsMu.Lock()
	defer localCacheStatsMu.Unlock()
	if databaseStatsCache == nil || time.Now().After(databaseStatsExpiry) {
		return databaseStorageStats{}, false
	}
	return *databaseStatsCache, true
}

func storeDatabaseStats(response databaseStorageStats) databaseStorageStats {
	localCacheStatsMu.Lock()
	defer localCacheStatsMu.Unlock()
	copied := response
	databaseStatsCache = &copied
	databaseStatsExpiry = time.Now().Add(localCacheStatsCacheTTL)
	return response
}

func invalidateLocalCacheStatsCache() {
	localCacheStatsMu.Lock()
	defer localCacheStatsMu.Unlock()
	localCacheStatsCache = nil
	databaseStatsCache = nil
	localCacheStatsExpiry = time.Time{}
	databaseStatsExpiry = time.Time{}
}

func quoteSQLiteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func listSQLiteTableNames(db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = struct{}{}
	}
	return out, rows.Err()
}

func listDatabaseTableStats(dbPath string) ([]databaseTableStats, error) {
	db, err := openSQLiteFile(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	existing, err := listSQLiteTableNames(db)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(existing))
	for name := range existing {
		names = append(names, name)
	}
	sort.Strings(names)

	dbstatSizes := readDBStatTableSizes(db, names)
	out := make([]databaseTableStats, 0, len(names))
	for _, name := range names {
		quoted := quoteSQLiteIdentifier(name)
		var rowCount int64
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + quoted).Scan(&rowCount); err != nil {
			return nil, err
		}
		meta, cleanupOK := databaseCleanupTables[name]
		item := databaseTableStats{
			Name: name, Rows: rowCount, SizeSource: "estimated",
			Label: name, Category: "其他", CleanupSupported: cleanupOK,
		}
		if cleanupOK {
			item.Label = meta.Label
			item.Category = meta.Category
			item.Description = meta.Description
			dc := meta.DateColumn
			item.CleanupDateColumn = &dc
		}
		if dbstatSizes != nil {
			item.Size = dbstatSizes[name]
			item.SizeSource = "dbstat"
		} else {
			item.Size = estimateTableDataSize(db, name, rowCount)
		}
		out = append(out, item)
	}
	return out, nil
}

func readDBStatTableSizes(db *sql.DB, tableNames []string) map[string]int64 {
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS temp.local_cache_dbstat USING dbstat(main)`); err != nil {
		return nil
	}
	rows, err := db.Query(`SELECT name, SUM(pgsize) AS size FROM temp.local_cache_dbstat GROUP BY name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	objectSizes := map[string]int64{}
	for rows.Next() {
		var name string
		var size int64
		if err := rows.Scan(&name, &size); err != nil {
			return nil
		}
		objectSizes[name] = size
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	out := map[string]int64{}
	for _, tableName := range tableNames {
		tableSize := objectSizes[tableName]
		for _, indexName := range listTableIndexes(db, tableName) {
			tableSize += objectSizes[indexName]
		}
		out[tableName] = tableSize
	}
	return out
}

func listTableIndexes(db *sql.DB, tableName string) []string {
	quoted := quoteSQLiteIdentifier(tableName)
	rows, err := db.Query(`PRAGMA index_list(` + quoted + `)`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil || len(cols) < 2 {
		return nil
	}
	var indexes []string
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		name := fmt.Sprint(values[1])
		if name != "" && name != "<nil>" {
			indexes = append(indexes, name)
		}
	}
	return indexes
}

func estimateTableDataSize(db *sql.DB, tableName string, rows int64) int64 {
	if rows <= 0 {
		return 0
	}
	colRows, err := db.Query(`PRAGMA table_info(` + quoteSQLiteIdentifier(tableName) + `)`)
	if err != nil {
		return 0
	}
	defer colRows.Close()
	var expressions []string
	for colRows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := colRows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return 0
		}
		expressions = append(expressions, fmt.Sprintf("COALESCE(LENGTH(CAST(%s AS BLOB)), 0)", quoteSQLiteIdentifier(name)))
	}
	if len(expressions) == 0 {
		return 0
	}
	sampleLimit := rows
	if sampleLimit > 200 {
		sampleLimit = 200
	}
	query := fmt.Sprintf(
		"SELECT COALESCE(SUM(%s), 0), COUNT(*) FROM (SELECT * FROM %s LIMIT %d)",
		strings.Join(expressions, " + "), quoteSQLiteIdentifier(tableName), sampleLimit,
	)
	var sampleSize, sampleRows int64
	if err := db.QueryRow(query).Scan(&sampleSize, &sampleRows); err != nil || sampleRows == 0 {
		return 0
	}
	return sampleSize * rows / sampleRows
}

// deleteDatabaseRecords 对齐 Python _delete_database_records
func deleteDatabaseRecords(dbPath string, tableNames []string, mode string, olderThanDays *int) (int64, error) {
	if len(tableNames) == 0 {
		return 0, errDatabaseCleanupEmptyTables
	}
	invalid := map[string]struct{}{}
	for _, name := range tableNames {
		if _, ok := databaseCleanupTables[name]; !ok {
			invalid[name] = struct{}{}
		}
	}
	if len(invalid) > 0 {
		names := make([]string, 0, len(invalid))
		for name := range invalid {
			names = append(names, name)
		}
		sort.Strings(names)
		return 0, fmt.Errorf("不支持清理这些表: %s", strings.Join(names, ", "))
	}
	if mode == "" {
		mode = "all"
	}
	if mode == "older_than_days" && olderThanDays == nil {
		return 0, errDatabaseCleanupNeedDays
	}

	db, err := openSQLiteFile(dbPath)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	existing, err := listSQLiteTableNames(db)
	if err != nil {
		return 0, err
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var removed int64
	for _, tableName := range tableNames {
		if _, ok := existing[tableName]; !ok {
			continue
		}
		quotedTable := quoteSQLiteIdentifier(tableName)
		var result sql.Result
		if mode == "all" {
			result, err = tx.Exec(`DELETE FROM ` + quotedTable)
		} else {
			days := 0
			if olderThanDays != nil {
				days = *olderThanDays
			}
			cutoff := time.Now().AddDate(0, 0, -days)
			quotedCol := quoteSQLiteIdentifier(databaseCleanupTables[tableName].DateColumn)
			result, err = tx.Exec(`DELETE FROM `+quotedTable+` WHERE `+quotedCol+` < ?`, cutoff)
		}
		if err != nil {
			return 0, err
		}
		affected, _ := result.RowsAffected()
		removed += affected
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return removed, nil
}

func openSQLiteFile(path string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("数据库路径不是普通文件")
	}
	// mode=rwc 允许 VACUUM；只读统计也走同一打开方式，文件不存在时上层已判断
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func readSQLitePageStats(dbPath string) (pageSize, pageCount, freelistCount int64, err error) {
	if _, statErr := os.Stat(dbPath); statErr != nil {
		return 0, 0, 0, statErr
	}
	db, err := openSQLiteFile(dbPath)
	if err != nil {
		return 0, 0, 0, err
	}
	defer db.Close()
	if err := db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		return 0, 0, 0, err
	}
	if err := db.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		return 0, 0, 0, err
	}
	if err := db.QueryRow("PRAGMA freelist_count").Scan(&freelistCount); err != nil {
		return 0, 0, 0, err
	}
	return pageSize, pageCount, freelistCount, nil
}

func databaseTotalSize(paths systemCachePaths) int64 {
	var total int64
	for _, candidate := range []string{paths.Database, paths.Database + "-wal", paths.Database + "-shm"} {
		if info, err := os.Stat(candidate); err == nil {
			total += info.Size()
		}
	}
	return total
}

// checkpointSQLite 对齐 Python _checkpoint_database：PRAGMA wal_checkpoint(TRUNCATE)
func checkpointSQLite(dbPath string) (busy, log, checkpointed int) {
	if _, err := os.Stat(dbPath); err != nil {
		return 0, 0, 0
	}
	db, err := openSQLiteFile(dbPath)
	if err != nil {
		return 0, 0, 0
	}
	defer db.Close()
	row := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)")
	if err := row.Scan(&busy, &log, &checkpointed); err != nil {
		return 0, 0, 0
	}
	return busy, log, checkpointed
}

// vacuumSQLiteDatabase 对齐 Python _vacuum_database_response
func vacuumSQLiteDatabase(paths systemCachePaths) (map[string]any, error) {
	if _, err := os.Stat(paths.Database); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errDatabaseMissing
		}
		return nil, err
	}
	before := databaseTotalSize(paths)
	_, _, _ = checkpointSQLite(paths.Database) // discard pre-vacuum checkpoint counts like Python

	db, err := openSQLiteFile(paths.Database)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("VACUUM"); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = db.Close()

	busy, log, checkpointed := checkpointSQLite(paths.Database)
	after := databaseTotalSize(paths)
	reclaimed := before - after
	if reclaimed < 0 {
		reclaimed = 0
	}
	return map[string]any{
		"success":              true,
		"message":              "数据库 VACUUM 已完成",
		"database_size_before": before,
		"database_size_after":  after,
		"reclaimed_bytes":      reclaimed,
		"checkpoint_busy":      busy,
		"checkpoint_log":       log,
		"checkpointed":         checkpointed,
	}, nil
}

var errDatabaseMissing = errors.New("数据库文件不存在")

func buildDirectoryStats(key, label, path string, extra ...string) cacheDirectoryStats {
	summary := summarizePath(path)
	stats := cacheDirectoryStats{Key: key, Label: label, Path: path, Exists: pathExists(path), FileCount: summary.FileCount, TotalSize: summary.TotalSize}
	for _, candidate := range extra {
		extraSummary := summarizePath(candidate)
		stats.FileCount += extraSummary.FileCount
		stats.TotalSize += extraSummary.TotalSize
		stats.Exists = stats.Exists || pathExists(candidate)
	}
	return stats
}

type cacheDataEntry struct {
	RelativePath     string  `json:"relative_path"`
	Name             string  `json:"name"`
	FullPath         string  `json:"full_path"`
	Kind             string  `json:"kind"`
	FileCount        int     `json:"file_count"`
	TotalSize        int64   `json:"total_size"`
	ModifiedTime     float64 `json:"modified_time"`
	Protected        bool    `json:"protected"`
	ProtectionReason *string `json:"protection_reason"`
}

func dataProtectionReason(path string, paths systemCachePaths) string {
	resolved := filepath.Clean(path)
	if resolved == filepath.Clean(paths.Data) {
		return "data 根目录不能删除"
	}
	for _, protected := range []string{paths.Database, paths.Database + "-wal", paths.Database + "-shm"} {
		if resolved == filepath.Clean(protected) {
			return "当前正在使用的主数据库文件不能在这里删除"
		}
	}
	for _, cacheDir := range []string{paths.Images, paths.Emoji, paths.Thumbnails} {
		if isPathWithin(resolved, cacheDir) {
			return "图片、表情和缩略图缓存请使用上方专用清理入口，以同步数据库记录"
		}
	}
	return ""
}

func buildDataEntry(path string, paths systemCachePaths) cacheDataEntry {
	summary := summarizePath(path)
	relative, err := filepath.Rel(paths.Data, path)
	if err != nil {
		relative = filepath.Base(path)
	}
	kind := "file"
	if isDir(path) {
		kind = "directory"
	}
	reason := dataProtectionReason(path, paths)
	entry := cacheDataEntry{
		RelativePath: filepath.ToSlash(relative), Name: filepath.Base(path), FullPath: path,
		Kind: kind, FileCount: summary.FileCount, TotalSize: summary.TotalSize,
		ModifiedTime: summary.Modified, Protected: reason != "",
	}
	if reason != "" {
		entry.ProtectionReason = &reason
	}
	return entry
}

func sortDataEntries(entries []cacheDataEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if (entries[i].Kind == "directory") != (entries[j].Kind == "directory") {
			return entries[i].Kind == "directory"
		}
		if entries[i].TotalSize != entries[j].TotalSize {
			return entries[i].TotalSize > entries[j].TotalSize
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
}

type cacheLogDirectory struct {
	RelativePath  string  `json:"relative_path"`
	Name          string  `json:"name"`
	FullPath      string  `json:"full_path"`
	Depth         int     `json:"depth"`
	FileCount     int     `json:"file_count"`
	TotalSize     int64   `json:"total_size"`
	ModifiedTime  float64 `json:"modified_time"`
	RootFilesOnly bool    `json:"root_files_only"`
}

func buildLogDirectories(logsDir string) []cacheLogDirectory {
	if !isDir(logsDir) {
		return []cacheLogDirectory{}
	}
	items := []cacheLogDirectory{}
	entries, err := os.ReadDir(logsDir)
	if err == nil {
		rootFiles := []string{}
		for _, entry := range entries {
			if !entry.IsDir() {
				rootFiles = append(rootFiles, filepath.Join(logsDir, entry.Name()))
			}
		}
		if len(rootFiles) > 0 {
			summary := pathSummary{}
			for _, file := range rootFiles {
				fileSummary := summarizePath(file)
				summary.FileCount += fileSummary.FileCount
				summary.TotalSize += fileSummary.TotalSize
				if fileSummary.Modified > summary.Modified {
					summary.Modified = fileSummary.Modified
				}
			}
			items = append(items, cacheLogDirectory{
				RelativePath: "", Name: "根目录文件", FullPath: logsDir, Depth: 0,
				FileCount: summary.FileCount, TotalSize: summary.TotalSize,
				ModifiedTime: summary.Modified, RootFilesOnly: true,
			})
		}
	}
	directories := []string{}
	_ = filepath.WalkDir(logsDir, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr == nil && entry.IsDir() && current != logsDir {
			directories = append(directories, current)
		}
		return nil
	})
	sort.Strings(directories)
	for _, directory := range directories {
		relative, err := filepath.Rel(logsDir, directory)
		if err != nil {
			continue
		}
		summary := summarizePath(directory)
		items = append(items, cacheLogDirectory{
			RelativePath: filepath.ToSlash(relative), Name: filepath.Base(directory), FullPath: directory,
			Depth:     len(strings.Split(filepath.ToSlash(relative), "/")),
			FileCount: summary.FileCount, TotalSize: summary.TotalSize, ModifiedTime: summary.Modified,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].RootFilesOnly != items[j].RootFilesOnly {
			return items[i].RootFilesOnly
		}
		return items[i].RelativePath < items[j].RelativePath
	})
	return items
}

func resolveLogDirectory(logsDir, relative string) (string, error) {
	resolved, err := resolveWithin(logsDir, relative)
	if err != nil {
		return "", errors.New("无效的日志目录路径")
	}
	if filepath.Clean(resolved) == filepath.Clean(logsDir) {
		return resolved, nil
	}
	if !isDir(resolved) {
		return "", e404("未找到指定日志目录")
	}
	return resolved, nil
}

func removeDirectoryContents(directory string) (int, int64) {
	if !isDir(directory) {
		return 0, 0
	}
	removedFiles, removedBytes := 0, int64(0)
	for _, file := range listFiles(directory) {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}
		if err := os.Remove(file); err != nil {
			continue
		}
		removedFiles++
		removedBytes += info.Size()
	}
	dirs := []string{}
	_ = filepath.WalkDir(directory, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr == nil && entry.IsDir() && current != directory {
			dirs = append(dirs, current)
		}
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		_ = os.Remove(dir)
	}
	return removedFiles, removedBytes
}

func removeDirectFiles(directory string) (int, int64) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, 0
	}
	removedFiles, removedBytes := 0, int64(0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		file := filepath.Join(directory, entry.Name())
		info, err := os.Stat(file)
		if err != nil {
			continue
		}
		if err := os.Remove(file); err != nil {
			continue
		}
		removedFiles++
		removedBytes += info.Size()
	}
	return removedFiles, removedBytes
}

func cacheCleanupResult(target, message string, removedFiles int, removedBytes int64) map[string]any {
	return map[string]any{
		"success": true, "message": message, "target": target,
		"removed_files": removedFiles, "removed_bytes": removedBytes, "removed_records": 0,
		"vacuumed": false, "reclaimed_bytes": 0,
	}
}

type e404 string

func (e e404) Error() string { return string(e) }

func cacheNowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }
