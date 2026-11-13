package storage

// A_memorix 关系生命周期、投影队列与外部引用重建，对齐 Python metadata_schema.py

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// ensureLifecycleColumns 建立关系级生命周期和段落显式过期字段
func ensureLifecycleColumns(conn *Conn) error {
	if err := ensureColumns(conn, "paragraphs", [][2]string{
		{"expires_at", "ALTER TABLE paragraphs ADD COLUMN expires_at REAL"},
		{"deletion_reason", "ALTER TABLE paragraphs ADD COLUMN deletion_reason TEXT"},
	}); err != nil {
		return err
	}

	relationColumns := [][2]string{
		{"retention_strength", "ALTER TABLE relations ADD COLUMN retention_strength REAL NOT NULL DEFAULT 1.0"},
		{"retention_anchor_at", "ALTER TABLE relations ADD COLUMN retention_anchor_at REAL"},
		{"next_lifecycle_at", "ALTER TABLE relations ADD COLUMN next_lifecycle_at REAL"},
		{"reinforcement_count", "ALTER TABLE relations ADD COLUMN reinforcement_count INTEGER NOT NULL DEFAULT 0"},
		{"lifecycle_revision", "ALTER TABLE relations ADD COLUMN lifecycle_revision INTEGER NOT NULL DEFAULT 0"},
		{"inactive_reason", "ALTER TABLE relations ADD COLUMN inactive_reason TEXT"},
		{"last_access_reinforced_at", "ALTER TABLE relations ADD COLUMN last_access_reinforced_at REAL"},
	}
	deletedRelationColumns := make([][2]string, 0, len(relationColumns))
	for _, migration := range relationColumns {
		deletedRelationColumns = append(deletedRelationColumns, [2]string{
			migration[0],
			strings.Replace(migration[1], "ALTER TABLE relations", "ALTER TABLE deleted_relations", 1),
		})
	}
	if err := ensureColumns(conn, "relations", relationColumns); err != nil {
		return err
	}
	if err := ensureColumns(conn, "deleted_relations", deletedRelationColumns); err != nil {
		return err
	}

	migrationEpoch := nowTimestamp()
	if _, err := conn.Exec(relationsLifecycleNormalizeSQL, migrationEpoch, migrationEpoch); err != nil {
		return err
	}
	if _, err := conn.Exec(deletedRelationsLifecycleNormalizeSQL, migrationEpoch); err != nil {
		return err
	}
	if _, err := conn.Exec(paragraphsSoftDeleteReasonSQL); err != nil {
		return err
	}

	if err := ensureLifecycleNotNullConstraints(conn); err != nil {
		return err
	}
	if _, err := conn.Exec("DROP INDEX IF EXISTS idx_relations_lifecycle_due"); err != nil {
		return err
	}
	// SQLite 表重建会删除原表所属索引，迁移后必须恢复全新库的基础索引集合
	if err := execAll(conn,
		ddlIdxRelationsLifecycleDue,
		ddlIdxRelationsInactiveReason,
		ddlIdxRelationsVector,
		ddlIdxRelationsSubject,
		ddlIdxRelationsObject,
		ddlIdxRelationsInactive,
		ddlIdxRelationsProtected,
		ddlIdxParagraphsExpiration,
	); err != nil {
		return err
	}
	return nil
}

// lifecycleColumnNames 表重建时复制的列，顺序与 Python 一致
var lifecycleColumnNames = []string{
	"hash",
	"subject",
	"predicate",
	"object",
	"vector_index",
	"confidence",
	"vector_state",
	"vector_updated_at",
	"vector_error",
	"vector_retry_count",
	"created_at",
	"source_paragraph",
	"metadata",
	"is_permanent",
	"last_accessed",
	"access_count",
	"last_access_reinforced_at",
	"is_inactive",
	"inactive_since",
	"is_pinned",
	"protected_until",
	"last_reinforced",
	"retention_strength",
	"retention_anchor_at",
	"next_lifecycle_at",
	"reinforcement_count",
	"lifecycle_revision",
	"inactive_reason",
}

// ensureLifecycleNotNullConstraints 把历史 ALTER 列收敛为与全新数据库一致的 NOT NULL 契约
func ensureLifecycleNotNullConstraints(conn *Conn) error {
	tablesToRebuild := make([]string, 0, 2)
	for _, table := range []string{"relations", "deleted_relations"} {
		infos, err := readTableInfo(conn, table)
		if err != nil {
			return err
		}
		var anchor *tableColumnInfo
		for i := range infos {
			if infos[i].Name == "retention_anchor_at" {
				anchor = &infos[i]
				break
			}
		}
		if anchor == nil {
			return fmt.Errorf("Schema迁移失败 (%s.retention_anchor_at 缺失)", table)
		}
		if anchor.NotNull != 1 {
			tablesToRebuild = append(tablesToRebuild, table)
		}
	}
	if len(tablesToRebuild) == 0 {
		return nil
	}

	if err := conn.Commit(); err != nil {
		return err
	}
	var foreignKeys int
	if err := conn.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return err
	}
	if foreignKeys != 0 {
		if _, err := conn.Exec("PRAGMA foreign_keys = OFF"); err != nil {
			return err
		}
	}

	rebuildErr := TransactionOn(conn, true, func(tx *Tx) error {
		for _, table := range tablesToRebuild {
			if err := rebuildLifecycleTable(conn, table); err != nil {
				return err
			}
		}
		violations, err := collectForeignKeyViolations(conn)
		if err != nil {
			return err
		}
		if len(violations) > 0 {
			return fmt.Errorf(
				"Schema迁移后外键校验失败: %d 项，详情=%s",
				len(violations),
				formatForeignKeyViolationSummary(violations),
			)
		}
		return nil
	})
	if foreignKeys != 0 {
		if _, err := conn.Exec("PRAGMA foreign_keys = ON"); err != nil && rebuildErr == nil {
			return err
		}
	}
	return rebuildErr
}

// rebuildLifecycleTable 重建单张关系表以恢复 NOT NULL 约束
func rebuildLifecycleTable(conn *Conn, table string) error {
	temporaryTable := table + "_schema20_rebuild"
	schemaObjects, err := readSchemaObjects(conn, table)
	if err != nil {
		return err
	}
	if _, err := conn.Exec("DROP TABLE IF EXISTS " + temporaryTable); err != nil {
		return err
	}

	deletedAtColumn := ""
	if table == "deleted_relations" {
		deletedAtColumn = ",\n    deleted_at REAL"
	}
	uniqueConstraint := ""
	if table == "relations" {
		uniqueConstraint = ",\n    UNIQUE(subject, predicate, object)"
	}
	createSQL := fmt.Sprintf(`
CREATE TABLE %s (
    hash TEXT PRIMARY KEY,
    subject TEXT NOT NULL,
    predicate TEXT NOT NULL,
    object TEXT NOT NULL,
    vector_index INTEGER,
    confidence REAL DEFAULT 1.0,
    vector_state TEXT DEFAULT 'none',
    vector_updated_at REAL,
    vector_error TEXT,
    vector_retry_count INTEGER DEFAULT 0,
    created_at REAL,
    source_paragraph TEXT,
    metadata TEXT,
    is_permanent BOOLEAN DEFAULT 0,
    last_accessed REAL,
    access_count INTEGER DEFAULT 0,
    last_access_reinforced_at REAL,
    is_inactive BOOLEAN DEFAULT 0,
    inactive_since REAL,
    is_pinned BOOLEAN DEFAULT 0,
    protected_until REAL,
    last_reinforced REAL,
    retention_strength REAL NOT NULL DEFAULT 1.0,
    retention_anchor_at REAL NOT NULL,
    next_lifecycle_at REAL,
    reinforcement_count INTEGER NOT NULL DEFAULT 0,
    lifecycle_revision INTEGER NOT NULL DEFAULT 0,
    inactive_reason TEXT%s%s
)`, temporaryTable, deletedAtColumn, uniqueConstraint)
	if _, err := conn.Exec(createSQL); err != nil {
		return err
	}

	copiedColumns := append([]string{}, lifecycleColumnNames...)
	if table == "deleted_relations" {
		copiedColumns = append(copiedColumns, "deleted_at")
	}
	columnsSQL := strings.Join(copiedColumns, ", ")
	copySQL := fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s", temporaryTable, columnsSQL, columnsSQL, table)
	if _, err := conn.Exec(copySQL); err != nil {
		return err
	}
	if _, err := conn.Exec("DROP TABLE " + table); err != nil {
		return err
	}
	if _, err := conn.Exec("ALTER TABLE " + temporaryTable + " RENAME TO " + table); err != nil {
		return err
	}
	for _, object := range schemaObjects {
		statement := strings.TrimSpace(object.SQL)
		if statement == "" {
			return fmt.Errorf("Schema迁移失败 (%s.%s 定义为空)", table, object.Name)
		}
		if _, err := conn.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

// schemaObject sqlite_master 中的索引/触发器定义
type schemaObject struct {
	Type string
	Name string
	SQL  string
}

// readSchemaObjects 读取表所属的索引与触发器定义，对齐 Python schema_objects 查询
func readSchemaObjects(conn *Conn, table string) ([]schemaObject, error) {
	rows, err := conn.Query(`
		SELECT type, name, sql
		FROM sqlite_master
		WHERE tbl_name = ?
		  AND type IN ('index', 'trigger')
		  AND sql IS NOT NULL
		ORDER BY CASE type WHEN 'index' THEN 0 ELSE 1 END, name
	`, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []schemaObject
	for rows.Next() {
		var (
			objectType string
			name       string
			sqlText    sql.NullString
		)
		if err := rows.Scan(&objectType, &name, &sqlText); err != nil {
			return nil, err
		}
		out = append(out, schemaObject{Type: objectType, Name: name, SQL: sqlText.String})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// fkViolation 对应 PRAGMA foreign_key_check 的一行
type fkViolation struct {
	Table  string
	RowID  sql.NullInt64
	Parent string
	FKID   int
}

// collectForeignKeyViolations 收集当前数据库中已经存在的外键违规记录
func collectForeignKeyViolations(conn *Conn) ([]fkViolation, error) {
	rows, err := conn.Query("PRAGMA foreign_key_check")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []fkViolation
	for rows.Next() {
		var (
			table  string
			rowID  sql.NullInt64
			parent string
			fkID   int
		)
		if err := rows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			return nil, err
		}
		out = append(out, fkViolation{Table: table, RowID: rowID, Parent: parent, FKID: fkID})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// formatForeignKeyViolationSummary 按子表、父表和外键编号汇总外键违规
func formatForeignKeyViolationSummary(violations []fkViolation) string {
	type key struct {
		child  string
		parent string
		fkID   int
	}
	grouped := make(map[key]int, len(violations))
	for _, violation := range violations {
		item := key{child: violation.Table, parent: violation.Parent, fkID: violation.FKID}
		grouped[item]++
	}
	keys := make([]key, 0, len(grouped))
	for item := range grouped {
		keys = append(keys, item)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].child != keys[j].child {
			return keys[i].child < keys[j].child
		}
		if keys[i].parent != keys[j].parent {
			return keys[i].parent < keys[j].parent
		}
		return keys[i].fkID < keys[j].fkID
	})

	parts := make([]string, 0, len(keys))
	for _, item := range keys {
		parts = append(parts, fmt.Sprintf("%s->%s(fk#%d)=%d", item.child, item.parent, item.fkID, grouped[item]))
	}
	return strings.Join(parts, ", ")
}

// ensureRelationGraphProjectionTables 建立关系活跃态到持久化图的可重试投影队列
func ensureRelationGraphProjectionTables(conn *Conn) error {
	if err := execAll(conn, ddlRelationGraphProjectionJobs, ddlIdxRelationGraphProjectionReady); err != nil {
		return err
	}
	if _, err := conn.Exec("DROP TRIGGER IF EXISTS trg_relations_graph_projection_lifecycle"); err != nil {
		return err
	}
	return execAll(conn, ddlTrgRelationsGraphProjectionLifecycle)
}

// seedRelationGraphProjectionJobs 迁移时为全部现存关系建立权威基线，不能信任升级前图快照
func seedRelationGraphProjectionJobs(conn *Conn) error {
	migrationEpoch := nowTimestamp()
	_, err := conn.Exec(relationGraphProjectionSeedSQL, migrationEpoch, migrationEpoch)
	return err
}

// ensureExternalMemoryRefsForeignKey 重建外部引用表，使悬空映射不可能再次产生
func ensureExternalMemoryRefsForeignKey(conn *Conn) error {
	rows, err := conn.Query("PRAGMA foreign_key_list(external_memory_refs)")
	if err != nil {
		return err
	}
	hasParagraphFK := false
	for rows.Next() {
		var (
			id        int
			seq       int
			table     string
			from      string
			to        sql.NullString
			onUpdate  string
			onDelete  string
			matchType string
		)
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &matchType); err != nil {
			_ = rows.Close()
			return err
		}
		if table == "paragraphs" && from == "paragraph_hash" && strings.ToUpper(onDelete) == "CASCADE" {
			hasParagraphFK = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if hasParagraphFK {
		return nil
	}

	var beforeCount int
	if err := conn.QueryRow("SELECT COUNT(*) FROM external_memory_refs").Scan(&beforeCount); err != nil {
		return err
	}
	if _, err := conn.Exec("DROP TABLE IF EXISTS external_memory_refs_schema_19"); err != nil {
		return err
	}
	if err := execAll(conn, ddlExternalMemoryRefsSchema19); err != nil {
		return err
	}
	// Python 在此比较重建前后行数并告警清理的悬空引用，Go 侧暂未接入 logger
	if _, err := conn.Exec(externalMemoryRefsRebuildInsertSQL); err != nil {
		return err
	}
	if _, err := conn.Exec("DROP TABLE external_memory_refs"); err != nil {
		return err
	}
	if _, err := conn.Exec("ALTER TABLE external_memory_refs_schema_19 RENAME TO external_memory_refs"); err != nil {
		return err
	}
	return execAll(conn, ddlIdxExternalMemoryRefsParagraphRebuild)
}
