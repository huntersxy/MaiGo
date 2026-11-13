package storage

// A_memorix 行扫描与通用规范化，对齐 Python metadata_store.py 的通用助手

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sqlite "github.com/glebarez/go-sqlite"

	"go.uber.org/zap"
)

// scanRowToMap 把结果行转为 map，保持列名与 Python sqlite3.Row 一致
func scanRowToMap(rows *sql.Rows) (map[string]any, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	if err := rows.Scan(pointers...); err != nil {
		return nil, err
	}
	out := make(map[string]any, len(columns))
	for i, column := range columns {
		out[column] = normalizeSQLiteValue(values[i])
	}
	return out, nil
}

// scanRowsToMaps 读取全部结果行
func scanRowsToMaps(rows *sql.Rows) ([]map[string]any, error) {
	defer rows.Close()
	out := make([]map[string]any, 0, 16)
	for rows.Next() {
		row, err := scanRowToMap(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// normalizeSQLiteValue 把驱动返回值归一为 Python 风格的常见类型
func normalizeSQLiteValue(value any) any {
	switch v := value.(type) {
	case []byte:
		return string(v)
	default:
		return value
	}
}

// rowToDict 对齐 Python _row_to_dict：解码 metadata JSON 字段
func (s *Store) rowToDict(row map[string]any) map[string]any {
	if row == nil {
		return nil
	}
	if raw, ok := row["metadata"]; ok && raw != nil {
		if text, isText := raw.(string); isText && text != "" {
			decoded := decodeMetadata(text)
			row["metadata"] = decoded
		} else if !isText {
			row["metadata"] = map[string]any{}
		}
	}
	return row
}

// normalizeHashSequence 规范化 hash 列表并保持首次出现顺序
func normalizeHashSequence(hashValues []string) []string {
	normalized := make([]string, 0, len(hashValues))
	seen := make(map[string]struct{}, len(hashValues))
	for _, item := range hashValues {
		token := strings.TrimSpace(item)
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		normalized = append(normalized, token)
	}
	return normalized
}

// iterSQLBatches 按 SQLite 参数数量限制切分批量查询
func iterSQLBatches(items []string, batchSize int) [][]string {
	if batchSize < 1 {
		batchSize = 1
	}
	batches := make([][]string, 0, (len(items)+batchSize-1)/batchSize)
	for start := 0; start < len(items); start += batchSize {
		end := start + batchSize
		if end > len(items) {
			end = len(items)
		}
		batches = append(batches, items[start:end])
	}
	return batches
}

// sqlPlaceholders 生成 "?, ?, ?" 占位符
func sqlPlaceholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

// stringArgs 把字符串切片转为 []any
func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}

// canonicalizeName 规范化名称（去首尾空白并转小写）
func canonicalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// isIntegrityError 判断是否为 SQLite 完整性约束错误，对齐 Python sqlite3.IntegrityError
func isIntegrityError(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code()&0xff == 19
	}
	return false
}

// optionalString 把空字符串转为 nil，用于写入可空 TEXT 列
func optionalString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// stringFromRow 读取 map 中的字符串列
func stringFromRow(row map[string]any, key string) string {
	if row == nil {
		return ""
	}
	raw, ok := row[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// warnQueryError 统一记录查询失败日志
func warnQueryError(message string, err error) {
	zap.L().Warn(message, zap.Error(err))
}
