package storage

// A_memorix 关系状态、保护与回收站操作，对齐 Python core/storage/metadata_store.py L3653-L4130

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"

	"MaiGo/internal/A_memorix/core/utils"
)

// relationBackupColumns 回收站备份与恢复共用的关系列顺序
const relationBackupColumns = "hash, subject, predicate, object, vector_index, confidence, created_at, vector_state, vector_updated_at, vector_error, vector_retry_count, source_paragraph, metadata, is_permanent, last_accessed, access_count, last_access_reinforced_at, is_inactive, inactive_since, is_pinned, protected_until, last_reinforced, retention_strength, retention_anchor_at, next_lifecycle_at, reinforcement_count, lifecycle_revision, inactive_reason"

// GetRelationStatusBatch 批量读取关系生命周期状态
func (s *Store) GetRelationStatusBatch(hashes []string) (map[string]map[string]any, error) {
	normalized := normalizeHashSequence(hashes)
	result := make(map[string]map[string]any, len(normalized))
	if len(normalized) == 0 {
		return result, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`
		SELECT hash, is_inactive, confidence, is_pinned, protected_until,
		       last_reinforced, inactive_since, retention_strength, retention_anchor_at,
		       next_lifecycle_at, reinforcement_count, lifecycle_revision,
		       inactive_reason, last_access_reinforced_at
		FROM relations
		WHERE hash IN (%s)
		`, sqlPlaceholders(len(normalized)))
	rows, err := c.Query(query, stringArgs(normalized)...)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	for _, row := range items {
		result[stringFromRow(row, "hash")] = map[string]any{
			"is_inactive":               miscBoolValue(row["is_inactive"]),
			"confidence":                row["confidence"],
			"is_pinned":                 miscBoolValue(row["is_pinned"]),
			"protected_until":           row["protected_until"],
			"last_reinforced":           row["last_reinforced"],
			"inactive_since":            row["inactive_since"],
			"retention_strength":        row["retention_strength"],
			"retention_anchor_at":       row["retention_anchor_at"],
			"next_lifecycle_at":         row["next_lifecycle_at"],
			"reinforcement_count":       intFromRow(row, "reinforcement_count"),
			"lifecycle_revision":        intFromRow(row, "lifecycle_revision"),
			"inactive_reason":           stringFromRow(row, "inactive_reason"),
			"last_access_reinforced_at": row["last_access_reinforced_at"],
		}
	}
	return result, nil
}

// MarkRelationsActive 清除关系冻结标记并重置下一到期时间
func (s *Store) MarkRelationsActive(hashes []string) error {
	normalized := normalizeHashSequence(hashes)
	if len(normalized) == 0 {
		return nil
	}
	return s.Transaction(true, func(tx *Tx) error {
		query := fmt.Sprintf(`
			UPDATE relations
			SET is_inactive = 0,
			    inactive_since = NULL,
			    inactive_reason = NULL,
			    next_lifecycle_at = retention_anchor_at,
			    lifecycle_revision = COALESCE(lifecycle_revision, 0) + 1
			WHERE hash IN (%s)
			  AND (is_inactive = 1 OR inactive_since IS NOT NULL OR inactive_reason IS NOT NULL)
			`, sqlPlaceholders(len(normalized)))
		_, err := tx.Conn().Exec(query, stringArgs(normalized)...)
		return err
	})
}

// UpdateRelationsProtection 更新保护时间、置顶状态或上次强化时间
func (s *Store) UpdateRelationsProtection(hashes []string, protectedUntil *float64, isPinned *bool, lastReinforced *float64) error {
	normalized := normalizeHashSequence(hashes)
	if len(normalized) == 0 {
		return nil
	}
	setParts := make([]string, 0, 4)
	conditionParts := make([]string, 0, 6)
	setArgs := make([]any, 0, 4)
	conditionArgs := make([]any, 0, 6)
	if protectedUntil != nil {
		setParts = append(setParts, "protected_until = ?")
		conditionParts = append(conditionParts, "protected_until IS NOT ?")
		setArgs = append(setArgs, *protectedUntil)
		conditionArgs = append(conditionArgs, *protectedUntil)
	}
	if isPinned != nil {
		setParts = append(setParts,
			"is_pinned = ?",
			"next_lifecycle_at = CASE WHEN ? = 1 THEN NULL ELSE COALESCE(next_lifecycle_at, retention_anchor_at) END")
		conditionParts = append(conditionParts,
			"is_pinned IS NOT ?",
			"(? = 1 AND next_lifecycle_at IS NOT NULL)",
			"(? = 0 AND next_lifecycle_at IS NULL)")
		setArgs = append(setArgs, *isPinned, *isPinned)
		conditionArgs = append(conditionArgs, *isPinned, *isPinned, *isPinned)
	}
	if lastReinforced != nil {
		setParts = append(setParts, "last_reinforced = ?")
		conditionParts = append(conditionParts, "last_reinforced IS NOT ?")
		setArgs = append(setArgs, *lastReinforced)
		conditionArgs = append(conditionArgs, *lastReinforced)
	}
	if len(setParts) == 0 {
		return nil
	}
	setParts = append(setParts, "lifecycle_revision = COALESCE(lifecycle_revision, 0) + 1")
	whereSQL := fmt.Sprintf("hash IN (%s)", sqlPlaceholders(len(normalized)))
	if len(conditionParts) > 0 {
		whereSQL += " AND (" + strings.Join(conditionParts, " OR ") + ")"
	}
	query := fmt.Sprintf("UPDATE relations SET %s WHERE %s", strings.Join(setParts, ", "), whereSQL)
	params := make([]any, 0, len(setArgs)+len(normalized)+len(conditionArgs))
	params = append(params, setArgs...)
	params = append(params, stringArgs(normalized)...)
	params = append(params, conditionArgs...)
	return s.Transaction(true, func(tx *Tx) error {
		_, err := tx.Conn().Exec(query, params...)
		return err
	})
}

// GetPruneCandidates 返回冷冻期已过的关系 hash
func (s *Store) GetPruneCandidates(cutoffTime float64, limit int) ([]string, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	return miscQueryStrings(c, `
		SELECT hash FROM relations
		WHERE is_inactive = 1
		AND inactive_since < ?
		LIMIT ?
		`, cutoffTime, limit)
}

// BackupAndDeleteRelations 把关系写入回收站后物理删除
func (s *Store) BackupAndDeleteRelations(hashes []string) (int, error) {
	normalized := normalizeHashSequence(hashes)
	if len(normalized) == 0 {
		return 0, nil
	}
	deleted := 0
	err := s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		placeholders := sqlPlaceholders(len(normalized))
		insertSQL := fmt.Sprintf(`
			INSERT OR REPLACE INTO deleted_relations (%s, deleted_at)
			SELECT %s, ?
			FROM relations
			WHERE hash IN (%s)
			`, relationBackupColumns, relationBackupColumns, placeholders)
		insertArgs := make([]any, 0, len(normalized)+1)
		insertArgs = append(insertArgs, nowTimestamp())
		insertArgs = append(insertArgs, stringArgs(normalized)...)
		if _, err := c.Exec(insertSQL, insertArgs...); err != nil {
			return err
		}
		result, err := c.Exec(fmt.Sprintf("DELETE FROM relations WHERE hash IN (%s)", placeholders), stringArgs(normalized)...)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		deleted = int(affected)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// relationStatusRestoreMetadata 在事务内从回收站恢复关系元数据
func (s *Store) relationStatusRestoreMetadata(c *Conn, hashValue string) (bool, error) {
	rows, err := c.Query("SELECT * FROM deleted_relations WHERE hash = ?", hashValue)
	if err != nil {
		return false, err
	}
	if !rows.Next() {
		_ = rows.Close()
		return false, nil
	}
	data, err := scanRowToMap(rows)
	_ = rows.Close()
	if err != nil {
		return false, err
	}
	delete(data, "deleted_at")
	currentRevision := -1
	var current sql.NullInt64
	err = c.QueryRow("SELECT lifecycle_revision FROM relations WHERE hash = ?", hashValue).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if err == nil {
		currentRevision = 0
		if current.Valid {
			currentRevision = int(current.Int64)
		}
	}
	tombstoneRevision := 0
	if raw, ok := data["lifecycle_revision"]; ok && raw != nil {
		tombstoneRevision = int(cleanupInt64(raw))
	}
	data["lifecycle_revision"] = max(currentRevision, tombstoneRevision) + 1

	columns := make([]string, 0, len(data))
	for column := range data {
		columns = append(columns, column)
	}
	sort.Strings(columns)
	placeholders := make([]string, len(columns))
	updateParts := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for index, column := range columns {
		placeholders[index] = "?"
		args = append(args, data[column])
		if column != "hash" {
			updateParts = append(updateParts, fmt.Sprintf("%s = excluded.%s", column, column))
		}
	}
	insertSQL := fmt.Sprintf("INSERT INTO relations (%s) VALUES (%s) ON CONFLICT(hash) DO UPDATE SET %s",
		strings.Join(columns, ", "), strings.Join(placeholders, ", "), strings.Join(updateParts, ", "))
	if _, err := c.Exec(insertSQL, args...); err != nil {
		return false, err
	}
	if _, err := c.Exec("DELETE FROM deleted_relations WHERE hash = ?", hashValue); err != nil {
		return false, err
	}
	return true, nil
}

// RestoreRelationMetadata 从回收站恢复关系元数据，不存在时返回 nil
func (s *Store) RestoreRelationMetadata(hashValue string) (map[string]any, error) {
	token := strings.TrimSpace(hashValue)
	if token == "" {
		return nil, nil
	}
	restored := false
	err := s.Transaction(true, func(tx *Tx) error {
		ok, err := s.relationStatusRestoreMetadata(tx.Conn(), token)
		if err != nil {
			return err
		}
		restored = ok
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !restored {
		return nil, nil
	}
	return s.GetRelation(token, true)
}

// RestoreRelation 兼容旧调用名
func (s *Store) RestoreRelation(hashValue string) (map[string]any, error) {
	return s.RestoreRelationMetadata(hashValue)
}

// RestoreRelationStatusFromSnapshot 用状态快照覆盖关系生命周期字段
func (s *Store) RestoreRelationStatusFromSnapshot(hashValue string, snapshot map[string]any) (map[string]any, error) {
	token := strings.TrimSpace(hashValue)
	if token == "" || snapshot == nil {
		return nil, nil
	}
	restoredMissing := false
	err := s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		var current sql.NullInt64
		err := c.QueryRow("SELECT lifecycle_revision FROM relations WHERE hash = ?", token).Scan(&current)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == sql.ErrNoRows {
			ok, restoreErr := s.relationStatusRestoreMetadata(c, token)
			if restoreErr != nil {
				return restoreErr
			}
			if !ok {
				restoredMissing = true
				return nil
			}
			err = c.QueryRow("SELECT lifecycle_revision FROM relations WHERE hash = ?", token).Scan(&current)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
		}
		if err == sql.ErrNoRows {
			return fmt.Errorf("关系状态恢复后仍不存在: %s", token)
		}
		currentRevision := 0
		if current.Valid {
			currentRevision = int(current.Int64)
		}
		snapshotRevision := int(cleanupInt64(snapshot["lifecycle_revision"]))
		nextRevision := max(currentRevision, snapshotRevision) + 1

		strengthRaw, ok := snapshot["retention_strength"]
		if !ok || strengthRaw == nil {
			return fmt.Errorf("关系状态快照缺少 retention_strength: %s", token)
		}
		anchorRaw, ok := snapshot["retention_anchor_at"]
		if !ok || anchorRaw == nil {
			return fmt.Errorf("关系状态快照缺少 retention_anchor_at: %s", token)
		}
		reinforcementRaw, ok := snapshot["reinforcement_count"]
		if !ok || reinforcementRaw == nil {
			return fmt.Errorf("关系状态快照缺少 reinforcement_count: %s", token)
		}
		inactiveFlag := 0
		if miscBoolValue(snapshot["is_inactive"]) {
			inactiveFlag = 1
		}
		pinnedFlag := 0
		if miscBoolValue(snapshot["is_pinned"]) {
			pinnedFlag = 1
		}
		reason := strings.TrimSpace(stringFromRow(snapshot, "inactive_reason"))
		var reasonArg any
		if reason != "" {
			reasonArg = reason
		}
		_, err = c.Exec(`
			UPDATE relations
			SET is_inactive = ?,
			    confidence = ?,
			    is_pinned = ?,
			    protected_until = ?,
			    last_reinforced = ?,
			    inactive_since = ?,
			    last_access_reinforced_at = ?,
			    retention_strength = ?,
			    retention_anchor_at = ?,
			    next_lifecycle_at = ?,
			    reinforcement_count = ?,
			    lifecycle_revision = ?,
			    inactive_reason = ?
			WHERE hash = ?
			`,
			inactiveFlag,
			cleanupFloatValue(snapshot["confidence"]),
			pinnedFlag,
			miscNullableFloat64(floatPtrFromRow(snapshot, "protected_until")),
			miscNullableFloat64(floatPtrFromRow(snapshot, "last_reinforced")),
			miscNullableFloat64(floatPtrFromRow(snapshot, "inactive_since")),
			miscNullableFloat64(floatPtrFromRow(snapshot, "last_access_reinforced_at")),
			cleanupFloatValue(strengthRaw),
			cleanupFloatValue(anchorRaw),
			miscNullableFloat64(floatPtrFromRow(snapshot, "next_lifecycle_at")),
			int(cleanupInt64(reinforcementRaw)),
			nextRevision,
			reasonArg,
			token,
		)
		return err
	})
	if err != nil {
		return nil, err
	}
	if restoredMissing {
		return nil, nil
	}
	batch, err := s.GetRelationStatusBatch([]string{token})
	if err != nil {
		return nil, err
	}
	return batch[token], nil
}

// GetProtectedRelationsHashes 返回置顶或仍在保护期内的关系 hash
func (s *Store) GetProtectedRelationsHashes() ([]string, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	return miscQueryStrings(c, `
		SELECT hash FROM relations
		WHERE is_pinned = 1 OR protected_until > ?
		`, nowTimestamp())
}

// relationStatusDecodeDeletedRow 解码回收站行的 metadata 字段
func (s *Store) relationStatusDecodeDeletedRow(row map[string]any) map[string]any {
	if row == nil {
		return nil
	}
	raw, ok := row["metadata"]
	if !ok || raw == nil {
		return row
	}
	if text, isText := raw.(string); isText {
		if text == "" {
			return row
		}
		row["metadata"] = decodeMetadata(text)
	}
	return row
}

// GetDeletedRelations 按删除时间倒序读取回收站关系
func (s *Store) GetDeletedRelations(limit int) ([]map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query("SELECT * FROM deleted_relations ORDER BY deleted_at DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index] = s.relationStatusDecodeDeletedRow(items[index])
	}
	return items, nil
}

// GetDeletedRelation 读取单条回收站关系
func (s *Store) GetDeletedRelation(hashValue string) (map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query("SELECT * FROM deleted_relations WHERE hash = ?", hashValue)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return nil, nil
	}
	row, err := scanRowToMap(rows)
	if err != nil {
		return nil, err
	}
	return s.relationStatusDecodeDeletedRow(row), nil
}

// ReinforceRelations 按关系生命周期策略执行显式强化
func (s *Store) ReinforceRelations(hashes []string, policy utils.RelationLifecyclePolicy, now float64, strength float64) ([]map[string]any, error) {
	return s.ApplyRelationLifecycleEvent(hashes, utils.LifecycleEventReinforce, policy, now, strength)
}

// MarkRelationsInactive 把关系标记为冻结并保留既有冻结起点
func (s *Store) MarkRelationsInactive(hashes []string, inactiveSince *float64, reason string) error {
	normalized := normalizeHashSequence(hashes)
	if len(normalized) == 0 {
		return nil
	}
	markTime := nowTimestamp()
	if inactiveSince != nil {
		markTime = *inactiveSince
	}
	return s.Transaction(true, func(tx *Tx) error {
		for _, batch := range iterSQLBatches(normalized, 500) {
			query := fmt.Sprintf(`
				UPDATE relations
				SET is_inactive = 1,
				    inactive_since = CASE
				        WHEN is_inactive = 1 THEN inactive_since
				        ELSE MAX(
				            COALESCE(retention_anchor_at, ?),
				            COALESCE(last_accessed, ?),
				            COALESCE(last_reinforced, ?),
				            COALESCE(last_access_reinforced_at, ?),
				            ?
				        )
				    END,
				    inactive_reason = ?,
				    next_lifecycle_at = NULL,
				    lifecycle_revision = COALESCE(lifecycle_revision, 0) + 1
				WHERE hash IN (%s)
				  AND (is_inactive = 0 OR inactive_reason IS NOT ?)
				`, sqlPlaceholders(len(batch)))
			args := make([]any, 0, 7+len(batch))
			for i := 0; i < 5; i++ {
				args = append(args, markTime)
			}
			args = append(args, reason)
			args = append(args, stringArgs(batch)...)
			args = append(args, reason)
			if _, err := tx.Conn().Exec(query, args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// ProtectRelations 设置关系置顶并可选设置保护期
func (s *Store) ProtectRelations(hashes []string, isPinned bool, ttlSeconds float64) error {
	normalized := normalizeHashSequence(hashes)
	if len(normalized) == 0 {
		return nil
	}
	protectedUntil := 0.0
	if ttlSeconds > 0 {
		protectedUntil = nowTimestamp() + ttlSeconds
	}
	pinned := isPinned
	return s.UpdateRelationsProtection(normalized, &protectedUntil, &pinned, nil)
}

// Vacuum 执行 SQLite 空间整理
func (s *Store) Vacuum() error {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return err
	}
	if _, err := c.Exec("VACUUM"); err != nil {
		return err
	}
	if err := c.Commit(); err != nil {
		return err
	}
	zap.L().Info("数据库优化完成")
	return nil
}
