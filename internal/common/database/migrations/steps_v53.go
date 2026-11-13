package migration

import (
	"context"
	"database/sql"
	"errors"
)

// migrateV53 把 person_info 的列类型对齐 Python SQLModel 在 PostgreSQL 上编译出的 DDL
//
// Python src/common/database/database_model.py:386-414 的字段声明：
// person_id VARCHAR(255)、person_name VARCHAR(255)、name_reason VARCHAR、
// platform VARCHAR(100)、user_id VARCHAR(255)、user_nickname VARCHAR(255)、
// group_cardname VARCHAR、memory_points VARCHAR。
// Go v17 建表把前六列写成 TEXT、后两列写成 JSONB；JSONB 与 Python 的
// `json.loads(record.group_cardname)` / `json.loads(record.memory_points)` 不兼容
// （psycopg 会把 jsonb 直接解码成对象），必须改成文本列。
// 历史 v17 DDL 不改；SQLite 无 person_info 生产建表路径，这里 no-op。
func migrateV53(ctx context.Context, q querier) error {
	if q.Dialect() == DialectSQLite {
		return nil
	}
	exists, err := tableExists(ctx, q, "person_info")
	if err != nil || !exists {
		return err
	}
	targets := []struct {
		column   string
		dataType string
	}{
		{"person_id", "character varying"},
		{"person_name", "character varying"},
		{"name_reason", "character varying"},
		{"platform", "character varying"},
		{"user_id", "character varying"},
		{"user_nickname", "character varying"},
		{"group_cardname", "character varying"},
		{"memory_points", "character varying"},
	}
	for _, target := range targets {
		current, err := columnDataType(ctx, q, "person_info", target.column)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return err
		}
		if current == target.dataType {
			continue
		}
		if err := q.Exec(ctx, personInfoColumnTypeStatement(target.column)); err != nil {
			return err
		}
	}
	return nil
}

// personInfoColumnTypeStatement 生成带长度约束的类型变更语句，与 Python DDL 的长度一致
func personInfoColumnTypeStatement(column string) string {
	length := ""
	switch column {
	case "person_id", "person_name", "user_id", "user_nickname":
		length = "(255)"
	case "platform":
		length = "(100)"
	}
	return "ALTER TABLE person_info ALTER COLUMN " + column + " TYPE VARCHAR" + length + " USING " + column + "::VARCHAR" + length
}

func columnDataType(ctx context.Context, q querier, table, column string) (string, error) {
	var dataType string
	row := q.QueryRow(ctx, "SELECT data_type FROM information_schema.columns "+
		"WHERE table_schema=current_schema() AND table_name=? AND column_name=?", table, column)
	if err := row.Scan(&dataType); err != nil {
		return "", err
	}
	return dataType, nil
}
