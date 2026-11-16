package migration

import "context"

// migrateV55 新增 WebUI 答疑会话与问答轮次表，对齐 Python v46_to_v47。
func migrateV55(ctx context.Context, q querier) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS webui_help_sessions (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			updated_at DOUBLE PRECISION NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS ix_webui_help_sessions_updated_at ON webui_help_sessions(updated_at)`,
		`CREATE TABLE IF NOT EXISTS webui_help_turns (
			id BIGSERIAL PRIMARY KEY,
			session_id TEXT NOT NULL REFERENCES webui_help_sessions(id),
			question TEXT NOT NULL,
			reply_json TEXT,
			error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS ix_webui_help_turns_session_id ON webui_help_turns(session_id)`,
	}
	if q.Dialect() == DialectSQLite {
		statements = []string{
			`CREATE TABLE IF NOT EXISTS webui_help_sessions (
				id TEXT PRIMARY KEY,
				title TEXT NOT NULL,
				updated_at REAL NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS ix_webui_help_sessions_updated_at ON webui_help_sessions(updated_at)`,
			`CREATE TABLE IF NOT EXISTS webui_help_turns (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id TEXT NOT NULL REFERENCES webui_help_sessions(id),
				question TEXT NOT NULL,
				reply_json TEXT,
				error TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX IF NOT EXISTS ix_webui_help_turns_session_id ON webui_help_turns(session_id)`,
		}
	}
	for _, statement := range statements {
		if err := q.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
