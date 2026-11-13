package storage

// A_memorix 统计与快照，对齐 Python metadata_store.py 的统计方法

import (
	"fmt"
	"time"
)

// GetStatistics 获取统计信息
func (s *Store) GetStatistics() (map[string]int, error) {
	stats := map[string]int{
		"paragraph_count":                      0,
		"entity_count":                         0,
		"relation_count":                       0,
		"stale_paragraph_mark_count":           0,
		"person_profile_refresh_pending_count": 0,
		"person_profile_refresh_failed_count":  0,
		"total_words":                          0,
	}
	queries := []struct {
		key   string
		query string
	}{
		{"paragraph_count", "SELECT COUNT(*) FROM paragraphs"},
		{"entity_count", "SELECT COUNT(*) FROM entities"},
		{"relation_count", "SELECT COUNT(*) FROM relations"},
		{"stale_paragraph_mark_count", "SELECT COUNT(*) FROM paragraph_stale_relation_marks"},
		{"person_profile_refresh_pending_count", "SELECT COUNT(*) FROM person_profile_refresh_queue WHERE status IN ('pending', 'running', 'failed')"},
		{"person_profile_refresh_failed_count", "SELECT COUNT(*) FROM person_profile_refresh_queue WHERE status = 'failed'"},
		{"total_words", "SELECT SUM(word_count) FROM paragraphs"},
	}
	for _, item := range queries {
		count, err := s.scalarInt(item.query)
		if err != nil {
			return nil, err
		}
		stats[item.key] = count
	}
	return stats, nil
}

// CountParagraphs 获取段落数量
func (s *Store) CountParagraphs(includeDeleted bool, onlyDeleted bool) (int, error) {
	switch {
	case onlyDeleted:
		return s.scalarInt("SELECT COUNT(*) FROM paragraphs WHERE is_deleted = 1")
	case includeDeleted:
		return s.scalarInt("SELECT COUNT(*) FROM paragraphs")
	default:
		return s.scalarInt("SELECT COUNT(*) FROM paragraphs WHERE is_deleted = 0")
	}
}

// CountRelations 获取关系数量
func (s *Store) CountRelations(includeDeleted bool, onlyDeleted bool) (int, error) {
	if onlyDeleted {
		return s.scalarInt("SELECT COUNT(*) FROM deleted_relations")
	}
	activeCount, err := s.scalarInt("SELECT COUNT(*) FROM relations")
	if err != nil {
		return 0, err
	}
	if !includeDeleted {
		return activeCount, nil
	}
	deletedCount, err := s.scalarInt("SELECT COUNT(*) FROM deleted_relations")
	if err != nil {
		return 0, err
	}
	return activeCount + deletedCount, nil
}

// CountEntities 获取实体数量
func (s *Store) CountEntities() (int, error) {
	return s.scalarInt("SELECT COUNT(*) FROM entities")
}

// GetKnowledgeTypeDistribution 获取段落知识类型分布
func (s *Store) GetKnowledgeTypeDistribution() (map[string]int, error) {
	rows, err := s.Query("SELECT knowledge_type, COUNT(*) as count FROM paragraphs " +
		"WHERE is_deleted = 0 GROUP BY knowledge_type")
	if err != nil {
		return nil, err
	}
	result := make(map[string]int, len(rows))
	for _, row := range rows {
		typeName := stringFromRow(row, "knowledge_type")
		if typeName == "" {
			typeName = "未分类"
		}
		result[typeName] = intFromRow(row, "count")
	}
	return result, nil
}

// GetMemoryStatusSummary 聚合 memory status 统计
func (s *Store) GetMemoryStatusSummary(nowTS *float64) (map[string]int, error) {
	now := float64(time.Now().UnixNano()) / 1e9
	if nowTS != nil {
		now = *nowTS
	}
	activeCount, err := s.scalarInt("SELECT COUNT(*) FROM relations WHERE is_inactive = 0")
	if err != nil {
		return nil, err
	}
	inactiveCount, err := s.scalarInt("SELECT COUNT(*) FROM relations WHERE is_inactive = 1")
	if err != nil {
		return nil, err
	}
	deletedCount, err := s.scalarInt("SELECT COUNT(*) FROM deleted_relations")
	if err != nil {
		return nil, err
	}
	pinnedCount, err := s.scalarInt("SELECT COUNT(*) FROM relations WHERE is_pinned = 1")
	if err != nil {
		return nil, err
	}
	tempCount, err := s.scalarInt("SELECT COUNT(*) FROM relations WHERE protected_until > ?", now)
	if err != nil {
		return nil, err
	}
	return map[string]int{
		"active_count":         activeCount,
		"inactive_count":       inactiveCount,
		"deleted_count":        deletedCount,
		"pinned_count":         pinnedCount,
		"temp_protected_count": tempCount,
	}, nil
}

// GetRelationsSubjectObjectMap 批量获取关系 hash 对应的主语与宾语
func (s *Store) GetRelationsSubjectObjectMap(hashes []string) (map[string][2]string, error) {
	out := make(map[string][2]string, len(hashes))
	if len(hashes) == 0 {
		return out, nil
	}
	query := fmt.Sprintf("SELECT hash, subject, object FROM relations WHERE hash IN (%s)", sqlPlaceholders(len(hashes)))
	rows, err := s.Query(query, stringArgs(hashes)...)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[stringFromRow(row, "hash")] = [2]string{stringFromRow(row, "subject"), stringFromRow(row, "object")}
	}
	return out, nil
}

// GetRelationDBSnapshot 返回关系快照 (relation_count, max_created_at, max_hash)
func (s *Store) GetRelationDBSnapshot() (int, float64, string, error) {
	rows, err := s.Query("SELECT COUNT(*) AS relation_count, COALESCE(MAX(created_at), 0) AS max_created_at, " +
		"COALESCE(MAX(hash), '') AS max_hash FROM relations")
	if err != nil {
		return 0, 0, "", err
	}
	if len(rows) == 0 {
		return 0, 0, "", nil
	}
	count := intFromRow(rows[0], "relation_count")
	maxCreated := floatPtrFromRow(rows[0], "max_created_at")
	value := 0.0
	if maxCreated != nil {
		value = *maxCreated
	}
	return count, value, stringFromRow(rows[0], "max_hash"), nil
}

// scalarInt 执行单值整数查询
func (s *Store) scalarInt(query string, args ...any) (int, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return 0, err
	}
	rows, err := c.Query(query, args...)
	if err != nil {
		return 0, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		return 0, nil
	}
	for _, value := range items[0] {
		if value == nil {
			return 0, nil
		}
		switch v := value.(type) {
		case int64:
			return int(v), nil
		case float64:
			return int(v), nil
		case []byte:
			return intFromRow(map[string]any{"value": string(v)}, "value"), nil
		default:
			return intFromRow(map[string]any{"value": value}, "value"), nil
		}
	}
	return 0, nil
}
