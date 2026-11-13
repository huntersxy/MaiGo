package storage

// A_memorix 反馈任务与回滚日志，逐条对齐 Python core/storage/metadata_feedback.py
// 任务状态机：pending → running → applied/skipped/error
// 回滚状态机：none → running → rolled_back/error
// 返回字典的字段名与 Python dict 完全一致（含 query_snapshot/decision_payload 等派生字段）

import (
	"fmt"
	"strings"
)

// 与 Python 白名单一致的状态集合
var (
	feedbackTaskStatusValues     = []string{"pending", "running", "applied", "skipped", "error"}
	feedbackRollbackStatusValues = []string{"none", "running", "rolled_back", "error"}
	feedbackFinalStatusValues    = []string{"applied", "skipped", "error"}
	feedbackRollbackFinalValues  = []string{"none", "rolled_back", "error"}
)

// feedbackContains 判断白名单是否包含目标值
func feedbackContains(values []string, target string) bool {
	for _, item := range values {
		if item == target {
			return true
		}
	}
	return false
}

// feedbackPlaceholders 生成 n 个占位符，对齐 Python ",".join(["?"] * n)
func feedbackPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// feedbackLeaseSeconds 对齐 Python max(1.0, float(lease_seconds))，nil 取默认 300 秒
func feedbackLeaseSeconds(leaseSeconds *float64) float64 {
	if leaseSeconds == nil {
		return 300.0
	}
	if *leaseSeconds < 1.0 {
		return 1.0
	}
	return *leaseSeconds
}

// FeedbackTaskEnqueueOptions 对齐 Python enqueue_feedback_task 的关键字参数
type FeedbackTaskEnqueueOptions struct {
	QueryToolID    string
	SessionID      string
	QueryTimestamp float64
	DueAt          float64
	// QuerySnapshot 写入 query_snapshot_json，nil 按 {} 处理
	QuerySnapshot map[string]any
}

// FeedbackTaskListOptions 对齐 Python list_feedback_tasks 的关键字参数
type FeedbackTaskListOptions struct {
	// Limit 条数上限，0 表示 Python 默认值 50
	Limit int
	// Statuses 任务状态过滤，仅保留 pending/running/applied/skipped/error
	Statuses []string
	// RollbackStatuses 回滚状态过滤，仅保留 none/running/rolled_back/error
	RollbackStatuses []string
	// Query 模糊搜索词，命中 7 个文本字段
	Query string
}

// FeedbackDueFetchOptions 对齐 Python fetch_due_feedback_tasks 的关键字参数
type FeedbackDueFetchOptions struct {
	// Limit 条数上限，0 表示 Python 默认值 20
	Limit int
	// Now 指定的当前时间戳，nil 表示系统当前时间
	Now *float64
	// LeaseSeconds 运行中任务的租约秒数，nil 表示 Python 默认 300，取值下限 1 秒
	LeaseSeconds *float64
}

// FeedbackTaskFinalizeOptions 对齐 Python finalize_feedback_task 的关键字参数
type FeedbackTaskFinalizeOptions struct {
	TaskID          int
	Status          string
	DecisionPayload map[string]any
	LastError       string
}

// FeedbackRollbackStartOptions 对齐 Python mark_feedback_task_rollback_running 的关键字参数
type FeedbackRollbackStartOptions struct {
	TaskID       int
	RequestedBy  string
	Reason       string
	LeaseSeconds *float64
}

// FeedbackRollbackFinalizeOptions 对齐 Python finalize_feedback_task_rollback 的关键字参数
type FeedbackRollbackFinalizeOptions struct {
	TaskID         int
	RollbackStatus string
	RollbackResult map[string]any
	RollbackError  string
}

// FeedbackActionLogAppendOptions 对齐 Python append_feedback_action_log 的关键字参数
type FeedbackActionLogAppendOptions struct {
	TaskID        int
	QueryToolID   string
	ActionType    string
	TargetHash    string
	BeforePayload map[string]any
	AfterPayload  map[string]any
	Reason        string
}

// GetFeedbackTask 按 query_tool_id 读取任务，对齐 Python get_feedback_task
func (s *Store) GetFeedbackTask(queryToolID string, conn *Conn) (map[string]any, error) {
	token := strings.TrimSpace(queryToolID)
	if token == "" {
		return nil, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	row, err := feedbackQueryDictRow(c, `
		SELECT *
		FROM memory_feedback_tasks
		WHERE query_tool_id = ?
		LIMIT 1
	`, token)
	if err != nil {
		return nil, err
	}
	return feedbackTaskRowToDict(row), nil
}

// GetFeedbackTaskByID 按主键读取任务，对齐 Python get_feedback_task_by_id
func (s *Store) GetFeedbackTaskByID(taskID int, conn *Conn) (map[string]any, error) {
	if taskID <= 0 {
		return nil, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	row, err := feedbackQueryDictRow(c, `
		SELECT *
		FROM memory_feedback_tasks
		WHERE id = ?
		LIMIT 1
	`, taskID)
	if err != nil {
		return nil, err
	}
	return feedbackTaskRowToDict(row), nil
}

// ListFeedbackTasks 条件列出反馈任务，对齐 Python list_feedback_tasks
func (s *Store) ListFeedbackTasks(opts FeedbackTaskListOptions, conn *Conn) ([]map[string]any, error) {
	safeLimit := opts.Limit
	if safeLimit == 0 {
		safeLimit = 50
	}
	if safeLimit < 1 {
		safeLimit = 1
	}

	params := make([]any, 0, 8)
	conditions := make([]string, 0, 3)

	statuses := make([]string, 0, len(opts.Statuses))
	for _, item := range opts.Statuses {
		token := strings.ToLower(strings.TrimSpace(item))
		if feedbackContains(feedbackTaskStatusValues, token) {
			statuses = append(statuses, token)
		}
	}
	if len(statuses) > 0 {
		conditions = append(conditions,
			fmt.Sprintf("LOWER(COALESCE(status, '')) IN (%s)", feedbackPlaceholders(len(statuses))))
		for _, item := range statuses {
			params = append(params, item)
		}
	}

	rollbackStatuses := make([]string, 0, len(opts.RollbackStatuses))
	for _, item := range opts.RollbackStatuses {
		token := strings.ToLower(strings.TrimSpace(item))
		if feedbackContains(feedbackRollbackStatusValues, token) {
			rollbackStatuses = append(rollbackStatuses, token)
		}
	}
	if len(rollbackStatuses) > 0 {
		conditions = append(conditions,
			fmt.Sprintf("LOWER(COALESCE(rollback_status, 'none')) IN (%s)", feedbackPlaceholders(len(rollbackStatuses))))
		for _, item := range rollbackStatuses {
			params = append(params, item)
		}
	}

	queryToken := strings.ToLower(strings.TrimSpace(opts.Query))
	if queryToken != "" {
		likeValue := "%" + queryToken + "%"
		conditions = append(conditions, `(
			LOWER(COALESCE(query_tool_id, '')) LIKE ?
			OR LOWER(COALESCE(session_id, '')) LIKE ?
			OR LOWER(COALESCE(query_snapshot_json, '')) LIKE ?
			OR LOWER(COALESCE(decision_json, '')) LIKE ?
			OR LOWER(COALESCE(last_error, '')) LIKE ?
			OR LOWER(COALESCE(rollback_reason, '')) LIKE ?
			OR LOWER(COALESCE(rollback_error, '')) LIKE ?
		)`)
		for i := 0; i < 7; i++ {
			params = append(params, likeValue)
		}
	}

	whereSQL := ""
	if len(conditions) > 0 {
		whereSQL = "WHERE " + strings.Join(conditions, " AND ")
	}
	params = append(params, safeLimit)
	query := fmt.Sprintf(`
		SELECT *
		FROM memory_feedback_tasks
		%s
		ORDER BY query_timestamp DESC, id DESC
		LIMIT ?
	`, whereSQL)

	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	return feedbackQueryDictRows(c, query, params...)
}

// EnqueueFeedbackTask 入队反馈任务，对齐 Python enqueue_feedback_task
// INSERT OR IGNORE 后再按 query_tool_id 回读，重复入队返回既有任务
func (s *Store) EnqueueFeedbackTask(opts FeedbackTaskEnqueueOptions, conn *Conn) (map[string]any, error) {
	toolToken := strings.TrimSpace(opts.QueryToolID)
	sessionToken := strings.TrimSpace(opts.SessionID)
	if toolToken == "" || sessionToken == "" {
		return nil, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	now := nowTimestamp()
	if _, err := c.Exec(`
		INSERT OR IGNORE INTO memory_feedback_tasks (
			query_tool_id, session_id, query_timestamp, due_at, status, attempt_count,
			query_snapshot_json, decision_json, last_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, 'pending', 0, ?, NULL, NULL, ?, ?)
	`, toolToken, sessionToken, opts.QueryTimestamp, opts.DueAt,
		feedbackDictJSON(opts.QuerySnapshot), now, now); err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	return s.GetFeedbackTask(toolToken, c)
}

// UpdateFeedbackTaskRollbackPlan 写入回滚计划，对齐 Python update_feedback_task_rollback_plan
func (s *Store) UpdateFeedbackTaskRollbackPlan(taskID int, rollbackPlan map[string]any, conn *Conn) (map[string]any, error) {
	if taskID <= 0 {
		return nil, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	if _, err := c.Exec(`
		UPDATE memory_feedback_tasks
		SET rollback_plan_json = ?,
			updated_at = ?
		WHERE id = ?
	`, feedbackDictJSON(rollbackPlan), nowTimestamp(), taskID); err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	return s.GetFeedbackTaskByID(taskID, c)
}

// FetchDueFeedbackTasks 领取到期任务，对齐 Python fetch_due_feedback_tasks
// pending 直接可选；running 仅在 updated_at 早于租约截止时间时可重新领取
func (s *Store) FetchDueFeedbackTasks(opts FeedbackDueFetchOptions, conn *Conn) ([]map[string]any, error) {
	safeLimit := opts.Limit
	if safeLimit == 0 {
		safeLimit = 20
	}
	if safeLimit < 1 {
		safeLimit = 1
	}
	nowTS := nowTimestamp()
	if opts.Now != nil {
		nowTS = *opts.Now
	}

	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	return feedbackQueryDictRows(c, `
		SELECT *
		FROM memory_feedback_tasks
		WHERE due_at <= ?
		  AND (
				status = 'pending'
				OR (status = 'running' AND updated_at <= ?)
			  )
		ORDER BY due_at ASC, id ASC
		LIMIT ?
	`, nowTS, nowTS-feedbackLeaseSeconds(opts.LeaseSeconds), safeLimit)
}

// MarkFeedbackTaskRunning 领取任务并把 attempt_count 加一，对齐 Python mark_feedback_task_running
// 条件不满足（非 pending 且租约未过期）时返回 nil
func (s *Store) MarkFeedbackTaskRunning(taskID int, leaseSeconds *float64, conn *Conn) (map[string]any, error) {
	if taskID <= 0 {
		return nil, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	now := nowTimestamp()
	result, err := c.Exec(`
		UPDATE memory_feedback_tasks
		SET status = 'running',
			attempt_count = COALESCE(attempt_count, 0) + 1,
			updated_at = ?
		WHERE id = ?
		  AND (
				status = 'pending'
				OR (status = 'running' AND updated_at <= ?)
			  )
	`, now, taskID, now-feedbackLeaseSeconds(leaseSeconds))
	if err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected <= 0 {
		return nil, nil
	}
	row, err := feedbackQueryDictRow(c, `
		SELECT *
		FROM memory_feedback_tasks
		WHERE id = ?
		LIMIT 1
	`, taskID)
	if err != nil {
		return nil, err
	}
	return feedbackTaskRowToDict(row), nil
}

// FinalizeFeedbackTask 结束任务，对齐 Python finalize_feedback_task
// 结束状态仅允许 applied/skipped/error，其余返回与 Python 一致的错误文案
func (s *Store) FinalizeFeedbackTask(opts FeedbackTaskFinalizeOptions, conn *Conn) (map[string]any, error) {
	finalStatus := strings.ToLower(strings.TrimSpace(opts.Status))
	if !feedbackContains(feedbackFinalStatusValues, finalStatus) {
		return nil, fmt.Errorf("不支持的反馈任务结束状态: %s", opts.Status)
	}
	if opts.TaskID <= 0 {
		return nil, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	if _, err := c.Exec(`
		UPDATE memory_feedback_tasks
		SET status = ?,
			decision_json = ?,
			last_error = ?,
			updated_at = ?
		WHERE id = ?
	`, finalStatus, feedbackDictJSON(opts.DecisionPayload),
		feedbackNullableString(opts.LastError), nowTimestamp(), opts.TaskID); err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	return s.GetFeedbackTaskByID(opts.TaskID, c)
}

// MarkFeedbackTaskRollbackRunning 进入回滚中，对齐 Python mark_feedback_task_rollback_running
// 仅 applied 任务可回滚；rollback_status 为 none/error 或 running 且租约过期时允许抢占
func (s *Store) MarkFeedbackTaskRollbackRunning(opts FeedbackRollbackStartOptions, conn *Conn) (map[string]any, error) {
	if opts.TaskID <= 0 {
		return nil, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	now := nowTimestamp()
	result, err := c.Exec(`
		UPDATE memory_feedback_tasks
		SET rollback_status = 'running',
			rollback_requested_by = ?,
			rollback_reason = ?,
			rollback_error = NULL,
			rollback_requested_at = ?,
			updated_at = ?
		WHERE id = ?
		  AND LOWER(COALESCE(status, '')) = 'applied'
		  AND (
				LOWER(COALESCE(rollback_status, 'none')) IN ('none', 'error')
				OR (
					LOWER(COALESCE(rollback_status, 'none')) = 'running'
					AND updated_at <= ?
				)
			  )
	`, feedbackNullableString(opts.RequestedBy), feedbackNullableString(opts.Reason),
		now, now, opts.TaskID, now-feedbackLeaseSeconds(opts.LeaseSeconds))
	if err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected <= 0 {
		return nil, nil
	}
	return s.GetFeedbackTaskByID(opts.TaskID, c)
}

// FinalizeFeedbackTaskRollback 结束回滚，对齐 Python finalize_feedback_task_rollback
// 回滚状态仅允许 none/rolled_back/error；rolled_back 时写入 rolled_back_at
func (s *Store) FinalizeFeedbackTaskRollback(opts FeedbackRollbackFinalizeOptions, conn *Conn) (map[string]any, error) {
	if opts.TaskID <= 0 {
		return nil, nil
	}
	finalStatus := strings.ToLower(strings.TrimSpace(opts.RollbackStatus))
	if !feedbackContains(feedbackRollbackFinalValues, finalStatus) {
		return nil, fmt.Errorf("不支持的反馈任务回退状态: %s", opts.RollbackStatus)
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	now := nowTimestamp()
	if _, err := c.Exec(`
		UPDATE memory_feedback_tasks
		SET rollback_status = ?,
			rollback_result_json = ?,
			rollback_error = ?,
			rolled_back_at = CASE WHEN ? = 'rolled_back' THEN ? ELSE rolled_back_at END,
			updated_at = ?
		WHERE id = ?
	`, finalStatus, feedbackDictJSON(opts.RollbackResult),
		feedbackNullableString(opts.RollbackError), finalStatus, now, now, opts.TaskID); err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	return s.GetFeedbackTaskByID(opts.TaskID, c)
}

// AppendFeedbackActionLog 追加动作日志，对齐 Python append_feedback_action_log
// 返回值字段与 Python 一致：before_json/after_json 为未解析的 JSON 文本
func (s *Store) AppendFeedbackActionLog(opts FeedbackActionLogAppendOptions, conn *Conn) (map[string]any, error) {
	if opts.TaskID <= 0 {
		return nil, nil
	}
	queryToken := strings.TrimSpace(opts.QueryToolID)
	if queryToken == "" {
		return nil, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}

	actionType := feedbackStrOr(opts.ActionType, "unknown")
	var beforeJSON any
	if opts.BeforePayload != nil {
		beforeJSON = feedbackDictJSON(opts.BeforePayload)
	}
	var afterJSON any
	if opts.AfterPayload != nil {
		afterJSON = feedbackDictJSON(opts.AfterPayload)
	}

	now := nowTimestamp()
	result, err := c.Exec(`
		INSERT INTO memory_feedback_action_logs (
			task_id, query_tool_id, action_type, target_hash,
			before_json, after_json, reason, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, opts.TaskID, queryToken, actionType, feedbackNullableString(opts.TargetHash),
		beforeJSON, afterJSON, feedbackNullableString(opts.Reason), now)
	if err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	lastID, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":            int(lastID),
		"task_id":       opts.TaskID,
		"query_tool_id": queryToken,
		"action_type":   actionType,
		"target_hash":   strings.TrimSpace(opts.TargetHash),
		"before_json":   beforeJSON,
		"after_json":    afterJSON,
		"reason":        strings.TrimSpace(opts.Reason),
		"created_at":    now,
	}, nil
}

// ListFeedbackActionLogs 列出任务的动作日志，对齐 Python list_feedback_action_logs
func (s *Store) ListFeedbackActionLogs(taskID int, conn *Conn) ([]map[string]any, error) {
	if taskID <= 0 {
		return []map[string]any{}, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	rows, err := feedbackQueryDictRows(c, `
		SELECT id, task_id, query_tool_id, action_type, target_hash, before_json, after_json, reason, created_at
		FROM memory_feedback_action_logs
		WHERE task_id = ?
		ORDER BY id ASC
	`, taskID)
	if err != nil {
		return nil, err
	}
	logs := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		logs = append(logs, feedbackActionLogRowToDict(row))
	}
	return logs, nil
}
