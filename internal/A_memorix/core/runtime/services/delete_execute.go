package services

// 删除执行、恢复与清空，对齐 Python delete_admin_service.py 的执行段

import (
	"context"
	"fmt"
	"strings"
	"time"

	"MaiGo/internal/A_memorix/core/storage"
)

// executeDeleteAction 对齐 _execute_delete_action（事务化执行删除计划）
func (s *DeleteAdminService) executeDeleteAction(ctx context.Context, mode string, selector map[string]any, reason string, requestedBy string, payload map[string]any) (map[string]any, error) {
	expectedStates := payload["expected_relation_states"]
	operationID := ""
	affectedSources := []string{}
	deletedVectorCount := 0
	staleRelationHashes := []string{}
	planResult := map[string]any{}
	txErr := s.Meta().Transaction(true, func(tx *storage.Tx) error {
		plan := s.buildDeletePlan(mode, selector)
		if !plan.Success {
			planResult = map[string]any{"success": false, "error": plan.ErrorMessage}
			return nil
		}
		items := plan.Items
		if expectedStates != nil {
			if mode != "relation" {
				planResult = map[string]any{"success": false, "error": "expected_relation_states 仅允许用于 relation 删除"}
				return nil
			}
			eligible, stale, filterErr := s.filterRelationDeletionPreconditions(items, expectedStates)
			if filterErr != nil {
				return filterErr
			}
			staleRelationHashes = stale
			if len(stale) > 0 {
				staleSet := map[string]struct{}{}
				for _, hashValue := range stale {
					staleSet[hashValue] = struct{}{}
				}
				filteredItems := make([]map[string]any, 0, len(eligible))
				for _, item := range items {
					if _, blocked := staleSet[asString(item["item_hash"])]; blocked {
						continue
					}
					filteredItems = append(filteredItems, item)
				}
				items = filteredItems
				plan.TargetHashes["relations"] = eligible
				plan.VectorIDs["relations"] = eligible
				plan.Counts["relations"] = len(eligible)
			}
			if len(items) == 0 {
				planResult = map[string]any{
					"success": true, "mode": mode, "operation_id": "", "counts": plan.Counts,
					"deleted_count": 0, "deleted_relation_count": 0,
					"stale_relation_hashes": stale, "skipped_due_to_concurrent_change": len(stale),
				}
				return nil
			}
		}

		// 实体删除时把关联段落来源并入受影响来源
		if mode == "entity" || mode == "mixed" {
			paragraphHashesForEntity := []string{}
			for _, item := range items {
				if asString(item["item_type"]) != "entity" {
					continue
				}
				entityPayload := asMap(item["payload"])
				entity := asMap(entityPayload["entity"])
				if entity == nil {
					continue
				}
				rows, err := s.Meta().GetParagraphsByEntity(rowString(entity, "name"))
				if err != nil {
					continue
				}
				for _, row := range rows {
					if source := strings.TrimSpace(rowString(row, "source")); source != "" {
						affectedSources = append(affectedSources, source)
					}
					paragraphHashesForEntity = append(paragraphHashesForEntity, rowString(row, "hash"))
				}
			}
			if len(paragraphHashesForEntity) > 0 {
				affectedSources = append(affectedSources, s.Meta().GetSourcesForParagraphHashes(dedupeStrings(paragraphHashesForEntity), true, tx.Conn())...)
			}
		}
		affectedSources = append(affectedSources, plan.Sources...)
		affectedSources = dedupeStrings(affectedSources)

		// 执行前以事务内当前状态刷新事实证据快照，避免计划生成后证据变化导致恢复不完整
		if paragraphHashes := plan.TargetHashes["paragraphs"]; len(paragraphHashes) > 0 {
			currentSnapshot, err := s.Meta().SnapshotFactEvidenceForParagraphs(paragraphHashes, tx.Conn())
			if err != nil {
				return err
			}
			for _, item := range items {
				if asString(item["item_type"]) != "paragraph" {
					continue
				}
				paragraphHash := strings.TrimSpace(asString(item["item_hash"]))
				payload := asMap(item["payload"])
				if payload == nil {
					payload = map[string]any{}
				}
				filteredEvidence := make([]any, 0)
				for _, value := range asList(currentSnapshot["evidence"]) {
					evidence := asMap(value)
					if evidence != nil && strings.TrimSpace(asString(evidence["evidence_id"])) == paragraphHash {
						filteredEvidence = append(filteredEvidence, evidence)
					}
				}
				payload["fact_evidence_snapshot"] = map[string]any{
					"paragraph_hashes": []string{paragraphHash},
					"claims":           currentSnapshot["claims"],
					"evidence":         filteredEvidence,
				}
				item["payload"] = payload
			}
		}

		createInput := storage.DeleteOperationInput{
			Mode:        mode,
			Selector:    selector,
			Items:       items,
			Reason:      reason,
			RequestedBy: requestedBy,
			Status:      "prepared",
			Summary: map[string]any{
				"counts":     plan.Counts,
				"sources":    affectedSources,
				"vector_ids": plan.VectorIDs,
				"state":      "prepared",
			},
		}
		operation, err := s.Meta().CreateDeleteOperation(createInput)
		if err != nil {
			return err
		}
		operationID = rowString(operation, "operation_id")

		paragraphHashes := plan.TargetHashes["paragraphs"]
		entityHashes := plan.TargetHashes["entities"]
		relationHashes := plan.TargetHashes["relations"]

		deleteReason := normalizeDeleteReason(reason)
		if len(paragraphHashes) > 0 {
			deleted, err := s.Meta().MarkAsDeleted(paragraphHashes, "paragraph", deleteReason)
			if err != nil {
				return err
			}
			if deleted != len(paragraphHashes) {
				return fmt.Errorf("段落删除计划在提交前已发生变化")
			}
			for _, hashValue := range paragraphHashes {
				if _, err := tx.Exec("DELETE FROM paragraph_entities WHERE paragraph_hash = ?", hashValue); err != nil {
					return err
				}
				if _, err := tx.Exec("DELETE FROM paragraph_relations WHERE paragraph_hash = ?", hashValue); err != nil {
					return err
				}
			}
			if _, err := s.Meta().DetachFactEvidenceForParagraphsOnConn(paragraphHashes, deleteReason, nil, tx.Conn()); err != nil {
				return err
			}
			s.Meta().DeleteParagraphNgramsIfReady(paragraphHashes, -1, tx.Conn())
			for _, hashValue := range paragraphHashes {
				s.Meta().FtsDeleteTokenizedParagraph(hashValue, tx.Conn())
			}
			if _, err := s.Meta().DeleteExternalMemoryRefsByParagraphs(paragraphHashes); err != nil {
				return err
			}
		}
		if len(entityHashes) > 0 {
			deleted, err := s.Meta().MarkAsDeleted(entityHashes, "entity", deleteReason)
			if err != nil {
				return err
			}
			if deleted != len(entityHashes) {
				return fmt.Errorf("实体删除计划在提交前已发生变化")
			}
			for _, hashValue := range entityHashes {
				if _, err := tx.Exec("DELETE FROM paragraph_entities WHERE entity_hash = ?", hashValue); err != nil {
					return err
				}
			}
		}
		if len(relationHashes) > 0 {
			deleted, err := s.Meta().BackupAndDeleteRelations(relationHashes)
			if err != nil {
				return err
			}
			if deleted != len(relationHashes) {
				return fmt.Errorf("关系删除计划在提交前已发生变化")
			}
		}
		if len(affectedSources) > 0 {
			s.Meta().EnqueueEpisodeSourceRebuilds(affectedSources, "delete_admin_execute", storage.EpisodeEnqueueOptions{Conn: tx.Conn()})
		}
		jobs := buildDeleteCleanupJobs(plan.TargetHashes)
		if len(jobs) > 0 {
			if _, err := s.Meta().EnqueueStorageCleanupJobs(operationID, jobs, tx.Conn()); err != nil {
				return err
			}
		}
		if _, err := s.Meta().UpdateDeleteOperationState(operationID, "pending_cleanup", map[string]any{
			"state": "pending_cleanup", "cleanup_jobs": len(jobs),
		}, tx.Conn()); err != nil {
			return err
		}
		s.enqueueProfileRefreshes(items, "delete_admin_execute", tx.Conn())

		errorMessage := ""
		if len(entityHashes) == 0 && len(relationHashes) == 0 && len(paragraphHashes) == 0 && plan.MatchedSourceCount == 0 {
			errorMessage = "未命中可删除内容"
		}
		planResult = buildStandardDeleteResult(mode, operationID, plan.Counts, affectedSources,
			len(entityHashes), len(relationHashes), len(paragraphHashes), plan.MatchedSourceCount,
			0, plan.RequestedSourceCount, plan.MatchedSourceCount, errorMessage)
		return nil
	})
	if txErr != nil {
		s.LoggerWarn("delete_admin execute 失败: %v", txErr)
		result := buildStandardDeleteResult(mode, operationID, map[string]any{}, affectedSources, 0, 0, 0, 0, 0, 0, 0, txErr.Error())
		return result, txErr
	}
	if len(planResult) == 0 {
		return map[string]any{"success": false, "error": "删除计划未生成"}, nil
	}
	if !pyTruthy(planResult["success"]) {
		return planResult, nil
	}
	cleanup := map[string]any{}
	if s.cleanup != nil {
		summary, err := s.cleanup.ProcessPendingStorageCleanupJobs(ctx, operationID, 100)
		if err != nil {
			planResult["cleanup_error"] = err.Error()
		} else {
			cleanup = summary
			deletedVectorCount = rowInt(summary, "deleted_vectors")
			planResult["deleted_vector_count"] = deletedVectorCount
		}
	}
	planResult["cleanup"] = cleanup
	planResult["stale_relation_hashes"] = staleRelationHashes
	planResult["skipped_due_to_concurrent_change"] = len(staleRelationHashes)
	planResult["profile_refresh_person_ids"] = s.profilePersonIDs(planResult)
	return planResult, nil
}

// filterRelationDeletionPreconditions 对齐 _filter_relation_deletion_preconditions
func (s *DeleteAdminService) filterRelationDeletionPreconditions(items []map[string]any, expectedStates any) ([]string, []string, error) {
	if expectedStates == nil {
		hashes := make([]string, 0, len(items))
		for _, item := range items {
			if hashValue := strings.TrimSpace(asString(item["item_hash"])); hashValue != "" {
				hashes = append(hashes, hashValue)
			}
		}
		return hashes, nil, nil
	}
	states, ok := expectedStates.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("expected_relation_states 必须是以关系 hash 为键的对象")
	}
	hashes := make([]string, 0, len(states))
	for hashValue := range states {
		hashes = append(hashes, hashValue)
	}
	eligible := []string{}
	stale := []string{}
	requiredKeys := []string{
		"expected_lifecycle_revision", "expected_retention_strength", "expected_retention_anchor_at",
		"expected_inactive_since", "expected_inactive_reason", "expected_is_inactive",
		"expected_is_permanent", "expected_is_pinned", "expected_protected_until",
	}
	for start := 0; start < len(hashes); start += 900 {
		end := start + 900
		if end > len(hashes) {
			end = len(hashes)
		}
		batch := hashes[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		params := make([]any, 0, len(batch))
		for _, hashValue := range batch {
			params = append(params, hashValue)
		}
		rows, err := s.Meta().Query("SELECT hash, lifecycle_revision, retention_strength, retention_anchor_at, inactive_since, inactive_reason, is_inactive, is_permanent, is_pinned, protected_until FROM relations WHERE hash IN ("+placeholders+")", params...)
		if err != nil {
			continue
		}
		byHash := make(map[string]map[string]any, len(rows))
		for _, row := range rows {
			byHash[rowString(row, "hash")] = row
		}
		for _, hashValue := range batch {
			expected, ok := states[hashValue].(map[string]any)
			if !ok {
				stale = append(stale, hashValue)
				continue
			}
			missing := false
			for _, key := range requiredKeys {
				if _, exists := expected[key]; !exists {
					missing = true
					break
				}
			}
			row, exists := byHash[hashValue]
			if missing || !exists {
				stale = append(stale, hashValue)
				continue
			}
			if relationPreconditionMatches(row, expected) {
				eligible = append(eligible, hashValue)
			} else {
				stale = append(stale, hashValue)
			}
		}
	}
	return eligible, stale, nil
}

// relationPreconditionMatches 逐字段比较关系乐观锁条件
func relationPreconditionMatches(row map[string]any, expected map[string]any) bool {
	pairs := [][2]string{
		{"expected_lifecycle_revision", "lifecycle_revision"},
		{"expected_retention_strength", "retention_strength"},
		{"expected_retention_anchor_at", "retention_anchor_at"},
		{"expected_inactive_since", "inactive_since"},
		{"expected_inactive_reason", "inactive_reason"},
		{"expected_is_inactive", "is_inactive"},
		{"expected_is_permanent", "is_permanent"},
		{"expected_is_pinned", "is_pinned"},
		{"expected_protected_until", "protected_until"},
	}
	for _, pair := range pairs {
		if !valuesEqual(expected[pair[0]], row[pair[1]]) {
			return false
		}
	}
	return true
}

// valuesEqual 比较期望值与行值，兼容 nil、布尔与数值
func valuesEqual(expected any, actual any) bool {
	if expected == nil || actual == nil {
		return expected == nil && actual == nil
	}
	if expectedBool, ok := expected.(bool); ok {
		return expectedBool == rowBool(map[string]any{"value": actual}, "value")
	}
	if expectedText, ok := expected.(string); ok {
		return expectedText == asString(actual)
	}
	expectedNumber, expectedOK := asFloat(expected)
	actualNumber, actualOK := asFloat(actual)
	if expectedOK && actualOK {
		return expectedNumber == actualNumber
	}
	return false
}

// buildDeleteCleanupJobs 对齐 _delete_cleanup_jobs
func buildDeleteCleanupJobs(targetHashes map[string][]string) []storage.CleanupJobSpec {
	jobs := []storage.CleanupJobSpec{}
	for _, pair := range [][2]string{{"paragraph", "paragraphs"}, {"entity", "entities"}, {"relation", "relations"}} {
		hashes := targetHashes[pair[1]]
		if len(hashes) == 0 {
			continue
		}
		jobs = append(jobs, storage.CleanupJobSpec{
			ResourceType:  pair[0],
			ResourceID:    "batch",
			Action:        "vector_delete",
			Payload:       map[string]any{pair[0] + "_hashes": hashes},
			ExpectedState: map[string]any{"operation_status": "pending_cleanup"},
		})
	}
	if len(jobs) > 0 {
		jobs = append(jobs, storage.CleanupJobSpec{
			ResourceType:  "graph",
			ResourceID:    "structure",
			Action:        "graph_rebuild",
			Payload:       map[string]any{},
			ExpectedState: map[string]any{"operation_status": "pending_cleanup"},
		})
	}
	return jobs
}

// enqueueProfileRefreshes 对齐 _enqueue_delete_profile_refreshes
func (s *DeleteAdminService) enqueueProfileRefreshes(items []map[string]any, reason string, conn *storage.Conn) []string {
	personIDs := s.profilePersonIDsForItems(items)
	for _, personID := range personIDs {
		s.Meta().EnqueuePersonProfileRefresh(storage.PersonProfileRefreshEnqueueInput{
			PersonID: personID, Reason: reason, Conn: conn,
		})
	}
	return personIDs
}

// profilePersonIDsForItems 收集删除项关联的人物 ID
func (s *DeleteAdminService) profilePersonIDsForItems(items []map[string]any) []string {
	out := []string{}
	seen := map[string]struct{}{}
	appendID := func(value string) {
		token := strings.TrimSpace(value)
		if token == "" {
			return
		}
		if _, ok := seen[token]; ok {
			return
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	for _, item := range items {
		payload := asMap(item["payload"])
		paragraphHashes := []string{}
		switch asString(item["item_type"]) {
		case "paragraph":
			paragraphHashes = append(paragraphHashes, asString(item["item_hash"]))
		case "relation":
			for _, hashValue := range asStringSlice(payload["paragraph_hashes"]) {
				paragraphHashes = append(paragraphHashes, hashValue)
			}
		case "entity":
			for _, linkValue := range asList(payload["paragraph_links"]) {
				link := asMap(linkValue)
				if hashValue := rowString(link, "paragraph_hash"); hashValue != "" {
					paragraphHashes = append(paragraphHashes, hashValue)
				}
			}
		}
		for _, hashValue := range paragraphHashes {
			row, err := s.Meta().GetParagraph(hashValue)
			if err != nil || row == nil {
				continue
			}
			metadata := jsonObject(rowString(row, "metadata"))
			for _, key := range []string{"person_ids", "person_id", "target_person_id"} {
				switch typed := metadata[key].(type) {
				case string:
					appendID(typed)
				case []any:
					for _, value := range typed {
						appendID(asString(value))
					}
				}
			}
			source := rowString(row, "source")
			if strings.HasPrefix(source, "person_fact:") {
				appendID(strings.TrimPrefix(source, "person_fact:"))
			}
		}
	}
	return out
}

// profilePersonIDs 从执行结果反推人物刷新 ID，供返回结构补齐
func (s *DeleteAdminService) profilePersonIDs(result map[string]any) []string {
	if ids, ok := result["profile_refresh_person_ids"].([]string); ok {
		return ids
	}
	return []string{}
}

// purgeDeletedMemory 对齐 _purge_deleted_memory
func (s *DeleteAdminService) purgeDeletedMemory(graceHours *float64, limit int) map[string]any {
	grace := 24.0
	if graceHours != nil {
		// 显式传入时不抬到 1，对齐 Python float(grace_hours)
		grace = *graceHours
	} else if configured, ok := asFloat(s.Cfg("memory.orphan.sweep_grace_hours", 24)); ok {
		if configured > 0 {
			grace = configured
		}
		if grace < 1 {
			grace = 1
		}
	}
	if limit < 1 {
		limit = 1000
	}
	now := float64(time.Now().UnixNano()) / 1e9
	cutoff := now - grace*3600
	relationRows, err := s.Meta().Query(
		"SELECT hash FROM deleted_relations WHERE deleted_at IS NOT NULL AND deleted_at < ? ORDER BY deleted_at ASC LIMIT ?",
		cutoff, limit,
	)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	relationCandidates := make([]string, 0, len(relationRows))
	for _, row := range relationRows {
		if hashValue := strings.TrimSpace(rowString(row, "hash")); hashValue != "" {
			relationCandidates = append(relationCandidates, hashValue)
		}
	}
	paragraphItems, err := s.Meta().SweepDeletedItems("paragraph", grace*3600)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	entityItems, err := s.Meta().SweepDeletedItems("entity", grace*3600)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	requested := map[string][]string{
		"relation":  relationCandidates,
		"paragraph": make([]string, 0, len(paragraphItems)),
		"entity":    make([]string, 0, len(entityItems)),
	}
	for _, item := range paragraphItems {
		requested["paragraph"] = append(requested["paragraph"], item.Hash)
	}
	for _, item := range entityItems {
		requested["entity"] = append(requested["entity"], item.Hash)
	}
	backedUp := map[string][]string{"relation": {}, "paragraph": {}, "entity": {}}
	for itemType, hashes := range requested {
		normalized := dedupeStrings(hashes)
		if len(normalized) == 0 {
			continue
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(normalized)), ",")
		params := make([]any, 0, len(normalized)+1)
		params = append(params, itemType)
		for _, hashValue := range normalized {
			params = append(params, hashValue)
		}
		rows, queryErr := s.Meta().Query(
			"SELECT DISTINCT item_hash FROM delete_operation_items WHERE item_type = ? AND item_hash IN ("+placeholders+")",
			params...,
		)
		if queryErr != nil {
			continue
		}
		matched := make([]string, 0, len(rows))
		for _, row := range rows {
			if hashValue := strings.TrimSpace(rowString(row, "item_hash")); hashValue != "" {
				matched = append(matched, hashValue)
			}
		}
		backedUp[itemType] = dedupeStrings(matched)
	}
	paragraphHashes := backedUp["paragraph"]
	entityHashes := backedUp["entity"]
	deletedRelationHashes := backedUp["relation"]
	txErr := s.Meta().Transaction(true, func(tx *storage.Tx) error {
		if len(paragraphHashes) > 0 {
			if _, err := s.Meta().PhysicallyDeleteParagraphs(paragraphHashes); err != nil {
				return err
			}
		}
		if len(entityHashes) > 0 {
			if _, err := s.Meta().PhysicallyDeleteEntities(entityHashes); err != nil {
				return err
			}
		}
		if len(deletedRelationHashes) > 0 {
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(deletedRelationHashes)), ",")
			params := make([]any, 0, len(deletedRelationHashes))
			for _, hashValue := range deletedRelationHashes {
				params = append(params, hashValue)
			}
			if _, err := tx.Exec("DELETE FROM deleted_relations WHERE hash IN ("+placeholders+")", params...); err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		return map[string]any{"success": false, "error": txErr.Error()}
	}
	skipped := map[string]any{}
	for itemType, hashes := range requested {
		skipped[itemType] = len(dedupeStrings(hashes)) - len(backedUp[itemType])
	}
	return map[string]any{
		"success":                  true,
		"grace_hours":              grace,
		"purged_deleted_relations": deletedRelationHashes,
		"purged_paragraph_hashes":  paragraphHashes,
		"purged_entity_hashes":     entityHashes,
		"purged_counts": map[string]any{
			"relations":  len(deletedRelationHashes),
			"paragraphs": len(paragraphHashes),
			"entities":   len(entityHashes),
		},
		"skipped_without_snapshot": skipped,
	}
}

// normalizeDeleteReason 对齐 mark_as_deleted(reason=str(reason or "user_delete").strip())
func normalizeDeleteReason(reason string) string {
	text := strings.TrimSpace(reason)
	if text == "" {
		return "user_delete"
	}
	return text
}
