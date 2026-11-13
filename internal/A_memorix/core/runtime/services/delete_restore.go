package services

// 删除恢复，对齐 Python delete_admin_service.py 的恢复段

import (
	"context"
	"fmt"
	"strings"
	"time"

	"maibot-go/internal/A_memorix/core/storage"
)

// restoreDeleteAction 对齐 _restore_delete_action
func (s *DeleteAdminService) restoreDeleteAction(ctx context.Context, mode string, selector map[string]any, operationID string, requestedBy string, reason string) map[string]any {
	if strings.TrimSpace(operationID) != "" {
		operation, err := s.Meta().GetDeleteOperation(operationID)
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}
		}
		if operation == nil {
			return map[string]any{"success": false, "error": "operation 不存在"}
		}
		return s.restoreDeleteOperation(ctx, operation)
	}
	if mode != "relation" {
		return map[string]any{"success": false, "error": "paragraph/entity/source 恢复必须提供 operation_id"}
	}
	target := firstNonEmpty(selector["query"], selector["target"], selector["hash"])
	hashes := s.resolveDeletedRelationHashes(selector)
	if len(hashes) == 0 && target != "" {
		return map[string]any{"success": false, "error": "未命中可恢复关系"}
	}
	if len(hashes) == 0 {
		return map[string]any{"success": false, "error": "未命中可恢复关系"}
	}
	if reason == "" {
		reason = "relation_restore"
	}
	return s.restoreDeletedRelations(ctx, hashes, requestedBy, reason)
}

// restoreDeletedRelations 对齐 _restore_deleted_relations_serialized
func (s *DeleteAdminService) restoreDeletedRelations(ctx context.Context, hashes []string, requestedBy string, reason string) map[string]any {
	tokensList := dedupeStrings(hashes)
	if len(tokensList) == 0 {
		return map[string]any{"success": false, "error": "未命中可恢复关系"}
	}
	operationID := "restore_" + randomToken()
	failures := []any{}
	restored := []string{}
	paragraphRelations := map[string][]string{}
	restoreJobs := []storage.CleanupJobSpec{}
	operationResult := map[string]any{}

	txErr := s.Meta().Transaction(true, func(tx *storage.Tx) error {
		for _, hashValue := range tokensList {
			deletedRow, err := s.Meta().GetDeletedRelation(hashValue)
			if err != nil || deletedRow == nil {
				failures = append(failures, map[string]any{"hash": hashValue, "error": "relation 不存在"})
				continue
			}
			payload := s.latestDeleteOperationPayload(hashValue)
			paragraphHashes := asStringSlice(payload["paragraph_hashes"])
			if source := strings.TrimSpace(rowString(deletedRow, "source_paragraph")); source != "" {
				paragraphHashes = append(paragraphHashes, source)
			}
			paragraphRelations[hashValue] = dedupeStrings(paragraphHashes)
			restored = append(restored, hashValue)
		}
		if len(restored) == 0 {
			return nil
		}
		if _, err := s.Meta().SupersedeDeleteCleanupResources("relation", restored, tx.Conn()); err != nil {
			return err
		}
		items := make([]map[string]any, 0, len(restored))
		for _, hashValue := range restored {
			deletedRow, err := s.Meta().GetDeletedRelation(hashValue)
			if err != nil || deletedRow == nil {
				continue
			}
			row := map[string]any{}
			for key, value := range deletedRow {
				row[key] = value
			}
			delete(row, "deleted_at")
			state := "none"
			if s.relationVectorsEnabled {
				state = "pending"
			}
			row["vector_state"] = state
			row["vector_error"] = nil
			row["vector_updated_at"] = nil
			items = append(items, map[string]any{
				"item_type": "relation", "item_hash": hashValue, "item_key": hashValue,
				"payload": map[string]any{"relation": row, "paragraph_hashes": paragraphRelations[hashValue]},
			})
			restoreJobs = append(restoreJobs, storage.CleanupJobSpec{
				ResourceType: "relation", ResourceID: hashValue, Action: "vector_upsert",
				Payload:       map[string]any{"item": row},
				ExpectedState: map[string]any{"operation_status": "restore_pending"},
			})
		}
		restoreJobs = append(restoreJobs, storage.CleanupJobSpec{
			ResourceType: "graph", ResourceID: "restore", Action: "graph_restore",
			Payload:       map[string]any{},
			ExpectedState: map[string]any{"operation_status": "restore_pending"},
		})
		createInput := storage.DeleteOperationInput{
			Mode:        "relation_restore",
			Selector:    map[string]any{"hashes": restored},
			Items:       items,
			Reason:      reason,
			RequestedBy: requestedBy,
			Status:      "restore_pending",
			Summary: map[string]any{
				"state": "restore_pending", "restore_jobs": len(restoreJobs),
			},
			OperationID: &operationID,
		}
		operation, err := s.Meta().CreateDeleteOperation(createInput)
		if err != nil {
			return err
		}
		operationResult = operation

		for _, item := range items {
			payload := asMap(item["payload"])
			row := asMap(payload["relation"])
			hashValue := asString(item["item_hash"])
			if _, err := s.Meta().RestoreTableRowFromSnapshot("relations", row, tx.Conn()); err != nil {
				return err
			}
			if _, err := tx.Exec("DELETE FROM deleted_relations WHERE hash = ?", hashValue); err != nil {
				return err
			}
			for _, paragraphHash := range paragraphRelations[hashValue] {
				if _, err := tx.Exec(
					`INSERT OR IGNORE INTO paragraph_relations (paragraph_hash, relation_hash)
					 SELECT ?, ? WHERE EXISTS (SELECT 1 FROM paragraphs WHERE hash = ? AND COALESCE(is_deleted, 0) = 0)`,
					paragraphHash, hashValue, paragraphHash); err != nil {
					return err
				}
			}
		}
		if len(restoreJobs) > 0 {
			if _, err := s.Meta().EnqueueStorageCleanupJobs(operationID, restoreJobs, tx.Conn()); err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		return map[string]any{"success": false, "error": txErr.Error(), "failures": failures}
	}
	if len(operationResult) == 0 {
		return map[string]any{
			"success": false, "restored_hashes": []string{}, "restored_count": 0,
			"failures": failures, "error": "未命中可恢复关系",
		}
	}
	if len(restored) > 0 {
		for _, hashValue := range restored {
			s.Meta().EnqueuePersonProfileRefresh(storage.PersonProfileRefreshEnqueueInput{
				PersonID: "", Reason: "delete_admin_relation_restore",
			})
			_ = hashValue
			break
		}
	}
	cleanup := map[string]any{}
	if s.cleanup != nil {
		if summary, err := s.cleanup.ProcessPendingStorageCleanupJobs(ctx, operationID, 100); err == nil {
			cleanup = summary
		}
	}
	refreshed, err := s.Meta().GetDeleteOperation(operationID)
	status := ""
	if err == nil && refreshed != nil {
		status = rowString(refreshed, "status")
	}
	return map[string]any{
		"success":         status == "restored",
		"operation_id":    operationID,
		"status":          status,
		"restored_hashes": restored,
		"restored_count":  len(restored),
		"failures":        failures,
		"cleanup":         cleanup,
	}
}

// restoreDeleteOperation 对齐 _restore_delete_operation_serialized
func (s *DeleteAdminService) restoreDeleteOperation(ctx context.Context, operation map[string]any) map[string]any {
	operationID := rowString(operation, "operation_id")
	status := rowString(operation, "status")
	if status == "restore_pending" || status == "restored" {
		cleanup := map[string]any{}
		if s.cleanup != nil && status == "restore_pending" {
			if summary, err := s.cleanup.ProcessPendingStorageCleanupJobs(ctx, operationID, 100); err == nil {
				cleanup = summary
			}
		}
		refreshed, _ := s.Meta().GetDeleteOperation(operationID)
		finalStatus := status
		if refreshed != nil {
			finalStatus = rowString(refreshed, "status")
		}
		return map[string]any{"success": finalStatus == "restored", "operation_id": operationID, "cleanup": cleanup, "status": finalStatus}
	}
	if status != "pending_cleanup" && status != "completed" {
		return map[string]any{"success": false, "operation_id": operationID, "status": status,
			"error": fmt.Sprintf("operation 当前状态不允许恢复: %s", status)}
	}
	items := operationItems(operation)
	restoredEntities := 0
	restoredParagraphs := 0
	restoredRelations := 0
	sources := []string{}
	restoreJobs := []storage.CleanupJobSpec{}
	var txErr error
	txErr = s.Meta().Transaction(true, func(tx *storage.Tx) error {
		if _, err := s.Meta().CancelDeleteCleanupJobs(operationID, tx.Conn()); err != nil {
			return err
		}
		for _, item := range items {
			payload := asMap(item["payload"])
			itemHash := asString(item["item_hash"])
			switch asString(item["item_type"]) {
			case "entity":
				entity := asMap(payload["entity"])
				if entity == nil {
					continue
				}
				if _, err := s.Meta().RestoreTableRowFromSnapshot("entities", entity, tx.Conn()); err != nil {
					return err
				}
				restoredEntities++
				restoreJobs = append(restoreJobs, storage.CleanupJobSpec{
					ResourceType: "entity", ResourceID: itemHash, Action: "vector_upsert",
					Payload: map[string]any{"item": entity}, ExpectedState: map[string]any{"operation_status": "restore_pending"},
				})
				for _, linkValue := range asList(payload["paragraph_links"]) {
					link := asMap(linkValue)
					if link == nil {
						continue
					}
					if _, err := tx.Exec("INSERT OR IGNORE INTO paragraph_entities (paragraph_hash, entity_hash, mention_count) VALUES (?, ?, ?)",
						rowString(link, "paragraph_hash"), rowString(link, "entity_hash"), rowInt(link, "mention_count")); err != nil {
						return err
					}
				}
			case "paragraph":
				paragraph := asMap(payload["paragraph"])
				if paragraph == nil {
					continue
				}
				if _, err := s.Meta().RestoreTableRowFromSnapshot("paragraphs", paragraph, tx.Conn()); err != nil {
					return err
				}
				restoredParagraphs++
				s.Meta().UpsertParagraphNgramIfReady(itemHash, rowString(paragraph, "content"), 1, tx.Conn())
				s.Meta().FtsUpsertTokenizedParagraph(itemHash, tx.Conn())
				restoreJobs = append(restoreJobs, storage.CleanupJobSpec{
					ResourceType: "paragraph", ResourceID: itemHash, Action: "vector_upsert",
					Payload: map[string]any{"item": paragraph}, ExpectedState: map[string]any{"operation_status": "restore_pending"},
				})
				for _, linkValue := range asList(payload["entity_links"]) {
					link := asMap(linkValue)
					if link == nil {
						continue
					}
					if _, err := tx.Exec("INSERT OR IGNORE INTO paragraph_entities (paragraph_hash, entity_hash, mention_count) VALUES (?, ?, ?)",
						rowString(link, "paragraph_hash"), rowString(link, "entity_hash"), rowInt(link, "mention_count")); err != nil {
						return err
					}
				}
				for _, relationHash := range asStringSlice(payload["relation_hashes"]) {
					if _, err := tx.Exec("INSERT OR IGNORE INTO paragraph_relations (paragraph_hash, relation_hash) VALUES (?, ?)",
						itemHash, relationHash); err != nil {
						return err
					}
				}
				if factSnapshot := asMap(payload["fact_evidence_snapshot"]); factSnapshot != nil {
					if _, err := s.Meta().RestoreFactEvidenceSnapshot(factSnapshot, "delete_operation_restored", nil, tx.Conn()); err != nil {
						return err
					}
				}
				if refs := asList(payload["external_refs"]); len(refs) > 0 {
					refMaps := make([]map[string]any, 0, len(refs))
					for _, ref := range refs {
						if refMap := asMap(ref); refMap != nil {
							refMaps = append(refMaps, refMap)
						}
					}
					if _, err := s.Meta().RestoreExternalMemoryRefs(refMaps); err != nil {
						return err
					}
				}
				if source := strings.TrimSpace(rowString(paragraph, "source")); source != "" {
					sources = append(sources, source)
				}
			case "relation":
				relation := asMap(payload["relation"])
				if relation == nil {
					continue
				}
				if _, err := s.Meta().RestoreTableRowFromSnapshot("relations", relation, tx.Conn()); err != nil {
					return err
				}
				restoredRelations++
				if _, err := tx.Exec("DELETE FROM deleted_relations WHERE hash = ?", itemHash); err != nil {
					return err
				}
				for _, paragraphHash := range asStringSlice(payload["paragraph_hashes"]) {
					if _, err := tx.Exec(
						`INSERT OR IGNORE INTO paragraph_relations (paragraph_hash, relation_hash)
						 SELECT ?, ? WHERE EXISTS (SELECT 1 FROM paragraphs WHERE hash = ? AND COALESCE(is_deleted, 0) = 0)`,
						paragraphHash, itemHash, paragraphHash); err != nil {
						return err
					}
				}
				if s.relationVectorsEnabled {
					restoreJobs = append(restoreJobs, storage.CleanupJobSpec{
						ResourceType: "relation", ResourceID: itemHash, Action: "vector_upsert",
						Payload: map[string]any{"item": relation}, ExpectedState: map[string]any{"operation_status": "restore_pending"},
					})
				}
			}
		}
		sources = dedupeStrings(sources)
		if len(sources) > 0 {
			s.Meta().EnqueueEpisodeSourceRebuilds(sources, "delete_admin_restore", storage.EpisodeEnqueueOptions{Conn: tx.Conn()})
		}
		total := restoredEntities + restoredParagraphs + restoredRelations
		if total > 0 {
			restoreJobs = append(restoreJobs, storage.CleanupJobSpec{
				ResourceType: "graph", ResourceID: "restore", Action: "graph_restore",
				Payload: map[string]any{}, ExpectedState: map[string]any{"operation_status": "restore_pending"},
			})
			if _, err := s.Meta().EnqueueStorageCleanupJobs(operationID, restoreJobs, tx.Conn()); err != nil {
				return err
			}
			if _, err := s.Meta().UpdateDeleteOperationState(operationID, "restore_pending", map[string]any{
				"state": "restore_pending", "restore_jobs": len(restoreJobs),
			}, tx.Conn()); err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		return map[string]any{"success": false, "error": txErr.Error()}
	}
	cleanup := map[string]any{}
	if s.cleanup != nil {
		if summary, err := s.cleanup.ProcessPendingStorageCleanupJobs(ctx, operationID, 100); err == nil {
			cleanup = summary
		}
	}
	refreshed, _ := s.Meta().GetDeleteOperation(operationID)
	finalStatus := ""
	if refreshed != nil {
		finalStatus = rowString(refreshed, "status")
	}
	return map[string]any{
		"success":                 finalStatus == "restored",
		"operation_id":            operationID,
		"restored_entities":       restoredEntities,
		"restored_paragraphs":     restoredParagraphs,
		"restored_relations":      restoredRelations,
		"sources":                 sources,
		"cleanup":                 cleanup,
		"restored_relation_count": restoredRelations,
		"relation_failures":       []any{},
	}
}

// latestDeleteOperationPayload 取该关系最近一次删除快照载荷
func (s *DeleteAdminService) latestDeleteOperationPayload(hashValue string) map[string]any {
	rows, err := s.Meta().Query(
		`SELECT payload_json FROM delete_operation_items WHERE item_hash = ?
		 ORDER BY rowid DESC LIMIT 1`, hashValue)
	if err != nil || len(rows) == 0 {
		return map[string]any{}
	}
	return jsonObject(rowString(rows[0], "payload_json"))
}

// operationItems 解析删除操作条目
func operationItems(operation map[string]any) []map[string]any {
	switch typed := operation["items"].(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if row := asMap(item); row != nil {
				out = append(out, row)
			}
		}
		return out
	default:
		return nil
	}
}

// randomToken 生成恢复操作随机后缀
func randomToken() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
