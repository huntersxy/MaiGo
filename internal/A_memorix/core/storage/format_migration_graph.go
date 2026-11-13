package storage

// 图元数据 pickle 转 JSON 与边到关系哈希映射表的迁移
// 对齐 Python format_migration.py 的 ensure_graph_edge_map_table/_normalize_edge_key/_edge_map_rows/
// _replace_graph_edge_map/_graph_edge_map_count/_json_edge_hash_map/_prepare_graph_metadata_payload/
// _load_graph_metadata_from_pickle/_validate_graph_metadata_json/_recover_graph_metadata_from_backup/
// _migrate_graph_metadata 以及 _graph_format_work

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// graphEdgeMapDDL 对齐 ensure_graph_edge_map_table 的建表语句
const graphEdgeMapDDL = "CREATE TABLE IF NOT EXISTS graph_edge_relation_map (" +
	"src_idx INTEGER NOT NULL, " +
	"dst_idx INTEGER NOT NULL, " +
	"relation_hash TEXT NOT NULL, " +
	"PRIMARY KEY (src_idx, dst_idx, relation_hash))"

// graphEdgeMapIndexDDL 对齐 ensure_graph_edge_map_table 的索引语句
const graphEdgeMapIndexDDL = "CREATE INDEX IF NOT EXISTS idx_graph_edge_relation_hash " +
	"ON graph_edge_relation_map(relation_hash)"

// graphInsertEdgeSQL 对齐 _replace_graph_edge_map 的写入语句
const graphInsertEdgeSQL = "INSERT OR IGNORE INTO graph_edge_relation_map (src_idx, dst_idx, relation_hash) VALUES (?, ?, ?)"

// graphMetadataRequiredKeys 对齐 _validate_graph_metadata_json 的必需字段
var graphMetadataRequiredKeys = []string{
	"nodes",
	"node_to_idx",
	"node_attrs",
	"matrix_format",
	"total_nodes_added",
	"total_edges_added",
	"total_nodes_deleted",
	"total_edges_deleted",
}

// normalizeEdgeKey 对齐 _normalize_edge_key
// 无法识别时返回 ok=false；整数转换失败时返回错误，与 Python int() 抛异常一致
func normalizeEdgeKey(key any) (int, int, bool, error) {
	switch typed := key.(type) {
	case []any:
		if len(typed) == 2 {
			return edgeKeyPair(typed[0], typed[1])
		}
	case pickleTupleValue:
		if len(typed) == 2 {
			return edgeKeyPair(typed[0], typed[1])
		}
	case string:
		normalized := strings.TrimSpace(typed)
		normalized = strings.ReplaceAll(normalized, "(", "")
		normalized = strings.ReplaceAll(normalized, ")", "")
		for _, separator := range []string{",", ":", "|"} {
			index := strings.Index(normalized, separator)
			if index < 0 {
				continue
			}
			left := strings.TrimSpace(normalized[:index])
			right := strings.TrimSpace(normalized[index+len(separator):])
			return edgeKeyPair(left, right)
		}
	}
	return 0, 0, false, nil
}

// edgeKeyPair 转换一对边端点
func edgeKeyPair(rawSrc any, rawDst any) (int, int, bool, error) {
	src, err := pythonInt(rawSrc)
	if err != nil {
		return 0, 0, false, err
	}
	dst, err := pythonInt(rawDst)
	if err != nil {
		return 0, 0, false, err
	}
	return src, dst, true, nil
}

// graphEdgeRow 对齐 (src_idx, dst_idx, relation_hash) 三元组
type graphEdgeRow struct {
	SrcIdx       int
	DstIdx       int
	RelationHash string
}

// edgeMapRows 对齐 _edge_map_rows：规范化失败跳过、并按出现顺序去重
func edgeMapRows(edgeHashMap any) ([]graphEdgeRow, error) {
	rows := make([]graphEdgeRow, 0, 8)
	object, ok := edgeHashMap.(map[string]any)
	if !ok {
		return rows, nil
	}

	// Python dict 保序，这里按 key 排序保证结果稳定
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	seen := make(map[graphEdgeRow]struct{}, len(keys))
	for _, key := range keys {
		srcIdx, dstIdx, ok, err := normalizeEdgeKey(key)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		for _, rawHash := range edgeHashValues(object[key]) {
			relationHash := strings.TrimSpace(relationHashText(rawHash))
			if relationHash == "" {
				continue
			}
			item := graphEdgeRow{SrcIdx: srcIdx, DstIdx: dstIdx, RelationHash: relationHash}
			if _, exists := seen[item]; exists {
				continue
			}
			seen[item] = struct{}{}
			rows = append(rows, item)
		}
	}
	return rows, nil
}

// edgeHashValues 对齐 isinstance(raw_hashes, (list, tuple, set)) 的分支
func edgeHashValues(raw any) []any {
	switch typed := raw.(type) {
	case []any:
		return typed
	case pickleTupleValue:
		return []any(typed)
	case pickleSet:
		return typed.items
	}
	return []any{raw}
}

// relationHashText 对齐 str(raw_hash or "").strip() 中的 str 与空值判断
func relationHashText(raw any) string {
	if !pythonTruthy(raw) {
		return ""
	}
	return pythonStr(raw)
}

// replaceGraphEdgeMap 对齐 _replace_graph_edge_map，返回输入行数
func replaceGraphEdgeMap(conn MigrationConn, rows []graphEdgeRow) (int, error) {
	if err := EnsureGraphEdgeMapTable(conn); err != nil {
		return 0, err
	}
	if _, err := conn.Exec("DELETE FROM graph_edge_relation_map"); err != nil {
		return 0, err
	}
	for _, row := range rows {
		if _, err := conn.Exec(graphInsertEdgeSQL, row.SrcIdx, row.DstIdx, row.RelationHash); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

// graphEdgeMapCount 对齐 _graph_edge_map_count
func graphEdgeMapCount(conn MigrationConn) (int, error) {
	exists, err := sqliteTableExists(conn, "graph_edge_relation_map")
	if err != nil || !exists {
		return 0, err
	}
	var count int64
	if err := conn.QueryRow("SELECT COUNT(*) FROM graph_edge_relation_map").Scan(&count); err != nil {
		return 0, err
	}
	return int(count), nil
}

// jsonEdgeHashMap 对齐 _json_edge_hash_map：把边映射写回 JSON 结构
func jsonEdgeHashMap(edgeHashMap any) (map[string]any, error) {
	rows, err := edgeMapRows(edgeHashMap)
	if err != nil {
		return nil, err
	}
	payload := make(map[string]any, len(rows))
	for _, row := range rows {
		key := fmt.Sprintf("%d,%d", row.SrcIdx, row.DstIdx)
		items, _ := payload[key].([]any)
		payload[key] = append(items, row.RelationHash)
	}
	return payload, nil
}

// PrepareGraphMetadataPayload 对齐 _prepare_graph_metadata_payload
// conn 非空时把边写进映射表并移除 edge_hash_map，否则把边写回 JSON 结构
func PrepareGraphMetadataPayload(payload map[string]any, path string, conn MigrationConn) (map[string]any, int, error) {
	nodes, ok := payload["nodes"].([]any)
	if !ok {
		return nil, 0, fmt.Errorf("图元数据缺少 nodes 列表: %s", path)
	}
	if _, ok := payload["node_to_idx"].(map[string]any); !ok {
		return nil, 0, fmt.Errorf("图元数据缺少 node_to_idx 对象: %s", path)
	}

	edgeRows, err := edgeMapRows(payload["edge_hash_map"])
	if err != nil {
		return nil, 0, err
	}
	metadata := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		metadata[key] = value
	}
	setDefaultMetadata(metadata, "node_attrs", map[string]any{})
	setDefaultMetadata(metadata, "matrix_format", "csr")
	setDefaultMetadata(metadata, "total_nodes_added", int64(len(nodes)))
	setDefaultMetadata(metadata, "total_edges_added", int64(len(edgeRows)))
	setDefaultMetadata(metadata, "total_nodes_deleted", int64(0))
	setDefaultMetadata(metadata, "total_edges_deleted", int64(0))

	edgeCount := 0
	if conn != nil {
		edgeCount, err = replaceGraphEdgeMap(conn, edgeRows)
		if err != nil {
			return nil, 0, err
		}
		delete(metadata, "edge_hash_map")
	} else {
		edgeCount = len(edgeRows)
		hashMap, err := jsonEdgeHashMap(payload["edge_hash_map"])
		if err != nil {
			return nil, 0, err
		}
		metadata["edge_hash_map"] = hashMap
	}

	schemaVersion := 1
	if raw, ok := metadata["schema_version"]; ok && pythonTruthy(raw) {
		parsed, err := pythonInt(raw)
		if err != nil {
			return nil, 0, err
		}
		schemaVersion = parsed
	}
	metadata["schema_version"] = int64(schemaVersion)
	return metadata, edgeCount, nil
}

// migrationIntPtr 构造可选整数，用于区分字段缺失与零值
func migrationIntPtr(value int) *int { return &value }

// setDefaultMetadata 对齐 dict.setdefault：仅在键缺失时写入默认值
func setDefaultMetadata(metadata map[string]any, key string, value any) {
	if _, ok := metadata[key]; ok {
		return
	}
	metadata[key] = value
}

// LoadGraphMetadataFromPickle 对齐 _load_graph_metadata_from_pickle
func LoadGraphMetadataFromPickle(path string, conn MigrationConn) (map[string]any, int, error) {
	payload, err := loadLegacyPickleDict(path, "图元数据")
	if err != nil {
		return nil, 0, err
	}
	return PrepareGraphMetadataPayload(payload, path, conn)
}

// ValidateGraphMetadataJSON 对齐 _validate_graph_metadata_json
func ValidateGraphMetadataJSON(path string) error {
	payload, err := readJSONDict(path)
	if err != nil {
		return err
	}
	missing := make([]string, 0, len(graphMetadataRequiredKeys))
	for _, key := range graphMetadataRequiredKeys {
		if _, ok := payload[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("图元数据缺少字段: %s", strings.Join(missing, ", "))
	}
	_, _, err = PrepareGraphMetadataPayload(payload, path, nil)
	return err
}

// recoverGraphMetadataFromBackup 对齐 _recover_graph_metadata_from_backup
func recoverGraphMetadataFromBackup(jsonPath string, backupPath string, conn MigrationConn) (GraphMigrationEntry, error) {
	metadata, edgeCount, err := LoadGraphMetadataFromPickle(backupPath, conn)
	if err != nil {
		return GraphMigrationEntry{}, err
	}
	if err := writeJSONFile(jsonPath, metadata); err != nil {
		return GraphMigrationEntry{}, err
	}
	return GraphMigrationEntry{
		Migrated:        boolPtr(false),
		Recovered:       boolPtr(true),
		Reason:          "legacy_backup_recovered",
		Backup:          backupPath,
		EdgeHashMapRows: migrationIntPtr(edgeCount),
	}, nil
}

// MigrateGraphMetadata 对齐 _migrate_graph_metadata
func MigrateGraphMetadata(dataDir string, conn MigrationConn) (GraphMigrationEntry, error) {
	graphDir := filepath.Join(dataDir, "graph")
	picklePath := filepath.Join(graphDir, "graph_metadata.pkl")
	jsonPath := filepath.Join(graphDir, "graph_metadata.json")

	if !pathExists(picklePath) {
		backups := legacyBackupCandidates(picklePath)
		needsRecovery := false
		if pathExists(jsonPath) {
			if err := ValidateGraphMetadataJSON(jsonPath); err != nil {
				needsRecovery = true
			}
		} else {
			needsRecovery = len(backups) > 0
		}
		if conn != nil && pathExists(jsonPath) && len(backups) > 0 {
			count, err := graphEdgeMapCount(conn)
			if err != nil {
				return GraphMigrationEntry{}, err
			}
			if count == 0 {
				needsRecovery = true
			}
		}
		if needsRecovery && len(backups) > 0 {
			return recoverGraphMetadataFromBackup(jsonPath, backups[0], conn)
		}
		if needsRecovery {
			return GraphMigrationEntry{}, fmt.Errorf("图元数据 JSON 损坏且没有可恢复备份: %s", jsonPath)
		}
		return GraphMigrationEntry{Migrated: boolPtr(false), Reason: "legacy_missing"}, nil
	}

	metadata, edgeCount, err := LoadGraphMetadataFromPickle(picklePath, conn)
	if err != nil {
		return GraphMigrationEntry{}, err
	}

	reason := ""
	if pathExists(jsonPath) {
		if err := ValidateGraphMetadataJSON(jsonPath); err != nil {
			if err := writeJSONFile(jsonPath, metadata); err != nil {
				return GraphMigrationEntry{}, err
			}
			reason = "json_recovered_from_legacy"
		}
	} else if err := writeJSONFile(jsonPath, metadata); err != nil {
		return GraphMigrationEntry{}, err
	}

	backup, err := backupLegacyFile(picklePath)
	if err != nil {
		return GraphMigrationEntry{}, err
	}
	entry := GraphMigrationEntry{
		Migrated:        boolPtr(true),
		Backup:          backup,
		EdgeHashMapRows: migrationIntPtr(edgeCount),
	}
	if reason != "" {
		entry.Recovered = boolPtr(true)
		entry.Reason = reason
	}
	return entry, nil
}

// graphFormatWork 对齐 _graph_format_work，只做检测不落盘
func graphFormatWork(dataDir string, conn MigrationConn, migrationRecordExists bool) (GraphMigrationEntry, error) {
	graphDir := filepath.Join(dataDir, "graph")
	picklePath := filepath.Join(graphDir, "graph_metadata.pkl")
	jsonPath := filepath.Join(graphDir, "graph_metadata.json")
	backups := legacyBackupCandidates(picklePath)

	jsonValid := false
	jsonError := ""
	if pathExists(jsonPath) {
		if err := ValidateGraphMetadataJSON(jsonPath); err != nil {
			jsonError = err.Error()
		} else {
			jsonValid = true
		}
	}

	if pathExists(picklePath) {
		if migrationRecordExists && jsonValid {
			return GraphMigrationEntry{Action: "none", Reason: "stale_legacy_artifact", Error: &jsonError}, nil
		}
		reason := "legacy_recovery"
		switch {
		case !pathExists(jsonPath):
			reason = "legacy_pickle"
		case jsonValid:
			reason = "incomplete_migration"
		}
		return GraphMigrationEntry{Action: "migrate", Reason: reason}, nil
	}
	if !jsonValid && len(backups) > 0 {
		return GraphMigrationEntry{Action: "recover", Reason: "legacy_backup"}, nil
	}
	if !migrationRecordExists && jsonValid && len(backups) > 0 && conn != nil {
		count, err := graphEdgeMapCount(conn)
		if err != nil {
			return GraphMigrationEntry{}, err
		}
		if count == 0 {
			return GraphMigrationEntry{Action: "recover", Reason: "legacy_edge_map_backup"}, nil
		}
	}
	if jsonValid {
		return GraphMigrationEntry{Action: "none", Reason: "current_valid"}, nil
	}
	if pathExists(jsonPath) {
		return GraphMigrationEntry{Action: "none", Reason: "current_format_corrupt", Error: &jsonError}, nil
	}
	return GraphMigrationEntry{Action: "none", Reason: "legacy_missing"}, nil
}
