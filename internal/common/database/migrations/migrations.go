package migration

import (
	"context"
	"fmt"

	"maibot-go/internal/common/database"

	"gorm.io/gorm"
)

// LatestSchemaVersion 返回当前程序支持的数据结构版本。
func LatestSchemaVersion() int { return latestSchemaVersion }

// RunMigrations 初始化空库，或把 v17..v54 的库升级到最新版本
//
// 旧库不是一律拒绝：完整 v17 结构会补写版本记录，更高版本会按步骤推进
// 无法识别的旧库返回 LegacySchemaError，宿主可先调用 GuardSchema 决定处理方式
//
// 默认数据库是 SQLite（对齐 MaiBot src/common/database/database.py:33），
// PostgreSQL 仅保留给已有部署的迁移期使用。
func RunMigrations(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}
	isPostgres := db.Dialector.Name() == "postgres"
	var maintenance []migrationMaintenanceJob
	if err := db.Transaction(func(tx *gorm.DB) error {
		if isPostgres {
			// SQLite 是单写者模型，且迁移在进程启动期单线程执行，无需 advisory lock
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext('maibot_schema_migrations'))").Error; err != nil {
				return fmt.Errorf("获取数据库迁移锁失败: %w", err)
			}
			if err := ensureExtensions(tx); err != nil {
				return err
			}
		}
		q := gormQuerier{db: tx, maintenance: &maintenance}
		state, err := resolveSchema(context.Background(), q)
		if err != nil {
			return err
		}
		switch {
		case state.Empty:
			if err := initializeV17Schema(tx); err != nil {
				return err
			}
			if err := recordSchemaVersion(tx, baseSchemaVersion, v17BaseName); err != nil {
				return err
			}
			state.Version = baseSchemaVersion
		case state.Legacy:
			return &LegacySchemaError{State: state}
		case state.Version > latestSchemaVersion:
			return &NewerSchemaError{Version: state.Version, Latest: latestSchemaVersion}
		case state.Source == SchemaSourceStructure || state.AdoptableV17:
			if err := ensureSchemaMigrationsTable(context.Background(), q); err != nil {
				return err
			}
			if err := recordSchemaVersion(tx, state.Version, "adopt_existing_v17_schema"); err != nil {
				return err
			}
			state.Version = baseSchemaVersion
		case state.Version < baseSchemaVersion:
			return &LegacySchemaError{State: state}
		}
		if state.Version == baseSchemaVersion {
			if err := validateV17Schema(tx); err != nil {
				return err
			}
		}
		if _, err := applyMigrations(context.Background(), q, state.Version, latestSchemaVersion); err != nil {
			return err
		}
		return validateLatestSchema(context.Background(), q)
	}); err != nil {
		return err
	}
	if len(maintenance) == 0 {
		return nil
	}
	postCommit := gormQuerier{db: db}
	for _, job := range maintenance {
		if err := job(context.Background(), postCommit); err != nil {
			return fmt.Errorf("数据库迁移维护步骤失败: %w", err)
		}
	}
	return nil
}

func ensureExtensions(db *gorm.DB) error {
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public").Error; err != nil {
		return fmt.Errorf("启用 PostgreSQL 扩展 pg_trgm 失败: %w", err)
	}
	return nil
}

func recordSchemaVersion(db *gorm.DB, version int, name string) error {
	return db.Exec("INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,CURRENT_TIMESTAMP) ON CONFLICT (version) DO NOTHING", version, name).Error
}

func validateV17Schema(db *gorm.DB) error {
	for _, table := range []string{
		"schema_migrations", "mai_messages", "message_forwards", "conversation_sessions",
		"conversation_transcript", "context_compaction_checkpoints", "topic_threads",
		"topic_assignments", "topic_summaries", "topic_summary_sources", "knowledge_items",
		"knowledge_revisions", "emotion_states", "emotion_revisions", "member_profiles",
		"member_names", "person_info", "mood_state", "model_call_hourly", "message_traffic_hourly", "message_traffic_chat_hourly", "online_time_daily", "conversation_targets", "chat_sessions",
		"session_messages", "app_config", "model_profiles", "mcp_servers",
		"expressions", "jargons", "high_frequency_terms", "file_assets", "file_bindings", "file_parses", "maisaka_monitor_events",
	} {
		if !db.Migrator().HasTable(table) {
			return fmt.Errorf("v17 缺少表 %s", table)
		}
	}
	for _, column := range []struct{ table, name string }{
		{"conversation_sessions", "reference_seen"},
		{"mai_messages", "message_id"},
		{"mai_messages", "platform"},
		{"mai_messages", "session_id"},
		{"mai_messages", "received_at"},
		{"mai_messages", "reply_frequency"},
		{"mai_messages", "raw_content"},
		{"mai_messages", "external_message_id"},
		{"mai_messages", "external_target_id"},
		{"mai_messages", "external_user_id"},
		{"mai_messages", "account_id"},
		{"mai_messages", "scope"},
		{"knowledge_items", "subject_type"},
		{"knowledge_items", "subject_target"},
		{"knowledge_items", "memory_type"},
		{"knowledge_items", "pinned"},
		{"topic_summary_sources", "is_anchor"},
		{"context_compaction_checkpoints", "reference_versions"},
		{"app_config", "config_key"},
		{"model_profiles", "thinking_type"},
		{"model_profiles", "reasoning_effort"},
		{"mcp_servers", "config_ciphertext"},
		{"file_assets", "reserved_bytes"},
		{"file_assets", "transfer_id"},
		{"file_assets", "transfer_started"},
		{"file_assets", "leased_until"},
		{"file_assets", "open_readers"},
		{"file_parses", "parser_version"},
		{"file_parses", "encoding"},
		{"file_parses", "state"},
		{"file_parses", "error"},
	} {
		if !db.Migrator().HasColumn(column.table, column.name) {
			return fmt.Errorf("v17 缺少字段 %s.%s", column.table, column.name)
		}
	}
	var version int
	if err := db.Model(&memory.SchemaMigration{}).Select("COALESCE(max(version),0)").Scan(&version).Error; err != nil {
		return err
	}
	if version != baseSchemaVersion {
		return fmt.Errorf("v17 schema 版本记录为 %d，期望 %d", version, baseSchemaVersion)
	}
	return nil
}
