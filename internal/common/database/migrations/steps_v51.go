package migration

import (
	"context"
)

// migrateV51 对齐 Python PersonInfo 的可空性与默认值
// Python src/common/database/database_model.py:386-414：user_nickname 为 NOT NULL 但无 server default，
// memory_points 为 Optional[str]（可空）且无 server default；SQLAlchemy PostgreSQL dialect 编译结果为
// user_nickname VARCHAR(255) NOT NULL、memory_points VARCHAR，均无 DEFAULT
// Go v17 建表给 user_nickname/platform/user_id 带了空串服务端默认，并给 memory_points 加了 NOT NULL DEFAULT '[]'::jsonb
// v51 去掉这些数据库层默认并放宽 memory_points，使 Python 可写 NULL、Go 也不再依赖默认值伪造空数组
// 历史 v17 DDL 不改；SQLite 无 person_info 生产建表路径（v17 初始化仅 PostgreSQL），这里 no-op
func migrateV51(ctx context.Context, q querier) error {
	if q.Dialect() == DialectSQLite {
		return nil
	}
	exists, err := tableExists(ctx, q, "person_info")
	if err != nil || !exists {
		return err
	}
	statements := []string{
		"ALTER TABLE person_info ALTER COLUMN user_nickname DROP DEFAULT",
		"ALTER TABLE person_info ALTER COLUMN platform DROP DEFAULT",
		"ALTER TABLE person_info ALTER COLUMN user_id DROP DEFAULT",
		"ALTER TABLE person_info ALTER COLUMN memory_points DROP NOT NULL",
		"ALTER TABLE person_info ALTER COLUMN memory_points DROP DEFAULT",
	}
	for _, statement := range statements {
		if err := q.Exec(ctx, statement); err != nil {
			return err
		}
	}
	// Python create_all 在 user_nickname 上建索引，Go v17 漏建；补建且可重复执行
	return ensureIndex(ctx, q, "ix_person_info_user_nickname", "person_info", "user_nickname")
}
