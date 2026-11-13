package storage

// A_memorix 图片认知与记忆关联，对齐 Python core/storage/metadata_image.py 的观察/关联部分

import (
	"fmt"
	"sort"
	"strings"
)

// ImageObservationInput 对齐 Python add_image_observation 的关键字参数
type ImageObservationInput struct {
	// OccurrenceID 所属出现记录
	OccurrenceID string
	// Text 认知文本，去空白后为空时报错
	Text string
	// SourceKind 认知来源（user_statement / model_description 参与去重）
	SourceKind string
	// ConfirmStatus 确认状态
	ConfirmStatus string
	// Evidence 证据字典，写成 JSON
	Evidence map[string]any
	// SupersedesID 被修正的旧认知标识
	SupersedesID string
	// Conn 指定连接，nil 表示主连接
	Conn *Conn
}

// ImageMemoryLinkInput 对齐 Python upsert_image_memory_link 的关键字参数
type ImageMemoryLinkInput struct {
	// OccurrenceID 所属出现记录
	OccurrenceID string
	// TargetType 目标类型
	TargetType string
	// TargetID 目标标识
	TargetID string
	// LinkKind 关联类型
	LinkKind string
	// Evidence 证据字典，写成 JSON
	Evidence map[string]any
	// Conn 指定连接，nil 表示主连接
	Conn *Conn
}

// imageRemapPair 一次改名映射
type imageRemapPair struct {
	oldToken string
	newToken string
}

// AddImageObservation 新增图片认知，对齐 Python add_image_observation
// 用户陈述与模型描述的重复写入直接复用已有记录，不重新激活人工修正过的旧文本
func (s *Store) AddImageObservation(input ImageObservationInput) (map[string]any, error) {
	content := strings.TrimSpace(input.Text)
	if content == "" {
		return nil, fmt.Errorf("图片认知文本不能为空")
	}
	c, err := s.ResolveConn(input.Conn)
	if err != nil {
		return nil, err
	}
	dedupeKinds := input.SourceKind == "user_statement" || input.SourceKind == "model_description"
	if input.SupersedesID == "" && dedupeKinds {
		existing, err := imageFirstDict(c,
			"SELECT * FROM image_observations WHERE occurrence_id=? AND source_kind=? AND text=? "+
				"ORDER BY created_at LIMIT 1",
			input.OccurrenceID, input.SourceKind, content)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
	}
	observationID := imageUUIDHex()
	now := nowTimestamp()
	version := int64(1)
	if input.SupersedesID != "" {
		previous, err := imageFirstDict(c,
			"SELECT observation_id, version FROM image_observations WHERE observation_id = ? AND occurrence_id = ?",
			input.SupersedesID, input.OccurrenceID)
		if err != nil {
			return nil, err
		}
		if previous == nil {
			return nil, fmt.Errorf("被修正的图片认知不存在")
		}
		version = imageIntValue(previous["version"]) + 1
		if _, err := c.Exec("UPDATE image_observations SET superseded_by = ? WHERE observation_id = ?",
			observationID, input.SupersedesID); err != nil {
			return nil, err
		}
	}
	if _, err := c.Exec(`
		INSERT INTO image_observations(
			observation_id, occurrence_id, text, source_kind, confirm_status,
			evidence_json, version, supersedes_id, superseded_by, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?)
	`, observationID, input.OccurrenceID, content, input.SourceKind, input.ConfirmStatus,
		imageJSONObject(input.Evidence), version, input.SupersedesID, now); err != nil {
		return nil, err
	}
	row, err := imageFirstDict(c, "SELECT * FROM image_observations WHERE observation_id = ?", observationID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("图片认知写入后无法读取")
	}
	return row, nil
}

// ListImageObservations 列出未被取代的图片认知，对齐 Python list_image_observations
func (s *Store) ListImageObservations(occurrenceIDs []string) ([]map[string]any, error) {
	tokens := imageTokenList(occurrenceIDs)
	if len(tokens) == 0 {
		return []map[string]any{}, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`
		SELECT * FROM image_observations
		WHERE occurrence_id IN (%s) AND superseded_by = ''
		ORDER BY created_at ASC
	`, sqlPlaceholders(len(tokens)))
	return imageQueryDicts(c, query, stringArgs(tokens)...)
}

// UpsertImageMemoryLink 写入图片记忆关联，对齐 Python upsert_image_memory_link
func (s *Store) UpsertImageMemoryLink(input ImageMemoryLinkInput) (map[string]any, error) {
	c, err := s.ResolveConn(input.Conn)
	if err != nil {
		return nil, err
	}
	now := nowTimestamp()
	linkID := imageUUIDHex()
	if _, err := c.Exec(`
		INSERT INTO image_memory_links(
			link_id, occurrence_id, target_type, target_id, link_kind,
			evidence_json, status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, 'active', ?, ?)
		ON CONFLICT(occurrence_id, target_type, target_id) DO UPDATE SET
			link_kind=excluded.link_kind,
			evidence_json=excluded.evidence_json,
			status='active',
			updated_at=excluded.updated_at
	`, linkID, input.OccurrenceID, input.TargetType, input.TargetID, input.LinkKind,
		imageJSONObject(input.Evidence), now, now); err != nil {
		return nil, err
	}
	row, err := imageFirstDict(c, `
		SELECT * FROM image_memory_links
		WHERE occurrence_id = ? AND target_type = ? AND target_id = ?
	`, input.OccurrenceID, input.TargetType, input.TargetID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("图片记忆关联写入后无法读取")
	}
	return row, nil
}

// ListImageMemoryLinks 列出有效的图片记忆关联，对齐 Python list_image_memory_links
func (s *Store) ListImageMemoryLinks(occurrenceIDs []string) ([]map[string]any, error) {
	tokens := imageTokenList(occurrenceIDs)
	if len(tokens) == 0 {
		return []map[string]any{}, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf(
		"SELECT * FROM image_memory_links WHERE status = 'active' AND occurrence_id IN (%s)",
		sqlPlaceholders(len(tokens)))
	return imageQueryDicts(c, query, stringArgs(tokens)...)
}

// InvalidateImageMemoryLinks 批量失效目标关联，对齐 Python invalidate_image_memory_links
func (s *Store) InvalidateImageMemoryLinks(targetType string, targetIDs []string, conn *Conn) (int, error) {
	tokens := imageTokenList(targetIDs)
	if len(tokens) == 0 {
		return 0, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return 0, err
	}
	args := make([]any, 0, len(tokens)+2)
	args = append(args, nowTimestamp(), targetType)
	args = append(args, stringArgs(tokens)...)
	result, err := c.Exec(fmt.Sprintf(`
		UPDATE image_memory_links SET status='invalid', updated_at=?
		WHERE status='active' AND target_type=? AND target_id IN (%s)
	`, sqlPlaceholders(len(tokens))), args...)
	if err != nil {
		return 0, err
	}
	return imageAffected(result)
}

// ListActiveImageMemoryLinkIDs 记录一次权威对象变更实际影响的图片关联，对齐 Python list_active_image_memory_link_ids
func (s *Store) ListActiveImageMemoryLinkIDs(targetType string, targetIDs []string) ([]string, error) {
	tokens := imageTokenList(targetIDs)
	if len(tokens) == 0 {
		return []string{}, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	args := make([]any, 0, len(tokens)+1)
	args = append(args, targetType)
	args = append(args, stringArgs(tokens)...)
	rows, err := imageQueryDicts(c, fmt.Sprintf(`
		SELECT link_id FROM image_memory_links
		WHERE status='active' AND target_type=? AND target_id IN (%s)
		ORDER BY created_at ASC
	`, sqlPlaceholders(len(tokens))), args...)
	if err != nil {
		return nil, err
	}
	linkIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		linkIDs = append(linkIDs, stringFromRow(row, "link_id"))
	}
	return linkIDs, nil
}

// RestoreImageMemoryLinks 恢复本次修正操作曾失效的关联，对齐 Python restore_image_memory_links
func (s *Store) RestoreImageMemoryLinks(linkIDs []string, conn *Conn) (int, error) {
	tokens := imageTokenList(linkIDs)
	if len(tokens) == 0 {
		return 0, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return 0, err
	}
	args := make([]any, 0, len(tokens)+1)
	args = append(args, nowTimestamp())
	args = append(args, stringArgs(tokens)...)
	result, err := c.Exec(fmt.Sprintf(`
		UPDATE image_memory_links SET status='active', updated_at=?
		WHERE status='invalid' AND link_id IN (%s)
	`, sqlPlaceholders(len(tokens))), args...)
	if err != nil {
		return 0, err
	}
	return imageAffected(result)
}

// InvalidateImageMemoryLink 失效单条关联，对齐 Python invalidate_image_memory_link
func (s *Store) InvalidateImageMemoryLink(linkID string, conn *Conn) (int, error) {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return 0, err
	}
	result, err := c.Exec(
		"UPDATE image_memory_links SET status='invalid', updated_at=? WHERE link_id=? AND status='active'",
		nowTimestamp(), linkID)
	if err != nil {
		return 0, err
	}
	return imageAffected(result)
}

// RemapImageMemoryLinks 在权威对象改名时迁移图片关联并合并已存在的目标关联
// 对齐 Python remap_image_memory_links；Go map 无插入顺序，按旧标识升序处理保证可重复
func (s *Store) RemapImageMemoryLinks(targetType string, targetIDMap map[string]string, conn *Conn) (int, error) {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return 0, err
	}
	pairs := make([]imageRemapPair, 0, len(targetIDMap))
	for oldID, newID := range targetIDMap {
		oldToken := strings.TrimSpace(oldID)
		newToken := strings.TrimSpace(newID)
		if oldToken == "" || newToken == "" || oldToken == newToken {
			continue
		}
		pairs = append(pairs, imageRemapPair{oldToken: oldToken, newToken: newToken})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].oldToken < pairs[j].oldToken })

	remapped := 0
	for _, pair := range pairs {
		rows, err := imageQueryDicts(c, `
			SELECT * FROM image_memory_links
			WHERE status='active' AND target_type=? AND target_id=?
		`, targetType, pair.oldToken)
		if err != nil {
			return remapped, err
		}
		for _, row := range rows {
			if _, err := s.UpsertImageMemoryLink(ImageMemoryLinkInput{
				OccurrenceID: stringFromRow(row, "occurrence_id"),
				TargetType:   targetType,
				TargetID:     pair.newToken,
				LinkKind:     stringFromRow(row, "link_kind"),
				Evidence:     imageEvidenceDict(row["evidence_json"]),
				Conn:         c,
			}); err != nil {
				return remapped, err
			}
			remapped++
		}
		if _, err := s.InvalidateImageMemoryLinks(targetType, []string{pair.oldToken}, c); err != nil {
			return remapped, err
		}
	}
	return remapped, nil
}
