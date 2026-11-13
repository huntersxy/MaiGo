// Package schema 管理统计相关的表与增量列
//
// 迁移文件不在本模块负责范围，统计表使用 CREATE TABLE IF NOT EXISTS 在运行时补齐
package schema

import (
	"context"
	"strings"
	"sync"

	"gorm.io/gorm"
)

var (
	schemaMu     sync.Mutex
	schemaStates = map[*gorm.DB]*schemaState{}
)

type schemaState struct {
	once sync.Once
	err  error
}

// Ensure 创建统计表并为旧 model_call_hourly 补齐 token 拆分列
func Ensure(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return nil
	}
	schemaMu.Lock()
	state := schemaStates[db]
	if state == nil {
		state = &schemaState{}
		schemaStates[db] = state
	}
	schemaMu.Unlock()
	state.once.Do(func() { state.err = ensure(ctx, db) })
	if state.err != nil {
		schemaMu.Lock()
		delete(schemaStates, db)
		schemaMu.Unlock()
	}
	return state.err
}

func ensure(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, statement := range schemaStatements(tx) {
			if err := tx.Exec(statement).Error; err != nil {
				return err
			}
		}
		if schemaDialect(tx) == "sqlite" {
			return ensureSQLiteModelCallHourlyColumns(ctx, tx)
		}
		return nil
	})
}

func schemaDialect(db *gorm.DB) string {
	if db != nil && db.Dialector != nil && strings.EqualFold(db.Dialector.Name(), "sqlite") {
		return "sqlite"
	}
	return "postgres"
}

func schemaStatements(db *gorm.DB) []string {
	if schemaDialect(db) == "sqlite" {
		return sqliteStatements
	}
	return postgresStatements
}

var postgresStatements = []string{
	`CREATE TABLE IF NOT EXISTS online_time (
		id BIGSERIAL PRIMARY KEY,
		timestamp TIMESTAMPTZ NOT NULL DEFAULT now(),
		duration_minutes INTEGER NOT NULL DEFAULT 5,
		start_timestamp TIMESTAMPTZ NOT NULL DEFAULT now(),
		end_timestamp TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_online_time_end ON online_time(end_timestamp DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_online_time_start ON online_time(start_timestamp)`,
	`CREATE TABLE IF NOT EXISTS statistics_message_hourly (
		id BIGSERIAL PRIMARY KEY,
		bucket_time TIMESTAMPTZ NOT NULL,
		chat_id TEXT NOT NULL,
		chat_name TEXT NOT NULL DEFAULT '',
		chat_type TEXT NOT NULL DEFAULT '',
		message_count BIGINT NOT NULL DEFAULT 0,
		latest_timestamp TIMESTAMPTZ NOT NULL,
		CONSTRAINT uq_statistics_message_hourly_bucket_chat UNIQUE(bucket_time,chat_id)
	)`,
	`CREATE INDEX IF NOT EXISTS ix_statistics_message_hourly_bucket_time ON statistics_message_hourly(bucket_time)`,
	`CREATE TABLE IF NOT EXISTS statistics_tool_hourly (
		id BIGSERIAL PRIMARY KEY,
		bucket_time TIMESTAMPTZ NOT NULL,
		tool_name TEXT NOT NULL,
		call_count BIGINT NOT NULL DEFAULT 0,
		CONSTRAINT uq_statistics_tool_hourly_bucket_tool UNIQUE(bucket_time,tool_name)
	)`,
	`CREATE INDEX IF NOT EXISTS ix_statistics_tool_hourly_bucket_time ON statistics_tool_hourly(bucket_time)`,
	`CREATE TABLE IF NOT EXISTS statistics_model_hourly (
		id BIGSERIAL PRIMARY KEY,
		bucket_time TIMESTAMPTZ NOT NULL,
		request_type TEXT NOT NULL,
		module_name TEXT NOT NULL DEFAULT '',
		provider_name TEXT NOT NULL DEFAULT '',
		model_name TEXT NOT NULL,
		request_count BIGINT NOT NULL DEFAULT 0,
		prompt_tokens BIGINT NOT NULL DEFAULT 0,
		completion_tokens BIGINT NOT NULL DEFAULT 0,
		total_tokens BIGINT NOT NULL DEFAULT 0,
		cost DOUBLE PRECISION NOT NULL DEFAULT 0,
		time_cost_sum DOUBLE PRECISION NOT NULL DEFAULT 0,
		time_cost_sq_sum DOUBLE PRECISION NOT NULL DEFAULT 0,
		CONSTRAINT uq_statistics_model_hourly_bucket_request_model_provider
			UNIQUE(bucket_time,request_type,model_name,provider_name)
	)`,
	`CREATE INDEX IF NOT EXISTS ix_statistics_model_hourly_bucket_time ON statistics_model_hourly(bucket_time)`,
	`CREATE TABLE IF NOT EXISTS statistics_aggregation_cursors (
		source_name TEXT PRIMARY KEY,
		last_processed_id BIGINT NOT NULL DEFAULT 0,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE TABLE IF NOT EXISTS llm_usage (
		id BIGSERIAL PRIMARY KEY,
		model_name TEXT NOT NULL,
		model_assign_name TEXT,
		model_api_provider_name TEXT NOT NULL,
		session_id TEXT NOT NULL,
		task_name TEXT,
		request_type TEXT NOT NULL,
		time_cost DOUBLE PRECISION,
		timestamp TIMESTAMPTZ,
		prompt_tokens INTEGER NOT NULL,
		completion_tokens INTEGER NOT NULL,
		total_tokens INTEGER NOT NULL,
		prompt_cache_enabled BOOLEAN NOT NULL DEFAULT FALSE,
		prompt_cache_hit_tokens INTEGER NOT NULL DEFAULT 0,
		prompt_cache_miss_tokens INTEGER NOT NULL DEFAULT 0,
		cost DOUBLE PRECISION NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS ix_llm_usage_timestamp ON llm_usage(timestamp)`,
	`CREATE INDEX IF NOT EXISTS ix_llm_usage_task ON llm_usage(task_name)`,
	`ALTER TABLE model_call_hourly ADD COLUMN IF NOT EXISTS prompt_tokens BIGINT NOT NULL DEFAULT 0`,
	`ALTER TABLE model_call_hourly ADD COLUMN IF NOT EXISTS completion_tokens BIGINT NOT NULL DEFAULT 0`,
}

var sqliteStatements = []string{
	`CREATE TABLE IF NOT EXISTS online_time (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		duration_minutes INTEGER NOT NULL DEFAULT 5,
		start_timestamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		end_timestamp TIMESTAMP NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_online_time_end ON online_time(end_timestamp DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_online_time_start ON online_time(start_timestamp)`,
	`CREATE TABLE IF NOT EXISTS statistics_message_hourly (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		bucket_time TIMESTAMP NOT NULL,
		chat_id TEXT NOT NULL,
		chat_name TEXT NOT NULL DEFAULT '',
		chat_type TEXT NOT NULL DEFAULT '',
		message_count INTEGER NOT NULL DEFAULT 0,
		latest_timestamp TIMESTAMP NOT NULL,
		CONSTRAINT uq_statistics_message_hourly_bucket_chat UNIQUE(bucket_time,chat_id)
	)`,
	`CREATE INDEX IF NOT EXISTS ix_statistics_message_hourly_bucket_time ON statistics_message_hourly(bucket_time)`,
	`CREATE TABLE IF NOT EXISTS statistics_tool_hourly (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		bucket_time TIMESTAMP NOT NULL,
		tool_name TEXT NOT NULL,
		call_count INTEGER NOT NULL DEFAULT 0,
		CONSTRAINT uq_statistics_tool_hourly_bucket_tool UNIQUE(bucket_time,tool_name)
	)`,
	`CREATE INDEX IF NOT EXISTS ix_statistics_tool_hourly_bucket_time ON statistics_tool_hourly(bucket_time)`,
	`CREATE TABLE IF NOT EXISTS statistics_model_hourly (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		bucket_time TIMESTAMP NOT NULL,
		request_type TEXT NOT NULL,
		module_name TEXT NOT NULL DEFAULT '',
		provider_name TEXT NOT NULL DEFAULT '',
		model_name TEXT NOT NULL,
		request_count INTEGER NOT NULL DEFAULT 0,
		prompt_tokens INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		cost REAL NOT NULL DEFAULT 0,
		time_cost_sum REAL NOT NULL DEFAULT 0,
		time_cost_sq_sum REAL NOT NULL DEFAULT 0,
		CONSTRAINT uq_statistics_model_hourly_bucket_request_model_provider
			UNIQUE(bucket_time,request_type,model_name,provider_name)
	)`,
	`CREATE INDEX IF NOT EXISTS ix_statistics_model_hourly_bucket_time ON statistics_model_hourly(bucket_time)`,
	`CREATE TABLE IF NOT EXISTS statistics_aggregation_cursors (
		source_name TEXT PRIMARY KEY,
		last_processed_id INTEGER NOT NULL DEFAULT 0,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS llm_usage (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		model_name TEXT NOT NULL,
		model_assign_name TEXT,
		model_api_provider_name TEXT NOT NULL,
		session_id TEXT NOT NULL,
		task_name TEXT,
		request_type TEXT NOT NULL,
		time_cost REAL,
		timestamp TIMESTAMP,
		prompt_tokens INTEGER NOT NULL,
		completion_tokens INTEGER NOT NULL,
		total_tokens INTEGER NOT NULL,
		prompt_cache_enabled BOOLEAN NOT NULL DEFAULT 0,
		prompt_cache_hit_tokens INTEGER NOT NULL DEFAULT 0,
		prompt_cache_miss_tokens INTEGER NOT NULL DEFAULT 0,
		cost REAL NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS ix_llm_usage_timestamp ON llm_usage(timestamp)`,
	`CREATE INDEX IF NOT EXISTS ix_llm_usage_task ON llm_usage(task_name)`,
}

// ensureSQLiteModelCallHourlyColumns SQLite 不支持 ADD COLUMN IF NOT EXISTS，按列探测后补齐
func ensureSQLiteModelCallHourlyColumns(ctx context.Context, db *gorm.DB) error {
	var exists int64
	if err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='model_call_hourly'`).Scan(&exists).Error; err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	columns := []struct {
		name       string
		definition string
	}{
		{name: "prompt_tokens", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "completion_tokens", definition: "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, column := range columns {
		var present int64
		if err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM pragma_table_info('model_call_hourly') WHERE name = ?`, column.name).Scan(&present).Error; err != nil {
			return err
		}
		if present > 0 {
			continue
		}
		if err := db.WithContext(ctx).Exec("ALTER TABLE model_call_hourly ADD COLUMN " + column.name + " " + column.definition).Error; err != nil {
			return err
		}
	}
	return nil
}
