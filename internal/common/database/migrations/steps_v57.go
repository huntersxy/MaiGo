package migration

import "context"

// migrateV57 建立在线聊天流迁移任务与复制账本，对齐 Python v49→v52 的最终结构
func migrateV57(ctx context.Context, q querier) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS chat_migration_jobs (
			id VARCHAR NOT NULL PRIMARY KEY,
			status VARCHAR NOT NULL,
			source VARCHAR NOT NULL,
			target VARCHAR NOT NULL,
			created_at VARCHAR NOT NULL,
			payload TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS chat_migration_copies (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			table_name VARCHAR NOT NULL,
			origin VARCHAR NOT NULL,
			target VARCHAR NOT NULL,
			target_key TEXT NOT NULL,
			amounts TEXT NOT NULL DEFAULT '{}',
			UNIQUE (table_name, origin, target)
		)`,
		`CREATE INDEX IF NOT EXISTS ix_chat_migration_copies_table_key ON chat_migration_copies (table_name, target_key)`,
	}
	for _, statement := range statements {
		if err := q.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
