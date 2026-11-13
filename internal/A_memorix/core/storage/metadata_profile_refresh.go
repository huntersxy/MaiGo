package storage

// A_memorix 人物画像刷新队列，逐条对齐 Python core/storage/metadata_profile.py 的队列部分

import (
	"database/sql"
	"fmt"
	"strings"
)

// personProfileRefreshColumns 与 Python 查询列顺序逐字一致
const personProfileRefreshColumns = `
			SELECT person_id, status, reason, source_query_tool_id, retry_count, last_error, requested_at, updated_at
			FROM person_profile_refresh_queue`

// scanPersonProfileRefreshRow 对齐 Python _person_profile_refresh_row_to_dict 的字段归一
func scanPersonProfileRefreshRow(scanner personProfileScanner) (map[string]any, error) {
	var (
		personID          sql.NullString
		status            sql.NullString
		reason            sql.NullString
		sourceQueryToolID sql.NullString
		retryCount        sql.NullInt64
		lastError         sql.NullString
		requestedAt       sql.NullFloat64
		updatedAt         sql.NullFloat64
	)
	if err := scanner.Scan(
		&personID, &status, &reason, &sourceQueryToolID,
		&retryCount, &lastError, &requestedAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	normalizedStatus := strings.ToLower(strings.TrimSpace(status.String))
	if normalizedStatus == "" {
		normalizedStatus = "pending"
	}
	return map[string]any{
		"person_id":            strings.TrimSpace(personID.String),
		"status":               normalizedStatus,
		"reason":               strings.TrimSpace(reason.String),
		"source_query_tool_id": strings.TrimSpace(sourceQueryToolID.String),
		"retry_count":          int(retryCount.Int64),
		"last_error":           strings.TrimSpace(lastError.String),
		"requested_at":         personProfileFloatValue(requestedAt),
		"updated_at":           personProfileFloatValue(updatedAt),
	}, nil
}

// GetPersonProfileRefreshRequest 读取单个人物的刷新请求，
// 对齐 Python get_person_profile_refresh_request
func (s *Store) GetPersonProfileRefreshRequest(personID string) (map[string]any, error) {
	token := strings.TrimSpace(personID)
	if token == "" {
		return nil, nil
	}
	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	item, err := scanPersonProfileRefreshRow(c.QueryRow(
		personProfileRefreshColumns+" WHERE person_id = ? LIMIT 1",
		token,
	))
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return item, nil
}

// PersonProfileRefreshEnqueueInput 对齐 Python enqueue_person_profile_refresh 的关键字参数
type PersonProfileRefreshEnqueueInput struct {
	// PersonID 人物 ID，会去除首尾空白
	PersonID string
	// Reason 入队原因，空白等价于 NULL
	Reason string
	// SourceQueryToolID 触发查询工具 ID，空白等价于 NULL
	SourceQueryToolID string
	// Conn 指定连接，nil 表示使用存储主连接；传入时不自行提交
	Conn *Conn
}

// EnqueuePersonProfileRefresh 入队或重置人物画像刷新请求，
// 对齐 Python enqueue_person_profile_refresh；person_id 为空时返回 nil
func (s *Store) EnqueuePersonProfileRefresh(input PersonProfileRefreshEnqueueInput) (map[string]any, error) {
	token := strings.TrimSpace(input.PersonID)
	if token == "" {
		return nil, nil
	}
	now := nowTimestamp()
	c, err := s.ResolveConn(input.Conn)
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(input.Reason)
	var reasonValue any
	if reason != "" {
		reasonValue = reason
	}
	sourceToolID := strings.TrimSpace(input.SourceQueryToolID)
	var sourceToolValue any
	if sourceToolID != "" {
		sourceToolValue = sourceToolID
	}
	if _, err := c.Exec(`
		INSERT INTO person_profile_refresh_queue (
			person_id, status, reason, source_query_tool_id, retry_count, last_error, requested_at, updated_at
		) VALUES (?, 'pending', ?, ?, 0, NULL, ?, ?)
		ON CONFLICT(person_id) DO UPDATE SET
			status = 'pending',
			reason = excluded.reason,
			source_query_tool_id = excluded.source_query_tool_id,
			retry_count = 0,
			last_error = NULL,
			requested_at = excluded.requested_at,
			updated_at = excluded.updated_at
	`, token, reasonValue, sourceToolValue, now, now); err != nil {
		return nil, err
	}
	if input.Conn == nil {
		if err := c.Commit(); err != nil {
			return nil, err
		}
	}
	return s.GetPersonProfileRefreshRequest(token)
}

// personProfileNonNegative 对齐 Python max(0.0, float(x or 0.0))
func personProfileNonNegative(value float64) float64 {
	if value > 0 {
		return value
	}
	return 0
}

// FetchPersonProfileRefreshBatch 取出待刷新与可重试失败的请求，
// 对齐 Python fetch_person_profile_refresh_batch
func (s *Store) FetchPersonProfileRefreshBatch(
	limit int,
	maxRetry int,
	debounceSeconds float64,
	retryBackoffSeconds float64,
) ([]map[string]any, error) {
	if limit < 1 {
		limit = 1
	}
	if maxRetry < 0 {
		maxRetry = 0
	}
	now := nowTimestamp()
	pendingReadyBefore := now - personProfileNonNegative(debounceSeconds)
	failedReadyBefore := now - personProfileNonNegative(retryBackoffSeconds)

	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(`
		SELECT person_id, status, reason, source_query_tool_id, retry_count, last_error, requested_at, updated_at
		FROM person_profile_refresh_queue
		WHERE (status = 'pending' AND requested_at <= ?)
		   OR (status = 'failed' AND retry_count < ? AND updated_at <= ?)
		ORDER BY requested_at ASC, updated_at ASC
		LIMIT ?
	`, pendingReadyBefore, maxRetry, failedReadyBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		item, err := scanPersonProfileRefreshRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// MarkPersonProfileRefreshRunning 抢占刷新请求，对齐 Python mark_person_profile_refresh_running
func (s *Store) MarkPersonProfileRefreshRunning(personID string, requestedAt *float64) (bool, error) {
	token := strings.TrimSpace(personID)
	if token == "" {
		return false, nil
	}
	now := nowTimestamp()
	query := `
		UPDATE person_profile_refresh_queue
		SET status = 'running',
			updated_at = ?
		WHERE person_id = ?
		  AND status IN ('pending', 'failed')`
	args := []any{now, token}
	if requestedAt != nil {
		query += " AND requested_at = ?"
		args = append(args, *requestedAt)
	}
	c, err := s.Conn()
	if err != nil {
		return false, err
	}
	result, err := c.Exec(query, args...)
	if err != nil {
		return false, err
	}
	if err := c.Commit(); err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// MarkPersonProfileRefreshDone 标记刷新完成；请求号已过期时回到 pending，
// 对齐 Python mark_person_profile_refresh_done
func (s *Store) MarkPersonProfileRefreshDone(personID string, requestedAt *float64) (bool, error) {
	token := strings.TrimSpace(personID)
	if token == "" {
		return false, nil
	}
	now := nowTimestamp()
	c, err := s.Conn()
	if err != nil {
		return false, err
	}
	var result sql.Result
	if requestedAt == nil {
		result, err = c.Exec(`
			UPDATE person_profile_refresh_queue
			SET status = 'done',
				last_error = NULL,
				updated_at = ?
			WHERE person_id = ?
		`, now, token)
	} else {
		result, err = c.Exec(`
			UPDATE person_profile_refresh_queue
			SET status = CASE
					WHEN requested_at > ? THEN 'pending'
					ELSE 'done'
				END,
				last_error = NULL,
				updated_at = ?
			WHERE person_id = ?
		`, *requestedAt, now, token)
	}
	if err != nil {
		return false, err
	}
	if err := c.Commit(); err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// MarkPersonProfileRefreshFailed 标记刷新失败并累加重试次数；请求号已过期时回到 pending，
// 对齐 Python mark_person_profile_refresh_failed
func (s *Store) MarkPersonProfileRefreshFailed(personID, errText string, requestedAt *float64) (bool, error) {
	token := strings.TrimSpace(personID)
	if token == "" {
		return false, nil
	}
	errorText := truncateRunes(strings.TrimSpace(errText), 500)
	now := nowTimestamp()
	c, err := s.Conn()
	if err != nil {
		return false, err
	}
	var result sql.Result
	if requestedAt == nil {
		result, err = c.Exec(`
			UPDATE person_profile_refresh_queue
			SET status = 'failed',
				retry_count = COALESCE(retry_count, 0) + 1,
				last_error = ?,
				updated_at = ?
			WHERE person_id = ?
		`, errorText, now, token)
	} else {
		reqTs := *requestedAt
		result, err = c.Exec(`
			UPDATE person_profile_refresh_queue
			SET status = CASE
					WHEN requested_at > ? THEN 'pending'
					ELSE 'failed'
				END,
				retry_count = CASE
					WHEN requested_at > ? THEN COALESCE(retry_count, 0)
					ELSE COALESCE(retry_count, 0) + 1
				END,
				last_error = CASE
					WHEN requested_at > ? THEN NULL
					ELSE ?
				END,
				updated_at = ?
			WHERE person_id = ?
		`, reqTs, reqTs, reqTs, errorText, now, token)
	}
	if err != nil {
		return false, err
	}
	if err := c.Commit(); err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// personProfileRefreshStatuses 合法状态集合，对齐 Python 的集合判断
var personProfileRefreshStatuses = map[string]struct{}{
	"pending": {},
	"running": {},
	"done":    {},
	"failed":  {},
}

// ListPersonProfileRefreshRequests 列出刷新请求，可按状态过滤，
// 对齐 Python list_person_profile_refresh_requests
func (s *Store) ListPersonProfileRefreshRequests(statuses []string, limit int) ([]map[string]any, error) {
	if limit < 1 {
		limit = 1
	}
	normalized := make([]string, 0, len(statuses))
	for _, item := range statuses {
		token := strings.ToLower(strings.TrimSpace(item))
		if _, ok := personProfileRefreshStatuses[token]; ok {
			normalized = append(normalized, token)
		}
	}
	condition := ""
	args := make([]any, 0, len(normalized)+1)
	if len(normalized) > 0 {
		condition = fmt.Sprintf(" WHERE status IN (%s)",
			strings.TrimSuffix(strings.Repeat("?,", len(normalized)), ","))
		for _, item := range normalized {
			args = append(args, item)
		}
	}
	args = append(args, limit)
	query := fmt.Sprintf(`
		SELECT person_id, status, reason, source_query_tool_id, retry_count, last_error, requested_at, updated_at
		FROM person_profile_refresh_queue%s
		ORDER BY updated_at DESC, person_id ASC
		LIMIT ?
	`, condition)

	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		item, err := scanPersonProfileRefreshRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// GetPersonProfileRefreshSummary 汇总刷新队列状态，
// 对齐 Python get_person_profile_refresh_summary（running 固定取 20 条）
func (s *Store) GetPersonProfileRefreshSummary(failedLimit int) (map[string]any, error) {
	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(`
		SELECT status, COUNT(*) AS cnt
		FROM person_profile_refresh_queue
		GROUP BY status
	`)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{"pending": 0, "running": 0, "done": 0, "failed": 0, "total": 0}
	for rows.Next() {
		var (
			status sql.NullString
			count  int64
		)
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return nil, err
		}
		key := strings.ToLower(strings.TrimSpace(status.String))
		counts[key] += int(count)
		counts["total"] += int(count)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	running, err := s.ListPersonProfileRefreshRequests([]string{"running"}, 20)
	if err != nil {
		return nil, err
	}
	if failedLimit < 1 {
		failedLimit = 1
	}
	failed, err := s.ListPersonProfileRefreshRequests([]string{"failed"}, failedLimit)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"counts":  counts,
		"running": running,
		"failed":  failed,
	}, nil
}
