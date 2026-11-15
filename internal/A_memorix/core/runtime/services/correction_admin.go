package services

// 记忆修正管理服务，对齐 Python core/runtime/services/correction_admin_service.py

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// CorrectionPlanner 由内核注入的记忆修正计划生成器，内部封装 LLM 调用
type CorrectionPlanner interface {
	BuildFuzzyModifyPlan(ctx context.Context, requestText, scope, personID, chatID string, candidates []any) (map[string]any, error)
}

// CorrectionApplier 由内核注入的修正计划执行器，负责段落级联与权威写入
type CorrectionApplier interface {
	ApplyFuzzyModifyPlan(ctx context.Context, plan map[string]any, requestedBy, reason string, factClaimConfirmed bool) (map[string]any, error)
	RollbackFuzzyModifyPlan(ctx context.Context, plan map[string]any, requestedBy, reason string) (map[string]any, error)
}

// CorrectionAdminOptions 修正服务依赖
type CorrectionAdminOptions struct {
	Base    *Base
	Planner CorrectionPlanner
	Applier CorrectionApplier
	// FeedbackConfig 反馈纠错集成配置，对齐 Python fuzzy_modify_cfg_* 的读取来源
	FeedbackConfig func() *utils.FeedbackIntegrationConfig
	// IngestText 写入修正产生的新记忆
	IngestText func(ctx context.Context, params IngestTextParams) (map[string]any, error)
	// RefreshPersonProfile 刷新人物画像
	RefreshPersonProfile func(ctx context.Context, personID string) (map[string]any, error)
	// DeleteParagraphs 回滚时删除新增段落
	DeleteParagraphs func(ctx context.Context, hashes []string, requestedBy string, reason string) (map[string]any, error)
	// RebuildGraphFromMetadata 对齐 _rebuild_graph_from_metadata
	RebuildGraphFromMetadata func(ctx context.Context) (map[string]any, error)
	// Persist 对齐 _persist
	Persist func(ctx context.Context) error
	// EnqueuePersonProfileRefresh 对齐 _enqueue_person_profile_refresh
	EnqueuePersonProfileRefresh func(personID string, reason string)
	// InvalidateEffectiveFilterCache 对齐清空 _current_effective_filter_cache
	InvalidateEffectiveFilterCache func()
	// ResolvePersonID 对齐 person_profile_service.resolve_person_id
	ResolvePersonID func(ctx context.Context, identifier string) (string, error)
}

// CorrectionAdminService 对齐 MemoryCorrectionAdminService
type CorrectionAdminService struct {
	*Base
	planner         CorrectionPlanner
	applier         CorrectionApplier
	feedbackConfig  func() *utils.FeedbackIntegrationConfig
	resolvePersonID func(ctx context.Context, identifier string) (string, error)
}

// NewCorrectionAdminService 构造修正服务
func NewCorrectionAdminService(options CorrectionAdminOptions) *CorrectionAdminService {
	service := &CorrectionAdminService{
		Base: options.Base, planner: options.Planner, applier: options.Applier,
		feedbackConfig: options.FeedbackConfig, resolvePersonID: options.ResolvePersonID,
	}
	if service.applier == nil && options.IngestText != nil {
		service.applier = &correctionApplier{
			service: service, ingestText: options.IngestText,
			refreshProfile: options.RefreshPersonProfile, deleteParagraphs: options.DeleteParagraphs,
			rebuildGraph: options.RebuildGraphFromMetadata, persist: options.Persist,
			enqueueProfile:        options.EnqueuePersonProfileRefresh,
			invalidateFilterCache: options.InvalidateEffectiveFilterCache,
		}
	}
	return service
}

// fuzzyCfg 返回反馈纠错集成配置，未注入时使用默认值
func (s *CorrectionAdminService) fuzzyCfg() *utils.FeedbackIntegrationConfig {
	if s == nil || s.feedbackConfig == nil {
		return nil
	}
	return s.feedbackConfig()
}

// MemoryCorrectionAdmin 对齐 memory_correction_admin
func (s *CorrectionAdminService) MemoryCorrectionAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	act := strings.ToLower(strings.TrimSpace(action))
	switch act {
	case "preview", "plan":
		return s.previewFuzzyModify(ctx, payload), nil
	case "execute":
		return s.executeFuzzyModify(ctx, payload), nil
	case "get":
		planID := strings.TrimSpace(asString(firstNonEmpty(payload["plan_id"], payload["id"])))
		plan, err := s.Meta().GetFuzzyModifyPlan(planID)
		if err != nil {
			return map[string]any{"success": false, "plan": nil, "error": err.Error()}, nil
		}
		errorText := ""
		if plan == nil {
			errorText = "修改计划不存在"
		}
		return map[string]any{"success": plan != nil, "plan": plan, "error": errorText}, nil
	case "list":
		statuses := dedupeStrings(mergeTokens(payload["statuses"], payload["status"]))
		limit := intOr(payload["limit"], 50, 1)
		items, err := s.Meta().ListFuzzyModifyPlans(limit, statuses, strings.TrimSpace(asString(payload["scope"])))
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}, nil
		}
		return map[string]any{"success": true, "items": items, "count": len(items)}, nil
	case "rollback":
		return s.rollbackFuzzyModify(ctx, payload), nil
	default:
		return map[string]any{"success": false, "error": fmt.Sprintf("不支持的记忆修正操作: %s", act)}, nil
	}
}

// MemoryFuzzyModifyAdmin 对齐 memory_fuzzy_modify_admin，直接转调
func (s *CorrectionAdminService) MemoryFuzzyModifyAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	return s.MemoryCorrectionAdmin(ctx, action, payload)
}

// normalizeFuzzyModifyScope 对齐 _normalize_fuzzy_modify_scope
func normalizeFuzzyModifyScope(value any) string {
	scope := strings.ToLower(strings.TrimSpace(asString(value)))
	switch scope {
	case "profile", "person", "person_fact":
		return "person_profile"
	case "memory", "general", "chat":
		return "memory"
	case "":
		return "person_profile"
	default:
		return scope
	}
}

// previewFuzzyModify 对齐 _preview_fuzzy_modify_action
func (s *CorrectionAdminService) previewFuzzyModify(ctx context.Context, payload map[string]any) map[string]any {
	fuzzy := s.fuzzyCfg()
	if !utils.FuzzyModifyCfgEnabled(fuzzy) {
		return map[string]any{"success": false, "error": "记忆修正功能未启用"}
	}
	requestText := strings.TrimSpace(asString(firstNonEmpty(payload["request_text"], payload["text"])))
	if requestText == "" {
		return map[string]any{"success": false, "error": "修改描述不能为空"}
	}
	scope := normalizeFuzzyModifyScope(payload["scope"])
	targets := asList(payload["targets"])
	personID := strings.TrimSpace(asString(payload["person_id"]))
	personKeyword := strings.TrimSpace(asString(firstNonEmpty(payload["person_keyword"], payload["keyword"])))
	chatID := strings.TrimSpace(asString(payload["chat_id"]))
	requestedBy := strings.TrimSpace(asString(payload["requested_by"]))
	if requestedBy == "" {
		requestedBy = "webui"
	}
	reason := strings.TrimSpace(asString(payload["reason"]))
	// 对齐 Python: min(max(1, int(limit or 20)), fuzzy_modify_cfg_candidate_limit())
	limit := intOr(payload["limit"], 20, 1)
	if cfgLimit := utils.FuzzyModifyCfgCandidateLimit(fuzzy); limit > cfgLimit {
		limit = cfgLimit
	}
	if targets != nil && (scope != "memory" || personID != "" || personKeyword != "" || chatID != "") {
		return map[string]any{"success": false, "error": "按所选记忆修正时请使用 memory 范围，不要同时指定人物或聊天流"}
	}
	if scope == "person_profile" {
		if personID == "" && personKeyword != "" && s.resolvePersonID != nil {
			resolved, err := s.resolvePersonID(ctx, personKeyword)
			if err == nil {
				personID = strings.TrimSpace(resolved)
			}
		}
		if personID == "" {
			return map[string]any{"success": false, "error": "人物画像修改需要提供 person_id 或 person_keyword"}
		}
	}
	if targets == nil && chatID == "" && !utils.FuzzyModifyCfgAllowGlobalScope(fuzzy) {
		return map[string]any{"success": false, "error": "非人物画像修正需要提供 chat_id，或开启全局记忆修正范围"}
	}
	var candidates []any
	var err error
	if targets != nil {
		candidates, err = s.collectSelectedCandidates(payload["targets"])
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}
		}
	} else {
		candidates, err = s.collectCandidates(scope, personID, personKeyword, chatID, limit)
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}
		}
	}
	if len(candidates) == 0 {
		return map[string]any{"success": false, "error": "未找到可修改的候选记忆", "candidates": []any{}}
	}
	if s.planner == nil {
		return map[string]any{"success": false, "error": "记忆修正 LLM 未接入，无法生成修改计划", "candidates": candidates}
	}
	rawPlan, err := s.planner.BuildFuzzyModifyPlan(ctx, requestText, scope, personID, chatID, candidates)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error(), "candidates": candidates}
	}
	plan := s.normalizeFuzzyModifyPlanCurrent(rawPlan, requestText, scope, personID, chatID, candidates)
	operations := asList(plan["operations"])
	if len(operations) == 0 {
		reasonText := strings.TrimSpace(asString(plan["reason"]))
		if reasonText == "" {
			reasonText = "LLM 未生成可执行修改计划"
		}
		return map[string]any{"success": false, "error": reasonText, "raw_plan": rawPlan, "candidates": candidates}
	}
	confidence := asFloatValue(plan["confidence"])
	confirmThreshold := utils.FuzzyModifyCfgConfirmThreshold(fuzzy)
	cascadePreview := s.buildFuzzyModifyCascadePreviewCurrent(operations)
	preview := map[string]any{
		"request_text": requestText, "scope": scope, "person_id": personID,
		"person_keyword": personKeyword, "chat_id": chatID, "candidates": candidates,
		"operations": operations, "cascade_preview": cascadePreview,
		"requires_confirmation": true, "confirm_threshold": confirmThreshold,
		"reason": strings.TrimSpace(asString(plan["reason"])),
	}
	plan["confidence"] = confidence
	record, err := s.Meta().CreateFuzzyModifyPlan(storage.FuzzyModifyPlanInput{
		RequestText: requestText, Scope: scope, Plan: plan, Preview: preview,
		TargetPersonID: personID, TargetChatID: chatID, Status: "awaiting_confirmation",
		Confidence: confidence, RequestedBy: requestedBy, Reason: reason,
	})
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	return map[string]any{
		"success": true, "plan_id": record["plan_id"], "plan": record,
		"preview": preview, "requires_confirmation": true,
	}
}

// collectCandidates 收集人物画像或记忆范围的候选段落
func (s *CorrectionAdminService) collectCandidates(scope, personID, personKeyword, chatID string, limit int) ([]any, error) {
	rows, err := s.Meta().Query(
		`SELECT * FROM paragraphs WHERE COALESCE(is_deleted, 0) = 0 ORDER BY updated_at DESC, created_at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	keyword := strings.TrimSpace(personKeyword)
	candidates := make([]any, 0, limit)
	for _, row := range rows {
		if scope == "person_profile" {
			personIDs := paragraphPersonIDs(row)
			if personID != "" {
				if !slices.Contains(personIDs, personID) {
					continue
				}
			} else if keyword != "" {
				if !strings.Contains(strings.ToLower(rowString(row, "content")), strings.ToLower(keyword)) {
					continue
				}
			}
		}
		if chatID != "" && !paragraphMatchesChat(row, chatID) {
			continue
		}
		if !isFuzzyModifyCandidateMutable(row) {
			continue
		}
		candidates = append(candidates, fuzzyModifyCandidate(row))
		if len(candidates) >= limit {
			break
		}
	}
	return candidates, nil
}

// collectSelectedCandidates 对齐 _collect_selected_fuzzy_modify_candidates
func (s *CorrectionAdminService) collectSelectedCandidates(targets any) ([]any, error) {
	if targets == nil {
		return nil, fmt.Errorf("targets 不是有效的记忆选择")
	}
	items := asList(targets)
	candidates := make([]any, 0, len(items))
	for _, itemValue := range items {
		item := asMap(itemValue)
		if item == nil {
			return nil, fmt.Errorf("targets 项必须是对象")
		}
		hashValue := strings.TrimSpace(asString(firstNonEmpty(item["paragraph_hash"], item["hash"])))
		if hashValue == "" {
			return nil, fmt.Errorf("targets 项缺少 paragraph_hash")
		}
		paragraph, err := s.Meta().GetParagraph(hashValue)
		if err != nil {
			return nil, err
		}
		if paragraph == nil || pyTruthy(paragraph["is_deleted"]) {
			continue
		}
		if !isFuzzyModifyCandidateMutable(paragraph) {
			continue
		}
		candidates = append(candidates, fuzzyModifyCandidate(paragraph))
	}
	return candidates, nil
}

// isFuzzyModifyCandidateMutable 判断段落是否允许修正
func isFuzzyModifyCandidateMutable(row map[string]any) bool {
	if pyTruthy(row["is_deleted"]) || pyTruthy(row["is_frozen"]) {
		return false
	}
	return strings.TrimSpace(rowString(row, "content")) != ""
}

// fuzzyModifyCandidate 构造候选记忆摘要，对齐 _normalize_fuzzy_modify_candidate
func fuzzyModifyCandidate(row map[string]any) map[string]any {
	hashValue := rowString(row, "hash")
	return map[string]any{
		"candidate_id":   "paragraph:" + hashValue,
		"target_type":    "paragraph",
		"evidence_type":  "paragraph",
		"hash":           hashValue,
		"paragraph_hash": hashValue,
		"content":        trimText(rowString(row, "content"), 420),
		"source":         rowString(row, "source"),
		"metadata":       jsonObject(rowString(row, "metadata")),
		"score":          row["score"],
		"person_ids":     paragraphPersonIDs(row),
	}
}

// paragraphPersonIDs 从段落元数据提取人物 ID
func paragraphPersonIDs(row map[string]any) []string {
	metadata := jsonObject(rowString(row, "metadata"))
	ids := mergeTokens(metadata["person_ids"], metadata["person_id"], metadata["target_person_id"])
	source := rowString(row, "source")
	if strings.HasPrefix(source, "person_fact:") {
		ids = append(ids, strings.TrimPrefix(source, "person_fact:"))
	}
	return dedupeStrings(ids)
}

// normalizeFuzzyModifyPlan 对齐 _normalize_fuzzy_modify_plan
func normalizeFuzzyModifyPlan(raw map[string]any, candidates []any) (map[string]any, error) {
	if raw == nil {
		return nil, fmt.Errorf("修改计划不是有效的对象")
	}
	candidateHashes := map[string]bool{}
	for _, candidateValue := range candidates {
		candidate := asMap(candidateValue)
		candidateHashes[asString(candidate["paragraph_hash"])] = true
	}
	operations := []any{}
	for _, operationValue := range asList(raw["operations"]) {
		operation := asMap(operationValue)
		if operation == nil {
			return nil, fmt.Errorf("修改计划包含无效操作")
		}
		action := strings.ToLower(strings.TrimSpace(asString(firstNonEmpty(operation["action"], operation["type"]))))
		switch action {
		case "update_paragraph", "delete_paragraph", "update_entity", "update_relation", "delete_relation":
		default:
			return nil, fmt.Errorf("修改计划包含不支持的操作: %s", action)
		}
		targetHash := strings.TrimSpace(asString(firstNonEmpty(operation["paragraph_hash"], operation["target_hash"])))
		if targetHash != "" && len(candidateHashes) > 0 && !candidateHashes[targetHash] {
			return nil, fmt.Errorf("修改计划引用了候选范围外的记忆: %s", targetHash)
		}
		normalized := map[string]any{"action": action}
		for key, value := range operation {
			normalized[key] = value
		}
		operations = append(operations, normalized)
	}
	plan := map[string]any{
		"operations": operations,
		"reason":     strings.TrimSpace(asString(raw["reason"])),
		"confidence": asFloatValue(raw["confidence"]),
	}
	if relations, ok := raw["relations"]; ok {
		plan["relations"] = relations
	}
	return plan, nil
}

// buildCascadePreview 对齐 _build_fuzzy_modify_cascade_preview
func buildCascadePreview(operations []any) map[string]any {
	paragraphHashes := []string{}
	entityHashes := []string{}
	relationHashes := []string{}
	for _, operationValue := range operations {
		operation := asMap(operationValue)
		switch strings.ToLower(asString(operation["action"])) {
		case "update_paragraph", "delete_paragraph":
			paragraphHashes = append(paragraphHashes, asString(operation["paragraph_hash"]))
		case "update_entity":
			entityHashes = append(entityHashes, asString(operation["entity_hash"]))
		case "update_relation", "delete_relation":
			relationHashes = append(relationHashes, asString(operation["relation_hash"]))
		}
	}
	return map[string]any{
		"paragraph_hashes": dedupeStrings(paragraphHashes),
		"entity_hashes":    dedupeStrings(entityHashes),
		"relation_hashes":  dedupeStrings(relationHashes),
		"operation_count":  len(operations),
	}
}

// executeFuzzyModify 对齐 _execute_fuzzy_modify_action
func (s *CorrectionAdminService) executeFuzzyModify(ctx context.Context, payload map[string]any) map[string]any {
	planID := strings.TrimSpace(asString(payload["plan_id"]))
	if planID == "" {
		return map[string]any{"success": false, "error": "plan_id 不能为空"}
	}
	plan, err := s.Meta().GetFuzzyModifyPlan(planID)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	if plan == nil {
		return map[string]any{"success": false, "error": "修改计划不存在"}
	}
	status := strings.ToLower(strings.TrimSpace(asString(plan["status"])))
	switch status {
	case "awaiting_confirmation", "failed", "executing":
	default:
		return map[string]any{"success": false, "error": fmt.Sprintf("当前计划状态不可执行: %s", status)}
	}
	confirmed := pyTruthy(payload["confirmed"])
	fuzzy := s.fuzzyCfg()
	confidence := asFloatValue(plan["confidence"])
	if !confirmed && (!utils.FuzzyModifyCfgAutoExecuteEnabled(fuzzy) || confidence < utils.FuzzyModifyCfgConfirmThreshold(fuzzy)) {
		return map[string]any{"success": false, "error": "需要用户确认后才能执行", "requires_confirmation": true}
	}
	claimed, err := s.Meta().ClaimFuzzyModifyPlan(planID, 900)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	if claimed == nil {
		latest, _ := s.Meta().GetFuzzyModifyPlan(planID)
		return map[string]any{"success": false, "error": "修改计划正在执行或状态已变化", "plan": latest}
	}
	requestedBy := strings.TrimSpace(asString(payload["requested_by"]))
	if requestedBy == "" {
		requestedBy = "webui"
	}
	reason := strings.TrimSpace(asString(payload["reason"]))
	executingPayload := map[string]any{
		"attempt": map[string]any{
			"status": "executing", "started_at": time.Now().Unix(),
			"requested_by": requestedBy, "reason": reason,
			"recovered_from_stale_executing": status == "executing",
		},
	}
	executing := "executing"
	if _, err := s.Meta().UpdateFuzzyModifyPlan(planID, storage.FuzzyModifyPlanUpdate{Status: &executing, Execution: executingPayload}); err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	if s.applier == nil {
		finish := "failed"
		executingPayload["success"] = false
		executingPayload["error"] = "记忆修正执行器未接入"
		executingPayload["attempt"].(map[string]any)["status"] = "failed"
		executingPayload["attempt"].(map[string]any)["finished_at"] = time.Now().Unix()
		updated, _ := s.Meta().UpdateFuzzyModifyPlan(planID, storage.FuzzyModifyPlanUpdate{Status: &finish, Execution: executingPayload, Reason: &reason})
		return map[string]any{"success": false, "plan": updated, "execution": executingPayload, "error": "记忆修正执行器未接入"}
	}
	execution, applyErr := s.applier.ApplyFuzzyModifyPlan(ctx, plan, requestedBy, reason, confirmed)
	if applyErr != nil {
		executingPayload["success"] = false
		executingPayload["error"] = applyErr.Error()
		attempt := executingPayload["attempt"].(map[string]any)
		attempt["status"] = "failed"
		attempt["finished_at"] = time.Now().Unix()
		finish := "failed"
		updated, _ := s.Meta().UpdateFuzzyModifyPlan(planID, storage.FuzzyModifyPlanUpdate{Status: &finish, Execution: executingPayload, Reason: &reason})
		s.LoggerWarn("记忆修正执行失败: %v", applyErr)
		return map[string]any{"success": false, "plan": updated, "execution": executingPayload, "error": applyErr.Error()}
	}
	for key, value := range execution {
		executingPayload[key] = value
	}
	attempt := executingPayload["attempt"].(map[string]any)
	attempt["status"] = "finished"
	attempt["finished_at"] = time.Now().Unix()
	applyOK := pyTruthy(execution["success"])
	executingPayload["success"] = applyOK
	finish := "failed"
	var executedAt *float64
	if applyOK {
		finish = "executed"
		now := float64(time.Now().Unix())
		executedAt = &now
	}
	updated, err := s.Meta().UpdateFuzzyModifyPlan(planID, storage.FuzzyModifyPlanUpdate{
		Status: &finish, Execution: executingPayload, ExecutedAt: executedAt, Reason: &reason,
	})
	if err != nil {
		return map[string]any{"success": false, "error": err.Error(), "execution": executingPayload}
	}
	return map[string]any{"success": applyOK, "plan": updated, "execution": executingPayload}
}

// rollbackFuzzyModify 对齐 _rollback_fuzzy_modify_action
func (s *CorrectionAdminService) rollbackFuzzyModify(ctx context.Context, payload map[string]any) map[string]any {
	planID := strings.TrimSpace(asString(payload["plan_id"]))
	if planID == "" {
		return map[string]any{"success": false, "error": "plan_id 不能为空"}
	}
	plan, err := s.Meta().GetFuzzyModifyPlan(planID)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	if plan == nil {
		return map[string]any{"success": false, "error": "修改计划不存在"}
	}
	if strings.ToLower(strings.TrimSpace(asString(plan["status"]))) != "executed" {
		return map[string]any{"success": false, "error": "只有已执行的修改计划可以回滚"}
	}
	requestedBy := strings.TrimSpace(asString(payload["requested_by"]))
	if requestedBy == "" {
		requestedBy = "webui"
	}
	reason := strings.TrimSpace(asString(payload["reason"]))
	if s.applier == nil {
		return map[string]any{"success": false, "error": "记忆修正执行器未接入"}
	}
	result, err := s.applier.RollbackFuzzyModifyPlan(ctx, plan, requestedBy, reason)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	rollbackOK := pyTruthy(result["success"])
	status := "rollback_failed"
	if rollbackOK {
		status = "rolled_back"
	}
	execution := asMap(plan["execution"])
	if execution == nil {
		execution = map[string]any{}
	}
	execution["rollback"] = result
	updated, err := s.Meta().UpdateFuzzyModifyPlan(planID, storage.FuzzyModifyPlanUpdate{
		Status: &status, Execution: execution, Reason: &reason,
	})
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	return map[string]any{"success": rollbackOK, "plan": updated, "rollback": result}
}

// asFloatValue 只取数值，缺省为 0
func asFloatValue(value any) float64 {
	parsed, _ := asFloat(value)
	return parsed
}

var _ = sort.Strings
