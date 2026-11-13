package storage

// A_memorix 删除恢复与清理任务收尾，逐条对齐 Python core/storage/metadata_cleanup.py
// 覆盖任务取消、旧任务资源剔除、operation 结算、状态更新与快照恢复

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CancelDeleteCleanupJobs 恢复删除操作时取消未领取任务；运行中的任务必须先完成或释放租约
// 对齐 Python cancel_delete_cleanup_jobs
func (s *Store) CancelDeleteCleanupJobs(operationID string, conn *Conn) (int, error) {
	operationToken := strings.TrimSpace(operationID)
	if operationToken == "" {
		return 0, errors.New("operation_id 不能为空")
	}
	target, err := s.ResolveConn(conn)
	if err != nil {
		return 0, err
	}
	running, err := countRows(target, `
		SELECT COUNT(*) AS count
		FROM storage_cleanup_jobs
		WHERE operation_id = ?
		  AND action IN ('vector_delete', 'graph_rebuild')
		  AND status = 'running'
	`, operationToken)
	if err != nil {
		return 0, err
	}
	if running > 0 {
		return 0, errors.New("删除外部清理任务仍在运行，暂不能恢复")
	}
	now := nowTimestamp()
	result, err := target.Exec(`
		UPDATE storage_cleanup_jobs
		SET status = 'cancelled', lease_token = NULL, lease_until = NULL,
			updated_at = ?, completed_at = ?
		WHERE operation_id = ?
		  AND action IN ('vector_delete', 'graph_rebuild')
		  AND status IN ('pending', 'failed')
	`, now, now, operationToken)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if conn == nil {
		if err := target.Commit(); err != nil {
			return 0, err
		}
	}
	if affected < 0 {
		return 0, nil
	}
	return int(affected), nil
}

// SupersedeDeleteCleanupResources 从旧删除任务中事务化剔除已恢复资源，不影响同批其他资源
// 对齐 Python supersede_delete_cleanup_resources
func (s *Store) SupersedeDeleteCleanupResources(resourceType string, resourceIDs []string, conn *Conn) (CleanupSupersedeResult, error) {
	result := CleanupSupersedeResult{}
	if resourceType != "paragraph" && resourceType != "entity" && resourceType != "relation" {
		return result, fmt.Errorf("未知资源类型: %s", resourceType)
	}
	normalized := make([]string, 0, len(resourceIDs))
	for _, item := range resourceIDs {
		if token := strings.TrimSpace(item); token != "" {
			normalized = append(normalized, token)
		}
	}
	normalized = dedupeStrings(normalized)
	if len(normalized) == 0 {
		return result, nil
	}
	target, err := s.ResolveConn(conn)
	if err != nil {
		return result, err
	}
	targetIDs := make(map[string]struct{}, len(normalized))
	for _, item := range normalized {
		targetIDs[item] = struct{}{}
	}
	now := nowTimestamp()

	operationArgs := make([]any, 0, len(normalized)+1)
	operationArgs = append(operationArgs, resourceType)
	for _, item := range normalized {
		operationArgs = append(operationArgs, item)
	}
	operationRows, err := cleanupQueryMaps(target, fmt.Sprintf(`
		SELECT DISTINCT i.operation_id
		FROM delete_operation_items i
		JOIN delete_operations o ON o.operation_id = i.operation_id
		WHERE i.item_type = ?
		  AND i.item_hash IN (%s)
		  AND o.status IN ('pending_cleanup', 'completed')
	`, cleanupPlaceholders(len(normalized))), operationArgs...)
	if err != nil {
		return result, err
	}

	// operationItemsFullyRestored 判断旧 operation 的条目是否已全部恢复
	// 只恢复批量操作的一部分时，旧 operation 仍是其余删除项的唯一恢复入口
	operationItemsFullyRestored := func(operationID string) (bool, error) {
		itemRows, err := cleanupQueryMaps(target, `
			SELECT item_type, item_hash
			FROM delete_operation_items
			WHERE operation_id = ?
		`, operationID)
		if err != nil {
			return false, err
		}
		if len(itemRows) == 0 {
			return false, nil
		}
		grouped := map[string][]string{"paragraph": {}, "entity": {}, "relation": {}}
		for _, itemRow := range itemRows {
			itemType := strings.TrimSpace(cleanupStringValue(itemRow["item_type"]))
			itemHash := strings.TrimSpace(cleanupStringValue(itemRow["item_hash"]))
			if _, ok := grouped[itemType]; !ok || itemHash == "" {
				return false, nil
			}
			grouped[itemType] = append(grouped[itemType], itemHash)
		}
		for _, itemType := range []string{"paragraph", "entity", "relation"} {
			expected := make(map[string]struct{}, len(grouped[itemType]))
			for _, itemHash := range grouped[itemType] {
				expected[itemHash] = struct{}{}
			}
			if itemType == resourceType {
				for itemHash := range targetIDs {
					delete(expected, itemHash)
				}
			}
			if len(expected) == 0 {
				continue
			}
			expectedArgs := make([]any, 0, len(expected))
			for itemHash := range expected {
				expectedArgs = append(expectedArgs, itemHash)
			}
			var rows []map[string]any
			if itemType == "relation" {
				rows, err = cleanupQueryMaps(target, fmt.Sprintf(
					"SELECT hash FROM relations WHERE hash IN (%s)",
					cleanupPlaceholders(len(expectedArgs)),
				), expectedArgs...)
			} else {
				tableName := "paragraphs"
				if itemType == "entity" {
					tableName = "entities"
				}
				rows, err = cleanupQueryMaps(target, fmt.Sprintf(`
					SELECT hash FROM %s
					WHERE hash IN (%s)
					  AND (is_deleted IS NULL OR is_deleted = 0)
				`, tableName, cleanupPlaceholders(len(expectedArgs))), expectedArgs...)
			}
			if err != nil {
				return false, err
			}
			if len(rows) != len(expected) {
				return false, nil
			}
			for _, row := range rows {
				if _, ok := expected[strings.TrimSpace(cleanupStringValue(row["hash"]))]; !ok {
					return false, nil
				}
			}
		}
		return true, nil
	}

	for _, operationRow := range operationRows {
		operationID := cleanupStringValue(operationRow["operation_id"])
		running, err := countRows(target, `
			SELECT COUNT(*) AS count
			FROM storage_cleanup_jobs
			WHERE operation_id = ?
			  AND action IN ('vector_delete', 'graph_rebuild')
			  AND status = 'running'
		`, operationID)
		if err != nil {
			return result, err
		}
		if running > 0 {
			return CleanupSupersedeResult{}, errors.New("旧删除外部清理任务仍在运行，暂不能恢复")
		}

		jobs, err := cleanupQueryMaps(target, `
			SELECT job_id, resource_id, payload_json
			FROM storage_cleanup_jobs
			WHERE operation_id = ?
			  AND resource_type = ?
			  AND action = 'vector_delete'
			  AND status IN ('pending', 'failed')
		`, operationID, resourceType)
		if err != nil {
			return result, err
		}
		payloadKey := resourceType + "_hashes"
		for _, job := range jobs {
			payload := jsonLoads(job["payload_json"], map[string]any{})
			declaredIDs, err := validateCleanupJobPayload(
				resourceType,
				cleanupStringValue(job["resource_id"]),
				cleanupActionVectorDelete,
				payload,
			)
			if err != nil {
				return result, err
			}
			remainingIDs := make([]string, 0, len(declaredIDs))
			for _, item := range declaredIDs {
				if _, superseded := targetIDs[item]; !superseded {
					remainingIDs = append(remainingIDs, item)
				}
			}
			if len(remainingIDs) == len(declaredIDs) {
				continue
			}
			jobID := cleanupInt64(job["job_id"])
			if len(remainingIDs) > 0 {
				if _, err := target.Exec(`
					UPDATE storage_cleanup_jobs
					SET payload_json = ?, updated_at = ?, last_error = NULL
					WHERE job_id = ? AND status IN ('pending', 'failed')
				`, jsonDumps(map[string]any{payloadKey: remainingIDs}), now, jobID); err != nil {
					return result, err
				}
				result.UpdatedJobs++
				continue
			}
			if _, err := target.Exec(`
				UPDATE storage_cleanup_jobs
				SET status = 'cancelled', lease_token = NULL, lease_until = NULL,
					updated_at = ?, completed_at = ?, last_error = NULL
				WHERE job_id = ? AND status IN ('pending', 'failed')
			`, now, now, jobID); err != nil {
				return result, err
			}
			result.CancelledJobs++
		}

		remaining, err := countRows(target, `
			SELECT COUNT(*) AS count
			FROM storage_cleanup_jobs
			WHERE operation_id = ?
			  AND action = 'vector_delete'
			  AND status IN ('pending', 'failed', 'running')
		`, operationID)
		if err != nil {
			return result, err
		}
		if remaining > 0 {
			continue
		}
		fullyRestored, err := operationItemsFullyRestored(operationID)
		if err != nil {
			return result, err
		}
		if !fullyRestored {
			continue
		}
		graphResult, err := target.Exec(`
			UPDATE storage_cleanup_jobs
			SET status = 'cancelled', lease_token = NULL, lease_until = NULL,
				updated_at = ?, completed_at = ?, last_error = NULL
			WHERE operation_id = ?
			  AND action = 'graph_rebuild'
			  AND status IN ('pending', 'failed')
		`, now, now, operationID)
		if err != nil {
			return result, err
		}
		graphAffected, err := graphResult.RowsAffected()
		if err != nil {
			return result, err
		}
		if graphAffected > 0 {
			result.CancelledJobs += int(graphAffected)
		}

		summaryRows, err := cleanupQueryMaps(target,
			"SELECT summary_json FROM delete_operations WHERE operation_id = ?", operationID)
		if err != nil {
			return result, err
		}
		summary := map[string]any{}
		if len(summaryRows) > 0 {
			summary = cleanupJSONObject(summaryRows[0]["summary_json"])
		}
		cleanupSummary, err := cleanupJobSummaryInTx(target, operationID)
		if err != nil {
			return result, err
		}
		summary["state"] = cleanupStatusSuperseded
		summary["superseded_by_restore"] = map[string]any{
			"resource_type": resourceType,
			"resource_ids":  normalized,
		}
		summary["cleanup"] = cleanupSummary
		cursor, err := target.Exec(`
			UPDATE delete_operations
			SET status = 'superseded', summary_json = ?
			WHERE operation_id = ? AND status IN ('pending_cleanup', 'completed')
		`, jsonDumps(summary), operationID)
		if err != nil {
			return result, err
		}
		cursorAffected, err := cursor.RowsAffected()
		if err != nil {
			return result, err
		}
		if cursorAffected > 0 {
			result.SupersededOperations += int(cursorAffected)
		}
	}
	return result, nil
}

// ReconcileSettledDeleteOperations 修复任务已终态但 operation 尚未迁移的崩溃窗口
// 对齐 Python reconcile_settled_delete_operations
func (s *Store) ReconcileSettledDeleteOperations(operationIDs []string) ([]string, error) {
	normalized := make([]string, 0, len(operationIDs))
	for _, item := range operationIDs {
		if token := strings.TrimSpace(item); token != "" {
			normalized = append(normalized, token)
		}
	}
	reconciled := make([]string, 0)
	err := s.Transaction(true, func(tx *Tx) error {
		params := make([]any, 0, len(normalized))
		operationClause := ""
		if len(normalized) > 0 {
			operationClause = fmt.Sprintf("AND operation_id IN (%s)", cleanupPlaceholders(len(normalized)))
			for _, item := range normalized {
				params = append(params, item)
			}
		}
		rows, err := cleanupQueryMaps(tx, fmt.Sprintf(`
			SELECT operation_id
			FROM delete_operations
			WHERE status IN ('pending_cleanup', 'restore_pending')
			%s
			ORDER BY created_at ASC
		`, operationClause), params...)
		if err != nil {
			return err
		}
		for _, row := range rows {
			operationID := cleanupStringValue(row["operation_id"])
			targetStatus, err := reconcileSettledDeleteOperation(tx, operationID)
			if err != nil {
				return err
			}
			if targetStatus != nil {
				reconciled = append(reconciled, operationID)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return reconciled, nil
}

// CreateCleanupWorkerToken 生成 cleanup_<32 位十六进制> 工作线程令牌
// 对齐 Python create_cleanup_worker_token
func (s *Store) CreateCleanupWorkerToken() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "cleanup_" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return "cleanup_" + hex.EncodeToString(buffer)
}

// UpdateDeleteOperationState 更新删除操作状态并合并可审计摘要
// 对齐 Python update_delete_operation_state
func (s *Store) UpdateDeleteOperationState(operationID string, status string, summaryPatch map[string]any, conn *Conn) (bool, error) {
	operationToken := strings.TrimSpace(operationID)
	statusToken := strings.TrimSpace(status)
	if operationToken == "" || statusToken == "" {
		return false, errors.New("operation_id 和 status 不能为空")
	}
	target, err := s.ResolveConn(conn)
	if err != nil {
		return false, err
	}
	rows, err := cleanupQueryMaps(target,
		"SELECT summary_json FROM delete_operations WHERE operation_id = ?", operationToken)
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, nil
	}
	summary := cleanupJSONObject(rows[0]["summary_json"])
	for key, value := range summaryPatch {
		summary[key] = value
	}
	result, err := target.Exec(`
		UPDATE delete_operations
		SET status = ?, summary_json = ?
		WHERE operation_id = ?
	`, statusToken, jsonDumps(summary), operationToken)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if conn == nil {
		if err := target.Commit(); err != nil {
			return false, err
		}
	}
	return affected > 0, nil
}

// RestoreTableRowFromSnapshot 按当前正式 schema 恢复被物理清理的基础行
// 对齐 Python restore_table_row_from_snapshot
func (s *Store) RestoreTableRowFromSnapshot(table string, snapshot map[string]any, conn *Conn) (bool, error) {
	switch table {
	case "paragraphs", "entities", "relations":
	default:
		return false, fmt.Errorf("不允许恢复表: %s", table)
	}
	if snapshot == nil || cleanupTrimmed(snapshot["hash"]) == "" {
		return false, errors.New("恢复快照缺少 hash")
	}
	target, err := s.ResolveConn(conn)
	if err != nil {
		return false, err
	}
	tableColumns, err := tableColumnSet(target, table)
	if err != nil {
		return false, err
	}
	restored := make(map[string]any, len(snapshot)+2)
	for _, key := range cleanupSortedKeys(snapshot) {
		if _, ok := tableColumns[key]; ok {
			restored[key] = snapshot[key]
		}
	}
	if metadata, ok := restored["metadata"].(map[string]any); ok {
		restored["metadata"] = encodeMetadata(metadata)
	}
	switch table {
	case "paragraphs":
		if cleanupStringValue(restored["content"]) == "" {
			return false, errors.New("段落恢复快照缺少 content")
		}
		restored["is_deleted"] = 0
		restored["deleted_at"] = nil
	case "entities":
		if cleanupTrimmed(restored["name"]) == "" {
			return false, errors.New("实体恢复快照缺少 name")
		}
		restored["is_deleted"] = 0
		restored["deleted_at"] = nil
	}
	columns := cleanupSortedKeys(restored)
	updates := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for _, column := range columns {
		args = append(args, restored[column])
		if column != "hash" {
			updates = append(updates, column+"=excluded."+column)
		}
	}
	if _, err := target.Exec(fmt.Sprintf(`
		INSERT INTO %s (%s)
		VALUES (%s)
		ON CONFLICT(hash) DO UPDATE SET %s
	`, table, strings.Join(columns, ","), cleanupPlaceholders(len(columns)), strings.Join(updates, ",")), args...); err != nil {
		return false, err
	}
	if conn == nil {
		if err := target.Commit(); err != nil {
			return false, err
		}
	}
	return true, nil
}
