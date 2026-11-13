package storage

// A_memorix 清理 Outbox 的入队、领取、完成、失败、列举与外部提交授权
// 逐条对齐 Python core/storage/metadata_cleanup.py 同名方法

import (
	"errors"
	"fmt"
	"strings"
)

// ClaimCleanupJobsOptions 对齐 Python claim_storage_cleanup_jobs 的关键字参数
type ClaimCleanupJobsOptions struct {
	// Limit 单次领取上限，0 表示默认 100
	Limit int
	// LeaseSeconds 租约时长（秒），0 表示默认 60
	LeaseSeconds float64
	// OperationID 只领取指定 operation 的任务，空串表示不限
	OperationID string
	// Now 指定的当前时间，nil 表示取系统时间
	Now *float64
}

// AuthorizeCleanupJobOptions 对齐 Python authorize_storage_cleanup_job 的关键字参数
type AuthorizeCleanupJobOptions struct {
	// LeaseSeconds 续期后的租约时长（秒），0 表示默认 300
	LeaseSeconds float64
	// Now 指定的当前时间，nil 表示取系统时间
	Now *float64
}

// ListCleanupJobsOptions 对齐 Python list_storage_cleanup_jobs 的关键字参数
type ListCleanupJobsOptions struct {
	// OperationID 只列举指定 operation 的任务，空串表示不限
	OperationID string
	// Statuses 只列举指定状态，空集合表示不限
	Statuses []string
	// Limit 单次列举上限，0 表示默认 500
	Limit int
}

// cleanupExpectedStatusByAction 返回动作对应的 operation 期望状态
// 对齐 Python expected_status_by_action
func cleanupExpectedStatusByAction(action string) (string, bool) {
	switch action {
	case cleanupActionVectorDelete, cleanupActionGraphRebuild:
		return cleanupStatusPendingCleanup, true
	case cleanupActionVectorUpsert, cleanupActionGraphRestore:
		return cleanupStatusRestorePending, true
	default:
		return "", false
	}
}

// cleanupExpectedStateMatches 判断 expected_state 是否恰为 {operation_status: expectedStatus}
func cleanupExpectedStateMatches(state map[string]any, expectedStatus string) bool {
	if len(state) != 1 {
		return false
	}
	value, ok := state["operation_status"].(string)
	return ok && value == expectedStatus
}

// cleanupJobFromRow 把一行 storage_cleanup_jobs 转为任务结构
// 对齐 Python dict(row) 后替换 payload_json/expected_state_json 的处理
func cleanupJobFromRow(row map[string]any) StorageCleanupJob {
	return StorageCleanupJob{
		JobID:         cleanupInt64(row["job_id"]),
		OperationID:   cleanupStringValue(row["operation_id"]),
		ResourceType:  cleanupStringValue(row["resource_type"]),
		ResourceID:    cleanupStringValue(row["resource_id"]),
		Action:        cleanupStringValue(row["action"]),
		Payload:       jsonLoads(row["payload_json"], map[string]any{}),
		ExpectedState: jsonLoads(row["expected_state_json"], map[string]any{}),
		Status:        cleanupStringValue(row["status"]),
		AttemptCount:  int(cleanupInt64(row["attempt_count"])),
		NextAttemptAt: cleanupFloatValue(row["next_attempt_at"]),
		LeaseToken:    cleanupStringPtr(row["lease_token"]),
		LeaseUntil:    cleanupFloatPtr(row["lease_until"]),
		LastError:     cleanupStringPtr(row["last_error"]),
		CreatedAt:     cleanupFloatValue(row["created_at"]),
		UpdatedAt:     cleanupFloatValue(row["updated_at"]),
		CompletedAt:   cleanupFloatPtr(row["completed_at"]),
	}
}

// EnqueueStorageCleanupJobs 在调用方事务中写入幂等清理任务
// 对齐 Python enqueue_storage_cleanup_jobs
func (s *Store) EnqueueStorageCleanupJobs(operationID string, jobs []CleanupJobSpec, conn *Conn) (int, error) {
	operationToken := strings.TrimSpace(operationID)
	if operationToken == "" {
		return 0, errors.New("operation_id 不能为空")
	}
	target, err := s.ResolveConn(conn)
	if err != nil {
		return 0, err
	}
	now := nowTimestamp()
	inserted := 0
	for _, job := range jobs {
		resourceType := strings.TrimSpace(job.ResourceType)
		resourceID := strings.TrimSpace(job.ResourceID)
		action := strings.TrimSpace(job.Action)
		if resourceType == "" || resourceID == "" || action == "" {
			return 0, errors.New("清理任务必须包含 resource_type、resource_id 和 action")
		}
		expectedStatus, ok := cleanupExpectedStatusByAction(action)
		if !ok {
			return 0, fmt.Errorf("未知清理任务 action: %s", action)
		}
		if !cleanupExpectedStateMatches(job.ExpectedState, expectedStatus) {
			return 0, fmt.Errorf(
				"清理任务 expected_state 必须为 {'operation_status': '%s'}: action=%s",
				expectedStatus, action,
			)
		}
		if _, err := validateCleanupJobPayload(resourceType, resourceID, action, job.Payload); err != nil {
			return 0, err
		}
		var expectedState map[string]any
		if job.ExpectedState != nil {
			expectedState = job.ExpectedState
		}
		result, err := target.Exec(`
			INSERT OR IGNORE INTO storage_cleanup_jobs (
				operation_id, resource_type, resource_id, action,
				payload_json, expected_state_json, status, attempt_count,
				next_attempt_at, lease_token, lease_until, last_error,
				created_at, updated_at, completed_at
			) VALUES (?, ?, ?, ?, ?, ?, 'pending', 0, ?, NULL, NULL, NULL, ?, ?, NULL)
		`, operationToken, resourceType, resourceID, action,
			jsonDumps(job.Payload), jsonDumps(expectedState), now, now, now)
		if err != nil {
			return 0, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if affected > 0 {
			inserted += int(affected)
		}
	}
	return inserted, nil
}

// ClaimStorageCleanupJobs 领取待执行任务；过期租约可以由其他工作线程重新领取
// 对齐 Python claim_storage_cleanup_jobs
func (s *Store) ClaimStorageCleanupJobs(workerToken string, opts ClaimCleanupJobsOptions) ([]StorageCleanupJob, error) {
	worker := strings.TrimSpace(workerToken)
	if worker == "" {
		return nil, errors.New("worker_token 不能为空")
	}
	current := nowTimestamp()
	if opts.Now != nil {
		current = *opts.Now
	}
	leaseSeconds := opts.LeaseSeconds
	if leaseSeconds == 0 {
		leaseSeconds = cleanupDefaultClaimLease
	}
	if leaseSeconds < 1 {
		leaseSeconds = 1
	}
	leaseUntil := current + leaseSeconds
	operationToken := strings.TrimSpace(opts.OperationID)
	limit := opts.Limit
	if limit == 0 {
		limit = cleanupDefaultClaimLimit
	}
	if limit < 1 {
		limit = 1
	}

	claimed := make([]map[string]any, 0)
	err := s.Transaction(true, func(tx *Tx) error {
		params := []any{current, current}
		operationClause := ""
		if operationToken != "" {
			operationClause = "AND operation_id = ?"
			params = append(params, operationToken)
		}
		params = append(params, limit)
		rows, err := cleanupQueryMaps(tx, fmt.Sprintf(`
			SELECT job_id
			FROM storage_cleanup_jobs
			WHERE (
				(status IN ('pending', 'failed') AND next_attempt_at <= ?)
				OR (status = 'running' AND lease_until <= ?)
			)
			%s
			ORDER BY created_at ASC, job_id ASC
			LIMIT ?
		`, operationClause), params...)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		jobIDs := make([]any, 0, len(rows))
		for _, row := range rows {
			jobIDs = append(jobIDs, cleanupInt64(row["job_id"]))
		}
		placeholders := cleanupPlaceholders(len(jobIDs))
		updateArgs := []any{worker, leaseUntil, current}
		updateArgs = append(updateArgs, jobIDs...)
		if _, err := tx.Exec(fmt.Sprintf(`
			UPDATE storage_cleanup_jobs
			SET status = 'running',
				attempt_count = attempt_count + 1,
				lease_token = ?,
				lease_until = ?,
				updated_at = ?,
				last_error = NULL
			WHERE job_id IN (%s)
		`, placeholders), updateArgs...); err != nil {
			return err
		}
		selectArgs := append(append([]any{}, jobIDs...), worker)
		claimed, err = cleanupQueryMaps(tx, fmt.Sprintf(`
			SELECT *
			FROM storage_cleanup_jobs
			WHERE job_id IN (%s) AND lease_token = ?
			ORDER BY created_at ASC, job_id ASC
		`, placeholders), selectArgs...)
		return err
	})
	if err != nil {
		return nil, err
	}
	result := make([]StorageCleanupJob, 0, len(claimed))
	for _, row := range claimed {
		result = append(result, cleanupJobFromRow(row))
	}
	return result, nil
}

// CompleteStorageCleanupJob 完成或取消当前工作线程持有的任务
// 对齐 Python complete_storage_cleanup_job
func (s *Store) CompleteStorageCleanupJob(jobID int64, workerToken string, status string) (bool, error) {
	normalizedStatus := strings.ToLower(strings.TrimSpace(status))
	if normalizedStatus == "" {
		normalizedStatus = cleanupStatusCompleted
	}
	if normalizedStatus != cleanupStatusCompleted && normalizedStatus != cleanupStatusCancelled {
		return false, errors.New("status 只能是 completed 或 cancelled")
	}
	worker := strings.TrimSpace(workerToken)
	now := nowTimestamp()
	completed := false
	err := s.Transaction(true, func(tx *Tx) error {
		rows, err := cleanupQueryMaps(tx, `
			SELECT operation_id
			FROM storage_cleanup_jobs
			WHERE job_id = ? AND status = 'running' AND lease_token = ?
		`, jobID, worker)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		result, err := tx.Exec(`
			UPDATE storage_cleanup_jobs
			SET status = ?, lease_token = NULL, lease_until = NULL,
				updated_at = ?, completed_at = ?, last_error = NULL
			WHERE job_id = ? AND status = 'running' AND lease_token = ?
		`, normalizedStatus, now, now, jobID, worker)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return nil
		}
		if _, err := reconcileSettledDeleteOperation(tx, cleanupStringValue(rows[0]["operation_id"])); err != nil {
			return err
		}
		completed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return completed, nil
}

// FailStorageCleanupJob 记录失败并按确定的退避时间重新排队
// 对齐 Python fail_storage_cleanup_job
func (s *Store) FailStorageCleanupJob(jobID int64, workerToken string, lastError string, retryDelaySeconds float64) (bool, error) {
	conn, err := s.Conn()
	if err != nil {
		return false, err
	}
	now := nowTimestamp()
	delay := retryDelaySeconds
	if delay < 0 {
		delay = 0
	}
	result, err := conn.Exec(`
		UPDATE storage_cleanup_jobs
		SET status = 'failed', lease_token = NULL, lease_until = NULL,
			next_attempt_at = ?, updated_at = ?, last_error = ?
		WHERE job_id = ? AND status = 'running' AND lease_token = ?
	`, now+delay, now, truncateRunes(cleanupStringValue(lastError), cleanupLastErrorMaxRunes),
		jobID, strings.TrimSpace(workerToken))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := conn.Commit(); err != nil {
		return false, err
	}
	return affected > 0, nil
}

// ListStorageCleanupJobs 列举清理任务，按创建时间与任务号排序
// 对齐 Python list_storage_cleanup_jobs
func (s *Store) ListStorageCleanupJobs(opts ListCleanupJobsOptions) ([]StorageCleanupJob, error) {
	conn, err := s.Conn()
	if err != nil {
		return nil, err
	}
	clauses := make([]string, 0, 2)
	params := make([]any, 0, 4)
	if operationToken := strings.TrimSpace(opts.OperationID); operationToken != "" {
		clauses = append(clauses, "operation_id = ?")
		params = append(params, operationToken)
	}
	statuses := make([]string, 0, len(opts.Statuses))
	for _, item := range opts.Statuses {
		if token := strings.ToLower(strings.TrimSpace(item)); token != "" {
			statuses = append(statuses, token)
		}
	}
	if len(statuses) > 0 {
		clauses = append(clauses, fmt.Sprintf("status IN (%s)", cleanupPlaceholders(len(statuses))))
		for _, item := range statuses {
			params = append(params, item)
		}
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	limit := opts.Limit
	if limit == 0 {
		limit = cleanupDefaultListLimit
	}
	if limit < 1 {
		limit = 1
	}
	params = append(params, limit)
	rows, err := cleanupQueryMaps(conn, fmt.Sprintf(`
		SELECT *
		FROM storage_cleanup_jobs
		%s
		ORDER BY created_at ASC, job_id ASC
		LIMIT ?
	`, where), params...)
	if err != nil {
		return nil, err
	}
	result := make([]StorageCleanupJob, 0, len(rows))
	for _, row := range rows {
		result = append(result, cleanupJobFromRow(row))
	}
	return result, nil
}

// AuthorizeStorageCleanupJob 校验租约、operation 及资源权威状态，并延长外部提交租约
// 对齐 Python authorize_storage_cleanup_job
func (s *Store) AuthorizeStorageCleanupJob(jobID int64, workerToken string, opts AuthorizeCleanupJobOptions) (*CleanupJobAuthorization, error) {
	worker := strings.TrimSpace(workerToken)
	if worker == "" {
		return nil, errors.New("worker_token 不能为空")
	}
	current := nowTimestamp()
	if opts.Now != nil {
		current = *opts.Now
	}
	leaseSeconds := opts.LeaseSeconds
	if leaseSeconds == 0 {
		leaseSeconds = cleanupDefaultAuthorizeLease
	}
	if leaseSeconds < 1 {
		leaseSeconds = 1
	}

	var authorization *CleanupJobAuthorization
	err := s.Transaction(true, func(tx *Tx) error {
		rows, err := cleanupQueryMaps(tx, `
			SELECT j.status AS job_status,
			       j.lease_token,
			       j.lease_until,
			       j.resource_type,
			       j.resource_id,
			       j.action,
			       j.payload_json,
			       j.expected_state_json,
			       o.status AS operation_status
			FROM storage_cleanup_jobs j
			JOIN delete_operations o ON o.operation_id = j.operation_id
			WHERE j.job_id = ?
		`, jobID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("storage cleanup job 不存在: job_id=%d", jobID)
		}
		row := rows[0]
		if cleanupStringValue(row["job_status"]) != cleanupStatusRunning ||
			cleanupStringValue(row["lease_token"]) != worker {
			return fmt.Errorf("storage cleanup job 租约已失效: job_id=%d", jobID)
		}
		if cleanupFloatValue(row["lease_until"]) <= current {
			return fmt.Errorf("storage cleanup job 租约已过期: job_id=%d", jobID)
		}

		expectedState, stateOK := jsonLoads(row["expected_state_json"], map[string]any{}).(map[string]any)
		_, hasOperationStatus := expectedState["operation_status"]
		if !stateOK || len(expectedState) != 1 || !hasOperationStatus {
			return fmt.Errorf("storage cleanup job expected_state 必须且只能包含 operation_status: job_id=%d", jobID)
		}
		expectedStatus := strings.TrimSpace(cleanupStringValue(expectedState["operation_status"]))
		if expectedStatus != cleanupStatusPendingCleanup && expectedStatus != cleanupStatusRestorePending {
			return fmt.Errorf("storage cleanup job operation_status 非法: job_id=%d, status=%s", jobID, expectedStatus)
		}

		resourceType := cleanupStringValue(row["resource_type"])
		resourceID := cleanupStringValue(row["resource_id"])
		action := cleanupStringValue(row["action"])
		payload := jsonLoads(row["payload_json"], map[string]any{})
		declaredIDs, err := validateCleanupJobPayload(resourceType, resourceID, action, payload)
		if err != nil {
			return err
		}
		operationMatches := cleanupStringValue(row["operation_status"]) == expectedStatus
		activeIDs := make(map[string]struct{})
		if len(declaredIDs) > 0 {
			activeIDs, err = activeCleanupResourceIDs(tx, resourceType, declaredIDs)
			if err != nil {
				return err
			}
		}
		authorizedIDs := make([]string, 0, len(declaredIDs))
		switch action {
		case cleanupActionVectorDelete:
			for _, item := range declaredIDs {
				if _, active := activeIDs[item]; !active {
					authorizedIDs = append(authorizedIDs, item)
				}
			}
		case cleanupActionVectorUpsert:
			for _, item := range declaredIDs {
				if _, active := activeIDs[item]; active {
					authorizedIDs = append(authorizedIDs, item)
				}
			}
		}
		if !operationMatches {
			authorization = &CleanupJobAuthorization{
				OperationMatches: false,
				Action:           action,
				ResourceType:     resourceType,
				ResourceIDs:      []string{},
			}
			return nil
		}
		result, err := tx.Exec(`
			UPDATE storage_cleanup_jobs
			SET lease_until = ?, updated_at = ?
			WHERE job_id = ? AND status = 'running' AND lease_token = ?
		`, current+leaseSeconds, current, jobID, worker)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("storage cleanup job 租约续期失败: job_id=%d", jobID)
		}
		authorization = &CleanupJobAuthorization{
			OperationMatches: true,
			Action:           action,
			ResourceType:     resourceType,
			ResourceIDs:      authorizedIDs,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return authorization, nil
}
