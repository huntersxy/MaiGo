package migration

import (
	"context"
	"database/sql"
	"fmt"
)

// dialect 是迁移 SQL 需要区分的最小方言集合
type dialect string

const (
	DialectPostgres dialect = "postgres"
	DialectSQLite   dialect = "sqlite"
)

// querier 是迁移引擎依赖的最小数据库接口
type querier interface {
	Dialect() dialect
	Exec(ctx context.Context, query string, args ...any) error
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, query string, args ...any) *sql.Row
}

// migrationStep 描述一个相邻版本升级
type migrationStep struct {
	Version     int
	Name        string
	Description string
	Apply       func(ctx context.Context, q querier) error
}

// MigrationVersions 返回已注册的升级版本，便于宿主与测试核对
func MigrationVersions() []int {
	result := make([]int, 0, len(migrationSteps))
	for _, step := range migrationSteps {
		result = append(result, step.Version)
	}
	return result
}

// MigrationName 返回指定版本迁移名
func MigrationName(version int) string {
	for _, step := range migrationSteps {
		if step.Version == version {
			return step.Name
		}
	}
	return ""
}

func applyMigrations(ctx context.Context, q querier, from, to int) (int, error) {
	if err := ensureSchemaMigrationsTable(ctx, q); err != nil {
		return 0, err
	}
	applied := 0
	for _, step := range migrationSteps {
		if step.Version <= from || step.Version > to {
			continue
		}
		if err := step.Apply(ctx, q); err != nil {
			return applied, fmt.Errorf("数据库迁移 v%d(%s) 失败: %w", step.Version, step.Name, err)
		}
		if err := recordVersion(ctx, q, step.Version, step.Name); err != nil {
			return applied, fmt.Errorf("记录数据库版本 v%d 失败: %w", step.Version, err)
		}
		applied++
	}
	return applied, nil
}

func ensureSchemaMigrationsTable(ctx context.Context, q querier) error {
	if exists, err := tableExists(ctx, q, "schema_migrations"); err != nil || exists {
		return err
	}
	var ddl string
	switch q.Dialect() {
	case DialectSQLite:
		ddl = "CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)"
	default:
		ddl = "CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())"
	}
	return q.Exec(ctx, ddl)
}

func recordVersion(ctx context.Context, q querier, version int, name string) error {
	return q.Exec(ctx,
		"INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,CURRENT_TIMESTAMP) ON CONFLICT (version) DO NOTHING",
		version, name)
}

func tableExists(ctx context.Context, q querier, table string) (bool, error) {
	var query string
	switch q.Dialect() {
	case DialectSQLite:
		query = "SELECT 1 FROM sqlite_master WHERE type='table' AND name=?"
	default:
		query = "SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name=?"
	}
	var one int
	err := q.QueryRow(ctx, query, table).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func columnExists(ctx context.Context, q querier, table, column string) (bool, error) {
	var query string
	switch q.Dialect() {
	case DialectSQLite:
		query = "SELECT 1 FROM pragma_table_info(?) WHERE name=?"
	default:
		query = "SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=? AND column_name=?"
	}
	var one int
	err := q.QueryRow(ctx, query, table, column).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func indexExists(ctx context.Context, q querier, index string) (bool, error) {
	var query string
	switch q.Dialect() {
	case DialectSQLite:
		query = "SELECT 1 FROM sqlite_master WHERE type='index' AND name=?"
	default:
		query = "SELECT 1 FROM pg_indexes WHERE schemaname=current_schema() AND indexname=?"
	}
	var one int
	err := q.QueryRow(ctx, query, index).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func ensureIndex(ctx context.Context, q querier, index, table, columns string) error {
	exists, err := indexExists(ctx, q, index)
	if err != nil || exists {
		return err
	}
	return q.Exec(ctx, fmt.Sprintf("CREATE INDEX %s ON %s(%s)", index, table, columns))
}

// ensureUniqueIndex 与 ensureIndex 相同，但创建唯一索引；表级 UNIQUE 约束一律改走唯一索引，
// 避免 GORM AutoMigrate 重建含 UNIQUE 关键字的表时被 glebarez 驱动误解析为列名
func ensureUniqueIndex(ctx context.Context, q querier, index, table, columns string) error {
	exists, err := indexExists(ctx, q, index)
	if err != nil || exists {
		return err
	}
	return q.Exec(ctx, fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s(%s)", index, table, columns))
}

func ensureColumn(ctx context.Context, q querier, table, column, definition string) error {
	exists, err := columnExists(ctx, q, table, column)
	if err != nil || exists {
		return err
	}
	return q.Exec(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
}

func dropTable(ctx context.Context, q querier, table string) error {
	return q.Exec(ctx, "DROP TABLE IF EXISTS "+table)
}

func serialPK(q querier) string {
	if q.Dialect() == DialectSQLite {
		return "INTEGER PRIMARY KEY AUTOINCREMENT"
	}
	return "BIGSERIAL PRIMARY KEY"
}

func blobType(q querier) string {
	if q.Dialect() == DialectSQLite {
		return "BLOB"
	}
	return "BYTEA"
}

func timestampType(q querier) string {
	if q.Dialect() == DialectSQLite {
		return "TIMESTAMP"
	}
	return "TIMESTAMPTZ"
}

func jsonType(q querier) string {
	if q.Dialect() == DialectSQLite {
		return "TEXT"
	}
	return "JSONB"
}

func validateLatestSchema(ctx context.Context, q querier) error {
	for _, table := range latestTables {
		exists, err := tableExists(ctx, q, table)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("v%d 缺少表 %s", latestSchemaVersion, table)
		}
	}
	for _, column := range latestColumns {
		exists, err := columnExists(ctx, q, column.table, column.name)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("v%d 缺少字段 %s.%s", latestSchemaVersion, column.table, column.name)
		}
	}
	var version int
	if err := q.QueryRow(ctx, "SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&version); err != nil {
		return err
	}
	if version != latestSchemaVersion {
		return fmt.Errorf("schema 版本记录为 %d，期望 %d", version, latestSchemaVersion)
	}
	return nil
}

var latestTables = []string{
	"schema_migrations", "mai_messages", "message_forwards", "conversation_sessions",
	"conversation_transcript", "context_compaction_checkpoints", "topic_threads",
	"topic_assignments", "topic_summaries", "topic_summary_sources", "knowledge_items",
	"knowledge_revisions", "emotion_states", "emotion_revisions", "member_profiles",
	"member_names", "person_info", "images", "mood_state", "model_call_hourly",
	"message_traffic_hourly", "message_traffic_chat_hourly", "online_time_daily",
	"conversation_targets", "chat_sessions", "session_messages", "app_config",
	"model_profiles", "mcp_servers", "expressions", "jargons",
	"high_frequency_terms", "file_assets", "file_bindings", "file_parses",
	"maisaka_monitor_events", "one_time_maintenance_tasks", "tool_records",
	"llm_usage", "maisaka_reply_effects", "bot_platform_accounts", "model_task_usage",
}

type columnRef struct{ table, name string }

var latestColumns = []columnRef{
	{"mai_messages", "account_id"},
	{"mai_messages", "scope"},
	{"mai_messages", "external_message_id"},
	{"mai_messages", "received_at"},
	{"mai_messages", "user_cardname"},
	{"mai_messages", "group_id"},
	{"mai_messages", "group_name"},
	{"mai_messages", "is_at"},
	{"mai_messages", "is_emoji"},
	{"mai_messages", "is_picture"},
	{"mai_messages", "is_command"},
	{"mai_messages", "is_notify"},
	{"mai_messages", "additional_config"},
	{"maisaka_reply_effects", "request_fingerprint"},
	{"maisaka_reply_effects", "record_blob"},
	{"high_frequency_terms", "chat_id"},
	{"jargons", "evidence_messages"},
	{"llm_usage", "session_id"},
}
