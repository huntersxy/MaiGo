package storage

// A_memorix 删除操作审计，对齐 Python metadata_store.py 的 delete_operations 方法

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// DeleteOperationInput 创建删除操作的入参
type DeleteOperationInput struct {
	Mode        string
	Selector    any
	Items       []map[string]any
	Reason      string
	RequestedBy string
	Status      string
	Summary     map[string]any
	OperationID *string
}

// CreateDeleteOperation 记录一次删除操作
func (s *Store) CreateDeleteOperation(input DeleteOperationInput) (map[string]any, error) {
	opID := ""
	if input.OperationID != nil {
		opID = strings.TrimSpace(*input.OperationID)
	}
	if opID == "" {
		opID = "del_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	createdAt := float64(time.Now().UnixNano()) / 1e9
	normalizedItems := make([]map[string]any, 0, len(input.Items))
	for _, item := range input.Items {
		if item == nil {
			continue
		}
		itemType := strings.TrimSpace(stringFromRow(item, "item_type"))
		if itemType == "" {
			continue
		}
		itemHash := optionalString(strings.TrimSpace(stringFromRow(item, "item_hash")))
		itemKey := strings.TrimSpace(stringFromRow(item, "item_key"))
		if itemKey == "" {
			itemKey = strings.TrimSpace(stringFromRow(item, "item_hash"))
		}
		payload, _ := item["payload"].(map[string]any)
		if payload == nil {
			payload = map[string]any{}
		}
		normalizedItems = append(normalizedItems, map[string]any{
			"item_type": itemType,
			"item_hash": itemHash,
			"item_key":  optionalString(itemKey),
			"payload":   payload,
		})
	}
	mode := strings.TrimSpace(input.Mode)
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = "executed"
	}
	selector := input.Selector
	if selector == nil {
		selector = map[string]any{}
	}
	reason := strings.TrimSpace(input.Reason)
	requestedBy := strings.TrimSpace(input.RequestedBy)
	summary := input.Summary
	if summary == nil {
		summary = map[string]any{}
	}

	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	const statement = "INSERT INTO delete_operations (" +
		"operation_id, mode, selector, reason, requested_by, status, created_at, restored_at, summary_json" +
		") VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)"
	if _, err := c.Exec(statement, opID, mode, jsonDumps(selector), optionalString(reason), optionalString(requestedBy),
		status, createdAt, jsonDumps(summary)); err != nil {
		return nil, err
	}
	if len(normalizedItems) > 0 {
		const itemStatement = "INSERT INTO delete_operation_items (" +
			"operation_id, item_type, item_hash, item_key, payload_json, created_at" +
			") VALUES (?, ?, ?, ?, ?, ?)"
		for _, item := range normalizedItems {
			if _, err := c.Exec(itemStatement, opID, item["item_type"], item["item_hash"], item["item_key"],
				jsonDumps(item["payload"]), createdAt); err != nil {
				return nil, err
			}
		}
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	created, err := s.GetDeleteOperation(opID)
	if err != nil {
		return nil, err
	}
	if created != nil {
		return created, nil
	}
	return map[string]any{
		"operation_id": opID,
		"mode":         mode,
		"selector":     selector,
		"reason":       reason,
		"requested_by": requestedBy,
		"status":       status,
		"created_at":   createdAt,
		"summary":      summary,
		"items":        normalizedItems,
	}, nil
}

// MarkDeleteOperationRestored 标记删除操作已恢复
func (s *Store) MarkDeleteOperationRestored(operationID string, summary map[string]any) (bool, error) {
	token := strings.TrimSpace(operationID)
	if token == "" {
		return false, nil
	}
	if summary == nil {
		summary = map[string]any{}
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return false, err
	}
	const statement = "UPDATE delete_operations SET status = ?, restored_at = ?, summary_json = ? WHERE operation_id = ?"
	result, err := c.Exec(statement, "restored", float64(time.Now().UnixNano())/1e9, jsonDumps(summary), token)
	if err != nil {
		return false, err
	}
	if err := c.Commit(); err != nil {
		return false, err
	}
	affected, err := rowsAffected(result)
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// UpdateParagraphMetadata 更新段落 metadata 并返回更新后的内容
func (s *Store) UpdateParagraphMetadata(paragraphHash string, patch map[string]any, merge bool) (map[string]any, error) {
	hashToken := strings.TrimSpace(paragraphHash)
	if hashToken == "" {
		return nil, errExternalParagraphHashEmpty
	}
	var updated map[string]any
	var found bool
	err := s.Transaction(true, func(tx *Tx) error {
		// 在同一事务内校验删除状态并合并，防止写回并发更新前的旧 metadata。
		rows, err := tx.Query("SELECT metadata, is_deleted FROM paragraphs WHERE hash = ? LIMIT 1", hashToken)
		if err != nil {
			return err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return err
		}
		if len(items) == 0 || intFromRow(items[0], "is_deleted") != 0 {
			return nil
		}
		metadata := decodeMetadata(stringFromRow(items[0], "metadata"))
		if merge {
			updated = mergeParagraphMetadata(metadata, patch)
		} else {
			updated = make(map[string]any, len(patch))
			for key, value := range patch {
				updated[key] = value
			}
		}
		found = true
		if _, err := tx.Exec("UPDATE paragraphs SET metadata = ?, updated_at = ? WHERE hash = ?",
			encodeMetadata(updated), float64(time.Now().UnixNano())/1e9, hashToken); err != nil {
			return err
		}
		sources := s.GetSourcesForParagraphHashes([]string{hashToken}, true, tx.Conn())
		s.EnqueueEpisodeSourceRebuilds(sources, "paragraph_metadata_updated", EpisodeEnqueueOptions{Conn: tx.Conn()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return updated, nil
}

// ListDeleteOperations 列出删除操作
func (s *Store) ListDeleteOperations(limit int, mode string) ([]map[string]any, error) {
	where := ""
	params := make([]any, 0, 2)
	modeToken := strings.ToLower(strings.TrimSpace(mode))
	if modeToken != "" {
		where = "WHERE LOWER(mode) = ?"
		params = append(params, modeToken)
	}
	if limit < 1 {
		limit = 50
	}
	params = append(params, limit)
	query := fmt.Sprintf("SELECT operation_id, mode, selector, reason, requested_by, status, created_at, restored_at, summary_json "+
		"FROM delete_operations %s ORDER BY created_at DESC LIMIT ?", where)
	rows, err := s.Query(query, params...)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		payload := make(map[string]any, len(row)+2)
		for key, value := range row {
			payload[key] = value
		}
		payload["selector"] = jsonLoads(payload["selector"], map[string]any{})
		payload["summary"] = jsonLoads(payload["summary_json"], map[string]any{})
		out = append(out, payload)
	}
	return out, nil
}

// GetDeleteOperation 读取删除操作及条目
func (s *Store) GetDeleteOperation(operationID string) (map[string]any, error) {
	token := strings.TrimSpace(operationID)
	if token == "" {
		return nil, nil
	}
	rows, err := s.Query("SELECT operation_id, mode, selector, reason, requested_by, status, created_at, restored_at, summary_json "+
		"FROM delete_operations WHERE operation_id = ? LIMIT 1", token)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	payload := make(map[string]any, len(rows[0])+3)
	for key, value := range rows[0] {
		payload[key] = value
	}
	payload["selector"] = jsonLoads(payload["selector"], map[string]any{})
	payload["summary"] = jsonLoads(payload["summary_json"], map[string]any{})

	itemRows, err := s.Query("SELECT item_type, item_hash, item_key, payload_json, created_at "+
		"FROM delete_operation_items WHERE operation_id = ? ORDER BY id ASC", token)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(itemRows))
	for _, item := range itemRows {
		items = append(items, map[string]any{
			"item_type":  stringFromRow(item, "item_type"),
			"item_hash":  stringFromRow(item, "item_hash"),
			"item_key":   stringFromRow(item, "item_key"),
			"payload":    jsonLoads(item["payload_json"], map[string]any{}),
			"created_at": item["created_at"],
		})
	}
	payload["items"] = items
	return payload, nil
}

// PurgeDeletedRelations 清理过期的软删除关系，返回被清除的 hash
func (s *Store) PurgeDeletedRelations(cutoffTime float64, limit int) ([]string, error) {
	if limit < 1 {
		limit = 1000
	}
	rows, err := s.Query("SELECT hash FROM deleted_relations WHERE deleted_at IS NOT NULL AND deleted_at < ? "+
		"ORDER BY deleted_at ASC LIMIT ?", cutoffTime, limit)
	if err != nil {
		return nil, err
	}
	hashes := make([]string, 0, len(rows))
	for _, row := range rows {
		token := strings.TrimSpace(stringFromRow(row, "hash"))
		if token != "" {
			hashes = append(hashes, token)
		}
	}
	if len(hashes) == 0 {
		return []string{}, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf("DELETE FROM deleted_relations WHERE hash IN (%s)", sqlPlaceholders(len(hashes)))
	if _, err := c.Exec(query, stringArgs(hashes)...); err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	return hashes, nil
}
