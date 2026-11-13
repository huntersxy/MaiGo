package migration

import (
	"context"
	"database/sql"
)

// migrateV49 去掉 llm_usage.model_assign_name 的历史空串默认值
// Python ModelUsage.model_assign_name 无 server_default（NULL 表示未分配），v30 建表时误带空串默认值
// v48 只放宽可空，PG 上该默认值仍在；SQLite 的 v48 重建已去掉默认值，这里无需处理
func migrateV49(ctx context.Context, q querier) error {
	if q.Dialect() == DialectSQLite {
		return nil
	}
	exists, err := tableExists(ctx, q, "llm_usage")
	if err != nil || !exists {
		return err
	}
	defaultExpr, err := columnDefault(ctx, q, "llm_usage", "model_assign_name")
	if err != nil || defaultExpr == "" {
		return err
	}
	return q.Exec(ctx, "ALTER TABLE llm_usage ALTER COLUMN model_assign_name DROP DEFAULT")
}

// columnDefault 读取列默认表达式；无默认值时返回空串
// PG 查 information_schema.column_default，SQLite 查 pragma_table_info.dflt_value
func columnDefault(ctx context.Context, q querier, table, column string) (string, error) {
	var value sql.NullString
	var err error
	if q.Dialect() == DialectSQLite {
		err = q.QueryRow(ctx, "SELECT dflt_value FROM pragma_table_info(?) WHERE name=?", table, column).Scan(&value)
	} else {
		err = q.QueryRow(ctx, "SELECT column_default FROM information_schema.columns "+
			"WHERE table_schema=current_schema() AND table_name=? AND column_name=?", table, column).Scan(&value)
	}
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return value.String, nil
}
