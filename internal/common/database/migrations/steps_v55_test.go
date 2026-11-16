package migration

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/glebarez/go-sqlite"
)

type v55TestQuerier struct{ db *sql.DB }

func (q v55TestQuerier) Dialect() dialect { return DialectSQLite }
func (q v55TestQuerier) Exec(ctx context.Context, query string, args ...any) error {
	_, err := q.db.ExecContext(ctx, query, args...)
	return err
}
func (q v55TestQuerier) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return q.db.QueryContext(ctx, query, args...)
}
func (q v55TestQuerier) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return q.db.QueryRowContext(ctx, query, args...)
}

func TestMigrateV55CreatesHelpHistoryTables(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer db.Close()
	q := v55TestQuerier{db: db}
	if err := migrateV55(context.Background(), q); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if err := migrateV55(context.Background(), q); err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}
	for _, table := range []string{"webui_help_sessions", "webui_help_turns"} {
		var count int64
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatalf("检查表 %s 失败: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("缺少表 %s", table)
		}
	}
}
