package services

// 记忆修正计划执行与回滚，对齐 Python _apply_fuzzy_modify_plan 与 _rollback_fuzzy_modify_action

import (
	"context"
	"fmt"
	"strings"
	"time"

	"maibot-go/internal/A_memorix/core/storage"
)

// correctionApplier 使用修正服务自身的级联助手执行计划
type correctionApplier struct {
	service               *CorrectionAdminService
	ingestText            func(ctx context.Context, params IngestTextParams) (map[string]any, error)
	refreshProfile        func(ctx context.Context, personID string) (map[string]any, error)
	deleteParagraphs      func(ctx context.Context, hashes []string, requestedBy string, reason string) (map[string]any, error)
	rebuildGraph          func(ctx context.Context) (map[string]any, error)
	persist               func(ctx context.Context) error
	enqueueProfile        func(personID string, reason string)
	invalidateFilterCache func()
}

// ApplyFuzzyModifyPlan 对齐 _apply_fuzzy_modify_plan
func (a *correctionApplier) ApplyFuzzyModifyPlan(ctx context.Context, planRecord map[string]any, requestedBy, reason string, factClaimConfirmed bool) (map[string]any, error) {
	if a == nil || a.service == nil {
		return nil, fmt.Errorf("记忆修正执行器未接入")
	}
	plan := asMap(planRecord["plan"])
	if plan == nil {
		plan = map[string]any{}
	}
	operations := make([]map[string]any, 0)
	for _, value := range asList(plan["operations"]) {
		if operation := asMap(value); operation != nil {
			operations = append(operations, operation)
		}
	}
	changeID := strings.TrimSpace(asString(planRecord["plan_id"]))
	if changeID == "" {
		changeID = fmt.Sprintf("fuzzy_%d", time.Now().Unix())
	}
	changedAt := float64(time.Now().UnixNano()) / 1e9
	storedIDs := make([]string, 0)
	ingestResults := make([]any, 0)
	supersededTargets := make([]any, 0)
	supersedeHashes := make([]string, 0)
	for _, operation := range operations {
		if asString(operation["action"]) == "mark_superseded" {
			if hashValue := strings.TrimSpace(asString(operation["hash"])); hashValue != "" {
				supersedeHashes = append(supersedeHashes, hashValue)
			}
		}
	}
	ingestIndex := 0
	for _, operation := range operations {
		if asString(operation["action"]) != "ingest_text" {
			continue
		}
		ingestIndex++
		if a.ingestText == nil {
			return nil, fmt.Errorf("记忆写入服务未接入")
		}
		opReason := strings.TrimSpace(asString(operation["reason"]))
		if opReason == "" {
			opReason = strings.TrimSpace(reason)
		}
		if opReason == "" {
			opReason = strings.TrimSpace(asString(plan["request_text"]))
		}
		validFrom := changedAt
		if parsed := optionalFloat(operation["valid_from"]); parsed != nil {
			validFrom = *parsed
		}
		metadata := map[string]any{
			"memory_change": map[string]any{
				"change_id": changeID, "change_type": "ingest_text", "changed_at": changedAt,
				"changed_by": requestedBy, "reason": opReason,
				"supersedes_hashes": supersedeHashes, "valid_from": validFrom,
			},
			"source_request": strings.TrimSpace(asString(plan["request_text"])),
		}
		sourceType := strings.TrimSpace(asString(operation["source_type"]))
		if sourceType == "" {
			sourceType = "memory"
		}
		if sourceType == "person_fact" && factClaimConfirmed {
			metadata["fact_claim"] = map[string]any{
				"trust": "manual_confirmed", "authority": "manual",
				"stability": "stable", "profile_section": "stable_facts",
			}
		}
		chatID := strings.TrimSpace(asString(operation["chat_id"]))
		if chatID == "" {
			chatID = strings.TrimSpace(asString(plan["chat_id"]))
		}
		result, err := a.ingestText(ctx, IngestTextParams{
			ExternalID: fmt.Sprintf("%s:ingest:%d", changeID, ingestIndex), SourceType: sourceType,
			Text: strings.TrimSpace(asString(operation["text"])), ChatID: chatID,
			PersonIDs: tokens(operation["person_ids"]), Participants: tokens(operation["participants"]),
			Timestamp: &validFrom, Tags: tokens(operation["tags"]), Metadata: metadata,
			Relations: correctionRelationMaps(operation["relations"]), RespectFilter: false,
		})
		if err != nil {
			return nil, err
		}
		resultIDs := tokens(result["stored_ids"])
		storedIDs = append(storedIDs, resultIDs...)
		ingestResults = append(ingestResults, map[string]any{"operation": operation, "result": result})
	}
	for _, operation := range operations {
		if asString(operation["action"]) != "mark_superseded" {
			continue
		}
		marked := a.service.markFuzzyModifyTargetSupersededCurrent(
			operation, operation, changeID, changedAt, requestedBy, storedIDs, changeID,
			firstNonEmpty(reason, asString(plan["request_text"])),
		)
		if len(marked) > 0 {
			supersededTargets = append(supersededTargets, marked)
		}
	}
	refreshedProfiles := make([]any, 0)
	for _, operation := range operations {
		if asString(operation["action"]) != "refresh_person_profile" {
			continue
		}
		personID := strings.TrimSpace(asString(operation["person_id"]))
		if personID == "" {
			continue
		}
		if a.refreshProfile == nil {
			return nil, fmt.Errorf("人物画像刷新未接入")
		}
		profile, err := a.refreshProfile(ctx, personID)
		if err != nil {
			return nil, err
		}
		refreshedProfiles = append(refreshedProfiles, profile)
	}
	if len(supersededTargets) > 0 {
		if a.invalidateFilterCache != nil {
			a.invalidateFilterCache()
		}
		if a.rebuildGraph != nil {
			if _, err := a.rebuildGraph(ctx); err != nil {
				return nil, err
			}
		}
		if a.persist != nil {
			if err := a.persist(ctx); err != nil {
				return nil, err
			}
		}
	}
	return map[string]any{
		"success":            len(storedIDs) > 0 || len(supersededTargets) > 0 || len(refreshedProfiles) > 0,
		"stored_ids":         storedIDs,
		"ingest_results":     ingestResults,
		"superseded_targets": supersededTargets,
		"refreshed_profiles": refreshedProfiles,
		"changed_at":         changedAt,
		"changed_by":         requestedBy,
		"reason":             reason,
	}, nil
}

// RollbackFuzzyModifyPlan 对齐 _rollback_fuzzy_modify_action
func (a *correctionApplier) RollbackFuzzyModifyPlan(ctx context.Context, planRecord map[string]any, requestedBy, reason string) (map[string]any, error) {
	if a == nil || a.service == nil {
		return nil, fmt.Errorf("记忆修正执行器未接入")
	}
	store, err := correctionStore(a.service.Meta())
	if err != nil {
		return nil, err
	}
	execution := asMap(planRecord["execution"])
	storedIDs := tokens(execution["stored_ids"])
	paragraphHashes := make([]string, 0)
	relationHashes := make([]string, 0)
	for _, hashValue := range storedIDs {
		if paragraph, _ := store.GetParagraph(hashValue); paragraph != nil {
			paragraphHashes = append(paragraphHashes, hashValue)
			continue
		}
		if relation, _ := store.GetRelation(hashValue, true); relation != nil {
			relationHashes = append(relationHashes, hashValue)
		}
	}
	items := make([]any, 0)
	if len(paragraphHashes) > 0 {
		if a.deleteParagraphs == nil {
			return nil, fmt.Errorf("记忆删除服务未接入")
		}
		deleteResult, err := a.deleteParagraphs(ctx, paragraphHashes, requestedBy, firstNonEmpty(reason, "fuzzy_modify_rollback"))
		if err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"type": "delete_new_paragraphs", "result": deleteResult})
		if !pyTruthy(deleteResult["success"]) {
			return map[string]any{
				"success": false, "error": firstNonEmpty(asString(deleteResult["error"]), "回滚删除新增记忆失败"),
				"stored_ids_delete_requested": paragraphHashes, "new_relations_deactivated": []any{},
				"restored_targets": []any{}, "items": items, "requested_by": requestedBy, "reason": reason,
			}, nil
		}
	}
	restoredTargets := make([]any, 0)
	restoreFailures := make([]any, 0)
	staleMarksRolledBack := make([]any, 0)
	restoredImageLinks := 0
	for _, value := range asList(execution["superseded_targets"]) {
		target := asMap(value)
		if target == nil {
			continue
		}
		targetType := strings.TrimSpace(asString(target["target_type"]))
		hashValue := strings.TrimSpace(asString(target["hash"]))
		previousMetadata := correctionCoerceMetadata(target["previous_metadata"])
		if targetType == "paragraph" && hashValue != "" {
			cascade := asMap(target["cascade"])
			for _, relationValue := range asList(cascade["relations_marked_inactive"]) {
				relationItem := asMap(relationValue)
				relationHash := strings.TrimSpace(asString(relationItem["relation_hash"]))
				if relationHash == "" {
					continue
				}
				previousRelationMetadata := correctionCoerceMetadata(relationItem["previous_metadata"])
				if _, err := store.UpdateRelationMetadata(relationHash, previousRelationMetadata, false); err != nil {
					restoreFailures = append(restoreFailures, map[string]any{"target_type": "relation", "hash": relationHash, "error": "级联关系不存在"})
					continue
				}
				// 对齐 Python correction_admin_service.py:373-380：失效/恢复与图片链接失败都要计入 restore_failures
				if pyTruthy(relationItem["previous_is_inactive"]) {
					if err := store.MarkRelationsInactive([]string{relationHash}, optionalFloat(relationItem["previous_inactive_since"]), ""); err != nil {
						restoreFailures = append(restoreFailures, map[string]any{"target_type": "relation", "hash": relationHash, "error": err.Error()})
						continue
					}
				} else {
					if err := store.MarkRelationsActive([]string{relationHash}); err != nil {
						restoreFailures = append(restoreFailures, map[string]any{"target_type": "relation", "hash": relationHash, "error": err.Error()})
						continue
					}
				}
				restored, restoreErr := store.RestoreImageMemoryLinks(tokens(relationItem["image_link_ids"]), nil)
				if restoreErr != nil {
					restoreFailures = append(restoreFailures, map[string]any{"target_type": "relation", "hash": relationHash, "error": restoreErr.Error()})
					continue
				}
				restoredImageLinks += restored
				restoredTargets = append(restoredTargets, map[string]any{"target_type": "relation", "hash": relationHash, "cascade_from": hashValue})
			}
			for _, snapshotValue := range asList(cascade["stale_mark_snapshots"]) {
				snapshot := asMap(snapshotValue)
				paragraphHash := strings.TrimSpace(asString(snapshot["paragraph_hash"]))
				if paragraphHash == "" {
					paragraphHash = hashValue
				}
				relationHash := strings.TrimSpace(asString(snapshot["relation_hash"]))
				if paragraphHash == "" || relationHash == "" {
					continue
				}
				rollback, err := store.RollbackParagraphStaleRelationMark(storage.ParagraphStaleMarkRollbackOptions{
					ParagraphHash: paragraphHash, RelationHash: relationHash,
					ExpectedSourceType:        firstNonEmpty(asString(snapshot["source_type"]), "memory_correction"),
					ExpectedSourceID:          firstNonEmpty(asString(snapshot["source_id"]), strings.TrimSpace(asString(planRecord["plan_id"]))),
					ExpectedSourceOperationID: asString(snapshot["source_operation_id"]),
					PreviousMark:              asMap(snapshot["previous_mark"]),
				}, nil)
				if err != nil {
					restoreFailures = append(restoreFailures, map[string]any{"target_type": "stale_mark", "hash": relationHash, "error": err.Error()})
					continue
				}
				staleMarksRolledBack = append(staleMarksRolledBack, rollback)
			}
			// 对齐 Python correction_admin_service.py:420-431：updated 为 None 表示目标段落不存在或已删除
			updated, updateErr := store.UpdateParagraphMetadata(hashValue, previousMetadata, false)
			if updateErr != nil {
				restoreFailures = append(restoreFailures, map[string]any{"target_type": "paragraph", "hash": hashValue, "error": updateErr.Error()})
				continue
			}
			if updated == nil {
				restoreFailures = append(restoreFailures, map[string]any{"target_type": "paragraph", "hash": hashValue, "error": "目标段落不存在或已删除"})
				continue
			}
			if factSnapshot := asMap(target["fact_evidence_snapshot"]); factSnapshot != nil {
				restoreReason := firstNonEmpty(reason, "fuzzy_modify_rollback_restore_fact_evidence")
				if _, err := store.RestoreFactEvidenceSnapshot(factSnapshot, restoreReason, nil, nil); err != nil {
					restoreFailures = append(restoreFailures, map[string]any{"target_type": "paragraph", "hash": hashValue, "error": err.Error()})
					continue
				}
			}
			restored, restoreErr := store.RestoreImageMemoryLinks(tokens(target["image_link_ids"]), nil)
			if restoreErr != nil {
				restoreFailures = append(restoreFailures, map[string]any{"target_type": "paragraph", "hash": hashValue, "error": restoreErr.Error()})
				continue
			}
			restoredImageLinks += restored
			restoredTargets = append(restoredTargets, map[string]any{"target_type": "paragraph", "hash": hashValue})
			continue
		}
		if targetType == "relation" && hashValue != "" {
			// 对齐 Python correction_admin_service.py:443-455：updated 为 None 表示目标关系不存在
			updated, updateErr := store.UpdateRelationMetadata(hashValue, previousMetadata, false)
			if updateErr != nil {
				restoreFailures = append(restoreFailures, map[string]any{"target_type": "relation", "hash": hashValue, "error": updateErr.Error()})
				continue
			}
			if updated == nil {
				restoreFailures = append(restoreFailures, map[string]any{"target_type": "relation", "hash": hashValue, "error": "目标关系不存在"})
				continue
			}
			if pyTruthy(target["previous_is_inactive"]) {
				if err := store.MarkRelationsInactive([]string{hashValue}, optionalFloat(target["previous_inactive_since"]), ""); err != nil {
					restoreFailures = append(restoreFailures, map[string]any{"target_type": "relation", "hash": hashValue, "error": err.Error()})
					continue
				}
			} else {
				if err := store.MarkRelationsActive([]string{hashValue}); err != nil {
					restoreFailures = append(restoreFailures, map[string]any{"target_type": "relation", "hash": hashValue, "error": err.Error()})
					continue
				}
			}
			restored, restoreErr := store.RestoreImageMemoryLinks(tokens(target["image_link_ids"]), nil)
			if restoreErr != nil {
				restoreFailures = append(restoreFailures, map[string]any{"target_type": "relation", "hash": hashValue, "error": restoreErr.Error()})
				continue
			}
			restoredImageLinks += restored
			restoredTargets = append(restoredTargets, map[string]any{"target_type": "relation", "hash": hashValue})
		}
	}
	if len(relationHashes) > 0 {
		now := float64(time.Now().UnixNano()) / 1e9
		if err := store.MarkRelationsInactive(relationHashes, &now, ""); err != nil {
			return nil, err
		}
	}
	if len(relationHashes) > 0 || len(restoredTargets) > 0 {
		if a.rebuildGraph != nil {
			if _, err := a.rebuildGraph(ctx); err != nil {
				return nil, err
			}
		}
		if a.persist != nil {
			if err := a.persist(ctx); err != nil {
				return nil, err
			}
		}
	}
	result := map[string]any{
		"success": len(restoreFailures) == 0, "stored_ids_delete_requested": paragraphHashes,
		"new_relations_deactivated": relationHashes, "restored_targets": restoredTargets,
		"restore_failures": restoreFailures, "stale_marks_rolled_back": staleMarksRolledBack,
		"restored_image_link_count": restoredImageLinks, "items": items,
		"requested_by": requestedBy, "reason": reason,
	}
	if len(restoreFailures) > 0 {
		result["error"] = "回滚部分目标失败"
	} else if a.enqueueProfile != nil {
		profileIDs := tokens(planRecord["target_person_id"])
		if plan := asMap(planRecord["plan"]); plan != nil {
			profileIDs = append(profileIDs, tokens(plan["person_id"])...)
		}
		seen := map[string]struct{}{}
		for _, personID := range profileIDs {
			personID = strings.TrimSpace(personID)
			if personID == "" {
				continue
			}
			if _, ok := seen[personID]; ok {
				continue
			}
			seen[personID] = struct{}{}
			a.enqueueProfile(personID, "memory_correction_rollback")
		}
	}
	return result, nil
}

// correctionRelationMaps 把操作中的关系列表转为写入参数
func correctionRelationMaps(value any) []map[string]any {
	relations := make([]map[string]any, 0)
	for _, item := range asList(value) {
		if relation := asMap(item); relation != nil {
			relations = append(relations, relation)
		}
	}
	return relations
}

// buildFuzzyModifyCascadePreviewCurrent 汇总计划操作的级联影响
func (s *CorrectionAdminService) buildFuzzyModifyCascadePreviewCurrent(operations []any) map[string]any {
	relations := make([]any, 0)
	entities := make([]any, 0)
	seenRelations := map[string]struct{}{}
	seenEntities := map[string]struct{}{}
	for _, value := range operations {
		operation := asMap(value)
		if operation == nil || asString(operation["action"]) != "mark_superseded" {
			continue
		}
		if strings.TrimSpace(asString(operation["target_type"])) != "paragraph" {
			continue
		}
		paragraphHash := strings.TrimSpace(asString(operation["hash"]))
		if paragraphHash == "" {
			continue
		}
		cascade := s.buildFuzzyModifyParagraphCascadeCurrent(paragraphHash, strings.TrimSpace(asString(operation["reason"])), true, "")
		for _, itemValue := range asList(cascade["relations"]) {
			item := asMap(itemValue)
			relationHash := strings.TrimSpace(asString(item["relation_hash"]))
			key := paragraphHash + ":" + relationHash
			if relationHash == "" {
				continue
			}
			if _, ok := seenRelations[key]; ok {
				continue
			}
			seenRelations[key] = struct{}{}
			relations = append(relations, item)
		}
		for _, itemValue := range asList(cascade["entities"]) {
			item := asMap(itemValue)
			entityHash := strings.TrimSpace(asString(item["entity_hash"]))
			key := paragraphHash + ":" + entityHash
			if entityHash == "" {
				continue
			}
			if _, ok := seenEntities[key]; ok {
				continue
			}
			seenEntities[key] = struct{}{}
			entities = append(entities, item)
		}
	}
	counts := map[string]any{
		"relations": len(relations), "relations_mark_inactive": 0,
		"relations_mark_stale_evidence": 0, "relations_skipped_protected": 0,
		"entities": len(entities),
	}
	for _, value := range relations {
		switch asString(asMap(value)["action"]) {
		case "mark_inactive":
			counts["relations_mark_inactive"] = counts["relations_mark_inactive"].(int) + 1
		case "mark_stale_evidence":
			counts["relations_mark_stale_evidence"] = counts["relations_mark_stale_evidence"].(int) + 1
		case "skipped_protected":
			counts["relations_skipped_protected"] = counts["relations_skipped_protected"].(int) + 1
		}
	}
	return map[string]any{"relations": relations, "entities": entities, "counts": counts}
}
