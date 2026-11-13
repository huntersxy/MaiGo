package storage

// A_memorix 跨存储清理 Outbox 与可恢复快照，逐条对齐 Python core/storage/metadata_cleanup.py
// 本文件承载任务协议校验、资源权威判定、任务摘要与 operation 结算

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// 清理任务动作，对齐 Python expected_status_by_action 的键
const (
	cleanupActionVectorDelete = "vector_delete"
	cleanupActionVectorUpsert = "vector_upsert"
	cleanupActionGraphRebuild = "graph_rebuild"
	cleanupActionGraphRestore = "graph_restore"
)

// operation 与任务状态字面量，对齐 Python 中直接书写的取值
const (
	cleanupStatusPending        = "pending"
	cleanupStatusRunning        = "running"
	cleanupStatusFailed         = "failed"
	cleanupStatusCompleted      = "completed"
	cleanupStatusCancelled      = "cancelled"
	cleanupStatusPendingCleanup = "pending_cleanup"
	cleanupStatusRestorePending = "restore_pending"
	cleanupStatusRestored       = "restored"
	cleanupStatusSuperseded     = "superseded"
)

// 接口默认值与分块参数，对齐 Python 关键字参数默认值
const (
	cleanupDefaultClaimLimit     = 100
	cleanupDefaultClaimLease     = 60.0
	cleanupDefaultAuthorizeLease = 300.0
	cleanupDefaultListLimit      = 500
	cleanupDefaultExpiredLimit   = 1000
	cleanupActiveChunkSize       = 900
	cleanupLastErrorMaxRunes     = 2000
)

// CleanupJobSpec 对齐 Python enqueue_storage_cleanup_jobs 的 jobs 列表项字典
type CleanupJobSpec struct {
	// ResourceType 资源类型：paragraph/entity/relation/graph
	ResourceType string
	// ResourceID 资源标识：向量批任务为 batch，图任务为 structure/restore
	ResourceID string
	// Action 清理动作：vector_delete/vector_upsert/graph_rebuild/graph_restore
	Action string
	// Payload 动作载荷，向量删除为 {<resource_type>_hashes: [...]}，向量写入为 {item: {...}}
	Payload any
	// ExpectedState 任务声明的 operation 状态，必须恰为 {operation_status: ...}
	ExpectedState map[string]any
}

// StorageCleanupJob 对齐 Python claim/list 返回的任务字典，字段与表列一一对应
type StorageCleanupJob struct {
	JobID         int64
	OperationID   string
	ResourceType  string
	ResourceID    string
	Action        string
	Payload       any
	ExpectedState any
	Status        string
	AttemptCount  int
	NextAttemptAt float64
	LeaseToken    *string
	LeaseUntil    *float64
	LastError     *string
	CreatedAt     float64
	UpdatedAt     float64
	CompletedAt   *float64
}

// CleanupJobAuthorization 对齐 Python authorize_storage_cleanup_job 返回的字典
type CleanupJobAuthorization struct {
	// OperationMatches operation 状态是否仍与任务声明一致
	OperationMatches bool
	Action           string
	ResourceType     string
	// ResourceIDs 允许外部提交的资源 hash
	ResourceIDs []string
}

// CleanupSupersedeResult 对齐 Python supersede_delete_cleanup_resources 返回的字典
type CleanupSupersedeResult struct {
	UpdatedJobs          int
	CancelledJobs        int
	SupersededOperations int
}

// cleanupQueryer 抽象 *Conn 与 *Tx 的公共语句能力，对齐 Python 方法接收 connection 参数
type cleanupQueryer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// cleanupPlaceholders 生成 n 个逗号分隔的占位符
func cleanupPlaceholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

// cleanupSortedKeys 返回有序键集合，保证拼接 SQL 时列顺序稳定
func cleanupSortedKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// cleanupStringValue 对齐 Python str(value or "")
func cleanupStringValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	case bool:
		if v {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprint(value)
	}
}

// cleanupFalsy 对齐 Python 的真值判断
func cleanupFalsy(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case bool:
		return !v
	case int:
		return v == 0
	case int64:
		return v == 0
	case float64:
		return v == 0
	case []any:
		return len(v) == 0
	case []string:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	default:
		return false
	}
}

// cleanupTrimmed 对齐 Python str(value or "").strip()
func cleanupTrimmed(value any) string {
	if cleanupFalsy(value) {
		return ""
	}
	return strings.TrimSpace(cleanupStringValue(value))
}

// cleanupInt64 把行内整数列归一为 int64
func cleanupInt64(value any) int64 {
	switch v := value.(type) {
	case nil:
		return 0
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case []byte:
		parsed, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

// cleanupFloatValue 把行内 REAL 列归一为 float64
func cleanupFloatValue(value any) float64 {
	switch v := value.(type) {
	case nil:
		return 0
	case float64:
		return v
	case float32:
		return float64(v)
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case []byte:
		parsed, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return 0
		}
		return parsed
	case string:
		parsed, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

// cleanupStringPtr 把可空文本列转为指针
func cleanupStringPtr(value any) *string {
	if value == nil {
		return nil
	}
	text := cleanupStringValue(value)
	return &text
}

// cleanupFloatPtr 把可空 REAL 列转为指针
func cleanupFloatPtr(value any) *float64 {
	if value == nil {
		return nil
	}
	number := cleanupFloatValue(value)
	return &number
}

// cleanupQueryMaps 执行查询并把每行转为列名映射，对齐 Python sqlite3.Row 的字典用法
func cleanupQueryMaps(c cleanupQueryer, query string, args ...any) ([]map[string]any, error) {
	rows, err := c.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, name := range columns {
			row[name] = cleanupNormalizeValue(values[i])
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// cleanupNormalizeValue 把驱动返回的字节切片统一为字符串
func cleanupNormalizeValue(value any) any {
	if raw, ok := value.([]byte); ok {
		return string(raw)
	}
	return value
}

// cleanupJSONObject 解析 JSON 文本为字典，解析失败或非字典返回空字典
func cleanupJSONObject(value any) map[string]any {
	loaded := jsonLoads(value, map[string]any{})
	if dict, ok := loaded.(map[string]any); ok {
		return dict
	}
	return map[string]any{}
}

// validateCleanupJobPayload 校验正式 Outbox 协议，并返回任务声明的资源 hash
// 对齐 Python _validate_cleanup_job_payload
func validateCleanupJobPayload(resourceType string, resourceID string, action string, payload any) ([]string, error) {
	payloadMap, ok := payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("清理任务 payload 必须是对象: action=%s", action)
	}
	if action == cleanupActionGraphRebuild || action == cleanupActionGraphRestore {
		expectedResourceID := "restore"
		if action == cleanupActionGraphRebuild {
			expectedResourceID = "structure"
		}
		if resourceType != "graph" || resourceID != expectedResourceID || len(payloadMap) != 0 {
			return nil, fmt.Errorf("图清理任务协议非法: action=%s", action)
		}
		return []string{}, nil
	}
	if resourceType != "paragraph" && resourceType != "entity" && resourceType != "relation" {
		return nil, fmt.Errorf("未知向量资源类型: %s", resourceType)
	}
	if action == cleanupActionVectorDelete {
		key := resourceType + "_hashes"
		rawIDs, present := payloadMap[key]
		items, isList := cleanupListValue(rawIDs)
		if len(payloadMap) != 1 || !present || !isList {
			return nil, fmt.Errorf("vector_delete payload 必须且只能包含 %s", key)
		}
		resourceIDs := make([]string, 0, len(items))
		for _, item := range items {
			resourceIDs = append(resourceIDs, cleanupTrimmed(item))
		}
		if len(resourceIDs) == 0 {
			return nil, errors.New("vector_delete 资源 hash 不能为空")
		}
		for _, item := range resourceIDs {
			if item == "" {
				return nil, errors.New("vector_delete 资源 hash 不能为空")
			}
		}
		if len(dedupeStrings(resourceIDs)) != len(resourceIDs) {
			return nil, errors.New("vector_delete 资源 hash 不允许重复")
		}
		return resourceIDs, nil
	}
	if action == cleanupActionVectorUpsert {
		itemValue, present := payloadMap["item"]
		item, itemOK := itemValue.(map[string]any)
		if len(payloadMap) != 1 || !present || !itemOK {
			return nil, errors.New("vector_upsert payload 必须且只能包含 item 对象")
		}
		itemHash := cleanupTrimmed(item["hash"])
		if itemHash == "" || itemHash != resourceID {
			return nil, errors.New("vector_upsert item.hash 必须等于 resource_id")
		}
		return []string{itemHash}, nil
	}
	return nil, fmt.Errorf("未知清理任务 action: %s", action)
}

// cleanupListValue 判断载荷列表，兼容 []any 与 []string
func cleanupListValue(value any) ([]any, bool) {
	switch items := value.(type) {
	case []any:
		return items, true
	case []string:
		out := make([]any, len(items))
		for i, item := range items {
			out[i] = item
		}
		return out, true
	default:
		return nil, false
	}
}

// activeCleanupResourceIDs 查询资源当前有效的 hash 集合
// 对齐 Python _active_cleanup_resource_ids：relation 不按 is_deleted 过滤
func activeCleanupResourceIDs(c cleanupQueryer, resourceType string, resourceIDs []string) (map[string]struct{}, error) {
	active := make(map[string]struct{}, len(resourceIDs))
	if len(resourceIDs) == 0 {
		return active, nil
	}
	table := ""
	switch resourceType {
	case "paragraph":
		table = "paragraphs"
	case "entity":
		table = "entities"
	case "relation":
		table = "relations"
	default:
		return nil, fmt.Errorf("未知向量资源类型: %s", resourceType)
	}
	activeClause := "AND (is_deleted IS NULL OR is_deleted = 0)"
	if resourceType == "relation" {
		activeClause = ""
	}
	for offset := 0; offset < len(resourceIDs); offset += cleanupActiveChunkSize {
		end := offset + cleanupActiveChunkSize
		if end > len(resourceIDs) {
			end = len(resourceIDs)
		}
		chunk := resourceIDs[offset:end]
		query := fmt.Sprintf(
			"SELECT hash FROM %s WHERE hash IN (%s) %s",
			table,
			cleanupPlaceholders(len(chunk)),
			activeClause,
		)
		args := make([]any, len(chunk))
		for i, item := range chunk {
			args[i] = item
		}
		rows, err := cleanupQueryMaps(c, query, args...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			active[cleanupTrimmed(row["hash"])] = struct{}{}
		}
	}
	return active, nil
}

// cleanupJobSummaryInTx 汇总某 operation 的清理任务状态
// 对齐 Python _cleanup_job_summary_in_transaction
func cleanupJobSummaryInTx(c cleanupQueryer, operationID string) (map[string]int, error) {
	rows, err := cleanupQueryMaps(c, `
		SELECT status, COUNT(*) AS count
		FROM storage_cleanup_jobs
		WHERE operation_id = ?
		GROUP BY status
	`, operationID)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(rows)+2)
	total := 0
	for _, row := range rows {
		count := int(cleanupInt64(row["count"]))
		counts[cleanupStringValue(row["status"])] = count
		total += count
	}
	counts["total"] = total
	unfinished := 0
	for status, count := range counts {
		switch status {
		case cleanupStatusPending, cleanupStatusFailed, cleanupStatusRunning:
			unfinished += count
		}
	}
	counts["unfinished"] = unfinished
	return counts, nil
}

// reconcileSettledDeleteOperation 修复任务已终态但 operation 尚未迁移的崩溃窗口
// 对齐 Python _reconcile_settled_delete_operation，返回迁移后的状态
func reconcileSettledDeleteOperation(c cleanupQueryer, operationID string) (*string, error) {
	summary, err := cleanupJobSummaryInTx(c, operationID)
	if err != nil {
		return nil, err
	}
	if summary["unfinished"] != 0 {
		return nil, nil
	}
	rows, err := cleanupQueryMaps(c,
		"SELECT status, summary_json FROM delete_operations WHERE operation_id = ?", operationID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	currentStatus := cleanupStringValue(rows[0]["status"])
	var (
		targetStatus string
		restoredAt   any
	)
	switch currentStatus {
	case cleanupStatusPendingCleanup:
		targetStatus = cleanupStatusCompleted
	case cleanupStatusRestorePending:
		targetStatus = cleanupStatusRestored
		restoredAt = nowTimestamp()
	default:
		return nil, nil
	}
	operationSummary := cleanupJSONObject(rows[0]["summary_json"])
	operationSummary["state"] = targetStatus
	operationSummary["cleanup"] = summary
	if _, err := c.Exec(`
		UPDATE delete_operations
		SET status = ?, restored_at = COALESCE(?, restored_at), summary_json = ?
		WHERE operation_id = ? AND status = ?
	`, targetStatus, restoredAt, jsonDumps(operationSummary), operationID, currentStatus); err != nil {
		return nil, err
	}
	return &targetStatus, nil
}

// SummarizeStorageCleanupJobs 汇总某 operation 的清理任务状态
// 对齐 Python summarize_storage_cleanup_jobs
func (s *Store) SummarizeStorageCleanupJobs(operationID string) (map[string]int, error) {
	conn, err := s.Conn()
	if err != nil {
		return nil, err
	}
	return cleanupJobSummaryInTx(conn, strings.TrimSpace(operationID))
}
