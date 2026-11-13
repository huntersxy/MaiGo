package storage

// 元数据库连接与 SQLite metadata 列的 pickle 转 JSON 迁移
// 对齐 Python format_migration.py 的 _connect_metadata_db/_metadata_to_json_text/_migrate_sqlite_metadata

import (
	"database/sql"
	"fmt"
	"unicode/utf8"
)

// MigrationConn 是格式迁移需要的最小数据库能力集合
// *MigrationDB、*Conn、*sql.DB、*sql.Tx 都满足该接口，便于在测试中替换
type MigrationConn interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// MigrationDB 包装元数据库连接，复刻 Python sqlite3 的隐式事务
// 只有真正写入时才开启事务，保证“无待迁移数据时不改写数据库文件”
type MigrationDB struct {
	db *sql.DB
	tx *sql.Tx
}

// ConnectMetadataDB 对齐 _connect_metadata_db
// 不设置 journal_mode 等 PRAGMA，避免仅打开连接就改动库文件
func ConnectMetadataDB(dbPath string) (*MigrationDB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	return &MigrationDB{db: db}, nil
}

// Exec 在首次写入时惰性开启事务，之后复用它
func (m *MigrationDB) Exec(query string, args ...any) (sql.Result, error) {
	if err := m.ensureTx(); err != nil {
		return nil, err
	}
	return m.tx.Exec(query, args...)
}

// Query 优先使用已开启的事务，保证读到未提交的写入
func (m *MigrationDB) Query(query string, args ...any) (*sql.Rows, error) {
	if m.tx != nil {
		return m.tx.Query(query, args...)
	}
	return m.db.Query(query, args...)
}

// QueryRow 优先使用已开启的事务
func (m *MigrationDB) QueryRow(query string, args ...any) *sql.Row {
	if m.tx != nil {
		return m.tx.QueryRow(query, args...)
	}
	return m.db.QueryRow(query, args...)
}

// ensureTx 惰性开启事务
func (m *MigrationDB) ensureTx() error {
	if m.tx != nil {
		return nil
	}
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	m.tx = tx
	return nil
}

// Commit 对齐 conn.commit：没有事务时为空操作
func (m *MigrationDB) Commit() error {
	if m.tx == nil {
		return nil
	}
	if err := m.tx.Commit(); err != nil {
		return err
	}
	m.tx = nil
	return nil
}

// Rollback 对齐 conn.rollback：没有事务时为空操作
func (m *MigrationDB) Rollback() error {
	if m.tx == nil {
		return nil
	}
	tx := m.tx
	m.tx = nil
	return tx.Rollback()
}

// Close 先回滚未提交事务再关闭连接，对齐 Python finally 中的 close
func (m *MigrationDB) Close() error {
	if err := m.Rollback(); err != nil {
		_ = m.db.Close()
		return err
	}
	return m.db.Close()
}

// metadataToJSONText 对齐 _metadata_to_json_text
// 返回转换后的文本与是否发生变化；raw 为 None 时返回 (nil, false)
func metadataToJSONText(raw any) (*string, bool, error) {
	switch typed := raw.(type) {
	case nil:
		return nil, false, nil
	case map[string]any:
		text, err := pythonJSONDumps(typed)
		if err != nil {
			return nil, false, err
		}
		return &text, true, nil
	case []byte:
		if len(typed) == 0 {
			return emptyText(), false, nil
		}
		// 先按 UTF-8 JSON 对象尝试，任何失败都退回 pickle，与 Python 的 except Exception 一致
		if utf8.Valid(typed) {
			value, err := decodeJSONValue(string(typed))
			if err == nil {
				if object, ok := value.(map[string]any); ok {
					text, dumpErr := pythonJSONDumps(object)
					if dumpErr != nil {
						return nil, false, dumpErr
					}
					return &text, true, nil
				}
			}
		}
		value, pickleErr := unpickleLegacy(typed)
		if pickleErr != nil {
			return nil, false, pickleErr
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("pickle metadata 必须解码为 dict")
		}
		text, err := pythonJSONDumps(object)
		if err != nil {
			return nil, false, err
		}
		return &text, true, nil
	case string:
		if typed == "" {
			return emptyText(), false, nil
		}
		value, err := decodeJSONValue(typed)
		if err != nil {
			return nil, false, fmt.Errorf("metadata 文本既不是合法 JSON，也不是可迁移的 pickle bytes")
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("metadata JSON 必须是对象")
		}
		normalized, err := pythonJSONDumps(object)
		if err != nil {
			return nil, false, err
		}
		return &normalized, normalized != typed, nil
	}
	return nil, false, fmt.Errorf("不支持的 metadata 类型: %s", pythonTypeName(raw))
}

// emptyText 返回空字符串指针
func emptyText() *string {
	text := ""
	return &text
}

// sqliteMetadataRow 单行业务数据，rowid 用于回写
type sqliteMetadataRow struct {
	rowID    int64
	metadata any
}

// MigrateSQLiteMetadata 对齐 _migrate_sqlite_metadata
func MigrateSQLiteMetadata(conn MigrationConn) (SQLiteMigrationSummary, error) {
	result := SQLiteMigrationSummary{Tables: make(map[string]SQLiteTableMigration, len(metadataTables))}
	for _, table := range metadataTables {
		exists, err := sqliteColumnExists(conn, table, "metadata")
		if err != nil {
			return result, err
		}
		if !exists {
			continue
		}

		rows, err := fetchMetadataRows(conn, table)
		if err != nil {
			return result, err
		}

		tableUpdated := 0
		for _, row := range rows {
			converted, changed, convErr := metadataToJSONText(row.metadata)
			if convErr != nil {
				return result, convErr
			}
			if !changed {
				continue
			}
			var value any
			if converted != nil {
				value = *converted
			}
			if _, execErr := conn.Exec(
				fmt.Sprintf("UPDATE %s SET metadata = ? WHERE rowid = ?", table), value, row.rowID,
			); execErr != nil {
				return result, execErr
			}
			tableUpdated++
		}
		result.Tables[table] = SQLiteTableMigration{Scanned: len(rows), Updated: tableUpdated}
		result.Updated += tableUpdated
	}
	return result, nil
}

// fetchMetadataRows 一次性读取全部非空 metadata，对应 Python 的 fetchall
func fetchMetadataRows(conn MigrationConn, table string) ([]sqliteMetadataRow, error) {
	rows, err := conn.Query(fmt.Sprintf("SELECT rowid, metadata FROM %s WHERE metadata IS NOT NULL", table))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	collected := make([]sqliteMetadataRow, 0, 16)
	for rows.Next() {
		var rowID int64
		var metadata any
		if err := rows.Scan(&rowID, &metadata); err != nil {
			return nil, err
		}
		collected = append(collected, sqliteMetadataRow{rowID: rowID, metadata: metadata})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return collected, nil
}
