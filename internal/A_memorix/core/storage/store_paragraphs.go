package storage

// A_memorix 段落写入与读取，对齐 Python core/storage/metadata_store.py

import (
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"MaiGo/internal/A_memorix/core/utils"
)

// AddParagraph 添加段落，对齐 Python add_paragraph
func (s *Store) AddParagraph(content string, vectorIndex *int, source string, metadata map[string]any, knowledgeType string, timeMeta map[string]any) (string, error) {
	contentNormalized := utils.NormalizeText(content)
	hashValue, err := utils.ComputeHash(contentNormalized)
	if err != nil {
		return "", err
	}
	resolvedType, err := ValidateStoredKnowledgeType(knowledgeType)
	if err != nil {
		return "", err
	}
	now := nowTimestamp()
	wordCount := len(strings.Fields(contentNormalized))
	normalizedTime, err := utils.NormalizeTimeMeta(timeMeta)
	if err != nil {
		return "", err
	}
	confidence := any(1.0)
	if value, ok := normalizedTime["time_confidence"]; ok {
		confidence = value
	}

	insertErr := s.Transaction(true, func(tx *Tx) error {
		if _, err := tx.Exec(`INSERT INTO paragraphs
			(
				hash, content, vector_index, created_at, updated_at, metadata, source, word_count,
				event_time, event_time_start, event_time_end, time_granularity, time_confidence,
				knowledge_type
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			hashValue,
			content,
			optionalInt(vectorIndex),
			now,
			now,
			encodeMetadata(metadata),
			optionalString(source),
			wordCount,
			normalizedTime["event_time"],
			normalizedTime["event_time_start"],
			normalizedTime["event_time_end"],
			normalizedTime["time_granularity"],
			confidence,
			string(resolvedType),
		); err != nil {
			return err
		}
		s.UpsertParagraphNgramIfReady(hashValue, content, 1, tx.Conn())
		s.FtsUpsertTokenizedParagraph(hashValue, tx.Conn())
		if strings.TrimSpace(source) != "" {
			s.EnqueueEpisodeSourceRebuild(source, "paragraph_added", EpisodeEnqueueOptions{Conn: tx.Conn()})
		}
		return nil
	})
	if insertErr != nil {
		if !isIntegrityError(insertErr) {
			return "", insertErr
		}
		zap.L().Debug("段落已存在", zap.String("hash", truncateRunes(hashValue, 16)))
		if len(metadata) > 0 {
			if _, err := s.MergeExistingParagraphMetadata(hashValue, metadata, nil); err != nil {
				return "", err
			}
		}
		if _, err := s.ReviveIfDeleted(nil, []string{hashValue}); err != nil {
			return "", err
		}
		return hashValue, nil
	}
	zap.L().Debug("添加段落",
		zap.String("hash", truncateRunes(hashValue, 16)),
		zap.Int("words", wordCount),
		zap.String("type", string(resolvedType)),
	)
	return hashValue, nil
}

// optionalInt 把 *int 转为可空列值
func optionalInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

// GetParagraph 获取段落，不存在返回 nil
func (s *Store) GetParagraph(hashValue string) (map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query("SELECT * FROM paragraphs WHERE hash = ?", hashValue)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return s.rowToDict(items[0]), nil
}

// GetParagraphsByHashes 批量获取有效段落，返回 hash -> paragraph
func (s *Store) GetParagraphsByHashes(hashValues []string) (map[string]map[string]any, error) {
	normalized := normalizeHashSequence(hashValues)
	out := make(map[string]map[string]any, len(normalized))
	if len(normalized) == 0 {
		return out, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	for _, batch := range iterSQLBatches(normalized, 900) {
		query := fmt.Sprintf(`SELECT * FROM paragraphs
			WHERE hash IN (%s)
			  AND (is_deleted IS NULL OR is_deleted = 0)`, sqlPlaceholders(len(batch)))
		rows, err := c.Query(query, stringArgs(batch)...)
		if err != nil {
			return nil, err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			payload := s.rowToDict(item)
			out[stringFromRow(payload, "hash")] = payload
		}
	}
	return out, nil
}

// UpdateParagraphTimeMeta 更新段落时间元信息，对齐 Python update_paragraph_time_meta
func (s *Store) UpdateParagraphTimeMeta(paragraphHash string, timeMeta map[string]any) (bool, error) {
	normalized, err := utils.NormalizeTimeMeta(timeMeta)
	if err != nil {
		return false, err
	}
	if len(normalized) == 0 {
		return false, nil
	}
	sourcesToRebuild := s.GetSourcesForParagraphHashes([]string{paragraphHash}, true, nil)

	updates := make([]string, 0, 6)
	params := make([]any, 0, 8)
	for _, key := range []string{"event_time", "event_time_start", "event_time_end", "time_granularity", "time_confidence"} {
		if value, ok := normalized[key]; ok {
			updates = append(updates, key+" = ?")
			params = append(params, value)
		}
	}
	if len(updates) == 0 {
		return false, nil
	}
	updates = append(updates, "updated_at = ?")
	params = append(params, nowTimestamp())
	params = append(params, paragraphHash)

	changed := false
	err = s.Transaction(true, func(tx *Tx) error {
		result, err := tx.Exec(fmt.Sprintf("UPDATE paragraphs SET %s WHERE hash = ?", strings.Join(updates, ", ")), params...)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		changed = affected > 0
		if changed {
			s.EnqueueEpisodeSourceRebuilds(sourcesToRebuild, "paragraph_time_updated", EpisodeEnqueueOptions{Conn: tx.Conn()})
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// QueryParagraphsTemporal 查询时序命中的段落（区间相交语义），对齐 Python query_paragraphs_temporal
func (s *Store) QueryParagraphsTemporal(startTS *float64, endTS *float64, person string, source string, limit int, allowCreatedFallback bool, allowedHashes []string) ([]map[string]any, error) {
	if limit <= 0 {
		return []map[string]any{}, nil
	}
	effectiveStart := "COALESCE(p.event_time_start, p.event_time, p.event_time_end"
	effectiveEnd := "COALESCE(p.event_time_end, p.event_time, p.event_time_start"
	if allowCreatedFallback {
		effectiveStart += ", p.created_at)"
		effectiveEnd += ", p.created_at)"
	} else {
		effectiveStart += ")"
		effectiveEnd += ")"
	}

	conditions := []string{"(p.is_deleted IS NULL OR p.is_deleted = 0)"}
	params := make([]any, 0, 8)
	if allowedHashes != nil {
		normalizedAllowed := normalizeHashSequence(allowedHashes)
		if len(normalizedAllowed) == 0 {
			return []map[string]any{}, nil
		}
		payload, err := json.Marshal(normalizedAllowed)
		if err != nil {
			return nil, err
		}
		conditions = append(conditions, "p.hash IN (SELECT value FROM json_each(?))")
		params = append(params, string(payload))
	}
	if strings.TrimSpace(source) != "" {
		conditions = append(conditions, "p.source = ?")
		params = append(params, source)
	}
	if strings.TrimSpace(person) != "" {
		conditions = append(conditions, `EXISTS (
				SELECT 1
				FROM paragraph_entities pe
				JOIN entities e ON e.hash = pe.entity_hash
				WHERE pe.paragraph_hash = p.hash
				  AND LOWER(e.name) LIKE ?
			)`)
		params = append(params, "%"+strings.ToLower(strings.TrimSpace(person))+"%")
	}
	if startTS != nil && endTS != nil {
		conditions = append(conditions, fmt.Sprintf("(%s >= ? AND %s <= ?)", effectiveEnd, effectiveStart))
		params = append(params, *startTS, *endTS)
	} else if startTS != nil {
		conditions = append(conditions, fmt.Sprintf("(%s >= ?)", effectiveEnd))
		params = append(params, *startTS)
	} else if endTS != nil {
		conditions = append(conditions, fmt.Sprintf("(%s <= ?)", effectiveStart))
		params = append(params, *endTS)
	}

	query := fmt.Sprintf(`SELECT p.*
		FROM paragraphs p
		WHERE %s
		ORDER BY %s DESC, p.updated_at DESC
		LIMIT ?`, strings.Join(conditions, " AND "), effectiveEnd)
	params = append(params, limit)

	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(query, params...)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, s.rowToDict(item))
	}
	return out, nil
}
