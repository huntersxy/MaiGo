package storage

// A_memorix 陈旧关系标记，逐条对齐 Python core/storage/metadata_feedback.py 的
// upsert/get/get_batch/count/delete/rollback 与 _restore_paragraph_stale_relation_mark

import "strings"

// ParagraphStaleMarkUpsertOptions 对齐 Python upsert_paragraph_stale_relation_mark 的关键字参数
type ParagraphStaleMarkUpsertOptions struct {
	ParagraphHash string
	RelationHash  string
	QueryToolID   string
	// TaskID 关联的反馈任务，<=0 时写入 NULL
	TaskID            int
	Reason            string
	SourceType        string
	SourceID          string
	SourceOperationID string
}

// ParagraphStaleMarkRollbackOptions 对齐 Python rollback_paragraph_stale_relation_mark 的关键字参数
type ParagraphStaleMarkRollbackOptions struct {
	ParagraphHash             string
	RelationHash              string
	ExpectedSourceType        string
	ExpectedSourceID          string
	ExpectedSourceOperationID string
	// PreviousMark 待恢复的快照，nil 表示直接删除当前标记
	PreviousMark map[string]any
}

// UpsertParagraphStaleRelationMark 写入或更新陈旧关系标记，对齐 Python upsert_paragraph_stale_relation_mark
// 冲突时刷新来源与更新时间，但保留 created_at
func (s *Store) UpsertParagraphStaleRelationMark(opts ParagraphStaleMarkUpsertOptions, conn *Conn) (map[string]any, error) {
	paragraphToken := strings.TrimSpace(opts.ParagraphHash)
	relationToken := strings.TrimSpace(opts.RelationHash)
	if paragraphToken == "" || relationToken == "" {
		return nil, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	now := nowTimestamp()
	if _, err := c.Exec(`
		INSERT INTO paragraph_stale_relation_marks (
			paragraph_hash, relation_hash, query_tool_id, task_id, reason,
			source_type, source_id, source_operation_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(paragraph_hash, relation_hash) DO UPDATE SET
			query_tool_id = excluded.query_tool_id,
			task_id = excluded.task_id,
			reason = excluded.reason,
			source_type = excluded.source_type,
			source_id = excluded.source_id,
			source_operation_id = excluded.source_operation_id,
			updated_at = excluded.updated_at
	`, paragraphToken, relationToken, feedbackNullableString(opts.QueryToolID),
		feedbackNullableTaskID(opts.TaskID), feedbackNullableString(opts.Reason),
		feedbackNullableString(opts.SourceType), feedbackNullableString(opts.SourceID),
		feedbackNullableString(opts.SourceOperationID), now, now); err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	return s.GetParagraphStaleRelationMark(paragraphToken, relationToken, c)
}

// GetParagraphStaleRelationMark 读取单条陈旧关系标记，对齐 Python get_paragraph_stale_relation_mark
func (s *Store) GetParagraphStaleRelationMark(paragraphHash, relationHash string, conn *Conn) (map[string]any, error) {
	paragraphToken := strings.TrimSpace(paragraphHash)
	relationToken := strings.TrimSpace(relationHash)
	if paragraphToken == "" || relationToken == "" {
		return nil, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	row, err := feedbackQueryDictRow(c, `
		SELECT paragraph_hash, relation_hash, query_tool_id, task_id, reason,
			   source_type, source_id, source_operation_id, created_at, updated_at
		FROM paragraph_stale_relation_marks
		WHERE paragraph_hash = ? AND relation_hash = ?
	`, paragraphToken, relationToken)
	if err != nil {
		return nil, err
	}
	return feedbackStaleMarkRowToDict(row), nil
}

// GetParagraphStaleRelationMarksBatch 批量读取陈旧关系标记，对齐 Python get_paragraph_stale_relation_marks_batch
// 返回按段落哈希分组的结果，未命中的段落保留空列表
func (s *Store) GetParagraphStaleRelationMarksBatch(paragraphHashes []string, conn *Conn) (map[string][]map[string]any, error) {
	normalized := make([]string, 0, len(paragraphHashes))
	seen := make(map[string]struct{}, len(paragraphHashes))
	for _, item := range paragraphHashes {
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

	grouped := make(map[string][]map[string]any, len(normalized))
	if len(normalized) == 0 {
		return grouped, nil
	}
	for _, token := range normalized {
		grouped[token] = []map[string]any{}
	}

	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	query := "SELECT paragraph_hash, relation_hash, query_tool_id, task_id, reason, " +
		"source_type, source_id, source_operation_id, created_at, updated_at " +
		"FROM paragraph_stale_relation_marks " +
		"WHERE paragraph_hash IN (" + feedbackPlaceholders(len(normalized)) + ") " +
		"ORDER BY updated_at DESC, paragraph_hash ASC, relation_hash ASC"
	args := make([]any, len(normalized))
	for i, token := range normalized {
		args[i] = token
	}
	rows, err := feedbackQueryDictRows(c, query, args...)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		payload := feedbackStaleMarkRowToDict(row)
		if payload == nil {
			continue
		}
		key, _ := payload["paragraph_hash"].(string)
		grouped[key] = append(grouped[key], payload)
	}
	return grouped, nil
}

// CountParagraphStaleRelationMarks 统计陈旧关系标记总数，对齐 Python count_paragraph_stale_relation_marks
func (s *Store) CountParagraphStaleRelationMarks(conn *Conn) (int, error) {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return 0, err
	}
	return countRows(c, "SELECT COUNT(*) FROM paragraph_stale_relation_marks")
}

// DeleteParagraphStaleRelationMarks 逐条删除陈旧关系标记并返回删除行数，对齐 Python delete_paragraph_stale_relation_marks
func (s *Store) DeleteParagraphStaleRelationMarks(marks [][2]string, conn *Conn) (int, error) {
	normalized := make([][2]string, 0, len(marks))
	seen := make(map[[2]string]struct{}, len(marks))
	for _, item := range marks {
		paragraphToken := strings.TrimSpace(item[0])
		relationToken := strings.TrimSpace(item[1])
		if paragraphToken == "" || relationToken == "" {
			continue
		}
		key := [2]string{paragraphToken, relationToken}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	if len(normalized) == 0 {
		return 0, nil
	}

	c, err := s.ResolveConn(conn)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, key := range normalized {
		result, err := c.Exec(`
			DELETE FROM paragraph_stale_relation_marks
			WHERE paragraph_hash = ? AND relation_hash = ?
		`, key[0], key[1])
		if err != nil {
			return deleted, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return deleted, err
		}
		deleted += int(affected)
	}
	if err := c.Commit(); err != nil {
		return deleted, err
	}
	return deleted, nil
}

// RollbackParagraphStaleRelationMark 回滚单条陈旧关系标记，对齐 Python rollback_paragraph_stale_relation_mark
// 返回字典的 success/action 与 Python 完全一致：
// invalid_target / already_missing / skipped_due_to_source_mismatch / restored / restore_failed / deleted
func (s *Store) RollbackParagraphStaleRelationMark(opts ParagraphStaleMarkRollbackOptions, conn *Conn) (map[string]any, error) {
	paragraphToken := strings.TrimSpace(opts.ParagraphHash)
	relationToken := strings.TrimSpace(opts.RelationHash)
	expectedType := strings.TrimSpace(opts.ExpectedSourceType)
	expectedID := strings.TrimSpace(opts.ExpectedSourceID)
	expectedOperationID := strings.TrimSpace(opts.ExpectedSourceOperationID)
	if paragraphToken == "" || relationToken == "" {
		return map[string]any{
			"success":        false,
			"action":         "invalid_target",
			"paragraph_hash": paragraphToken,
			"relation_hash":  relationToken,
			"error":          "paragraph_hash 和 relation_hash 不能为空",
		}, nil
	}

	current, err := s.GetParagraphStaleRelationMark(paragraphToken, relationToken, conn)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return map[string]any{
			"success":        true,
			"action":         "already_missing",
			"paragraph_hash": paragraphToken,
			"relation_hash":  relationToken,
		}, nil
	}

	currentSource := [3]string{
		feedbackStrOr(current["source_type"], ""),
		feedbackStrOr(current["source_id"], ""),
		feedbackStrOr(current["source_operation_id"], ""),
	}
	if currentSource != [3]string{expectedType, expectedID, expectedOperationID} {
		return map[string]any{
			"success":        true,
			"action":         "skipped_due_to_source_mismatch",
			"paragraph_hash": paragraphToken,
			"relation_hash":  relationToken,
			"current":        current,
			"expected_source": map[string]any{
				"source_type":         expectedType,
				"source_id":           expectedID,
				"source_operation_id": expectedOperationID,
			},
		}, nil
	}

	before := current
	if opts.PreviousMark != nil {
		restored, err := s.restoreParagraphStaleRelationMark(conn, opts.PreviousMark)
		if err != nil {
			return nil, err
		}
		action := "restore_failed"
		if restored != nil {
			action = "restored"
		}
		// Python 返回 None，Go 侧需写入 untyped nil 而不是 typed-nil map
		var after any
		if restored != nil {
			after = restored
		}
		return map[string]any{
			"success":        restored != nil,
			"action":         action,
			"paragraph_hash": paragraphToken,
			"relation_hash":  relationToken,
			"before":         before,
			"after":          after,
		}, nil
	}

	deleted, err := s.DeleteParagraphStaleRelationMarks([][2]string{{paragraphToken, relationToken}}, conn)
	if err != nil {
		return nil, err
	}
	action := "already_missing"
	if deleted > 0 {
		action = "deleted"
	}
	return map[string]any{
		"success":        true,
		"action":         action,
		"paragraph_hash": paragraphToken,
		"relation_hash":  relationToken,
		"before":         before,
	}, nil
}

// restoreParagraphStaleRelationMark 按快照恢复标记，对齐 Python _restore_paragraph_stale_relation_mark
func (s *Store) restoreParagraphStaleRelationMark(conn *Conn, snapshot map[string]any) (map[string]any, error) {
	paragraphToken := feedbackStrOr(snapshot["paragraph_hash"], "")
	relationToken := feedbackStrOr(snapshot["relation_hash"], "")
	if paragraphToken == "" || relationToken == "" {
		return nil, nil
	}

	createdAt := nowTimestamp()
	if value := OptionalFloat(snapshot["created_at"]); value != nil && *value != 0 {
		createdAt = *value
	}
	updatedAt := createdAt
	if value := OptionalFloat(snapshot["updated_at"]); value != nil && *value != 0 {
		updatedAt = *value
	}
	taskValue := feedbackIntValue(snapshot["task_id"])

	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	if _, err := c.Exec(`
		INSERT INTO paragraph_stale_relation_marks (
			paragraph_hash, relation_hash, query_tool_id, task_id, reason,
			source_type, source_id, source_operation_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(paragraph_hash, relation_hash) DO UPDATE SET
			query_tool_id = excluded.query_tool_id,
			task_id = excluded.task_id,
			reason = excluded.reason,
			source_type = excluded.source_type,
			source_id = excluded.source_id,
			source_operation_id = excluded.source_operation_id,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at
	`, paragraphToken, relationToken, feedbackNullableString(snapshot["query_tool_id"]),
		feedbackNullableTaskID(taskValue), feedbackNullableString(snapshot["reason"]),
		feedbackNullableString(snapshot["source_type"]), feedbackNullableString(snapshot["source_id"]),
		feedbackNullableString(snapshot["source_operation_id"]), createdAt, updatedAt); err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	return s.GetParagraphStaleRelationMark(paragraphToken, relationToken, c)
}
