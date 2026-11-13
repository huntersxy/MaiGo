package storage

// A_memorix schema 修复与运行时自动迁移，对齐 Python metadata_schema.py
// 覆盖 _run_runtime_auto_migration / _repair_historical_foreign_key_violations 及其辅助函数

import (
	"fmt"
	"os"
)

// fkRepair 一条定向清理历史外键违规的语句
type fkRepair struct {
	label string
	sql   string
}

// runRuntimeAutoMigration 对 1.0 之后的已版本化库执行轻量自动迁移
func (s *Store) runRuntimeAutoMigration(conn *Conn, currentVersion int) error {
	if _, err := s.repairHistoricalForeignKeyViolations(conn, currentVersion); err != nil {
		return err
	}
	if err := s.migrateSchema(conn); err != nil {
		return err
	}
	if _, err := s.RebuildRelationHashAliases(conn); err != nil {
		return err
	}
	if _, err := s.NormalizeParagraphKnowledgeTypes(conn); err != nil {
		return err
	}
	// 28 已具备事实账本，29 只扩展写入检查点，不重新扫描和迁移存量人物记忆
	if currentVersion < 28 {
		if _, err := s.BackfillPersonFactClaims(conn); err != nil {
			return err
		}
	}
	return s.setSchemaVersionOn(conn, SchemaVersion)
}

// repairHistoricalForeignKeyViolations 定向清理缺少父记录、因而无法再被正常使用的历史关联数据
func (s *Store) repairHistoricalForeignKeyViolations(conn *Conn, currentVersion int) (map[string]int, error) {
	violations, err := collectForeignKeyViolations(conn)
	if err != nil {
		return nil, err
	}
	if len(violations) == 0 {
		return map[string]int{}, nil
	}

	backupPath, err := s.backupMetadataDatabaseForSchemaRepair(currentVersion)
	if err != nil {
		return nil, err
	}
	tables, err := s.listTableNames(conn)
	if err != nil {
		return nil, err
	}

	// 关联表、缓存表和操作明细缺少父记录后已没有独立业务含义，可以安全清理
	// 主体记忆表不进入该列表，未知异常会在事务末尾触发回滚
	repairs := buildForeignKeyRepairs(tables, conn)

	repaired := make(map[string]int, len(repairs))
	repairErr := TransactionOn(conn, true, func(tx *Tx) error {
		for _, repair := range repairs {
			result, err := conn.Exec(repair.sql)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if affected > 0 {
				repaired[repair.label] = int(affected)
			}
		}
		remaining, err := collectForeignKeyViolations(conn)
		if err != nil {
			return err
		}
		if len(remaining) > 0 {
			return fmt.Errorf(
				"历史数据库包含无法自动修复的外键异常: %d 项，详情=%s，备份=%s",
				len(remaining),
				formatForeignKeyViolationSummary(remaining),
				backupPath,
			)
		}
		return nil
	})
	if repairErr != nil {
		return nil, repairErr
	}
	return repaired, nil
}

// listTableNames 列出当前库的全部表名
func (s *Store) listTableNames(conn *Conn) (map[string]struct{}, error) {
	rows, err := conn.Query("SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	tables := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

// tableSetHas 判断给定表是否全部存在，对齐 Python issubset
func tableSetHas(tables map[string]struct{}, names ...string) bool {
	return hasAllColumns(tables, names...)
}

// buildForeignKeyRepairs 按表存在情况组装清理语句，顺序与 Python 完全一致
func buildForeignKeyRepairs(tables map[string]struct{}, conn *Conn) []fkRepair {
	repairs := make([]fkRepair, 0, 12)
	if tableSetHas(tables, "paragraph_relations", "paragraphs", "relations") {
		repairs = append(repairs, fkRepair{"paragraph_relations", `
			DELETE FROM paragraph_relations
			WHERE NOT EXISTS (
			    SELECT 1 FROM paragraphs p
			    WHERE p.hash = paragraph_relations.paragraph_hash
			)
			   OR NOT EXISTS (
			    SELECT 1 FROM relations r
			    WHERE r.hash = paragraph_relations.relation_hash
			)
		`})
	}
	if tableSetHas(tables, "paragraph_entities", "paragraphs", "entities") {
		repairs = append(repairs, fkRepair{"paragraph_entities", `
			DELETE FROM paragraph_entities
			WHERE NOT EXISTS (
			    SELECT 1 FROM paragraphs p
			    WHERE p.hash = paragraph_entities.paragraph_hash
			)
			   OR NOT EXISTS (
			    SELECT 1 FROM entities e
			    WHERE e.hash = paragraph_entities.entity_hash
			)
		`})
	}
	if tableSetHas(tables, "episode_paragraphs", "episodes", "paragraphs") {
		repairs = append(repairs, fkRepair{"episode_paragraphs", `
			DELETE FROM episode_paragraphs
			WHERE NOT EXISTS (
			    SELECT 1 FROM episodes e
			    WHERE e.episode_id = episode_paragraphs.episode_id
			)
			   OR NOT EXISTS (
			    SELECT 1 FROM paragraphs p
			    WHERE p.hash = episode_paragraphs.paragraph_hash
			)
		`})
	}
	if tableSetHas(tables, "memory_feedback_action_logs", "memory_feedback_tasks") {
		repairs = append(repairs, fkRepair{"memory_feedback_action_logs", `
			DELETE FROM memory_feedback_action_logs
			WHERE NOT EXISTS (
			    SELECT 1 FROM memory_feedback_tasks t
			    WHERE t.id = memory_feedback_action_logs.task_id
			)
		`})
	}
	if tableSetHas(tables, "paragraph_stale_relation_marks", "paragraphs", "relations") {
		repairs = append(repairs, fkRepair{"paragraph_stale_relation_marks", `
			DELETE FROM paragraph_stale_relation_marks
			WHERE NOT EXISTS (
			    SELECT 1 FROM paragraphs p
			    WHERE p.hash = paragraph_stale_relation_marks.paragraph_hash
			)
			   OR NOT EXISTS (
			    SELECT 1 FROM relations r
			    WHERE r.hash = paragraph_stale_relation_marks.relation_hash
			)
		`})
	}

	// 陈旧关系标记的 task_id 只做置空，不删除标记本身
	if tableSetHas(tables, "paragraph_stale_relation_marks", "memory_feedback_tasks") {
		columns, err := tableColumnSet(conn, "paragraph_stale_relation_marks")
		if err == nil {
			if _, ok := columns["task_id"]; ok {
				repairs = append(repairs, fkRepair{"paragraph_stale_relation_marks.task_id", `
					UPDATE paragraph_stale_relation_marks
					SET task_id = NULL
					WHERE task_id IS NOT NULL
					  AND NOT EXISTS (
					    SELECT 1 FROM memory_feedback_tasks t
					    WHERE t.id = paragraph_stale_relation_marks.task_id
					)
				`})
			}
		}
	}
	if tableSetHas(tables, "external_memory_refs", "paragraphs") {
		repairs = append(repairs, fkRepair{"external_memory_refs", `
			DELETE FROM external_memory_refs
			WHERE NOT EXISTS (
			    SELECT 1 FROM paragraphs p
			    WHERE p.hash = external_memory_refs.paragraph_hash
			)
		`})
	}
	if tableSetHas(tables, "delete_operation_items", "delete_operations") {
		repairs = append(repairs, fkRepair{"delete_operation_items", `
			DELETE FROM delete_operation_items
			WHERE NOT EXISTS (
			    SELECT 1 FROM delete_operations o
			    WHERE o.operation_id = delete_operation_items.operation_id
			)
		`})
	}
	if tableSetHas(tables, "storage_cleanup_jobs", "delete_operations") {
		repairs = append(repairs, fkRepair{"storage_cleanup_jobs", `
			DELETE FROM storage_cleanup_jobs
			WHERE NOT EXISTS (
			    SELECT 1 FROM delete_operations o
			    WHERE o.operation_id = storage_cleanup_jobs.operation_id
			)
		`})
	}
	if tableSetHas(tables, "fact_evidence", "fact_claims") {
		repairs = append(repairs, fkRepair{"fact_evidence", `
			DELETE FROM fact_evidence
			WHERE NOT EXISTS (
			    SELECT 1 FROM fact_claims c
			    WHERE c.claim_id = fact_evidence.claim_id
			)
		`})
	}
	if tableSetHas(tables, "paragraph_ngrams", "paragraphs") {
		repairs = append(repairs, fkRepair{"paragraph_ngrams", `
			DELETE FROM paragraph_ngrams
			WHERE NOT EXISTS (
			    SELECT 1 FROM paragraphs p
			    WHERE p.hash = paragraph_ngrams.paragraph_hash
			)
		`})
	}
	return repairs
}

// backupMetadataDatabaseForSchemaRepair 自动清理历史孤立关联前创建一致性 SQLite 备份
func (s *Store) backupMetadataDatabaseForSchemaRepair(currentVersion int) (string, error) {
	databasePath, err := s.DBPath()
	if err != nil {
		return "", err
	}
	backupPath := fmt.Sprintf("%s.pre-schema%d-to-%d-repair.bak", databasePath, currentVersion, SchemaVersion)
	if _, err := os.Stat(backupPath); err == nil {
		// 备份已存在则继续使用，与 Python 行为一致
		return backupPath, nil
	}
	// Python 用 sqlite3 backup API；Go 侧用 VACUUM INTO 得到同样的完整副本
	conn, err := s.Conn()
	if err != nil {
		return "", err
	}
	if _, err := conn.Exec("VACUUM INTO ?", backupPath); err != nil {
		_ = os.Remove(backupPath)
		return "", err
	}
	return backupPath, nil
}
