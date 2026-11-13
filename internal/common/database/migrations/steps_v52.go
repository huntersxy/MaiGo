package migration

import (
	"context"
	"database/sql"
)

// migrateV52 把 file_assets / file_bindings / file_parses 对齐 MaiBot src/files/models.py
//
// Python 侧三个模型的时间字段都是 float（time.time() 的 Unix 秒），SQLAlchemy 在 PostgreSQL
// 上编译为 DOUBLE PRECISION；Go v17 早期建表用了 TIMESTAMPTZ，并把 source/source_key 交给
// 运行期 ALTER 补列，还带了 Python create_all 不会产生的服务端默认值。
// v52 统一为 DOUBLE PRECISION、补齐 source/source_key、去掉服务端默认值，
// 使两侧列类型、列集与可空性一致。SQLite 无 files 生产建表路径，这里 no-op。
func migrateV52(ctx context.Context, q querier) error {
	if q.Dialect() == DialectSQLite {
		return nil
	}
	exists, err := tableExists(ctx, q, "file_assets")
	if err != nil || !exists {
		return err
	}
	// 1) 补 Python 模型里直接声明的两列
	if err := ensureColumn(ctx, q, "file_assets", "source", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, q, "file_assets", "source_key", "TEXT"); err != nil {
		return err
	}
	// 2) 时间列 TIMESTAMPTZ → DOUBLE PRECISION（已是浮点则跳过，保证可重跑）
	for _, column := range []struct{ table, name string }{
		{"file_assets", "created_at"},
		{"file_assets", "accessed_at"},
		{"file_assets", "leased_until"},
		{"file_assets", "transfer_started"},
		{"file_bindings", "created_at"},
		{"file_parses", "updated_at"},
	} {
		if err := timestampColumnToEpoch(ctx, q, column.table, column.name); err != nil {
			return err
		}
	}
	// 3) Python create_all 不写服务端默认值，Go 早期 DDL 有
	statements := []string{
		"ALTER TABLE file_assets ALTER COLUMN size DROP DEFAULT",
		"ALTER TABLE file_assets ALTER COLUMN sha256 DROP DEFAULT",
		"ALTER TABLE file_assets ALTER COLUMN origin DROP DEFAULT",
		"ALTER TABLE file_assets ALTER COLUMN state DROP DEFAULT",
		"ALTER TABLE file_assets ALTER COLUMN error DROP DEFAULT",
		"ALTER TABLE file_assets ALTER COLUMN reserved_bytes DROP DEFAULT",
		"ALTER TABLE file_assets ALTER COLUMN open_readers DROP DEFAULT",
		"ALTER TABLE file_assets ALTER COLUMN transfer_id DROP DEFAULT",
		"ALTER TABLE file_assets ALTER COLUMN mime_type DROP DEFAULT",
		"ALTER TABLE file_parses ALTER COLUMN parser_version DROP DEFAULT",
		"ALTER TABLE file_parses ALTER COLUMN encoding DROP DEFAULT",
		"ALTER TABLE file_parses ALTER COLUMN state DROP DEFAULT",
		"ALTER TABLE file_parses ALTER COLUMN error DROP DEFAULT",
		"ALTER TABLE file_bindings ALTER COLUMN message_id DROP DEFAULT",
		"ALTER TABLE file_assets ALTER COLUMN created_at DROP DEFAULT",
		"ALTER TABLE file_assets ALTER COLUMN accessed_at DROP DEFAULT",
		"ALTER TABLE file_assets ALTER COLUMN leased_until DROP DEFAULT",
		"ALTER TABLE file_assets ALTER COLUMN transfer_started DROP DEFAULT",
	}
	for _, statement := range statements {
		if err := q.Exec(ctx, statement); err != nil {
			return err
		}
	}
	// 4) source_key 改为普通唯一约束（Python 是 UNIQUE 列），去掉早期的部分唯一索引
	if err := q.Exec(ctx, "DROP INDEX IF EXISTS idx_file_assets_source_key"); err != nil {
		return err
	}
	var constraint int
	err = q.QueryRow(ctx, `SELECT 1 FROM pg_constraint WHERE conname = 'file_assets_source_key_key'`).Scan(&constraint)
	if err == sql.ErrNoRows {
		return q.Exec(ctx, "ALTER TABLE file_assets ADD CONSTRAINT file_assets_source_key_key UNIQUE (source_key)")
	}
	return err
}

// timestampColumnToEpoch 把 TIMESTAMP/TIMESTAMPTZ 列改成 Unix 秒 DOUBLE PRECISION
//
// 已经是浮点或整型的实例直接跳过，避免重复 ALTER 报错。
func timestampColumnToEpoch(ctx context.Context, q querier, table, column string) error {
	var dataType string
	err := q.QueryRow(ctx, `SELECT data_type FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`, table, column).Scan(&dataType)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	switch dataType {
	case "timestamp with time zone", "timestamp without time zone":
	default:
		return nil
	}
	statement := "ALTER TABLE " + table + " ALTER COLUMN " + column +
		" TYPE DOUBLE PRECISION USING EXTRACT(EPOCH FROM " + column + ")::double precision"
	if err := q.Exec(ctx, statement); err != nil {
		return err
	}
	return q.Exec(ctx, "ALTER TABLE "+table+" ALTER COLUMN "+column+" SET NOT NULL")
}
