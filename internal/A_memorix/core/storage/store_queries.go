package storage

// A_memorix 段落 / 实体 / 关系的只读查询 API，对齐 Python metadata_store.py

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	memorixutils "maibot-go/internal/A_memorix/core/utils"
)

// ErrRelationHashEmpty 对齐 Python ValueError("relation_hash 不能为空")
var ErrRelationHashEmpty = errors.New("relation_hash 不能为空")

// Triple 对齐 get_all_triples 的四元组
type Triple struct {
	Subject   string
	Predicate string
	Object    string
	Hash      string
}

// SourceStat 对齐 get_all_sources 的统计项
type SourceStat struct {
	Source      string
	Count       int
	LastUpdated *float64
}

// GetEntity 获取实体，不存在返回 nil
func (s *Store) GetEntity(hashValue string) (map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query("SELECT * FROM entities WHERE hash = ?", hashValue)
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

// GetEntitiesByHashes 批量获取实体，返回 hash -> entity
func (s *Store) GetEntitiesByHashes(hashValues []string) (map[string]map[string]any, error) {
	out := make(map[string]map[string]any)
	normalized := normalizeHashSequence(hashValues)
	if len(normalized) == 0 {
		return out, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	for _, batch := range iterSQLBatches(normalized, 900) {
		query := fmt.Sprintf("SELECT * FROM entities WHERE hash IN (%s)", sqlPlaceholders(len(batch)))
		rows, err := c.Query(query, stringArgs(batch)...)
		if err != nil {
			return nil, err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return nil, err
		}
		for _, row := range items {
			payload := s.rowToDict(row)
			out[stringFromRow(payload, "hash")] = payload
		}
	}
	return out, nil
}

// GetRelation 获取关系，不存在返回 nil
func (s *Store) GetRelation(hashValue string, includeInactive bool) (map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	query := "SELECT * FROM relations WHERE hash = ?"
	if !includeInactive {
		query = `SELECT * FROM relations
			WHERE hash = ?
			  AND (is_inactive IS NULL OR is_inactive = 0)`
	}
	rows, err := c.Query(query, hashValue)
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

// UpdateRelationMetadata 更新关系 metadata，返回更新后的 metadata
func (s *Store) UpdateRelationMetadata(relationHash string, patch map[string]any, merge bool) (map[string]any, error) {
	hashToken := strings.TrimSpace(relationHash)
	if hashToken == "" {
		return nil, ErrRelationHashEmpty
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query("SELECT metadata FROM relations WHERE hash = ? LIMIT 1", hashToken)
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
	metadata := decodeMetadata(stringFromRow(items[0], "metadata"))
	var updated map[string]any
	if merge {
		updated = deepMergeDict(metadata, patch)
	} else {
		updated = make(map[string]any, len(patch))
		for key, value := range patch {
			updated[key] = value
		}
	}
	if _, err := c.Exec("UPDATE relations SET metadata = ? WHERE hash = ?", encodeMetadata(updated), hashToken); err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

// GetRelationsByHashes 批量获取关系，返回 hash -> relation
func (s *Store) GetRelationsByHashes(hashValues []string, includeInactive bool) (map[string]map[string]any, error) {
	out := make(map[string]map[string]any)
	normalized := normalizeHashSequence(hashValues)
	if len(normalized) == 0 {
		return out, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	inactiveSQL := ""
	if !includeInactive {
		inactiveSQL = "AND (is_inactive IS NULL OR is_inactive = 0)"
	}
	for _, batch := range iterSQLBatches(normalized, 900) {
		query := fmt.Sprintf(`SELECT * FROM relations
			WHERE hash IN (%s)
			  %s`, sqlPlaceholders(len(batch)), inactiveSQL)
		rows, err := c.Query(query, stringArgs(batch)...)
		if err != nil {
			return nil, err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return nil, err
		}
		for _, row := range items {
			payload := s.rowToDict(row)
			out[stringFromRow(payload, "hash")] = payload
		}
	}
	return out, nil
}

// GetParagraphRelations 获取段落的所有关系
func (s *Store) GetParagraphRelations(paragraphHash string) ([]map[string]any, error) {
	return s.queryRelationRows(`SELECT r.* FROM relations r
		JOIN paragraph_relations pr ON r.hash = pr.relation_hash
		WHERE pr.paragraph_hash = ?`, paragraphHash)
}

// GetParagraphHashesByRelationHashes 批量获取关系对应段落 hash
func (s *Store) GetParagraphHashesByRelationHashes(relationHashes []string) (map[string][]string, error) {
	normalized := normalizeHashSequence(relationHashes)
	grouped := make(map[string][]string, len(normalized))
	if len(normalized) == 0 {
		return grouped, nil
	}
	for _, token := range normalized {
		grouped[token] = []string{}
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	for _, batch := range iterSQLBatches(normalized, 900) {
		query := fmt.Sprintf(`SELECT pr.relation_hash, pr.paragraph_hash
			FROM paragraph_relations pr
			JOIN paragraphs p ON p.hash = pr.paragraph_hash
			WHERE pr.relation_hash IN (%s)
			  AND (p.is_deleted IS NULL OR p.is_deleted = 0)
			ORDER BY pr.relation_hash ASC, p.updated_at DESC, p.created_at DESC, pr.paragraph_hash ASC`, sqlPlaceholders(len(batch)))
		rows, err := c.Query(query, stringArgs(batch)...)
		if err != nil {
			return nil, err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return nil, err
		}
		for _, row := range items {
			relationHash := strings.TrimSpace(stringFromRow(row, "relation_hash"))
			paragraphHash := strings.TrimSpace(stringFromRow(row, "paragraph_hash"))
			if relationHash == "" || paragraphHash == "" {
				continue
			}
			bucket := grouped[relationHash]
			if !slices.Contains(bucket, paragraphHash) {
				grouped[relationHash] = append(bucket, paragraphHash)
			}
		}
	}
	return grouped, nil
}

// GetParagraphEntities 获取段落的所有实体
func (s *Store) GetParagraphEntities(paragraphHash string) ([]map[string]any, error) {
	return s.queryEntityRows(`SELECT e.*, pe.mention_count
		FROM entities e
		JOIN paragraph_entities pe ON e.hash = pe.entity_hash
		WHERE pe.paragraph_hash = ?`, paragraphHash)
}

// GetParagraphEntitiesByHashes 批量获取段落实体映射
func (s *Store) GetParagraphEntitiesByHashes(paragraphHashes []string) (map[string][]map[string]any, error) {
	normalized := normalizeHashSequence(paragraphHashes)
	grouped := make(map[string][]map[string]any, len(normalized))
	if len(normalized) == 0 {
		return grouped, nil
	}
	for _, token := range normalized {
		grouped[token] = []map[string]any{}
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	for _, batch := range iterSQLBatches(normalized, 900) {
		query := fmt.Sprintf(`SELECT pe.paragraph_hash, e.*, pe.mention_count
			FROM paragraph_entities pe
			JOIN entities e ON e.hash = pe.entity_hash
			WHERE pe.paragraph_hash IN (%s)`, sqlPlaceholders(len(batch)))
		rows, err := c.Query(query, stringArgs(batch)...)
		if err != nil {
			return nil, err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return nil, err
		}
		for _, row := range items {
			paragraphHash := strings.TrimSpace(stringFromRow(row, "paragraph_hash"))
			if paragraphHash == "" {
				continue
			}
			grouped[paragraphHash] = append(grouped[paragraphHash], s.rowToDict(row))
		}
	}
	return grouped, nil
}

// GetParagraphsByEntity 获取包含指定实体的所有段落
func (s *Store) GetParagraphsByEntity(entityName string) ([]map[string]any, error) {
	nameCanon := canonicalizeName(entityName)
	if nameCanon == "" {
		return []map[string]any{}, nil
	}
	entityHash, hashErr := memorixutils.ComputeHash(nameCanon)
	if hashErr != nil {
		return nil, hashErr
	}
	return s.queryParagraphRows(`SELECT p.*
		FROM paragraphs p
		JOIN paragraph_entities pe ON p.hash = pe.paragraph_hash
		WHERE pe.entity_hash = ?`, entityHash)
}

// GetParagraphHashesByEntityHashes 批量获取实体支撑段落 hash
func (s *Store) GetParagraphHashesByEntityHashes(entityHashes []string) (map[string][]string, error) {
	normalized := normalizeHashSequence(entityHashes)
	grouped := make(map[string][]string, len(normalized))
	if len(normalized) == 0 {
		return grouped, nil
	}
	for _, token := range normalized {
		grouped[token] = []string{}
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	for _, batch := range iterSQLBatches(normalized, 900) {
		query := fmt.Sprintf(`SELECT pe.entity_hash, pe.paragraph_hash
			FROM paragraph_entities pe
			JOIN paragraphs p ON p.hash = pe.paragraph_hash
			WHERE pe.entity_hash IN (%s)
			  AND (p.is_deleted IS NULL OR p.is_deleted = 0)
			ORDER BY pe.entity_hash ASC, p.updated_at DESC, p.created_at DESC, pe.paragraph_hash ASC`, sqlPlaceholders(len(batch)))
		rows, err := c.Query(query, stringArgs(batch)...)
		if err != nil {
			return nil, err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return nil, err
		}
		for _, row := range items {
			entityHash := strings.TrimSpace(stringFromRow(row, "entity_hash"))
			paragraphHash := strings.TrimSpace(stringFromRow(row, "paragraph_hash"))
			if entityHash == "" || paragraphHash == "" {
				continue
			}
			bucket := grouped[entityHash]
			if !slices.Contains(bucket, paragraphHash) {
				grouped[entityHash] = append(bucket, paragraphHash)
			}
		}
	}
	return grouped, nil
}

// GetParagraphsByEntityHashes 批量获取实体支撑段落
func (s *Store) GetParagraphsByEntityHashes(entityHashes []string) (map[string][]map[string]any, error) {
	normalized := normalizeHashSequence(entityHashes)
	grouped := make(map[string][]map[string]any, len(normalized))
	if len(normalized) == 0 {
		return grouped, nil
	}
	for _, token := range normalized {
		grouped[token] = []map[string]any{}
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	for _, batch := range iterSQLBatches(normalized, 900) {
		query := fmt.Sprintf(`SELECT pe.entity_hash, p.*
			FROM paragraph_entities pe
			JOIN paragraphs p ON p.hash = pe.paragraph_hash
			WHERE pe.entity_hash IN (%s)
			  AND (p.is_deleted IS NULL OR p.is_deleted = 0)
			ORDER BY pe.entity_hash ASC, p.updated_at DESC, p.created_at DESC, pe.paragraph_hash ASC`, sqlPlaceholders(len(batch)))
		rows, err := c.Query(query, stringArgs(batch)...)
		if err != nil {
			return nil, err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return nil, err
		}
		for _, row := range items {
			entityHash := strings.TrimSpace(stringFromRow(row, "entity_hash"))
			if entityHash == "" {
				continue
			}
			grouped[entityHash] = append(grouped[entityHash], s.rowToDict(row))
		}
	}
	return grouped, nil
}

// GetRelations 按主语/谓语/宾语查询关系，空字符串表示不过滤
func (s *Store) GetRelations(subject, predicate, object string, includeInactive bool) ([]map[string]any, error) {
	conditions := make([]string, 0, 4)
	params := make([]any, 0, 4)
	if subject != "" {
		conditions = append(conditions, "LOWER(subject) = ?")
		params = append(params, canonicalizeName(subject))
	}
	if predicate != "" {
		conditions = append(conditions, "LOWER(predicate) = ?")
		params = append(params, canonicalizeName(predicate))
	}
	if object != "" {
		conditions = append(conditions, "LOWER(object) = ?")
		params = append(params, canonicalizeName(object))
	}
	if !includeInactive {
		conditions = append(conditions, "(is_inactive IS NULL OR is_inactive = 0)")
	}
	query := "SELECT * FROM relations"
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	return s.queryRelationRows(query, params...)
}

// GetAllTriples 获取全部三元组
func (s *Store) GetAllTriples() ([]Triple, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query("SELECT subject, predicate, object, hash FROM relations")
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	out := make([]Triple, 0, len(items))
	for _, row := range items {
		out = append(out, Triple{
			Subject:   stringFromRow(row, "subject"),
			Predicate: stringFromRow(row, "predicate"),
			Object:    stringFromRow(row, "object"),
			Hash:      stringFromRow(row, "hash"),
		})
	}
	return out, nil
}

// GetParagraphsByRelation 获取支持指定关系的所有段落
func (s *Store) GetParagraphsByRelation(relationHash string) ([]map[string]any, error) {
	return s.queryParagraphRows(`SELECT p.*
		FROM paragraphs p
		JOIN paragraph_relations pr ON p.hash = pr.paragraph_hash
		WHERE pr.relation_hash = ?`, relationHash)
}

// GetParagraphsByRelationHashes 批量获取关系支撑段落
func (s *Store) GetParagraphsByRelationHashes(relationHashes []string) (map[string][]map[string]any, error) {
	normalized := normalizeHashSequence(relationHashes)
	grouped := make(map[string][]map[string]any, len(normalized))
	if len(normalized) == 0 {
		return grouped, nil
	}
	for _, token := range normalized {
		grouped[token] = []map[string]any{}
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	for _, batch := range iterSQLBatches(normalized, 900) {
		query := fmt.Sprintf(`SELECT pr.relation_hash, p.*
			FROM paragraph_relations pr
			JOIN paragraphs p ON p.hash = pr.paragraph_hash
			WHERE pr.relation_hash IN (%s)
			  AND (p.is_deleted IS NULL OR p.is_deleted = 0)
			ORDER BY pr.relation_hash ASC, p.updated_at DESC, p.created_at DESC, pr.paragraph_hash ASC`, sqlPlaceholders(len(batch)))
		rows, err := c.Query(query, stringArgs(batch)...)
		if err != nil {
			return nil, err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return nil, err
		}
		for _, row := range items {
			relationHash := strings.TrimSpace(stringFromRow(row, "relation_hash"))
			if relationHash == "" {
				continue
			}
			grouped[relationHash] = append(grouped[relationHash], s.rowToDict(row))
		}
	}
	return grouped, nil
}

// GetParagraphsBySource 按来源获取段落
func (s *Store) GetParagraphsBySource(source string) ([]map[string]any, error) {
	return s.queryParagraphRows("SELECT * FROM paragraphs WHERE source = ?", source)
}

// GetAllSources 获取所有来源统计
func (s *Store) GetAllSources() ([]SourceStat, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(`SELECT source, COUNT(*) as count, MAX(created_at) as last_updated
		FROM paragraphs
		WHERE source IS NOT NULL AND source != ''
		  AND (is_deleted IS NULL OR is_deleted = 0)
		GROUP BY source
		ORDER BY last_updated DESC`)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	out := make([]SourceStat, 0, len(items))
	for _, row := range items {
		out = append(out, SourceStat{
			Source:      stringFromRow(row, "source"),
			Count:       intFromRow(row, "count"),
			LastUpdated: floatPtrFromRow(row, "last_updated"),
		})
	}
	return out, nil
}

// SearchParagraphsByContent 按内容模糊搜索段落
func (s *Store) SearchParagraphsByContent(contentQuery string) ([]map[string]any, error) {
	return s.queryParagraphRows("SELECT * FROM paragraphs WHERE content LIKE ?", "%"+contentQuery+"%")
}

// queryParagraphRows 查询段落行并解码 metadata
func (s *Store) queryParagraphRows(query string, args ...any) ([]map[string]any, error) {
	return s.queryRowsToDict(query, args...)
}

// queryEntityRows 查询实体行并解码 metadata
func (s *Store) queryEntityRows(query string, args ...any) ([]map[string]any, error) {
	return s.queryRowsToDict(query, args...)
}

// queryRelationRows 查询关系行并解码 metadata
func (s *Store) queryRelationRows(query string, args ...any) ([]map[string]any, error) {
	return s.queryRowsToDict(query, args...)
}

// queryRowsToDict 统一查询入口
func (s *Store) queryRowsToDict(query string, args ...any) ([]map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(query, args...)
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

// intFromRow 读取整数列
func intFromRow(row map[string]any, key string) int {
	raw, ok := row[key]
	if !ok || raw == nil {
		return 0
	}
	switch v := raw.(type) {
	case int64:
		return int(v)
	case int:
		return v
	case float64:
		return int(v)
	case []byte:
		return intFromRow(map[string]any{key: string(v)}, key)
	case string:
		var parsed int
		if _, err := fmt.Sscanf(v, "%d", &parsed); err == nil {
			return parsed
		}
		return 0
	default:
		return 0
	}
}

// floatPtrFromRow 读取可空浮点列
func floatPtrFromRow(row map[string]any, key string) *float64 {
	raw, ok := row[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case float64:
		return &v
	case int64:
		converted := float64(v)
		return &converted
	case []byte:
		converted := parseFloatString(string(v))
		return converted
	case string:
		return parseFloatString(v)
	default:
		return nil
	}
}

// parseFloatString 解析浮点字符串，失败返回 nil
func parseFloatString(text string) *float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		return nil
	}
	return &parsed
}
