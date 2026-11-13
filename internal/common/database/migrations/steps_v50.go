package migration

import (
	"context"
)

const llmUsageColumnsV50 = "id,model_name,model_assign_name,model_api_provider_name,session_id,task_name," +
	"request_type,time_cost,timestamp,prompt_tokens,completion_tokens,total_tokens," +
	"prompt_cache_enabled,prompt_cache_hit_tokens,prompt_cache_miss_tokens,cost"

// v50 让 llm_usage 的可空性与默认值对齐 Python ModelUsage 的 create_all 运行模型
// Python 实测：task_name/time_cost 可空且无 server default；model_name/model_api_provider_name/
// session_id/request_type/total_tokens 等 NOT NULL 但无 server default（session_id 仅有客户端 default）
// v30 建表时给这些列带了空串或 0 默认，v48/v49 只处理了 model_assign_name，这里补齐
// 历史迁移不改：v50 对旧库做 PG ALTER / SQLite 重建，幂等
func migrateV50(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, "llm_usage")
	if err != nil || !exists {
		return err
	}
	if q.Dialect() == DialectSQLite {
		aligned, err := sqliteLLMUsageV50Aligned(ctx, q)
		if err != nil || aligned {
			return err
		}
		return rebuildSQLiteLLMUsageV50(ctx, q)
	}
	statements := []string{
		"ALTER TABLE llm_usage ALTER COLUMN model_assign_name DROP NOT NULL",
		"ALTER TABLE llm_usage ALTER COLUMN model_assign_name DROP DEFAULT",
		"ALTER TABLE llm_usage ALTER COLUMN task_name DROP NOT NULL",
		"ALTER TABLE llm_usage ALTER COLUMN task_name DROP DEFAULT",
		"ALTER TABLE llm_usage ALTER COLUMN time_cost DROP NOT NULL",
		"ALTER TABLE llm_usage ALTER COLUMN time_cost DROP DEFAULT",
		"ALTER TABLE llm_usage ALTER COLUMN model_name DROP DEFAULT",
		"ALTER TABLE llm_usage ALTER COLUMN model_api_provider_name DROP DEFAULT",
		"ALTER TABLE llm_usage ALTER COLUMN session_id DROP DEFAULT",
		"ALTER TABLE llm_usage ALTER COLUMN request_type DROP DEFAULT",
		"ALTER TABLE llm_usage ALTER COLUMN prompt_tokens DROP DEFAULT",
		"ALTER TABLE llm_usage ALTER COLUMN completion_tokens DROP DEFAULT",
		"ALTER TABLE llm_usage ALTER COLUMN total_tokens DROP DEFAULT",
		"ALTER TABLE llm_usage ALTER COLUMN cost DROP DEFAULT",
	}
	for _, statement := range statements {
		if err := q.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// sqliteLLMUsageV50Aligned 判断 SQLite 表是否已是 v50 形态
// 已是目标形态则不再重建，避免重复调用 migrateV50 时反复搬表
func sqliteLLMUsageV50Aligned(ctx context.Context, q querier) (bool, error) {
	for _, column := range []string{"task_name", "time_cost"} {
		nullable, err := columnNullable(ctx, q, "llm_usage", column)
		if err != nil || !nullable {
			return false, err
		}
	}
	for _, column := range []string{
		"model_name", "model_assign_name", "model_api_provider_name", "session_id",
		"task_name", "request_type", "time_cost", "prompt_tokens", "completion_tokens",
		"total_tokens", "cost",
	} {
		defaultExpr, err := columnDefault(ctx, q, "llm_usage", column)
		if err != nil || defaultExpr != "" {
			return false, err
		}
	}
	return true, nil
}

// rebuildSQLiteLLMUsageV50 SQLite 不支持 DROP NOT NULL/DEFAULT，按 v30 列顺序重建表并保留数据与索引
func rebuildSQLiteLLMUsageV50(ctx context.Context, q querier) error {
	ddl := "CREATE TABLE llm_usage_v50 (" +
		"id " + serialPK(q) + ", " +
		"model_name TEXT NOT NULL, " +
		"model_assign_name TEXT, " +
		"model_api_provider_name TEXT NOT NULL, " +
		"session_id TEXT NOT NULL, " +
		"task_name TEXT, " +
		"request_type TEXT NOT NULL, " +
		"time_cost DOUBLE PRECISION, " +
		"timestamp " + timestampType(q) + ", " +
		"prompt_tokens INTEGER NOT NULL, " +
		"completion_tokens INTEGER NOT NULL, " +
		"total_tokens INTEGER NOT NULL, " +
		"prompt_cache_enabled BOOLEAN NOT NULL DEFAULT FALSE, " +
		"prompt_cache_hit_tokens INTEGER NOT NULL DEFAULT 0, " +
		"prompt_cache_miss_tokens INTEGER NOT NULL DEFAULT 0, " +
		"cost DOUBLE PRECISION NOT NULL)"
	if err := q.Exec(ctx, ddl); err != nil {
		return err
	}
	if err := q.Exec(ctx, "INSERT INTO llm_usage_v50("+llmUsageColumnsV50+") SELECT "+llmUsageColumnsV50+" FROM llm_usage"); err != nil {
		return err
	}
	if err := dropTable(ctx, q, "llm_usage"); err != nil {
		return err
	}
	if err := q.Exec(ctx, "ALTER TABLE llm_usage_v50 RENAME TO llm_usage"); err != nil {
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
