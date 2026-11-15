package services

// 记忆修正段落级联，对齐 Python correction_admin_service 的级联构建与执行

import (
	"strings"
	"time"

	"MaiGo/internal/A_memorix/core/storage"
)

// relationHasRemainingParagraphsCurrent 对齐 _relation_has_remaining_paragraphs
func relationHasRemainingParagraphsCurrent(meta Metadata, relationHash string, removing map[string]struct{}) bool {
	if meta == nil {
		return false
	}
	rows, err := meta.Query(
		`SELECT p.hash AS hash, p.metadata AS metadata FROM paragraph_relations pr
		 JOIN paragraphs p ON p.hash = pr.paragraph_hash
		 WHERE pr.relation_hash = ? AND COALESCE(p.is_deleted, 0) = 0`, relationHash)
	if err != nil {
		return false
	}
	now := float64(time.Now().UnixNano()) / 1e9
	for _, row := range rows {
		if _, ok := removing[rowString(row, "hash")]; ok {
			continue
		}
		metadata := jsonObject(rowString(row, "metadata"))
		change := asMap(metadata["memory_change"])
		validTo := optionalFloat(change["valid_to"])
		if validTo == nil || *validTo > now {
			return true
		}
	}
	return false
}

// buildFuzzyModifyParagraphCascadeCurrent 对齐 _build_fuzzy_modify_paragraph_cascade
func (s *CorrectionAdminService) buildFuzzyModifyParagraphCascadeCurrent(paragraphHash, reason string, previewOnly bool, planID string) map[string]any {
	paragraphToken := strings.TrimSpace(paragraphHash)
	if paragraphToken == "" {
		return map[string]any{"relations": []any{}, "entities": []any{}}
	}
	store, err := correctionStore(s.Meta())
	if err != nil {
		s.LoggerWarn("构建段落级联失败: %v", err)
		return map[string]any{"relations": []any{}, "entities": []any{}}
	}
	rawRelations, err := store.GetParagraphRelations(paragraphToken)
	if err != nil {
		s.LoggerWarn("读取段落关系失败: %v", err)
		rawRelations = nil
	}
	relationHashes := make([]string, 0, len(rawRelations))
	for _, relation := range rawRelations {
		if hashValue := strings.TrimSpace(rowString(relation, "hash")); hashValue != "" {
			relationHashes = append(relationHashes, hashValue)
		}
	}
	statuses := map[string]map[string]any{}
	if len(relationHashes) > 0 {
		if loaded, err := store.GetRelationStatusBatch(relationHashes); err == nil {
			statuses = loaded
		}
	}
	now := float64(time.Now().UnixNano()) / 1e9
	removing := map[string]struct{}{paragraphToken: {}}
	relations := make([]any, 0, len(rawRelations))
	for _, relation := range rawRelations {
		relationHash := strings.TrimSpace(rowString(relation, "hash"))
		if relationHash == "" {
			continue
		}
		status := statuses[relationHash]
		if status == nil {
			status = map[string]any{}
		}
		protectedUntil := 0.0
		if parsed := optionalFloat(status["protected_until"]); parsed != nil {
			protectedUntil = *parsed
		}
		isPinned := pyTruthy(status["is_pinned"])
		action := ""
		actionReason := ""
		switch {
		case isPinned || protectedUntil > now:
			action = "skipped_protected"
			if isPinned {
				actionReason = "relation_is_pinned"
			} else {
				actionReason = "relation_is_temporarily_protected"
			}
		case relationHasRemainingParagraphsCurrent(s.Meta(), relationHash, removing):
			action = "mark_stale_evidence"
			actionReason = "relation_has_other_active_paragraphs"
		default:
			action = "mark_inactive"
			actionReason = "only_supported_by_superseded_paragraph"
		}
		sourceOperationID := ""
		if strings.TrimSpace(planID) != "" {
			sourceOperationID = fuzzyModifyStaleSourceOperationID(planID, paragraphToken, relationHash)
		}
		var protectedValue any
		if protectedUntil != 0 {
			protectedValue = protectedUntil
		}
		relations = append(relations, map[string]any{
			"paragraph_hash": paragraphToken, "relation_hash": relationHash,
			"action": action, "reason": actionReason, "source_reason": reason,
			"subject": rowString(relation, "subject"), "predicate": rowString(relation, "predicate"),
			"object": rowString(relation, "object"), "is_pinned": isPinned,
			"protected_until": protectedValue, "is_inactive": pyTruthy(status["is_inactive"]),
			"inactive_since": status["inactive_since"], "preview_only": previewOnly,
			"source_operation_id": sourceOperationID,
		})
	}
	rawEntities, err := store.GetParagraphEntities(paragraphToken)
	if err != nil {
		rawEntities = nil
	}
	entities := make([]any, 0, len(rawEntities))
	for _, entity := range rawEntities {
		entityHash := strings.TrimSpace(rowString(entity, "hash"))
		if entityHash == "" {
			continue
		}
		entities = append(entities, map[string]any{
			"paragraph_hash": paragraphToken, "entity_hash": entityHash,
			"action": "record_impact_only", "reason": "entity_state_has_no_superseded_semantics",
			"name":         firstNonEmpty(rowString(entity, "name"), rowString(entity, "entity")),
			"type":         firstNonEmpty(rowString(entity, "type"), rowString(entity, "entity_type")),
			"preview_only": previewOnly,
		})
	}
	return map[string]any{"relations": relations, "entities": entities}
}

// fuzzyModifyStaleSourceOperationID 对齐 _fuzzy_modify_stale_source_operation_id
func fuzzyModifyStaleSourceOperationID(planID, paragraphHash, relationHash string) string {
	return strings.TrimSpace(planID) + ":" + strings.TrimSpace(paragraphHash) + ":" + strings.TrimSpace(relationHash)
}

// executeFuzzyModifyParagraphCascadeCurrent 对齐 _execute_fuzzy_modify_paragraph_cascade
func (s *CorrectionAdminService) executeFuzzyModifyParagraphCascadeCurrent(paragraphHash, planID string, changedAt float64, reason string) map[string]any {
	store, err := correctionStore(s.Meta())
	result := map[string]any{
		"relations_marked_inactive": []any{}, "relations_marked_stale": []any{},
		"relations_skipped": []any{}, "impacted_entities": []any{}, "stale_mark_snapshots": []any{},
	}
	if err != nil {
		return result
	}
	cascade := s.buildFuzzyModifyParagraphCascadeCurrent(paragraphHash, reason, false, planID)
	result["impacted_entities"] = cascade["entities"]
	inactive := make([]any, 0)
	stale := make([]any, 0)
	skipped := make([]any, 0)
	snapshots := make([]any, 0)
	paragraphToken := strings.TrimSpace(paragraphHash)
	planToken := strings.TrimSpace(planID)
	for _, value := range asList(cascade["relations"]) {
		relation := asMap(value)
		relationHash := strings.TrimSpace(asString(relation["relation_hash"]))
		if relationHash == "" {
			continue
		}
		switch asString(relation["action"]) {
		case "skipped_protected":
			skipped = append(skipped, relation)
		case "mark_inactive":
			previous, _ := store.GetRelation(relationHash, true)
			previousMetadata := correctionCoerceMetadata(previous["metadata"])
			imageLinkIDs, _ := store.ListActiveImageMemoryLinkIDs("relation", []string{relationHash})
			patch := map[string]any{"memory_change": map[string]any{
				"change_id": planToken, "change_type": "paragraph_cascade_inactive",
				"changed_at": changedAt, "changed_by": "memory_correction", "reason": reason,
				"source_paragraph_hash": paragraphToken,
			}}
			updatedMetadata, updateErr := store.UpdateRelationMetadata(relationHash, patch, true)
			if updateErr != nil {
				s.LoggerWarn("更新关系元数据失败: %v", updateErr)
				continue
			}
			if err := store.MarkRelationsInactive([]string{relationHash}, &changedAt, ""); err != nil {
				s.LoggerWarn("标记关系失效失败: %v", err)
				continue
			}
			// 对齐 Python correction_admin_service.py:1000-1003：失效图片链接失败要暴露，不能当成功
			if _, invalidateErr := store.InvalidateImageMemoryLinks("relation", []string{relationHash}, nil); invalidateErr != nil {
				s.LoggerWarn("失效关系图片链接失败: %v", invalidateErr)
				result["image_link_errors"] = append(asList(result["image_link_errors"]), map[string]any{
					"target_type": "relation", "hash": relationHash, "error": invalidateErr.Error(),
				})
			}
			item := copyStringAnyMap(relation)
			item["previous_metadata"] = previousMetadata
			if updatedMetadata == nil {
				item["updated_metadata"] = map[string]any{}
			} else {
				item["updated_metadata"] = updatedMetadata
			}
			item["image_link_ids"] = imageLinkIDs
			item["previous_is_inactive"] = pyTruthy(previous["is_inactive"])
			item["previous_inactive_since"] = previous["inactive_since"]
			inactive = append(inactive, item)
		case "mark_stale_evidence":
			sourceOperationID := fuzzyModifyStaleSourceOperationID(planToken, paragraphToken, relationHash)
			previousMark, _ := store.GetParagraphStaleRelationMark(paragraphToken, relationHash, nil)
			staleReason := reason
			if staleReason == "" {
				staleReason = "memory_correction_paragraph_superseded"
			}
			written, writeErr := store.UpsertParagraphStaleRelationMark(storage.ParagraphStaleMarkUpsertOptions{
				ParagraphHash: paragraphToken, RelationHash: relationHash, Reason: staleReason,
				SourceType: "memory_correction", SourceID: planToken, SourceOperationID: sourceOperationID,
			}, nil)
			if writeErr != nil {
				s.LoggerWarn("写入陈旧关系标记失败: %v", writeErr)
				continue
			}
			snapshot := map[string]any{
				"paragraph_hash": paragraphToken, "relation_hash": relationHash,
				"source_type": "memory_correction", "source_id": planToken,
				"source_operation_id": sourceOperationID, "previous_mark": previousMark,
				"written_mark": written,
			}
			snapshots = append(snapshots, snapshot)
			item := copyStringAnyMap(relation)
			if written == nil {
				item["written_mark"] = map[string]any{}
			} else {
				item["written_mark"] = written
			}
			stale = append(stale, item)
		}
	}
	result["relations_marked_inactive"] = inactive
	result["relations_marked_stale"] = stale
	result["relations_skipped"] = skipped
	result["stale_mark_snapshots"] = snapshots
	return result
}

// markFuzzyModifyTargetSupersededCurrent 对齐 _mark_fuzzy_modify_target_superseded
func (s *CorrectionAdminService) markFuzzyModifyTargetSupersededCurrent(payload map[string]any, operation map[string]any, changeID string, changedAt float64, changedBy string, replacementHashes []string, planID, defaultReason string) map[string]any {
	if operation == nil {
		operation = payload
	}
	store, err := correctionStore(s.Meta())
	if err != nil {
		return map[string]any{}
	}
	targetType := strings.TrimSpace(asString(operation["target_type"]))
	hashValue := strings.TrimSpace(asString(operation["hash"]))
	if (targetType != "paragraph" && targetType != "relation") || hashValue == "" {
		return map[string]any{}
	}
	validTo := changedAt
	if parsed := optionalFloat(operation["valid_to"]); parsed != nil {
		validTo = *parsed
	}
	reason := strings.TrimSpace(asString(operation["reason"]))
	if reason == "" {
		reason = strings.TrimSpace(defaultReason)
	}
	patch := map[string]any{"memory_change": map[string]any{
		"change_id": changeID, "change_type": "mark_superseded", "changed_at": changedAt,
		"changed_by": changedBy, "reason": reason, "valid_to": validTo,
		"superseded_by_hashes": tokens(replacementHashes),
	}}
	if targetType == "paragraph" {
		previous, _ := store.GetParagraph(hashValue)
		if previous == nil {
			return map[string]any{}
		}
		previousMetadata := correctionCoerceMetadata(previous["metadata"])
		imageLinkIDs, _ := store.ListActiveImageMemoryLinkIDs("paragraph", []string{hashValue})
		updated, updateErr := store.UpdateParagraphMetadata(hashValue, patch, true)
		if updateErr != nil || updated == nil {
			return map[string]any{}
		}
		// 对齐 Python correction_admin_service.py:1189-1192：失效图片链接失败要暴露
		imageLinkErrors := make([]any, 0)
		if _, invalidateErr := store.InvalidateImageMemoryLinks("paragraph", []string{hashValue}, nil); invalidateErr != nil {
			s.LoggerWarn("失效段落图片链接失败: %v", invalidateErr)
			imageLinkErrors = append(imageLinkErrors, map[string]any{
				"target_type": "paragraph", "hash": hashValue, "error": invalidateErr.Error(),
			})
		}
		detachReason := reason
		if detachReason == "" {
			detachReason = "memory_correction_paragraph_superseded"
		}
		detached, detachErr := store.DetachFactEvidenceForParagraphs([]string{hashValue}, detachReason, &validTo)
		if detachErr != nil {
			s.LoggerWarn("解绑事实证据失败: %v", detachErr)
		}
		cascade := s.executeFuzzyModifyParagraphCascadeCurrent(hashValue, planID, changedAt, reason)
		result := map[string]any{
			"target_type": targetType, "hash": hashValue, "previous_metadata": previousMetadata,
			"updated_metadata": updated, "image_link_ids": imageLinkIDs,
			"fact_evidence_snapshot": detached, "cascade": cascade,
		}
		if len(imageLinkErrors) > 0 {
			result["image_link_errors"] = imageLinkErrors
		}
		return result
	}
	previous, _ := store.GetRelation(hashValue, true)
	if previous == nil {
		return map[string]any{}
	}
	previousMetadata := correctionCoerceMetadata(previous["metadata"])
	imageLinkIDs, _ := store.ListActiveImageMemoryLinkIDs("relation", []string{hashValue})
	updated, updateErr := store.UpdateRelationMetadata(hashValue, patch, true)
	if updateErr != nil || updated == nil {
		return map[string]any{}
	}
	// 对齐 Python correction_admin_service.py:1225-1228：失效与图片链接失败都要暴露
	imageLinkErrors := make([]any, 0)
	if markErr := store.MarkRelationsInactive([]string{hashValue}, &validTo, ""); markErr != nil {
		s.LoggerWarn("标记关系失效失败: %v", markErr)
		imageLinkErrors = append(imageLinkErrors, map[string]any{
			"target_type": "relation", "hash": hashValue, "error": markErr.Error(),
		})
	}
	if _, invalidateErr := store.InvalidateImageMemoryLinks("relation", []string{hashValue}, nil); invalidateErr != nil {
		s.LoggerWarn("失效关系图片链接失败: %v", invalidateErr)
		imageLinkErrors = append(imageLinkErrors, map[string]any{
			"target_type": "relation", "hash": hashValue, "error": invalidateErr.Error(),
		})
	}
	result := map[string]any{
		"target_type": targetType, "hash": hashValue, "previous_metadata": previousMetadata,
		"updated_metadata": updated, "image_link_ids": imageLinkIDs,
		"previous_is_inactive":    pyTruthy(previous["is_inactive"]),
		"previous_inactive_since": previous["inactive_since"],
	}
	if len(imageLinkErrors) > 0 {
		result["image_link_errors"] = imageLinkErrors
	}
	return result
}

// copyStringAnyMap 复制对象，避免级联结果污染原始行
func copyStringAnyMap(value map[string]any) map[string]any {
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}
