package storage

// A_memorix 图片记忆通用辅助，对齐 Python core/storage/metadata_image.py 的模块级助手
// 行字典统一复用 store_rows.go 的 scanRowsToMaps（等价 Python dict(row)）

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// imageQuerier 抽象主连接与事务连接的查询能力
type imageQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// imageQueryDicts 执行查询并返回全部行字典，对齐 Python [dict(row) for row in fetchall()]
func imageQueryDicts(q imageQuerier, query string, args ...any) ([]map[string]any, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	return scanRowsToMaps(rows)
}

// imageFirstDict 执行查询并返回首行字典，无结果返回 nil，对齐 Python fetchone()
func imageFirstDict(q imageQuerier, query string, args ...any) (map[string]any, error) {
	items, err := imageQueryDicts(q, query, args...)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return items[0], nil
}

// imageExists 判断查询是否有结果，对齐 Python fetchone() is not None
func imageExists(q imageQuerier, query string, args ...any) (bool, error) {
	var marker int
	err := q.QueryRow(query, args...).Scan(&marker)
	if err != nil {
		if isNoRows(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// imageUUIDHex 生成 32 位小写十六进制标识，对齐 Python uuid.uuid4().hex
func imageUUIDHex() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

// imageTokenList 对齐 Python [str(item) for item in values if str(item)]：仅丢弃空串
func imageTokenList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

// imageTrimmedTokens 对齐 Python 去空白后丢弃空串的列表
func imageTrimmedTokens(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		token := strings.TrimSpace(value)
		if token == "" {
			continue
		}
		out = append(out, token)
	}
	return out
}

// imageAtLeastOne 对齐 Python max(1, int(value))
func imageAtLeastOne(value int) int {
	if value < 1 {
		return 1
	}
	return value
}

// imageAtLeastZero 对齐 Python max(0, int(value))
func imageAtLeastZero(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

// imageLeaseDeadline 对齐 Python now + max(1.0, float(lease_seconds))
func imageLeaseDeadline(now float64, leaseSeconds float64) float64 {
	if leaseSeconds < 1 {
		leaseSeconds = 1
	}
	return now + leaseSeconds
}

// imageIntValue 把行字典中的数值列转成 int64，缺失或非法返回 0
func imageIntValue(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

// imageAffected 读取影响行数，对齐 Python cursor.rowcount
func imageAffected(result sql.Result) (int, error) {
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

// imageJSONObject 对齐 Python _json(evidence or {})
// 语义为 sort_keys=True、ensure_ascii=False、separators=(",", ":")，
// 因此用 encoding/json（键稳定排序）并关闭 HTML 转义，避免 sonic 默认不排序键
func imageJSONObject(value map[string]any) string {
	if len(value) == 0 {
		return "{}"
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "{}"
	}
	return strings.TrimSuffix(buffer.String(), "\n")
}

// imageEvidenceDict 对齐 Python json.loads(evidence_json or "{}") 后仅保留 dict
func imageEvidenceDict(value any) map[string]any {
	text := ""
	switch v := value.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	}
	if strings.TrimSpace(text) == "" {
		return map[string]any{}
	}
	loaded := jsonLoads(text, map[string]any{})
	if dict, ok := loaded.(map[string]any); ok {
		return dict
	}
	return map[string]any{}
}
