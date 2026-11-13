package services

// 记忆修正内核兼容方法入口，逐项转发到规范化与级联实现

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// NormalizeFuzzyModifyScope 对齐 _normalize_fuzzy_modify_scope 转发
func (s *CorrectionAdminService) NormalizeFuzzyModifyScope(_ context.Context, payload map[string]any) (any, error) {
	return normalizeFuzzyModifyScope(payload["scope"]), nil
}

// NormalizeFuzzyModifyCandidate 对齐 _normalize_fuzzy_modify_candidate 转发
func (s *CorrectionAdminService) NormalizeFuzzyModifyCandidate(_ context.Context, payload map[string]any) (any, error) {
	item := asMap(correctionFirstValue(payload["item"], payload["candidate"]))
	if item == nil {
		item = payload
	}
	return normalizeFuzzyModifyCandidateCurrent(item), nil
}

// NormalizeFuzzyModifyRelations 对齐 _normalize_fuzzy_modify_relations 转发
func (s *CorrectionAdminService) NormalizeFuzzyModifyRelations(_ context.Context, payload map[string]any) (any, error) {
	value := correctionFirstValue(payload["relations"], payload["value"])
	return normalizeFuzzyModifyRelationsCurrent(value), nil
}

// BuildFuzzyModifyCascadePreview 对齐 _build_fuzzy_modify_cascade_preview 转发
func (s *CorrectionAdminService) BuildFuzzyModifyCascadePreview(_ context.Context, payload map[string]any) (any, error) {
	operations := asList(correctionFirstValue(payload["operations"], asMap(payload["plan"])["operations"]))
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
		"relations":               len(relations),
		"relations_mark_inactive": 0, "relations_mark_stale_evidence": 0,
		"relations_skipped_protected": 0, "entities": len(entities),
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
	return map[string]any{"relations": relations, "entities": entities, "counts": counts}, nil
}

// BuildFuzzyModifyParagraphCascade 对齐 _build_fuzzy_modify_paragraph_cascade 转发
func (s *CorrectionAdminService) BuildFuzzyModifyParagraphCascade(_ context.Context, payload map[string]any) (any, error) {
	paragraphHash := correctionPayloadString(payload, "paragraph_hash", "hash")
	reason := strings.TrimSpace(asString(payload["reason"]))
	previewOnly := pyTruthy(payload["preview_only"])
	planID := correctionPayloadString(payload, "plan_id")
	return s.buildFuzzyModifyParagraphCascadeCurrent(paragraphHash, reason, previewOnly, planID), nil
}

// FuzzyModifyStaleSourceOperationID 对齐 _fuzzy_modify_stale_source_operation_id 转发
func (s *CorrectionAdminService) FuzzyModifyStaleSourceOperationID(_ context.Context, payload map[string]any) (any, error) {
	return fuzzyModifyStaleSourceOperationID(
		correctionPayloadString(payload, "plan_id"),
		correctionPayloadString(payload, "paragraph_hash", "hash"),
		correctionPayloadString(payload, "relation_hash"),
	), nil
}

// ExecuteFuzzyModifyParagraphCascade 对齐 _execute_fuzzy_modify_paragraph_cascade 转发
func (s *CorrectionAdminService) ExecuteFuzzyModifyParagraphCascade(_ context.Context, payload map[string]any) (any, error) {
	changedAt := float64(time.Now().UnixNano()) / 1e9
	if parsed := optionalFloat(payload["changed_at"]); parsed != nil {
		changedAt = *parsed
	}
	return s.executeFuzzyModifyParagraphCascadeCurrent(
		correctionPayloadString(payload, "paragraph_hash", "hash"),
		correctionPayloadString(payload, "plan_id"), changedAt,
		strings.TrimSpace(asString(payload["reason"])),
	), nil
}

// MarkFuzzyModifyTargetSuperseded 对齐 _mark_fuzzy_modify_target_superseded 转发
func (s *CorrectionAdminService) MarkFuzzyModifyTargetSuperseded(_ context.Context, payload map[string]any) (any, error) {
	operation := asMap(correctionFirstValue(payload["operation"], payload))
	changedAt := float64(time.Now().UnixNano()) / 1e9
	if parsed := optionalFloat(payload["changed_at"]); parsed != nil {
		changedAt = *parsed
	}
	return s.markFuzzyModifyTargetSupersededCurrent(
		payload, operation, correctionPayloadString(payload, "change_id"), changedAt,
		correctionPayloadString(payload, "changed_by"), tokens(payload["replacement_hashes"]),
		correctionPayloadString(payload, "plan_id"), strings.TrimSpace(asString(payload["default_reason"])),
	), nil
}

// IsFuzzyModifyCandidateMutable 对齐 _is_fuzzy_modify_candidate_mutable 转发
func (s *CorrectionAdminService) IsFuzzyModifyCandidateMutable(_ context.Context, payload map[string]any) (any, error) {
	candidate := asMap(correctionFirstValue(payload["candidate"], payload["item"]))
	rawItem := asMap(payload["raw_item"])
	if rawItem == nil {
		rawItem = candidate
	}
	return s.isFuzzyModifyCandidateMutableCurrent(candidate, rawItem), nil
}

// NormalizeFuzzyModifyPlan 对齐 _normalize_fuzzy_modify_plan 转发
func (s *CorrectionAdminService) NormalizeFuzzyModifyPlan(_ context.Context, payload map[string]any) (any, error) {
	planValue := asMap(correctionFirstValue(payload["plan"], payload["payload"]))
	if planValue == nil {
		planValue = payload
	}
	candidates := asList(payload["candidates"])
	return s.normalizeFuzzyModifyPlanCurrent(
		planValue, correctionPayloadString(payload, "request_text"), normalizeFuzzyModifyScope(payload["scope"]),
		correctionPayloadString(payload, "person_id"), correctionPayloadString(payload, "chat_id"), candidates,
	), nil
}

// PreviewFuzzyModifyAction 对齐 _preview_fuzzy_modify_action 转发
func (s *CorrectionAdminService) PreviewFuzzyModifyAction(ctx context.Context, payload map[string]any) (any, error) {
	return s.previewFuzzyModify(ctx, payload), nil
}

// ExecuteFuzzyModifyAction 对齐 _execute_fuzzy_modify_action 转发
func (s *CorrectionAdminService) ExecuteFuzzyModifyAction(ctx context.Context, payload map[string]any) (any, error) {
	return s.executeFuzzyModify(ctx, payload), nil
}

// RollbackFuzzyModifyAction 对齐 _rollback_fuzzy_modify_action 转发
func (s *CorrectionAdminService) RollbackFuzzyModifyAction(ctx context.Context, payload map[string]any) (any, error) {
	return s.rollbackFuzzyModify(ctx, payload), nil
}

// CollectFuzzyModifyCandidates 对齐 _collect_fuzzy_modify_candidates 转发
func (s *CorrectionAdminService) CollectFuzzyModifyCandidates(_ context.Context, payload map[string]any) (any, error) {
	if raw := payload["targets"]; raw != nil {
		return s.collectSelectedCandidates(raw)
	}
	limit := intOr(payload["limit"], 20, 1)
	return s.collectCandidates(
		normalizeFuzzyModifyScope(payload["scope"]), correctionPayloadString(payload, "person_id"),
		correctionPayloadString(payload, "person_keyword", "keyword"), correctionPayloadString(payload, "chat_id"), limit,
	)
}

// BuildFuzzyModifyLLMPlan 对齐 _build_fuzzy_modify_llm_plan 转发
func (s *CorrectionAdminService) BuildFuzzyModifyLLMPlan(ctx context.Context, payload map[string]any) (any, error) {
	if s.planner == nil {
		return nil, fmt.Errorf("记忆修正 LLM 未接入，无法生成修改计划")
	}
	raw, err := s.planner.BuildFuzzyModifyPlan(ctx,
		correctionPayloadString(payload, "request_text", "text"),
		normalizeFuzzyModifyScope(payload["scope"]),
		correctionPayloadString(payload, "person_id"),
		correctionPayloadString(payload, "chat_id"),
		asList(payload["candidates"]),
	)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// ApplyFuzzyModifyPlan 对齐 _apply_fuzzy_modify_plan 转发
func (s *CorrectionAdminService) ApplyFuzzyModifyPlan(ctx context.Context, payload map[string]any) (any, error) {
	if s.applier == nil {
		return nil, fmt.Errorf("记忆修正执行器未接入")
	}
	record := asMap(correctionFirstValue(payload["plan_record"], payload["plan"]))
	plan := asMap(record["plan"])
	if plan == nil {
		plan = record
	}
	return s.applier.ApplyFuzzyModifyPlan(ctx, plan,
		correctionPayloadString(payload, "requested_by"), correctionPayloadString(payload, "reason"),
		pyTruthy(payload["fact_claim_confirmed"]) || pyTruthy(payload["confirmed"]))
}
