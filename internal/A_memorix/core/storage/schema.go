package storage

// A_memorix 元数据 schema 入口，对齐 Python core/storage/metadata_schema.py
// 负责建表初始化、版本校验、运行时自动迁移的对外方法

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// SchemaVersion 当前 metadata schema 版本，对齐 Python SCHEMA_VERSION
	SchemaVersion = 29
	// RuntimeAutoMigrationMinSchemaVersion 允许运行时自动迁移的最低版本
	// 对齐 Python RUNTIME_AUTO_MIGRATION_MIN_SCHEMA_VERSION
	RuntimeAutoMigrationMinSchemaVersion = 9
)

// ErrLegacySchema 缺少 schema_migrations 且库已存在，文案与 Python 逐字一致
var ErrLegacySchema = errors.New("检测到旧版 metadata schema（缺少 schema_migrations）。 请先执行 scripts/release_vnext_migrate.py migrate。")

// schemaVersionMismatchError 版本不匹配错误，文案与 Python 逐字一致
func schemaVersionMismatchError(current int) error {
	return fmt.Errorf(
		"metadata schema 版本不匹配: current=%d, expected=%d。 请执行 scripts/release_vnext_migrate.py migrate。",
		current,
		SchemaVersion,
	)
}

// EnsureSchema 幂等创建/升级元数据表结构，对齐 Python _initialize_tables
func (s *Store) EnsureSchema() error {
	conn, err := s.Conn()
	if err != nil {
		return err
	}
	return s.initializeTables(conn)
}

// AssertSchemaCompatible 校验 schema 版本，必要时执行运行时自动迁移
// 对齐 Python _assert_schema_compatible
func (s *Store) AssertSchemaCompatible(dbExisted bool) error {
	conn, err := s.Conn()
	if err != nil {
		return err
	}
	hasVersionTable, err := s.tableExists(conn, "schema_migrations")
	if err != nil {
		return err
	}
	if !hasVersionTable {
		if dbExisted {
			return ErrLegacySchema
		}
		return nil
	}

	version, err := s.schemaVersionOn(conn)
	if err != nil {
		return err
	}
	if version < SchemaVersion && version >= RuntimeAutoMigrationMinSchemaVersion {
		if err := s.runRuntimeAutoMigration(conn, version); err != nil {
			return err
		}
		version, err = s.schemaVersionOn(conn)
		if err != nil {
			return err
		}
	}
	if version != SchemaVersion {
		return schemaVersionMismatchError(version)
	}
	return nil
}

// SchemaVersion 读取 schema_migrations 的最大版本号，对齐 Python get_schema_version
// 表不存在或表为空时返回 0
func (s *Store) SchemaVersion() (int, error) {
	conn, err := s.Conn()
	if err != nil {
		return 0, err
	}
	return s.schemaVersionOn(conn)
}

// SetSchemaVersion 写入 schema 版本，对齐 Python set_schema_version
// 使用 INSERT OR REPLACE，重复写同一版本号不报错
func (s *Store) SetSchemaVersion(version int) error {
	conn, err := s.Conn()
	if err != nil {
		return err
	}
	return s.setSchemaVersionOn(conn, version)
}

// schemaVersionOn 在指定连接上读取最大版本号
func (s *Store) schemaVersionOn(conn *Conn) (int, error) {
	hasVersionTable, err := s.tableExists(conn, "schema_migrations")
	if err != nil {
		return 0, err
	}
	if !hasVersionTable {
		return 0, nil
	}
	var version sql.NullInt64
	if err := conn.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		return 0, err
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

// setSchemaVersionOn 在指定连接上写入版本号
func (s *Store) setSchemaVersionOn(conn *Conn, version int) error {
	if _, err := conn.Exec(ddlSchemaMigrations); err != nil {
		return err
	}
	if _, err := conn.Exec(
		"INSERT OR REPLACE INTO schema_migrations(version, applied_at) VALUES (?, ?)",
		version,
		nowTimestamp(),
	); err != nil {
		return err
	}
	return conn.Commit()
}

// nowTimestamp 对齐 Python datetime.now().timestamp() 的浮点秒
func nowTimestamp() float64 {
	return float64(time.Now().UnixNano()) / float64(time.Second)
}

// execAll 顺序执行 DDL 语句，任一条失败即返回带语句片段的错误
func execAll(conn *Conn, statements ...string) error {
	for _, stmt := range statements {
		if _, err := conn.Exec(stmt); err != nil {
			return fmt.Errorf("执行 DDL 失败（%s）: %w", firstLine(stmt), err)
		}
	}
	return nil
}

// firstLine 取语句首行，用于错误与日志定位
func firstLine(statement string) string {
	for _, line := range strings.Split(statement, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// tableColumnInfo 对应 PRAGMA table_info 的一行
type tableColumnInfo struct {
	Name    string
	Type    string
	NotNull int
	Default sql.NullString
	PK      int
}

// readTableInfo 读取表结构，对齐 Python PRAGMA table_info 用法
func readTableInfo(conn *Conn, table string) ([]tableColumnInfo, error) {
	rows, err := conn.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []tableColumnInfo
	for rows.Next() {
		var (
			cid     int
			name    string
			colType string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		out = append(out, tableColumnInfo{Name: name, Type: colType, NotNull: notNull, Default: dflt, PK: pk})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// tableColumnSet 返回列名集合，对齐 Python {row[1] for row in PRAGMA table_info}
func tableColumnSet(conn *Conn, table string) (map[string]struct{}, error) {
	infos, err := readTableInfo(conn, table)
	if err != nil {
		return nil, err
	}
	columns := make(map[string]struct{}, len(infos))
	for _, info := range infos {
		columns[info.Name] = struct{}{}
	}
	return columns, nil
}
