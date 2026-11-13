package migration

import (
	"context"
	"database/sql"
)

const llmUsageColumnsV48 = "id,model_name,model_assign_name,model_api_provider_name,session_id,task_name," +
	"request_type,time_cost,timestamp,prompt_tokens,completion_tokens,total_tokens," +
	"prompt_cache_enabled,prompt_cache_hit_tokens,prompt_cache_miss_tokens,cost"

// migrateV48 把 llm_usage.model_assign_name 放宽为可空
// Python ModelUsage.model_assign_name 可空（database_model.py:92、v29_to_v30.py:172），Go v30 收紧成 NOT NULL
// 放宽后 NULL 行与空串行都走 COALESCE(model_assign_name, model_name, 'unknown')，读取语义不变
func migrateV48(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, "llm_usage")
	if err != nil || !exists {
		return err
	}
	nullable, err := columnNullable(ctx, q, "llm_usage", "model_assign_name")
	if err != nil || nullable {
		return err
	}
	if q.Dialect() == DialectSQLite {
		return rebuildSQLiteLLMUsageAssignNullable(ctx, q)
	}
	return q.Exec(ctx, "ALTER TABLE llm_usage ALTER COLUMN model_assign_name DROP NOT NULL")
}

// columnNullable 判断列是否可空；表或列不存在时返回 false
func columnNullable(ctx context.Context, q querier, table, column string) (bool, error) {
	var (
		value string
		err   error
	)
	if q.Dialect() == DialectSQLite {
		var notNull int
		err = q.QueryRow(ctx, `SELECT "notnull" FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&notNull)
		if err == nil {
			return notNull == 0, nil
		}
	} else {
		err = q.QueryRow(ctx, "SELECT is_nullable FROM information_schema.columns "+
			"WHERE table_schema=current_schema() AND table_name=? AND column_name=?", table, column).Scan(&value)
		if err == nil {
			return value == "YES", nil
		}
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, err
}

// rebuildSQLiteLLMUsageAssignNullable SQLite 不支持 DROP NOT NULL，按 v30 列顺序重建表并保留数据与索引
func rebuildSQLiteLLMUsageAssignNullable(ctx context.Context, q querier) error {
	ddl := "CREATE TABLE llm_usage_v48 (" +
		"id " + serialPK(q) + ", " +
		"model_name TEXT NOT NULL DEFAULT '', " +
		"model_assign_name TEXT, " +
		"model_api_provider_name TEXT NOT NULL DEFAULT '', " +
		"session_id TEXT NOT NULL DEFAULT '', " +
		"task_name TEXT NOT NULL DEFAULT '', " +
		"request_type TEXT NOT NULL DEFAULT '', " +
		"time_cost DOUBLE PRECISION NOT NULL DEFAULT 0, " +
		"timestamp " + timestampType(q) + ", " +
		"prompt_tokens INTEGER NOT NULL DEFAULT 0, " +
		"completion_tokens INTEGER NOT NULL DEFAULT 0, " +
		"total_tokens INTEGER NOT NULL DEFAULT 0, " +
		"prompt_cache_enabled BOOLEAN NOT NULL DEFAULT FALSE, " +
		"prompt_cache_hit_tokens INTEGER NOT NULL DEFAULT 0, " +
		"prompt_cache_miss_tokens INTEGER NOT NULL DEFAULT 0, " +
		"cost DOUBLE PRECISION NOT NULL DEFAULT 0)"
	if err := q.Exec(ctx, ddl); err != nil {
		return err
	}
	if err := q.Exec(ctx, "INSERT INTO llm_usage_v48("+llmUsageColumnsV48+") SELECT "+llmUsageColumnsV48+" FROM llm_usage"); err != nil {
		return err
	}
	if err := dropTable(ctx, q, "llm_usage"); err != nil {
		return err
	}
	if err := q.Exec(ctx, "ALTER TABLE llm_usage_v48 RENAME TO llm_usage"); err != nil {
		return err
	}
	indexes := [][2]string{
		{"ix_llm_usage_model_name", "model_name"},
		{"ix_llm_usage_model_assign_name", "model_assign_name"},
		{"ix_llm_usage_provider", "model_api_provider_name"},
		{"ix_llm_usage_session_id", "session_id"},
		{"ix_llm_usage_task_name", "task_name"},
		{"ix_llm_usage_timestamp", "timestamp"},
		{"ix_llm_usage_timestamp_request", "timestamp,request_type"},
		{"ix_llm_usage_timestamp_model", "timestamp,model_name"},
		{"ix_llm_usage_timestamp_assign", "timestamp,model_assign_name"},
	}
	for _, item := range indexes {
		if err := ensureIndex(ctx, q, item[0], "llm_usage", item[1]); err != nil {
			return err
		}
	}
	return nil
}
