package storage

// A_memorix 向量状态 / 永久性 / 访问记录，对齐 Python metadata_store.py

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// vectorStateValues 关系向量状态允许值
var vectorStateValues = map[string]struct{}{
	"none": {}, "pending": {}, "ready": {}, "failed": {},
}

// SetRelationVectorState 更新关系向量状态
func (s *Store) SetRelationVectorState(hashValue string, state string, errText *string, bumpRetry bool) (bool, error) {
	stateNorm := strings.ToLower(strings.TrimSpace(state))
	if _, ok := vectorStateValues[stateNorm]; !ok {
		return false, fmt.Errorf("无效 vector_state: %s", state)
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return false, err
	}
	now := float64(time.Now().UnixNano()) / 1e9
	var storedError any
	if errText != nil {
		trimmed := strings.TrimSpace(*errText)
		if trimmed != "" {
			trimmed = truncateRunes(trimmed, 500)
			storedError = trimmed
		}
	}
	if stateNorm == "none" || stateNorm == "pending" || stateNorm == "ready" {
		storedError = nil
	}

	var execResult sql.Result
	if bumpRetry {
		execResult, err = c.Exec(`UPDATE relations
			SET vector_state = ?,
				vector_updated_at = ?,
				vector_error = ?,
				vector_retry_count = COALESCE(vector_retry_count, 0) + 1
			WHERE hash = ?`, stateNorm, now, storedError, hashValue)
	} else {
		execResult, err = c.Exec(`UPDATE relations
			SET vector_state = ?,
				vector_updated_at = ?,
				vector_error = ?
			WHERE hash = ?`, stateNorm, now, storedError, hashValue)
	}
	if err != nil {
		return false, err
	}
	if err := c.Commit(); err != nil {
		return false, err
	}
	affected, err := rowsAffected(execResult)
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// ResetVectorProjectionState 切换向量世代时重置派生状态
func (s *Store) ResetVectorProjectionState() (map[string]int, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	result := map[string]int{}
	resetExec, err := c.Exec(`UPDATE relations
		SET vector_state = 'none',
			vector_updated_at = NULL,
			vector_error = NULL,
			vector_retry_count = 0`)
	if err != nil {
		return nil, err
	}
	affected, err := rowsAffected(resetExec)
	if err != nil {
		return nil, err
	}
	result["relations_reset"] = affected
	clearedExec, err := c.Exec("DELETE FROM paragraph_vector_backfill")
	if err != nil {
		return nil, err
	}
	cleared, err := rowsAffected(clearedExec)
	if err != nil {
		return nil, err
	}
	result["paragraph_backfill_cleared"] = cleared
	if err := c.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// ListRelationsByVectorState 按向量状态列出关系
func (s *Store) ListRelationsByVectorState(states []string, limit int, maxRetry *int) ([]map[string]any, error) {
	normalized := make([]string, 0, len(states))
	for _, item := range states {
		token := strings.ToLower(strings.TrimSpace(item))
		if token == "" {
			continue
		}
		if _, ok := vectorStateValues[token]; !ok {
			continue
		}
		normalized = append(normalized, token)
	}
	if len(normalized) == 0 {
		return []map[string]any{}, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	params := make([]any, 0, len(normalized)+2)
	for _, token := range normalized {
		params = append(params, token)
	}
	query := fmt.Sprintf(`SELECT hash, subject, predicate, object, confidence, source_paragraph,
			vector_state, vector_updated_at, vector_error, vector_retry_count, created_at
		FROM relations
		WHERE vector_state IN (%s)`, sqlPlaceholders(len(normalized)))
	if maxRetry != nil {
		query += " AND COALESCE(vector_retry_count, 0) < ?"
		params = append(params, *maxRetry)
	}
	query += " ORDER BY COALESCE(vector_updated_at, created_at, 0) ASC LIMIT ?"
	effectiveLimit := limit
	if effectiveLimit < 1 {
		effectiveLimit = 1
	}
	params = append(params, effectiveLimit)
	rows, err := c.Query(query, params...)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, row := range items {
		out = append(out, s.rowToDict(row))
	}
	return out, nil
}

// CountRelationsByVectorState 统计关系向量状态分布
func (s *Store) CountRelationsByVectorState() (map[string]int, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(`SELECT COALESCE(vector_state, 'none') AS state, COUNT(*) AS cnt
		FROM relations
		GROUP BY COALESCE(vector_state, 'none')`)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	result := map[string]int{"none": 0, "pending": 0, "ready": 0, "failed": 0}
	total := 0
	for _, row := range items {
		state := strings.ToLower(stringFromRow(row, "state"))
		if state == "" {
			state = "none"
		}
		count := intFromRow(row, "cnt")
		result[state] += count
		total += count
	}
	result["total"] = total
	return result, nil
}

// UpdateVectorIndex 更新向量索引
func (s *Store) UpdateVectorIndex(itemType string, hashValue string, vectorIndex int) (bool, error) {
	table, ok := map[string]string{
		"paragraph": "paragraphs",
		"entity":    "entities",
		"relation":  "relations",
	}[itemType]
	if !ok {
		return false, fmt.Errorf("无效的类型: %s", itemType)
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return false, err
	}
	execResult, err := c.Exec(fmt.Sprintf("UPDATE %s SET vector_index = ? WHERE hash = ?", table), vectorIndex, hashValue)
	if err != nil {
		return false, err
	}
	if err := c.Commit(); err != nil {
		return false, err
	}
	affected, err := rowsAffected(execResult)
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// SetPermanence 设置永久记忆标记
func (s *Store) SetPermanence(hashValue string, itemType string, isPermanent bool) (bool, error) {
	if itemType != "paragraph" && itemType != "relation" {
		return false, fmt.Errorf("类型 %s 不支持设置永久性", itemType)
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return false, err
	}
	permanentValue := 0
	if isPermanent {
		permanentValue = 1
	}
	var execResult sql.Result
	if itemType == "paragraph" {
		execResult, err = c.Exec(`UPDATE paragraphs
			SET is_permanent = ?,
				expires_at = CASE WHEN ? = 1 THEN NULL ELSE expires_at END,
				deletion_reason = CASE WHEN ? = 1 THEN NULL ELSE deletion_reason END
			WHERE hash = ?`, permanentValue, permanentValue, permanentValue, hashValue)
		if err != nil {
			return false, err
		}
	} else {
		execResult, err = c.Exec(`UPDATE relations
			SET is_permanent = ?,
				is_pinned = ?,
				next_lifecycle_at = CASE WHEN ? = 1 THEN NULL ELSE retention_anchor_at END,
				lifecycle_revision = COALESCE(lifecycle_revision, 0) + 1
			WHERE hash = ?
			  AND (
				is_permanent IS NOT ?
				OR is_pinned IS NOT ?
				OR (? = 1 AND next_lifecycle_at IS NOT NULL)
				OR (? = 0 AND next_lifecycle_at IS NOT retention_anchor_at)
			  )`, permanentValue, permanentValue, permanentValue, hashValue,
			permanentValue, permanentValue, permanentValue, permanentValue)
		if err != nil {
			return false, err
		}
	}
	if err := c.Commit(); err != nil {
		return false, err
	}
	affected, err := rowsAffected(execResult)
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// RecordAccess 记录访问（更新时间和次数）
func (s *Store) RecordAccess(hashValue string, itemType string) (bool, error) {
	if itemType != "paragraph" && itemType != "relation" {
		return false, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return false, err
	}
	now := float64(time.Now().UnixNano()) / 1e9
	table := "paragraphs"
	if itemType == "relation" {
		table = "relations"
	}
	execResult, err := c.Exec(fmt.Sprintf(`UPDATE %s
		SET last_accessed = MAX(COALESCE(last_accessed, ?), ?),
			access_count = access_count + 1
		WHERE hash = ?`, table), now, now, hashValue)
	if err != nil {
		return false, err
	}
	if err := c.Commit(); err != nil {
		return false, err
	}
	affected, err := rowsAffected(execResult)
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// Query 执行自定义查询，对齐 Python Store.query
func (s *Store) Query(sql string, params ...any) ([]map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(sql, params...)
	if err != nil {
		return nil, err
	}
	return scanRowsToMaps(rows)
}

// rowsAffected 读取执行影响行数
func rowsAffected(result sql.Result) (int, error) {
	if result == nil {
		return 0, nil
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}
