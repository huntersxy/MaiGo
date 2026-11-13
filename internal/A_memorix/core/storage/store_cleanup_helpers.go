package storage

// A_memorix 查询助手、别名解析、软删除恢复与时间回填，对齐 Python metadata_store.py L2640-L3005、L4130-L4240

import (
	"database/sql"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// cleanupAllowedHashTables list_hashes 允许查询的表名
var cleanupAllowedHashTables = map[string]struct{}{
	"paragraphs":        {},
	"entities":          {},
	"relations":         {},
	"deleted_relations": {},
}

// miscIsHexToken 判断字符串是否全为十六进制字符
func miscIsHexToken(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F') {
			continue
		}
		return false
	}
	return true
}

// IsEntityStillReferenced 判断实体是否仍被段落关联或关系端点引用
func (s *Store) IsEntityStillReferenced(entityHash string, entityName string) (bool, error) {
	tokenHash := strings.TrimSpace(entityHash)
	c, err := s.ResolveConn(nil)
	if err != nil {
		return false, err
	}
	if tokenHash != "" {
		rows, err := c.Query("SELECT 1 FROM paragraph_entities WHERE entity_hash = ? LIMIT 1", tokenHash)
		if err != nil {
			return false, err
		}
		defer func() { _ = rows.Close() }()
		if rows.Next() {
			return true, nil
		}
		if err := rows.Err(); err != nil {
			return false, err
		}
	}
	canonName := canonicalizeName(entityName)
	if canonName == "" {
		return false, nil
	}
	rows, err := c.Query(`
		SELECT 1 FROM relations
		WHERE LOWER(TRIM(subject)) = ? OR LOWER(TRIM(object)) = ?
		LIMIT 1
		`, canonName, canonName)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	return rows.Next(), nil
}

// SearchRelationsBySubjectOrObject 按 subject/object 模糊查询关系
func (s *Store) SearchRelationsBySubjectOrObject(query string, limit int, includeDeleted bool) ([]map[string]any, error) {
	token := strings.TrimSpace(query)
	if token == "" {
		return []map[string]any{}, nil
	}
	maxLimit := max(1, limit)
	pattern := "%" + token + "%"
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	load := func(table string) ([]map[string]any, error) {
		sqlText := fmt.Sprintf("SELECT * FROM %s WHERE subject LIKE ? OR object LIKE ? LIMIT ?", table)
		rows, err := c.Query(sqlText, pattern, pattern, maxLimit)
		if err != nil {
			return nil, err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return nil, err
		}
		for index := range items {
			items[index] = s.rowToDict(items[index])
		}
		return items, nil
	}
	items, err := load("relations")
	if err != nil {
		return nil, err
	}
	if len(items) > 0 || !includeDeleted {
		return items, nil
	}
	return load("deleted_relations")
}

// ListHashes 安全枚举指定表的 hash 列
func (s *Store) ListHashes(table string) ([]string, error) {
	token := strings.ToLower(strings.TrimSpace(table))
	if _, ok := cleanupAllowedHashTables[token]; !ok {
		return nil, fmt.Errorf("unsupported table for list_hashes: %s", table)
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	return miscQueryStrings(c, fmt.Sprintf("SELECT hash FROM %s", token))
}

// GetOrphanDeletedRelationHashes 返回回收站中已不在关系表的孤儿 hash
func (s *Store) GetOrphanDeletedRelationHashes(limit int) ([]string, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	return miscQueryStrings(c, `
		SELECT d.hash
		FROM deleted_relations d
		LEFT JOIN relations r ON r.hash = d.hash
		WHERE r.hash IS NULL
		LIMIT ?
		`, max(1, limit))
}

// ResolveRelationHashAlias 解析 64 位关系 hash 或 32 位别名
func (s *Store) ResolveRelationHashAlias(value string, includeDeleted bool) ([]string, error) {
	token := strings.ToLower(strings.TrimSpace(value))
	if token == "" {
		return []string{}, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	if len(token) == 64 && miscIsHexToken(token) {
		var marker int
		err := c.QueryRow("SELECT 1 FROM relations WHERE hash = ? LIMIT 1", token).Scan(&marker)
		if err == nil {
			return []string{token}, nil
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
		if includeDeleted {
			err = c.QueryRow("SELECT 1 FROM deleted_relations WHERE hash = ? LIMIT 1", token).Scan(&marker)
			if err == nil {
				return []string{token}, nil
			}
			if err != sql.ErrNoRows {
				return nil, err
			}
		}
		return []string{}, nil
	}
	if len(token) != 32 || !miscIsHexToken(token) {
		return []string{}, nil
	}
	var resolved string
	err = c.QueryRow("SELECT hash FROM relation_hash_aliases WHERE alias32 = ?", token).Scan(&resolved)
	if err == sql.ErrNoRows {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return []string{resolved}, nil
}

// SearchRelationHashesByText 按关系内容模糊查询 hash
func (s *Store) SearchRelationHashesByText(query string, limit int) ([]string, error) {
	return s.cleanupSearchHashesByText(query, limit, "relations")
}

// SearchDeletedRelationHashesByText 按回收站关系内容模糊查询 hash
func (s *Store) SearchDeletedRelationHashesByText(query string, limit int) ([]string, error) {
	return s.cleanupSearchHashesByText(query, limit, "deleted_relations")
}

// cleanupSearchHashesByText 统一的 subject/object 文本 hash 检索
func (s *Store) cleanupSearchHashesByText(query string, limit int, table string) ([]string, error) {
	token := strings.TrimSpace(query)
	if token == "" {
		return []string{}, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	pattern := "%" + token + "%"
	return miscQueryStrings(c, fmt.Sprintf(`
		SELECT hash FROM %s
		WHERE subject LIKE ? OR object LIKE ?
		LIMIT ?
		`, table), pattern, pattern, max(1, limit))
}

// RestoreEntityByHash 恢复软删除实体
func (s *Store) RestoreEntityByHash(entityHash string) (bool, error) {
	changed := false
	err := s.Transaction(true, func(tx *Tx) error {
		result, err := tx.Conn().Exec("UPDATE entities SET is_deleted = 0, deleted_at = NULL WHERE hash = ?", entityHash)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		changed = affected > 0
		return nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// RestoreParagraphByHash 恢复软删除段落并重建检索索引
func (s *Store) RestoreParagraphByHash(paragraphHash string) (bool, error) {
	changed := false
	err := s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		var content sql.NullString
		rowErr := c.QueryRow("SELECT content FROM paragraphs WHERE hash = ? AND is_deleted = 1", paragraphHash).Scan(&content)
		if rowErr != nil && rowErr != sql.ErrNoRows {
			return rowErr
		}
		result, err := c.Exec(`
			UPDATE paragraphs
			SET is_deleted = 0, deleted_at = NULL, expires_at = NULL, deletion_reason = NULL
			WHERE hash = ? AND is_deleted = 1
			`, paragraphHash)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		changed = affected > 0 && rowErr == nil
		if !changed {
			return nil
		}
		s.UpsertParagraphNgramIfReady(paragraphHash, content.String, 1, c)
		s.FtsUpsertTokenizedParagraph(paragraphHash, c)
		return c.Commit()
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// BackfillTemporalMetadataFromCreatedAt 用 created_at 回填段落事件时间
func (s *Store) BackfillTemporalMetadataFromCreatedAt(limit int, dryRun bool, noCreatedFallback bool) (map[string]int, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(`
		SELECT hash, created_at, source
		FROM paragraphs
		WHERE (event_time IS NULL AND event_time_start IS NULL AND event_time_end IS NULL)
		ORDER BY created_at DESC
		LIMIT ?
		`, max(1, limit))
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	result := map[string]int{"candidates": len(items), "updated": 0}
	if dryRun || noCreatedFallback {
		return result, nil
	}
	updated := 0
	touchedSources := make([]string, 0, len(items))
	err = s.Transaction(true, func(tx *Tx) error {
		transactionConn := tx.Conn()
		for _, row := range items {
			createdAt := floatPtrFromRow(row, "created_at")
			if createdAt == nil {
				continue
			}
			res, err := transactionConn.Exec(`
				UPDATE paragraphs
				SET event_time = ?, time_granularity = ?, time_confidence = ?, updated_at = ?
				WHERE hash = ?
				`, *createdAt, "day", 0.2, *createdAt, stringFromRow(row, "hash"))
			if err != nil {
				return err
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if affected > 0 {
				updated++
				touchedSources = append(touchedSources, stringFromRow(row, "source"))
			}
		}
		if updated > 0 {
			s.EnqueueEpisodeSourceRebuilds(touchedSources, "paragraph_time_backfill", EpisodeEnqueueOptions{Conn: transactionConn})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result["updated"] = updated
	return result, nil
}

// ClearAll 按依赖顺序清空所有元数据表
func (s *Store) ClearAll() error {
	tables := []string{
		"relation_graph_projection_jobs",
		"paragraphs",
		"entities",
		"relations",
		"paragraph_relations",
		"paragraph_entities",
		"episodes",
		"episode_paragraphs",
		"episode_rebuild_sources",
		"paragraph_vector_backfill",
		"memory_feedback_tasks",
		"memory_feedback_action_logs",
		"paragraph_stale_relation_marks",
		"person_profile_refresh_queue",
		"person_profile_alias_overrides",
		"person_identity_aliases",
		"person_identities",
		"fact_transitions",
		"fact_evidence",
		"fact_claims",
		"storage_cleanup_jobs",
	}
	if err := s.Transaction(true, func(tx *Tx) error {
		for _, table := range tables {
			if _, err := tx.Conn().Exec("DELETE FROM " + table); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	zap.L().Info("元数据存储所有表已清空")
	return nil
}

// UpdateRelationTimestamp 更新关系访问时间与计数
func (s *Store) UpdateRelationTimestamp(hashValue string, accessCountDelta int) error {
	now := nowTimestamp()
	return s.Transaction(true, func(tx *Tx) error {
		_, err := tx.Conn().Exec(`
			UPDATE relations
			SET last_accessed = MAX(COALESCE(last_accessed, ?), ?),
			    access_count = access_count + ?
			WHERE hash = ?
			`, now, now, accessCountDelta, hashValue)
		return err
	})
}
