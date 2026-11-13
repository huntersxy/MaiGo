package storage

// A_memorix 实体 GC、软删除扫描、物理清理与连接状态，对齐 Python metadata_store.py L4130-L4597

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"maibot-go/internal/A_memorix/core/utils"
)

// SweepDeletedItem 对齐 Python sweep_deleted_items 返回的 (hash, name) 元组
type SweepDeletedItem struct {
	Hash string
	Name string
}

// GetEntityGCCandidates 返回已过保护期且不再被活跃段落引用的孤立实体
func (s *Store) GetEntityGCCandidates(isolatedHashes []string, retentionSeconds float64) ([]string, error) {
	if len(isolatedHashes) == 0 {
		return []string{}, nil
	}
	normalized := make([]string, 0, len(isolatedHashes))
	seen := make(map[string]struct{}, len(isolatedHashes))
	for _, item := range isolatedHashes {
		token := strings.TrimSpace(item)
		if token == "" {
			continue
		}
		hashValue := ""
		if len(token) == 64 && miscIsHexToken(token) {
			hashValue = strings.ToLower(token)
		} else {
			canon := canonicalizeName(token)
			if canon == "" {
				continue
			}
			computed, err := utils.ComputeHash(canon)
			if err != nil {
				return nil, err
			}
			hashValue = computed
		}
		if _, ok := seen[hashValue]; ok {
			continue
		}
		seen[hashValue] = struct{}{}
		normalized = append(normalized, hashValue)
	}
	if len(normalized) == 0 {
		return []string{}, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	cutoff := nowTimestamp() - retentionSeconds
	candidates := make([]string, 0, len(normalized))
	for _, batch := range iterSQLBatches(normalized, 900) {
		query := fmt.Sprintf(`
			SELECT e.hash FROM entities e
			WHERE e.hash IN (%s)
			AND e.is_deleted = 0
			AND (e.created_at IS NULL OR e.created_at < ?)
			AND NOT EXISTS (
				SELECT 1 FROM paragraph_entities pe
				JOIN paragraphs p ON pe.paragraph_hash = p.hash
				WHERE pe.entity_hash = e.hash
				AND p.is_deleted = 0
			)
			`, sqlPlaceholders(len(batch)))
		args := append(stringArgs(batch), cutoff)
		values, err := miscQueryStrings(c, query, args...)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, values...)
	}
	return candidates, nil
}

// MarkAsDeleted 标记实体或段落为软删除
func (s *Store) MarkAsDeleted(hashes []string, typeName string, reason string) (int, error) {
	if len(hashes) == 0 {
		return 0, nil
	}
	normalized := normalizeHashSequence(hashes)
	isParagraph := typeName == "paragraph"
	table := "paragraphs"
	if typeName == "entity" {
		table = "entities"
	}
	reasonToken := strings.TrimSpace(reason)
	if isParagraph && reasonToken == "" {
		return 0, fmt.Errorf("软删除段落必须提供明确的 reason")
	}
	count := 0
	err := s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		now := nowTimestamp()
		touchedSources := []string{}
		if isParagraph {
			touchedSources = s.GetSourcesForParagraphHashes(normalized, true, c)
		}
		for _, batch := range iterSQLBatches(normalized, 900) {
			placeholders := sqlPlaceholders(len(batch))
			var result sql.Result
			if isParagraph {
				res, err := c.Exec(fmt.Sprintf(`
					UPDATE paragraphs
					SET is_deleted = 1, deleted_at = ?, deletion_reason = ?
					WHERE is_deleted = 0 AND hash IN (%s)
					`, placeholders), append([]any{now, reasonToken}, stringArgs(batch)...)...)
				if err != nil {
					return err
				}
				result = res
			} else {
				res, err := c.Exec(fmt.Sprintf(`
					UPDATE %s
					SET is_deleted = 1, deleted_at = ?
					WHERE is_deleted = 0 AND hash IN (%s)
					`, table, placeholders), append([]any{now}, stringArgs(batch)...)...)
				if err != nil {
					return err
				}
				result = res
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			changed := int(affected)
			count += changed
			if isParagraph && changed > 0 {
				s.DeleteParagraphNgramsIfReady(batch, -changed, c)
				for _, paragraphHash := range batch {
					s.FtsDeleteTokenizedParagraph(paragraphHash, c)
				}
			}
		}
		if isParagraph && count > 0 && len(touchedSources) > 0 {
			s.EnqueueEpisodeSourceRebuilds(touchedSources, "paragraph_soft_deleted", EpisodeEnqueueOptions{Conn: c})
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if count > 0 {
		zap.L().Info("软删除标记", zap.String("table", table), zap.Int("count", count))
	}
	return count, nil
}

// SweepDeletedItems 返回已过宽限期的软删除项
func (s *Store) SweepDeletedItems(typeName string, gracePeriodSeconds float64) ([]SweepDeletedItem, error) {
	table := "paragraphs"
	columns := "hash, '' as name"
	if typeName == "entity" {
		table = "entities"
		columns = "hash, name"
	}
	cutoff := nowTimestamp() - gracePeriodSeconds
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(fmt.Sprintf(`
		SELECT %s FROM %s
		WHERE is_deleted = 1
		AND deleted_at < ?
		`, columns, table), cutoff)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]SweepDeletedItem, 0, 16)
	for rows.Next() {
		var item SweepDeletedItem
		if err := rows.Scan(&item.Hash, &item.Name); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// PhysicallyDeleteEntities 物理删除实体并触发来源重建
func (s *Store) PhysicallyDeleteEntities(hashes []string) (int, error) {
	if len(hashes) == 0 {
		return 0, nil
	}
	normalized := normalizeHashSequence(hashes)
	count := 0
	err := s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		touchedSources := []string{}
		for _, batch := range iterSQLBatches(normalized, 900) {
			query := fmt.Sprintf(`
				SELECT DISTINCT p.source
				FROM paragraph_entities pe
				JOIN paragraphs p ON p.hash = pe.paragraph_hash
				WHERE pe.entity_hash IN (%s)
				  AND p.source IS NOT NULL AND TRIM(p.source) != ''
				  AND (p.is_deleted IS NULL OR p.is_deleted = 0)
				`, sqlPlaceholders(len(batch)))
			values, err := miscQueryStrings(c, query, stringArgs(batch)...)
			if err != nil {
				return err
			}
			for _, value := range values {
				touchedSources = append(touchedSources, strings.TrimSpace(value))
			}
			result, err := c.Exec(fmt.Sprintf("DELETE FROM entities WHERE hash IN (%s)", sqlPlaceholders(len(batch))), stringArgs(batch)...)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			count += int(affected)
		}
		if count > 0 && len(touchedSources) > 0 {
			s.EnqueueEpisodeSourceRebuilds(touchedSources, "entity_physically_deleted", EpisodeEnqueueOptions{Conn: c})
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// PhysicallyDeleteParagraphs 物理删除段落、解绑事实证据并清理检索索引
func (s *Store) PhysicallyDeleteParagraphs(hashes []string) (int, error) {
	if len(hashes) == 0 {
		return 0, nil
	}
	normalized := normalizeHashSequence(hashes)
	count := 0
	err := s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		touchedSources := s.GetSourcesForParagraphHashes(normalized, true, c)
		if _, _, err := s.miscDetachFactEvidenceForParagraphs(normalized, "paragraph_physically_deleted", c, nil); err != nil {
			return err
		}
		activeDeleteCount := 0
		for _, batch := range iterSQLBatches(normalized, 900) {
			query := fmt.Sprintf(`
				SELECT hash FROM paragraphs
				WHERE (is_deleted IS NULL OR is_deleted = 0)
				  AND hash IN (%s)
				`, sqlPlaceholders(len(batch)))
			values, err := miscQueryStrings(c, query, stringArgs(batch)...)
			if err != nil {
				return err
			}
			activeDeleteCount += len(values)
		}
		s.DeleteParagraphNgramsIfReady(normalized, -activeDeleteCount, c)
		for _, paragraphHash := range normalized {
			s.FtsDeleteTokenizedParagraph(paragraphHash, c)
		}
		for _, batch := range iterSQLBatches(normalized, 900) {
			result, err := c.Exec(fmt.Sprintf("DELETE FROM paragraphs WHERE hash IN (%s)", sqlPlaceholders(len(batch))), stringArgs(batch)...)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			count += int(affected)
		}
		if count > 0 {
			if err := s.refreshParagraphTokenizedFTSMeta(c); err != nil {
				return err
			}
		}
		if count > 0 && len(touchedSources) > 0 {
			s.EnqueueEpisodeSourceRebuilds(touchedSources, "paragraph_physically_deleted", EpisodeEnqueueOptions{Conn: c})
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// GetEntityStatusBatch 批量读取实体软删除状态
func (s *Store) GetEntityStatusBatch(hashes []string) (map[string]map[string]any, error) {
	normalized := normalizeHashSequence(hashes)
	result := make(map[string]map[string]any, len(normalized))
	if len(normalized) == 0 {
		return result, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	for _, batch := range iterSQLBatches(normalized, 900) {
		query := fmt.Sprintf("SELECT hash, is_deleted, deleted_at FROM entities WHERE hash IN (%s)", sqlPlaceholders(len(batch)))
		rows, err := c.Query(query, stringArgs(batch)...)
		if err != nil {
			return nil, err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return nil, err
		}
		for _, row := range items {
			result[stringFromRow(row, "hash")] = map[string]any{
				"is_deleted": miscBoolValue(row["is_deleted"]),
				"deleted_at": row["deleted_at"],
			}
		}
	}
	return result, nil
}

// GetDeletedEntities 按删除时间倒序读取回收站实体
func (s *Store) GetDeletedEntities(limit int) ([]map[string]any, error) {
	if !s.HasTable("entities", nil) {
		return []map[string]any{}, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(`
		SELECT hash, name, deleted_at
		FROM entities
		WHERE is_deleted = 1
		ORDER BY deleted_at DESC
		LIMIT ?
		`, limit)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(items))
	for _, row := range items {
		result = append(result, map[string]any{
			"hash":       stringFromRow(row, "hash"),
			"name":       stringFromRow(row, "name"),
			"type":       "entity",
			"deleted_at": row["deleted_at"],
		})
	}
	return result, nil
}

// HasData 检查磁盘上是否已存在数据库文件
func (s *Store) HasData() bool {
	dataDir := s.DataDir()
	if dataDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dataDir, s.DBName()))
	return err == nil
}

// IsConnected 报告存储是否已建立连接
func (s *Store) IsConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.override != nil {
		return true
	}
	return s.manager != nil && !s.manager.Closed()
}

// String 对齐 Python __repr__
func (s *Store) String() string {
	stats := map[string]int{}
	if s.IsConnected() {
		if loaded, err := s.GetStatistics(); err == nil {
			stats = loaded
		}
	}
	return fmt.Sprintf("MetadataStore(paragraphs=%d, entities=%d, relations=%d)",
		stats["paragraph_count"], stats["entity_count"], stats["relation_count"])
}
