package storage

// A_memorix 外部记忆映射，对齐 Python metadata_store.py 的 external_memory_refs 方法

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// errExternalIDEmpty 对齐 Python ValueError("external_id 不能为空")
	errExternalIDEmpty = errors.New("external_id 不能为空")
	// errExternalParagraphHashEmpty 对齐 Python ValueError("paragraph_hash 不能为空")
	errExternalParagraphHashEmpty = errors.New("paragraph_hash 不能为空")
)

// GetExternalMemoryRef 按 external_id 查询外部记忆映射
func (s *Store) GetExternalMemoryRef(externalID string) (map[string]any, error) {
	token := strings.TrimSpace(externalID)
	if token == "" {
		return nil, nil
	}
	const query = "SELECT external_id, paragraph_hash, source_type, created_at, metadata_json " +
		"FROM external_memory_refs WHERE external_id = ? LIMIT 1"
	rows, err := s.Query(query, token)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return attachExternalRefMetadata(rows[0]), nil
}

// UpsertExternalMemoryRef 注册 external_id 到段落哈希的幂等映射
func (s *Store) UpsertExternalMemoryRef(externalID string, paragraphHash string, sourceType string, metadata map[string]any) (map[string]any, error) {
	externalToken := strings.TrimSpace(externalID)
	paragraphToken := strings.TrimSpace(paragraphHash)
	if externalToken == "" {
		return nil, errExternalIDEmpty
	}
	if paragraphToken == "" {
		return nil, errExternalParagraphHashEmpty
	}
	now := float64(time.Now().UnixNano()) / 1e9
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadataJSON := jsonDumps(metadata)
	sourceValue := optionalString(strings.TrimSpace(sourceType))
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	const statement = "INSERT INTO external_memory_refs " +
		"(external_id, paragraph_hash, source_type, created_at, metadata_json) VALUES (?, ?, ?, ?, ?) " +
		"ON CONFLICT(external_id) DO UPDATE SET paragraph_hash = excluded.paragraph_hash, " +
		"source_type = excluded.source_type, metadata_json = excluded.metadata_json"
	if _, err := c.Exec(statement, externalToken, paragraphToken, sourceValue, now, metadataJSON); err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	ref, err := s.GetExternalMemoryRef(externalToken)
	if err != nil {
		return nil, err
	}
	if ref != nil {
		return ref, nil
	}
	return map[string]any{
		"external_id":    externalToken,
		"paragraph_hash": paragraphToken,
		"source_type":    strings.TrimSpace(sourceType),
		"created_at":     now,
		"metadata":       metadata,
	}, nil
}

// ListExternalMemoryRefsByParagraphs 列出段落对应的外部记忆映射
func (s *Store) ListExternalMemoryRefsByParagraphs(paragraphHashes []string) ([]map[string]any, error) {
	hashes := splitNonEmptyTokens(paragraphHashes)
	if len(hashes) == 0 {
		return []map[string]any{}, nil
	}
	query := fmt.Sprintf("SELECT external_id, paragraph_hash, source_type, created_at, metadata_json "+
		"FROM external_memory_refs WHERE paragraph_hash IN (%s) "+
		"ORDER BY created_at ASC, external_id ASC", sqlPlaceholders(len(hashes)))
	rows, err := s.Query(query, stringArgs(hashes)...)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, attachExternalRefMetadata(row))
	}
	return items, nil
}

// DeleteExternalMemoryRefsByParagraphs 删除段落对应的外部记忆映射并返回原记录
func (s *Store) DeleteExternalMemoryRefsByParagraphs(paragraphHashes []string) ([]map[string]any, error) {
	items, err := s.ListExternalMemoryRefsByParagraphs(paragraphHashes)
	if err != nil {
		return nil, err
	}
	hashes := splitNonEmptyTokens(paragraphHashes)
	if len(hashes) == 0 {
		return items, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf("DELETE FROM external_memory_refs WHERE paragraph_hash IN (%s)", sqlPlaceholders(len(hashes)))
	if _, err := c.Exec(query, stringArgs(hashes)...); err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

// RestoreExternalMemoryRefs 恢复外部记忆映射，返回写入条数
func (s *Store) RestoreExternalMemoryRefs(refs []map[string]any) (int, error) {
	count := 0
	c, err := s.ResolveConn(nil)
	if err != nil {
		return 0, err
	}
	const statement = "INSERT INTO external_memory_refs " +
		"(external_id, paragraph_hash, source_type, created_at, metadata_json) VALUES (?, ?, ?, ?, ?) " +
		"ON CONFLICT(external_id) DO UPDATE SET paragraph_hash = excluded.paragraph_hash, " +
		"source_type = excluded.source_type, created_at = excluded.created_at, metadata_json = excluded.metadata_json"
	for _, item := range refs {
		externalID := strings.TrimSpace(stringFromRow(item, "external_id"))
		paragraphHash := strings.TrimSpace(stringFromRow(item, "paragraph_hash"))
		if externalID == "" || paragraphHash == "" {
			continue
		}
		createdAt := float64(time.Now().UnixNano()) / 1e9
		if raw := item["created_at"]; raw != nil {
			if parsed := floatPtrFromRow(map[string]any{"created_at": raw}, "created_at"); parsed != nil && *parsed != 0 {
				createdAt = *parsed
			}
		}
		metadata, _ := item["metadata"].(map[string]any)
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadataJSON := jsonDumps(metadata)
		sourceValue := optionalString(strings.TrimSpace(stringFromRow(item, "source_type")))
		result, err := c.Exec(statement, externalID, paragraphHash, sourceValue, createdAt, metadataJSON)
		if err != nil {
			return count, err
		}
		affected, err := rowsAffected(result)
		if err != nil {
			return count, err
		}
		if affected > 0 {
			count += affected
		}
	}
	if err := c.Commit(); err != nil {
		return count, err
	}
	return count, nil
}

// splitNonEmptyTokens 去空白并丢弃空串，保留原始顺序与重复项
func splitNonEmptyTokens(values []string) []string {
	out := make([]string, 0, len(values))
	for _, item := range values {
		token := strings.TrimSpace(item)
		if token != "" {
			out = append(out, token)
		}
	}
	return out
}

// attachExternalRefMetadata 解码 metadata_json 到 metadata 字段
func attachExternalRefMetadata(row map[string]any) map[string]any {
	payload := make(map[string]any, len(row)+1)
	for key, value := range row {
		payload[key] = value
	}
	payload["metadata"] = jsonLoads(payload["metadata_json"], map[string]any{})
	return payload
}
