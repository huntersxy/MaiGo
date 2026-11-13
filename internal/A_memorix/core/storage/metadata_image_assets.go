package storage

// A_memorix 图片资产与出现记录，对齐 Python core/storage/metadata_image.py 的资产/出现记录/统计部分
// 所有行都以 Python dict(row) 同构的 map 返回，字段名与列名一致

import (
	"fmt"
	"strings"
)

// ImageOccurrenceInput 对齐 Python upsert_image_occurrence 的关键字参数
type ImageOccurrenceInput struct {
	// AssetID 资产标识
	AssetID string
	// ExternalRef 外部引用，唯一键；为空时报错
	ExternalRef string
	// SourceKind 来源类型
	SourceKind string
	// ScopeType 作用域（global / chat）
	ScopeType string
	// ChatID 聊天标识
	ChatID string
	// MessageID 消息标识
	MessageID string
	// ComponentPath 消息内组件路径
	ComponentPath string
	// OccurredAt 为 nil 表示 NULL；冲突更新时回退为已有值
	OccurredAt *float64
	// InstallationID 安装标识
	InstallationID string
	// Conn 指定连接，nil 表示主连接
	Conn *Conn
}

// UpsertImageAsset 写入图片资产，对齐 Python upsert_image_asset
// 冲突按 content_hash 更新存储键与尺寸，asset_id 保持首次写入值
func (s *Store) UpsertImageAsset(contentHash, storageKey, mimeType string, byteSize, width, height int64, conn *Conn) (map[string]any, error) {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	now := nowTimestamp()
	if _, err := c.Exec(`
		INSERT INTO image_assets(
			asset_id, content_hash, storage_key, mime_type, byte_size, width, height,
			status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)
		ON CONFLICT(content_hash) DO UPDATE SET
			storage_key=excluded.storage_key,
			mime_type=excluded.mime_type,
			byte_size=excluded.byte_size,
			width=excluded.width,
			height=excluded.height,
			status='active',
			updated_at=excluded.updated_at
	`, contentHash, contentHash, storageKey, mimeType, byteSize, width, height, now, now); err != nil {
		return nil, err
	}
	row, err := imageFirstDict(c, "SELECT * FROM image_assets WHERE content_hash = ?", contentHash)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("图片资产写入后无法读取")
	}
	return row, nil
}

// GetImageAsset 读取图片资产，不存在返回 nil，对齐 Python get_image_asset
func (s *Store) GetImageAsset(assetID string) (map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	return imageFirstDict(c, "SELECT * FROM image_assets WHERE asset_id = ?", assetID)
}

// GetImageAssetByHash 按内容哈希读取图片资产，对齐 Python get_image_asset_by_hash
func (s *Store) GetImageAssetByHash(contentHash string) (map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	return imageFirstDict(c, "SELECT * FROM image_assets WHERE content_hash = ?", contentHash)
}

// UpsertImageOccurrence 写入图片出现记录，对齐 Python upsert_image_occurrence
// 缺少外部引用时返回 Python 同文案错误
func (s *Store) UpsertImageOccurrence(input ImageOccurrenceInput) (map[string]any, error) {
	c, err := s.ResolveConn(input.Conn)
	if err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(input.ExternalRef)
	if ref == "" {
		return nil, fmt.Errorf("图片出现记录缺少外部引用")
	}
	now := nowTimestamp()
	occurrenceID := imageUUIDHex()
	var occurredAt any
	if input.OccurredAt != nil {
		occurredAt = *input.OccurredAt
	}
	if _, err := c.Exec(`
		INSERT INTO image_occurrences(
			occurrence_id, asset_id, external_ref, source_kind, scope_type, chat_id,
			message_id, component_path, occurred_at, installation_id, status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)
		ON CONFLICT(external_ref) DO UPDATE SET
			asset_id=excluded.asset_id,
			source_kind=excluded.source_kind,
			scope_type=excluded.scope_type,
			chat_id=excluded.chat_id,
			message_id=excluded.message_id,
			component_path=excluded.component_path,
			occurred_at=COALESCE(excluded.occurred_at, image_occurrences.occurred_at),
			installation_id=excluded.installation_id,
			status='active',
			updated_at=excluded.updated_at
	`, occurrenceID, input.AssetID, ref, input.SourceKind, input.ScopeType, input.ChatID,
		input.MessageID, input.ComponentPath, occurredAt, input.InstallationID, now, now); err != nil {
		return nil, err
	}
	row, err := imageFirstDict(c, "SELECT * FROM image_occurrences WHERE external_ref = ?", ref)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("图片出现记录写入后无法读取")
	}
	return row, nil
}

// GetImageOccurrence 读取图片出现记录，不存在返回 nil，对齐 Python get_image_occurrence
func (s *Store) GetImageOccurrence(occurrenceID string) (map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	return imageFirstDict(c, "SELECT * FROM image_occurrences WHERE occurrence_id = ?", occurrenceID)
}

// GetImageOccurrenceByRef 按外部引用读取出现记录，对齐 Python get_image_occurrence_by_ref
func (s *Store) GetImageOccurrenceByRef(externalRef string) (map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	return imageFirstDict(c, "SELECT * FROM image_occurrences WHERE external_ref = ?", externalRef)
}

// ListVisibleImageOccurrences 列出可见图片出现记录，对齐 Python list_visible_image_occurrences
// chatIDs 为 nil 表示不按聊天过滤（Python chat_ids=None）；非 nil 时保留 global 与本聊天作用域，
// 传空且非 nil 的切片等价 Python chat_ids=[]
func (s *Store) ListVisibleImageOccurrences(chatIDs []string, excludeOccurrenceID string) ([]map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	query := `
		SELECT occurrence.*
		FROM image_occurrences AS occurrence
		JOIN image_assets AS asset ON asset.asset_id = occurrence.asset_id
		WHERE occurrence.status = 'active' AND asset.status = 'active'
	`
	params := make([]any, 0, 4)
	if chatIDs != nil {
		tokens := imageTrimmedTokens(chatIDs)
		clauses := []string{"occurrence.scope_type = 'global'"}
		if len(tokens) > 0 {
			clauses = append(clauses, fmt.Sprintf(
				"(occurrence.scope_type = 'chat' AND occurrence.chat_id IN (%s))",
				sqlPlaceholders(len(tokens))))
			params = append(params, stringArgs(tokens)...)
		}
		query += " AND (" + strings.Join(clauses, " OR ") + ")"
	}
	if excludeOccurrenceID != "" {
		query += " AND occurrence.occurrence_id != ?"
		params = append(params, excludeOccurrenceID)
	}
	query += " ORDER BY COALESCE(occurrence.occurred_at, occurrence.created_at) DESC"
	return imageQueryDicts(c, query, params...)
}

// ListImageOccurrencesForAsset 列出某资产的出现记录，对齐 Python list_image_occurrences_for_asset
func (s *Store) ListImageOccurrencesForAsset(assetID string, includeDeleted bool) ([]map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	query := "SELECT * FROM image_occurrences WHERE asset_id = ?"
	if !includeDeleted {
		query += " AND status = 'active'"
	}
	query += " ORDER BY COALESCE(occurred_at, created_at) DESC"
	return imageQueryDicts(c, query, assetID)
}

// ListImageAssets 列出图片资产，对齐 Python list_image_assets
// chatID 非空时仅保留在该聊天中出现过且仍可见的图片
func (s *Store) ListImageAssets(limit, offset int, chatID string) ([]map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	chatFilter := " WHERE asset.status='active'"
	params := make([]any, 0, 3)
	if normalizedChatID := strings.TrimSpace(chatID); normalizedChatID != "" {
		chatFilter += " AND EXISTS (" +
			"SELECT 1 FROM image_occurrences fc " +
			"WHERE fc.asset_id=asset.asset_id AND fc.status='active' AND fc.chat_id=?)"
		params = append(params, normalizedChatID)
	}
	params = append(params, imageAtLeastOne(limit), imageAtLeastZero(offset))
	query := fmt.Sprintf(`
		SELECT asset.*, COUNT(occurrence.occurrence_id) AS occurrence_count,
			   MAX(COALESCE(occurrence.occurred_at, occurrence.created_at)) AS latest_occurrence_at
		FROM image_assets AS asset
		LEFT JOIN image_occurrences AS occurrence
		  ON occurrence.asset_id=asset.asset_id AND occurrence.status='active'
		%s
		GROUP BY asset.asset_id
		ORDER BY latest_occurrence_at DESC, asset.updated_at DESC
		LIMIT ? OFFSET ?
	`, chatFilter)
	return imageQueryDicts(c, query, params...)
}

// ListImageChatStats 按聊天聚合可见图片资产数，对齐 Python list_image_chat_stats
func (s *Store) ListImageChatStats() ([]map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	return imageQueryDicts(c, `
		SELECT occurrence.chat_id AS chat_id,
			   COUNT(DISTINCT occurrence.asset_id) AS asset_count
		FROM image_occurrences AS occurrence
		WHERE occurrence.status='active' AND TRIM(occurrence.chat_id)!=''
		GROUP BY occurrence.chat_id
		ORDER BY asset_count DESC, occurrence.chat_id ASC
	`)
}

// ImageMemoryStats 汇总图片记忆计数，对齐 Python image_memory_stats
func (s *Store) ImageMemoryStats() (map[string]int, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	queries := []struct {
		key   string
		query string
	}{
		{"storage_bytes", "SELECT COALESCE(SUM(byte_size),0) FROM image_assets WHERE status='active'"},
		{"asset_count", "SELECT COUNT(*) FROM image_assets WHERE status='active'"},
		{"occurrence_count", "SELECT COUNT(*) FROM image_occurrences WHERE status='active'"},
		{"observation_count", "SELECT COUNT(*) FROM image_observations o JOIN image_occurrences c " +
			"ON c.occurrence_id=o.occurrence_id WHERE o.superseded_by='' AND c.status='active'"},
		{"link_count", "SELECT COUNT(*) FROM image_memory_links l JOIN image_occurrences c " +
			"ON c.occurrence_id=l.occurrence_id WHERE l.status='active' AND c.status='active'"},
		{"ready_vector_count", "SELECT COUNT(*) FROM image_embeddings e JOIN image_assets a ON a.asset_id=e.asset_id " +
			"WHERE e.status='ready' AND a.status='active'"},
		{"pending_job_count", "SELECT COUNT(*) FROM image_embedding_jobs WHERE status IN ('pending','running')"},
		{"failed_job_count", "SELECT COUNT(*) FROM image_embedding_jobs WHERE status='failed'"},
		{"pending_description_count", "SELECT COUNT(*) FROM image_description_compensations WHERE status IN ('pending','running')"},
		{"failed_description_count", "SELECT COUNT(*) FROM image_description_compensations WHERE status='failed'"},
		{"pending_unbound_description_count", "SELECT COUNT(*) FROM image_pending_descriptions"},
	}
	stats := make(map[string]int, len(queries))
	for _, item := range queries {
		value, err := countRows(c, item.query)
		if err != nil {
			return nil, err
		}
		stats[item.key] = value
	}
	return stats, nil
}

// ImageIndexProgress 统计索引进度，对齐 Python image_index_progress
func (s *Store) ImageIndexProgress(fingerprint string) (map[string]int, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	total, err := countRows(c, "SELECT COUNT(*) FROM image_assets WHERE status='active'")
	if err != nil {
		return nil, err
	}
	ready, err := countRows(c, `SELECT COUNT(*) FROM image_embeddings e JOIN image_assets a ON a.asset_id=e.asset_id
		WHERE a.status='active' AND e.status='ready' AND e.fingerprint_hash=?`, fingerprint)
	if err != nil {
		return nil, err
	}
	return map[string]int{"total": total, "ready": ready, "remaining": total - ready}, nil
}
