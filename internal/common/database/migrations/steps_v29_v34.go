package migration

import (
	"context"
	"fmt"
)

func migrateV29(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, "high_frequency_terms")
	if err != nil {
		return err
	}
	rebuild := !exists
	if exists {
		hasChatID, err := columnExists(ctx, q, "high_frequency_terms", "chat_id")
		if err != nil {
			return err
		}
		rebuild = !hasChatID
	}
	if rebuild {
		if err := dropTable(ctx, q, "high_frequency_terms"); err != nil {
			return err
		}
		ddl := "CREATE TABLE high_frequency_terms (" +
			"id " + serialPK(q) + ", " +
			"chat_id TEXT NOT NULL, " +
			"term TEXT NOT NULL, " +
			"rank INTEGER NOT NULL DEFAULT 0, " +
			"occurrence_count INTEGER NOT NULL DEFAULT 0, " +
			"message_count INTEGER NOT NULL DEFAULT 0, " +
			"frequency DOUBLE PRECISION NOT NULL DEFAULT 0, " +
			"message_frequency DOUBLE PRECISION NOT NULL DEFAULT 0, " +
			"created_at " + timestampType(q) + " NOT NULL DEFAULT CURRENT_TIMESTAMP, " +
			"updated_at " + timestampType(q) + " NOT NULL DEFAULT CURRENT_TIMESTAMP, " +
			"UNIQUE(chat_id,term))"
		if err := q.Exec(ctx, ddl); err != nil {
			return err
		}
	}
	indexes := [][2]string{
		{"idx_high_frequency_terms_chat_id", "chat_id"},
		{"idx_high_frequency_terms_chat_rank", "chat_id,rank"},
		{"idx_high_frequency_terms_updated", "updated_at"},
	}
	for _, item := range indexes {
		if err := ensureIndex(ctx, q, item[0], "high_frequency_terms", item[1]); err != nil {
			return err
		}
	}
	return nil
}

const llmUsageV29Backup = "llm_usage_v29_backup"

func migrateV30(ctx context.Context, q querier) error {
	// 对齐 MaiBot v29_to_v30：RENAME 备份 → 建 v30 表 → INSERT 回填 → DROP 备份
	if err := recoverInterruptedLLMUsageV30(ctx, q); err != nil {
		return err
	}
	exists, err := tableExists(ctx, q, "llm_usage")
	if err != nil {
		return err
	}
	if !exists {
		if err := createLLMUsageV30Table(ctx, q, "llm_usage"); err != nil {
			return err
		}
		return ensureLLMUsageV30Indexes(ctx, q)
	}
	isV30, err := isLLMUsageV30Schema(ctx, q)
	if err != nil {
		return err
	}
	if isV30 {
		return ensureLLMUsageV30Indexes(ctx, q)
	}
	if err := q.Exec(ctx, "ALTER TABLE llm_usage RENAME TO "+llmUsageV29Backup); err != nil {
		return err
	}
	if err := createLLMUsageV30Table(ctx, q, "llm_usage"); err != nil {
		return err
	}
	insert := "INSERT INTO llm_usage (" +
		"id, model_name, model_assign_name, model_api_provider_name, session_id, task_name, request_type, " +
		"time_cost, timestamp, prompt_tokens, completion_tokens, total_tokens, " +
		"prompt_cache_enabled, prompt_cache_hit_tokens, prompt_cache_miss_tokens, cost" +
		") SELECT " +
		"id, model_name, model_assign_name, model_api_provider_name, '', task_name, request_type, " +
		"COALESCE(time_cost, 0), timestamp, COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0), COALESCE(total_tokens, 0), " +
		"COALESCE(prompt_cache_enabled, 0), COALESCE(prompt_cache_hit_tokens, 0), COALESCE(prompt_cache_miss_tokens, 0), COALESCE(cost, 0) " +
		"FROM " + llmUsageV29Backup
	if err := q.Exec(ctx, insert); err != nil {
		return err
	}
	if err := dropTable(ctx, q, llmUsageV29Backup); err != nil {
		return err
	}
	return ensureLLMUsageV30Indexes(ctx, q)
}

func recoverInterruptedLLMUsageV30(ctx context.Context, q querier) error {
	hasBackup, err := tableExists(ctx, q, llmUsageV29Backup)
	if err != nil || !hasBackup {
		return err
	}
	hasCurrent, err := tableExists(ctx, q, "llm_usage")
	if err != nil {
		return err
	}
	if !hasCurrent {
		return q.Exec(ctx, "ALTER TABLE "+llmUsageV29Backup+" RENAME TO llm_usage")
	}
	isV29, err := isLLMUsageV29Schema(ctx, q)
	if err != nil {
		return err
	}
	if isV29 {
		return dropTable(ctx, q, llmUsageV29Backup)
	}
	isV30, err := isLLMUsageV30Schema(ctx, q)
	if err != nil {
		return err
	}
	if isV30 {
		if err := dropTable(ctx, q, "llm_usage"); err != nil {
			return err
		}
		return q.Exec(ctx, "ALTER TABLE "+llmUsageV29Backup+" RENAME TO llm_usage")
	}
	return fmt.Errorf("llm_usage 与 llm_usage_v29_backup 同时存在且结构无法识别")
}

func isLLMUsageV29Schema(ctx context.Context, q querier) (bool, error) {
	hasEndpoint, err := columnExists(ctx, q, "llm_usage", "endpoint")
	if err != nil {
		return false, err
	}
	if hasEndpoint {
		return true, nil
	}
	return columnExists(ctx, q, "llm_usage", "user_type")
}

func isLLMUsageV30Schema(ctx context.Context, q querier) (bool, error) {
	hasSession, err := columnExists(ctx, q, "llm_usage", "session_id")
	if err != nil || !hasSession {
		return false, err
	}
	hasEndpoint, err := columnExists(ctx, q, "llm_usage", "endpoint")
	if err != nil || hasEndpoint {
		return false, err
	}
	hasUserType, err := columnExists(ctx, q, "llm_usage", "user_type")
	if err != nil || hasUserType {
		return false, err
	}
	return true, nil
}

func createLLMUsageV30Table(ctx context.Context, q querier, table string) error {
	ddl := "CREATE TABLE " + table + " (" +
		"id " + serialPK(q) + ", " +
		"model_name TEXT NOT NULL, " +
		"model_assign_name TEXT, " +
		"model_api_provider_name TEXT NOT NULL, " +
		"session_id TEXT NOT NULL DEFAULT '', " +
		"task_name TEXT, " +
		"request_type TEXT NOT NULL, " +
		"time_cost DOUBLE PRECISION NOT NULL DEFAULT 0, " +
		"timestamp " + timestampType(q) + ", " +
		"prompt_tokens INTEGER NOT NULL, " +
		"completion_tokens INTEGER NOT NULL, " +
		"total_tokens INTEGER NOT NULL, " +
		"prompt_cache_enabled BOOLEAN NOT NULL DEFAULT FALSE, " +
		"prompt_cache_hit_tokens INTEGER NOT NULL DEFAULT 0, " +
		"prompt_cache_miss_tokens INTEGER NOT NULL DEFAULT 0, " +
		"cost DOUBLE PRECISION NOT NULL)"
	return q.Exec(ctx, ddl)
}

func ensureLLMUsageV30Indexes(ctx context.Context, q querier) error {
	indexes := [][2]string{
		{"ix_llm_usage_model_name", "model_name"},
		{"ix_llm_usage_model_assign_name", "model_assign_name"},
		{"ix_llm_usage_model_api_provider_name", "model_api_provider_name"},
		{"ix_llm_usage_session_id", "session_id"},
		{"ix_llm_usage_task_name", "task_name"},
		{"ix_llm_usage_timestamp", "timestamp"},
		{"ix_llm_usage_timestamp_request_type", "timestamp,request_type"},
		{"ix_llm_usage_timestamp_model_name", "timestamp,model_name"},
		{"ix_llm_usage_timestamp_model_assign_name", "timestamp,model_assign_name"},
	}
	for _, item := range indexes {
		if err := ensureIndex(ctx, q, item[0], "llm_usage", item[1]); err != nil {
			return err
		}
	}
	return nil
}

func migrateV32(ctx context.Context, q querier) error {
	return cleanupExpressionStylePollution(ctx, q)
}

func migrateV33(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, "jargons")
	if err != nil || !exists {
		return err
	}
	hasCreated, err := columnExists(ctx, q, "jargons", "created_timestamp")
	if err != nil || !hasCreated {
		return err
	}
	hasUpdated, err := columnExists(ctx, q, "jargons", "updated_timestamp")
	if err != nil || !hasUpdated {
		return err
	}
	// 对齐 v32_to_v33.py：NULL 与空串都修复，两列同时坏时兜底 CURRENT_TIMESTAMP
	return q.Exec(ctx, `
		UPDATE jargons
		SET
			created_timestamp = CASE
				WHEN created_timestamp IS NULL OR TRIM(CAST(created_timestamp AS TEXT)) = ''
					THEN COALESCE(NULLIF(TRIM(CAST(updated_timestamp AS TEXT)), ''), CURRENT_TIMESTAMP)
				ELSE created_timestamp
			END,
			updated_timestamp = CASE
				WHEN updated_timestamp IS NULL OR TRIM(CAST(updated_timestamp AS TEXT)) = ''
					THEN COALESCE(NULLIF(TRIM(CAST(created_timestamp AS TEXT)), ''), CURRENT_TIMESTAMP)
				ELSE updated_timestamp
			END
		WHERE created_timestamp IS NULL
			OR updated_timestamp IS NULL
			OR TRIM(CAST(created_timestamp AS TEXT)) = ''
			OR TRIM(CAST(updated_timestamp AS TEXT)) = ''
	`)
}

func migrateV34(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, "jargons")
	if err != nil || !exists {
		return err
	}
	if err := ensureColumn(ctx, q, "jargons", "evidence_messages", "TEXT"); err != nil {
		return err
	}
	hasRaw, err := columnExists(ctx, q, "jargons", "raw_content")
	if err != nil {
		return err
	}
	if hasRaw {
		if err := q.Exec(ctx, "ALTER TABLE jargons DROP COLUMN raw_content"); err != nil {
			return err
		}
	}
	// 对齐 v33_to_v34.py：重建时用 COALESCE(created_by,'AI') 修复空值
	hasCreatedBy, err := columnExists(ctx, q, "jargons", "created_by")
	if err != nil {
		return err
	}
	if hasCreatedBy {
		if err := q.Exec(ctx, "UPDATE jargons SET created_by='AI' WHERE created_by IS NULL OR TRIM(created_by)=''"); err != nil {
			return err
		}
	}
	indexes := [][2]string{
		{"ix_jargons_created_timestamp", "created_timestamp"},
		{"ix_jargons_updated_timestamp", "updated_timestamp"},
		{"ix_jargons_jargon_count", "is_jargon,count DESC,id DESC"},
		{"ix_jargons_global_count", "is_global,count DESC,id DESC"},
		{"ix_jargons_complete_count", "is_complete,count DESC,id DESC"},
	}
	for _, item := range indexes {
		if err := ensureIndex(ctx, q, item[0], "jargons", item[1]); err != nil {
			return err
		}
	}
	return nil
}
