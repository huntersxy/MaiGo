package migration

import (
	"context"
	"fmt"
)

func migrateV18(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, "high_frequency_terms")
	if err != nil {
		return err
	}
	if exists {
		if err := ensureIndex(ctx, q, "ix_high_frequency_terms_rank", "high_frequency_terms", "rank"); err != nil {
			return err
		}
		return ensureIndex(ctx, q, "ix_high_frequency_terms_updated_at", "high_frequency_terms", "updated_at")
	}
	ddl := "CREATE TABLE high_frequency_terms (" +
		"id " + serialPK(q) + ", " +
		"term TEXT NOT NULL, " +
		"normalized_term TEXT NOT NULL, " +
		"term_type TEXT NOT NULL DEFAULT 'word', " +
		"rank INTEGER NOT NULL DEFAULT 0, " +
		"occurrence_count INTEGER NOT NULL DEFAULT 0, " +
		"message_count INTEGER NOT NULL DEFAULT 0, " +
		"frequency DOUBLE PRECISION NOT NULL DEFAULT 0, " +
		"message_frequency DOUBLE PRECISION NOT NULL DEFAULT 0, " +
		"created_at " + timestampType(q) + " NOT NULL DEFAULT CURRENT_TIMESTAMP, " +
		"updated_at " + timestampType(q) + " NOT NULL DEFAULT CURRENT_TIMESTAMP, " +
		"UNIQUE(normalized_term))"
	if err := q.Exec(ctx, ddl); err != nil {
		return err
	}
	if err := ensureIndex(ctx, q, "ix_high_frequency_terms_rank", "high_frequency_terms", "rank"); err != nil {
		return err
	}
	return ensureIndex(ctx, q, "ix_high_frequency_terms_updated_at", "high_frequency_terms", "updated_at")
}

func migrateNoopV19(context.Context, querier) error { return nil }
func migrateNoopV20(context.Context, querier) error { return nil }
func migrateNoopV21(context.Context, querier) error { return nil }
func migrateNoopV22(context.Context, querier) error { return nil }
func migrateNoopV23(context.Context, querier) error { return nil }
func migrateNoopV24(context.Context, querier) error { return nil }
func migrateNoopV25(context.Context, querier) error { return nil }
func migrateNoopV26(context.Context, querier) error { return nil }
func migrateNoopV27(context.Context, querier) error { return nil }
func migrateNoopV31(context.Context, querier) error { return nil }

func migrateV28(ctx context.Context, q querier) error {
	ddl := "CREATE TABLE IF NOT EXISTS one_time_maintenance_tasks (" +
		"task_name TEXT PRIMARY KEY, " +
		"phase TEXT NOT NULL DEFAULT '', " +
		"status TEXT NOT NULL DEFAULT '', " +
		"cursor_id BIGINT NOT NULL DEFAULT 0, " +
		"stats_json TEXT NOT NULL DEFAULT '{}', " +
		"last_error TEXT, " +
		"completed_at " + timestampType(q) + ", " +
		"updated_at " + timestampType(q) + " NOT NULL DEFAULT CURRENT_TIMESTAMP)"
	if err := q.Exec(ctx, ddl); err != nil {
		return err
	}
	if err := ensureIndex(ctx, q, "ix_one_time_maintenance_tasks_updated_at", "one_time_maintenance_tasks", "updated_at"); err != nil {
		return err
	}
	exists, err := tableExists(ctx, q, "tool_records")
	if err != nil {
		return err
	}
	var rebuiltRecords int64
	var lastDeletedID int64
	if exists {
		for _, column := range []string{"tool_builtin_prompt", "tool_display_prompt"} {
			has, err := columnExists(ctx, q, "tool_records", column)
			if err != nil {
				return err
			}
			if has {
				rebuiltRecords, lastDeletedID, err = countToolRecordsForRebuild(ctx, q)
				if err != nil {
					return err
				}
				if err := dropTable(ctx, q, "tool_records"); err != nil {
					return err
				}
				exists = false
				break
			}
		}
	}
	if !exists {
		toolDDL := "CREATE TABLE tool_records (" +
			"id " + serialPK(q) + ", " +
			"tool_id TEXT NOT NULL DEFAULT '', " +
			"timestamp " + timestampType(q) + ", " +
			"session_id TEXT NOT NULL DEFAULT '', " +
			"tool_name TEXT NOT NULL DEFAULT '', " +
			"tool_reasoning TEXT, " +
			"tool_data TEXT)"
		if err := q.Exec(ctx, toolDDL); err != nil {
			return err
		}
		indexes := [][3]string{
			{"ix_tool_records_session_id", "tool_records", "session_id"},
			{"ix_tool_records_timestamp", "tool_records", "timestamp"},
			{"ix_tool_records_tool_id", "tool_records", "tool_id"},
			{"ix_tool_records_tool_name", "tool_records", "tool_name"},
			{"ix_tool_records_timestamp_tool_name", "tool_records", "timestamp,tool_name"},
		}
		for _, item := range indexes {
			if err := ensureIndex(ctx, q, item[0], item[1], item[2]); err != nil {
				return err
			}
		}
	}
	return insertMaintenanceSeed(ctx, q, rebuiltRecords, lastDeletedID)
}

// countToolRecordsForRebuild 记录 v27 tool_records 重建前删除的行数与游标
func countToolRecordsForRebuild(ctx context.Context, q querier) (int64, int64, error) {
	var count int64
	if err := q.QueryRow(ctx, "SELECT COUNT(*) FROM tool_records").Scan(&count); err != nil {
		return 0, 0, err
	}
	var lastID int64
	if err := q.QueryRow(ctx, "SELECT COALESCE(MAX(id),0) FROM tool_records").Scan(&lastID); err != nil {
		return 0, 0, err
	}
	return count, lastID, nil
}

// insertMaintenanceSeed 对齐 Python v27_to_v28：只有确实删除了旧工具记录时才等待启动 VACUUM
func insertMaintenanceSeed(ctx context.Context, q querier, scannedRecords, lastDeletedID int64) error {
	phase, status := "done", "done"
	completed := 1
	if scannedRecords > 0 {
		phase, status = "awaiting_vacuum", "pending"
		completed = 0
	}
	stats := fmt.Sprintf(`{"scanned_records":%d,"updated_records":%d}`, scannedRecords, scannedRecords)
	return q.Exec(ctx,
		"INSERT INTO one_time_maintenance_tasks(task_name,phase,status,cursor_id,stats_json,completed_at,updated_at) "+
			"VALUES(?,?,?,?,?,CASE WHEN ?=1 THEN CURRENT_TIMESTAMP ELSE NULL END,CURRENT_TIMESTAMP) "+
			"ON CONFLICT (task_name) DO UPDATE SET phase=excluded.phase,status=excluded.status,cursor_id=excluded.cursor_id,"+
			"stats_json=excluded.stats_json,completed_at=CASE WHEN ?=1 THEN CURRENT_TIMESTAMP ELSE one_time_maintenance_tasks.completed_at END,"+
			"updated_at=excluded.updated_at",
		"tool_record_prompt_payload_cleanup_v1", phase, status, lastDeletedID, stats, completed, completed)
}
