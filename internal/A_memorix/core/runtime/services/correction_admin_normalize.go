package services

// 记忆修正候选与计划规范化，对齐 Python correction_admin_service 的纯助手

import (
	"fmt"
	"strings"
	"time"

	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// correctionMetadataStore 级联修正需要的扩展存储能力
// Metadata 接口保持精简，这里通过类型断言取用真实 storage.Store
type correctionMetadataStore interface {
	GetParagraph(hashValue string) (map[string]any, error)
	GetRelation(hashValue string, includeInactive bool) (map[string]any, error)
	GetParagraphRelations(paragraphHash string) ([]map[string]any, error)
	GetParagraphEntities(paragraphHash string) ([]map[string]any, error)
	GetRelationStatusBatch(hashes []string) (map[string]map[string]any, error)
	UpdateParagraphMetadata(paragraphHash string, patch map[string]any, merge bool) (map[string]any, error)
	UpdateRelationMetadata(relationHash string, patch map[string]any, merge bool) (map[string]any, error)
	MarkRelationsInactive(hashes []string, inactiveSince *float64, reason string) error
	ListActiveImageMemoryLinkIDs(targetType string, targetIDs []string) ([]string, error)
	InvalidateImageMemoryLinks(targetType string, targetIDs []string, conn *storage.Conn) (int, error)
	GetParagraphStaleRelationMark(paragraphHash, relationHash string, conn *storage.Conn) (map[string]any, error)
	UpsertParagraphStaleRelationMark(opts storage.ParagraphStaleMarkUpsertOptions, conn *storage.Conn) (map[string]any, error)
	DetachFactEvidenceForParagraphs(paragraphHashes []string, reason string, detachedAt *float64) (map[string]any, error)
	RestoreFactEvidenceSnapshot(snapshot map[string]any, reason string, restoredAt *float64, conn *storage.Conn) (map[string]any, error)
	RestoreImageMemoryLinks(linkIDs []string, conn *storage.Conn) (int, error)
	MarkRelationsActive(hashes []string) error
	RollbackParagraphStaleRelationMark(opts storage.ParagraphStaleMarkRollbackOptions, conn *storage.Conn) (map[string]any, error)
}

// correctionStore 取扩展存储，未接入时返回明确错误
func correctionStore(meta Metadata) (correctionMetadataStore, error) {
	if meta == nil {
		return nil, fmt.Errorf("元数据存储未初始化")
	}
	store, ok := meta.(correctionMetadataStore)
	if !ok {
		return nil, fmt.Errorf("元数据存储不支持记忆修正级联")
	}
	return store, nil
}

// correctionFirstValue 取首个非 nil 字段，保留原始类型
func correctionFirstValue(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// correctionPayloadString 取首个非空字符串字段
func correctionPayloadString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(asString(payload[key])); value != "" {
			return value
		}
	}
	return ""
}

// correctionCoerceMetadata 对齐 coerce_metadata_dict
func correctionCoerceMetadata(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case string:
		return jsonObject(typed)
	default:
		return map[string]any{}
	}
}

// correctionTrimText 对齐 graph_admin_service._trim_text
func correctionTrimText(value string, limit int) string {
	text := strings.Join(strings.Fields(value), " ")
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

// normalizeFuzzyModifyCandidateCurrent 对齐 _normalize_fuzzy_modify_candidate
func normalizeFuzzyModifyCandidateCurrent(item map[string]any) map[string]any {
	if item == nil {
		return map[string]any{}
	}
	evidenceType := strings.TrimSpace(asString(firstNonEmpty(item["evidence_type"], item["type"])))
	targetType := "paragraph"
	if evidenceType == "relation" {
		targetType = "relation"
	}
	hashValue := strings.TrimSpace(asString(item["hash"]))
	metadata := correctionCoerceMetadata(item["metadata"])
	source := strings.TrimSpace(asString(firstNonEmpty(item["source"], metadata["source"])))
	return map[string]any{
		"candidate_id":  targetType + ":" + hashValue,
		"target_type":   targetType,
		"evidence_type": evidenceType,
		"hash":          hashValue,
		"content":       correctionTrimText(asString(firstNonEmpty(item["content"], item["title"])), 420),
		"source":        source,
		"metadata":      metadata,
		"score":         item["score"],
	}
}

// normalizeFuzzyModifyRelationsCurrent 对齐 _normalize_fuzzy_modify_relations
func normalizeFuzzyModifyRelationsCurrent(value any) []map[string]any {
	rows := asList(value)
	out := make([]map[string]any, 0, len(rows))
	for _, rowValue := range rows {
		row := asMap(rowValue)
		if row == nil {
			continue
		}
		subject := strings.TrimSpace(asString(row["subject"]))
		predicate := strings.TrimSpace(asString(row["predicate"]))
		object := strings.TrimSpace(asString(row["object"]))
		if subject == "" || predicate == "" || object == "" {
			continue
		}
		confidence := 1.0
		if parsed, ok := asFloat(row["confidence"]); ok {
			confidence = parsed
		}
		if confidence < 0 {
			confidence = 0
		}
		if confidence > 1 {
			confidence = 1
		}
		out = append(out, map[string]any{
			"subject": subject, "predicate": predicate, "object": object,
			"confidence": confidence, "metadata": correctionCoerceMetadata(row["metadata"]),
		})
	}
	return out
}

// isFuzzyModifyCandidateMutableCurrent 对齐 _is_fuzzy_modify_candidate_mutable
func (s *CorrectionAdminService) isFuzzyModifyCandidateMutableCurrent(candidate, rawItem map[string]any) bool {
	if rawItem != nil && rawItem["deletable"] == false {
		return false
	}
	if candidate == nil {
		return false
	}
	store, err := correctionStore(s.Meta())
	if err != nil {
		return false
	}
	targetType := strings.TrimSpace(asString(candidate["target_type"]))
	hashValue := strings.TrimSpace(asString(candidate["hash"]))
	if targetType == "" || hashValue == "" {
		return false
	}
	switch targetType {
	case "paragraph":
		paragraph, err := store.GetParagraph(hashValue)
		return err == nil && paragraph != nil && !pyTruthy(paragraph["is_deleted"])
	case "relation":
		relation, err := store.GetRelation(hashValue, false)
		if err != nil || relation == nil {
			return false
		}
		statuses, err := store.GetRelationStatusBatch([]string{hashValue})
		if err != nil {
			return false
		}
		status := statuses[hashValue]
		if status == nil {
			status = map[string]any{}
		}
		if pyTruthy(status["is_inactive"]) || pyTruthy(status["is_pinned"]) {
			return false
		}
		protectedUntil := 0.0
		if parsed := optionalFloat(status["protected_until"]); parsed != nil {
			protectedUntil = *parsed
		}
		return protectedUntil <= float64(time.Now().UnixNano())/1e9
	default:
		return false
	}
}

// normalizeFuzzyModifyPlanCurrent 对齐 _normalize_fuzzy_modify_plan
func (s *CorrectionAdminService) normalizeFuzzyModifyPlanCurrent(payload map[string]any, requestText, scope, personID, chatID string, candidates []any) map[string]any {
	candidateByID := map[string]map[string]any{}
	candidateByHash := map[string]map[string]any{}
	for _, value := range candidates {
		candidate := asMap(value)
		if candidate == nil {
			continue
		}
		if id := strings.TrimSpace(asString(candidate["candidate_id"])); id != "" {
			candidateByID[id] = candidate
		}
		if hashValue := strings.TrimSpace(asString(candidate["hash"])); hashValue != "" {
			candidateByHash[hashValue] = candidate
		}
	}
	confidence := 0.0
	if payload != nil {
		if parsed, ok := asFloat(payload["confidence"]); ok {
			confidence = parsed
		}
	}
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 1 {
		confidence = 1
	}
	maxTargets := utils.FuzzyModifyCfgMaxTargets(s.fuzzyCfg())
	operations := make([]any, 0)
	appendOperation := func(operation map[string]any) { operations = append(operations, operation) }
	var rawOperations []any
	if payload != nil {
		rawOperations = asList(payload["operations"])
	}
	for _, rawValue := range rawOperations {
		raw := asMap(rawValue)
		if raw == nil {
			continue
		}
		action := strings.ToLower(strings.TrimSpace(asString(firstNonEmpty(raw["action"], raw["op"]))))
		switch action {
		case "mark_superseded":
			candidate := candidateByID[strings.TrimSpace(asString(raw["candidate_id"]))]
			if candidate == nil {
				candidate = candidateByHash[strings.TrimSpace(asString(raw["hash"]))]
			}
			if candidate == nil {
				s.LoggerWarn("记忆修正计划引用了候选集外的目标: action=%s candidate_id=%s hash=%s", action, asString(raw["candidate_id"]), asString(raw["hash"]))
				continue
			}
			reason := strings.TrimSpace(asString(firstNonEmpty(raw["reason"], payload["reason"], requestText)))
			appendOperation(map[string]any{
				"action":       "mark_superseded",
				"candidate_id": asString(candidate["candidate_id"]),
				"target_type":  asString(candidate["target_type"]),
				"hash":         asString(candidate["hash"]),
				"reason":       reason,
				"valid_to":     optionalFloat(raw["valid_to"]),
			})
		case "ingest_text":
			text := strings.TrimSpace(asString(raw["text"]))
			if text == "" {
				continue
			}
			sourceType := strings.TrimSpace(asString(raw["source_type"]))
			if sourceType == "" {
				sourceType = "memory"
				if personID != "" {
					sourceType = "person_fact"
				}
			}
			operationChatID := strings.TrimSpace(asString(raw["chat_id"]))
			if operationChatID == "" {
				operationChatID = chatID
			}
			appendOperation(map[string]any{
				"action":       "ingest_text",
				"text":         text,
				"source_type":  sourceType,
				"chat_id":      operationChatID,
				"person_ids":   mergeTokens(raw["person_ids"], personID),
				"participants": tokens(raw["participants"]),
				"tags":         mergeTokens(raw["tags"], "fuzzy_modify"),
				"relations":    normalizeFuzzyModifyRelationsCurrent(raw["relations"]),
				"valid_from":   optionalFloat(raw["valid_from"]),
				"reason":       strings.TrimSpace(asString(firstNonEmpty(raw["reason"], payload["reason"], requestText))),
			})
		case "refresh_person_profile":
			targetPersonID := strings.TrimSpace(asString(raw["person_id"]))
			if targetPersonID == "" {
				targetPersonID = personID
			}
			if targetPersonID != "" {
				appendOperation(map[string]any{"action": "refresh_person_profile", "person_id": targetPersonID})
			}
		}
	}
	limit := maxTargets * 2
	if limit < 1 {
		limit = 1
	}
	if len(operations) > limit {
		operations = operations[:limit]
	}
	markCount := 0
	for _, value := range operations {
		if asString(asMap(value)["action"]) == "mark_superseded" {
			markCount++
		}
	}
	if markCount > maxTargets {
		kept := 0
		limited := make([]any, 0, len(operations))
		for _, value := range operations {
			operation := asMap(value)
			if asString(operation["action"]) != "mark_superseded" {
				limited = append(limited, operation)
				continue
			}
			kept++
			if kept <= maxTargets {
				limited = append(limited, operation)
			}
		}
		operations = limited
	}
	hasRefresh := false
	for _, value := range operations {
		if asString(asMap(value)["action"]) == "refresh_person_profile" {
			hasRefresh = true
			break
		}
	}
	if len(operations) > 0 && !hasRefresh && personID != "" {
		operations = append(operations, map[string]any{"action": "refresh_person_profile", "person_id": personID})
	}
	riskLevel := "medium"
	reasonText := ""
	if payload != nil {
		if value := strings.TrimSpace(asString(payload["risk_level"])); value != "" {
			riskLevel = value
		}
		reasonText = strings.TrimSpace(asString(payload["reason"]))
	}
	return map[string]any{
		"scope": scope, "request_text": requestText, "person_id": personID,
		"chat_id": chatID, "confidence": confidence, "risk_level": riskLevel,
		"reason": reasonText, "operations": operations,
	}
}
