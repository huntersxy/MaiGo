package services

// 反馈纠错的执行流程，对齐 feedback_correction_service.py 的回滚、应用、分类与后台循环段

import (
	"context"
	"fmt"
	"strings"
	"time"

	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// appendLog 对齐 append_feedback_action_log
func (s *FeedbackService) appendLog(taskID int, queryToolID string, actionType string, targetHash string, beforePayload map[string]any, afterPayload map[string]any, reason string) error {
	store := s.store()
	if store == nil {
		return nil
	}
	_, err := store.AppendFeedbackActionLog(storage.FeedbackActionLogAppendOptions{
		TaskID: taskID, QueryToolID: queryToolID, ActionType: actionType, TargetHash: targetHash,
		BeforePayload: beforePayload, AfterPayload: afterPayload, Reason: reason,
	}, nil)
	return err
}

// feedbackTaskDetailOrNil 任务存在时构造详情
func (s *FeedbackService) feedbackTaskDetailOrNil(task map[string]any) any {
	if task == nil {
		return nil
	}
	return s.feedbackTaskDetail(task)
}

// feedbackTaskDetail 同步版本详情
func (s *FeedbackService) feedbackTaskDetail(task map[string]any) map[string]any {
	detail := s.feedbackTaskSummary(task)
	actionLogs := []map[string]any{}
	if store := s.store(); store != nil {
		if rows, err := store.ListFeedbackActionLogs(feedbackInt(task["id"], 0), nil); err == nil && rows != nil {
			actionLogs = rows
		}
	}
	detail["query_snapshot"] = feedbackMapDict(task["query_snapshot"])
	detail["decision_payload"] = feedbackMapDict(task["decision_payload"])
	detail["rollback_plan_summary"] = s.feedbackRollbackPlanSummary(feedbackMapDict(task["rollback_plan"]))
	detail["rollback_result"] = feedbackMapDict(task["rollback_result"])
	detail["rollback_error"] = strings.TrimSpace(asString(task["rollback_error"]))
	detail["rollback_requested_by"] = strings.TrimSpace(asString(task["rollback_requested_by"]))
	detail["rollback_reason"] = strings.TrimSpace(asString(task["rollback_reason"]))
	detail["rollback_requested_at"] = task["rollback_requested_at"]
	detail["rolled_back_at"] = task["rolled_back_at"]
	detail["action_logs"] = actionLogs
	return detail
}

// RollbackFeedbackTask 对齐 _rollback_feedback_task
func (s *FeedbackService) RollbackFeedbackTask(ctx context.Context, payload map[string]any) (any, error) {
	store := s.store()
	if store == nil {
		return map[string]any{"success": false, "error": "metadata store unavailable"}, nil
	}
	taskID := feedbackInt(payload["task_id"], 0)
	requestedBy := strings.TrimSpace(asString(payload["requested_by"]))
	reason := strings.TrimSpace(asString(payload["reason"]))
	task, err := store.GetFeedbackTaskByID(taskID, nil)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return map[string]any{"success": false, "error": "反馈纠错任务不存在"}, nil
	}
	if strings.ToLower(strings.TrimSpace(asString(task["status"]))) != "applied" {
		return map[string]any{"success": false, "error": "仅 applied 的反馈纠错任务允许回退"}, nil
	}
	rollbackStatus := strings.ToLower(strings.TrimSpace(asString(task["rollback_status"])))
	if rollbackStatus == "" {
		rollbackStatus = "none"
	}
	if rollbackStatus == "rolled_back" {
		return map[string]any{
			"success": true, "already_rolled_back": true,
			"task": s.feedbackTaskDetailOrNil(task), "result": feedbackMapDict(task["rollback_result"]),
		}, nil
	}
	queryToolID := strings.TrimSpace(asString(task["query_tool_id"]))
	rollbackPlan := feedbackMapDict(task["rollback_plan"])
	if len(rollbackPlan) == 0 {
		runningTask, err := store.MarkFeedbackTaskRollbackRunning(storage.FeedbackRollbackStartOptions{TaskID: taskID, RequestedBy: requestedBy, Reason: reason}, nil)
		if err != nil {
			return nil, err
		}
		if runningTask == nil {
			return s.feedbackRollbackStartFailure(taskID)
		}
		_ = s.appendLog(taskID, queryToolID, "rollback_error", "", nil, nil, "rollback_plan_missing")
		failed, err := store.FinalizeFeedbackTaskRollback(storage.FeedbackRollbackFinalizeOptions{TaskID: taskID, RollbackStatus: "error", RollbackError: "rollback_plan_missing"}, nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": false, "error": "缺少 rollback_plan，无法回退", "task": s.feedbackTaskDetailOrNil(failed)}, nil
	}
	runningTask, err := store.MarkFeedbackTaskRollbackRunning(storage.FeedbackRollbackStartOptions{TaskID: taskID, RequestedBy: requestedBy, Reason: reason}, nil)
	if err != nil {
		return nil, err
	}
	if runningTask == nil {
		return s.feedbackRollbackStartFailure(taskID)
	}
	result := map[string]any{
		"task_id": taskID, "query_tool_id": queryToolID,
		"restored_relation_hashes": []string{}, "reverted_corrected_relation_hashes": []string{},
		"deleted_correction_paragraph_hashes": []string{}, "cleared_stale_mark_count": 0,
		"episode_sources_queued": []string{}, "profile_person_ids_queued": []string{}, "warnings": []string{},
	}
	applyErr := s.feedbackRollbackCompensate(ctx, taskID, queryToolID, reason, rollbackPlan, result)
	if applyErr != nil {
		s.LoggerWarn("反馈纠错回退失败: task_id=%d err=%v", taskID, applyErr)
		_ = s.appendLog(taskID, queryToolID, "rollback_error", "", nil, result, applyErr.Error())
		finalTask, err := store.FinalizeFeedbackTaskRollback(storage.FeedbackRollbackFinalizeOptions{
			TaskID: taskID, RollbackStatus: "error", RollbackResult: result, RollbackError: applyErr.Error(),
		}, nil)
		if err != nil {
			return nil, err
		}
		if finalTask == nil {
			finalTask = runningTask
		}
		return map[string]any{"success": false, "error": applyErr.Error(), "result": result, "task": s.feedbackTaskDetailOrNil(finalTask)}, nil
	}
	s.rebuildGraph()
	s.persist()
	finalTask, err := store.FinalizeFeedbackTaskRollback(storage.FeedbackRollbackFinalizeOptions{
		TaskID: taskID, RollbackStatus: "rolled_back", RollbackResult: result,
	}, nil)
	if err != nil {
		return nil, err
	}
	if finalTask == nil {
		finalTask = runningTask
	}
	return map[string]any{"success": true, "result": result, "task": s.feedbackTaskDetailOrNil(finalTask)}, nil
}

// feedbackRollbackStartFailure 对齐回退抢占失败分支
func (s *FeedbackService) feedbackRollbackStartFailure(taskID int) (any, error) {
	store := s.store()
	latest, err := store.GetFeedbackTaskByID(taskID, nil)
	if err != nil {
		return nil, err
	}
	latestStatus := strings.ToLower(strings.TrimSpace(asString(feedbackMapDict(latest)["rollback_status"])))
	if latestStatus == "" {
		latestStatus = "none"
	}
	if latestStatus == "running" {
		return map[string]any{"success": false, "error": "该反馈纠错任务正在回退中", "task": s.feedbackTaskDetailOrNil(latest)}, nil
	}
	if latestStatus == "rolled_back" {
		return map[string]any{
			"success": true, "already_rolled_back": true,
			"task": s.feedbackTaskDetailOrNil(latest), "result": feedbackMapDict(feedbackMapDict(latest)["rollback_result"]),
		}, nil
	}
	return map[string]any{"success": false, "error": "无法进入回退状态", "task": s.feedbackTaskDetailOrNil(latest)}, nil
}

// feedbackRollbackCompensate 执行回退补偿计划，错误表示流程需要收敛到 error
func (s *FeedbackService) feedbackRollbackCompensate(ctx context.Context, taskID int, queryToolID string, reason string, rollbackPlan map[string]any, result map[string]any) error {
	store := s.store()
	if store == nil {
		return fmt.Errorf("metadata store unavailable")
	}
	restoredHashes := make([]string, 0)
	for _, raw := range asList(rollbackPlan["forgotten_relations"]) {
		item := asMap(raw)
		if item == nil {
			continue
		}
		relationHash := strings.TrimSpace(asString(item["hash"]))
		snapshot := feedbackMapDict(item["before_status"])
		if relationHash == "" || len(snapshot) == 0 {
			continue
		}
		beforeStatus, err := store.GetRelationStatusBatch([]string{relationHash})
		if err != nil {
			return err
		}
		afterStatus, err := store.RestoreRelationStatusFromSnapshot(relationHash, snapshot)
		if err != nil {
			return err
		}
		if afterStatus == nil {
			result["warnings"] = append(result["warnings"].([]string), "restore_old_relation_failed:"+relationHash)
			continue
		}
		restoredHashes = append(restoredHashes, relationHash)
		if err := s.appendLog(taskID, queryToolID, "rollback_restore_relation", relationHash, feedbackMapDict(beforeStatus[relationHash]), afterStatus, reason); err != nil {
			return err
		}
	}
	result["restored_relation_hashes"] = restoredHashes

	correctedWrite := feedbackMapDict(rollbackPlan["corrected_write"])
	deleted, err := s.SoftDeleteFeedbackCorrectionParagraphs(ctx, map[string]any{"paragraph_hashes": tokens(correctedWrite["paragraph_hashes"])})
	if err != nil {
		return err
	}
	deletedMap := asMap(deleted)
	deletedHashes := tokens(deletedMap["deleted_hashes"])
	result["deleted_correction_paragraph_hashes"] = deletedHashes
	paragraphRows := feedbackMapDict(deletedMap["paragraph_rows"])
	refMap := map[string][]any{}
	for _, raw := range asList(deletedMap["deleted_external_refs"]) {
		ref := asMap(raw)
		paragraphHash := strings.TrimSpace(asString(ref["paragraph_hash"]))
		if ref == nil || paragraphHash == "" {
			continue
		}
		refMap[paragraphHash] = append(refMap[paragraphHash], ref)
	}
	for _, paragraphHash := range deletedHashes {
		before := map[string]any{"paragraph": paragraphRows[paragraphHash], "external_refs": refMap[paragraphHash]}
		if err := s.appendLog(taskID, queryToolID, "rollback_delete_correction_paragraph", paragraphHash, before, nil, reason); err != nil {
			return err
		}
	}

	revertedHashes := make([]string, 0)
	for _, raw := range asList(correctedWrite["corrected_relations"]) {
		item := asMap(raw)
		if item == nil {
			continue
		}
		relationHash := strings.TrimSpace(asString(item["hash"]))
		if relationHash == "" {
			continue
		}
		beforeStatus, err := store.GetRelationStatusBatch([]string{relationHash})
		if err != nil {
			return err
		}
		var afterStatus map[string]any
		if pyTruthy(item["existed_before"]) {
			afterStatus, err = store.RestoreRelationStatusFromSnapshot(relationHash, feedbackMapDict(item["before_status"]))
		} else {
			zero := 0.0
			pinned := false
			if err := store.UpdateRelationsProtection([]string{relationHash}, &zero, &pinned, nil); err != nil {
				return err
			}
			now := float64(s.now().UnixNano()) / 1e9
			if err := store.MarkRelationsInactive([]string{relationHash}, &now, ""); err != nil {
				return err
			}
			statuses, err := store.GetRelationStatusBatch([]string{relationHash})
			if err != nil {
				return err
			}
			afterStatus = statuses[relationHash]
		}
		if err != nil {
			return err
		}
		if afterStatus == nil {
			result["warnings"] = append(result["warnings"].([]string), "revert_corrected_relation_failed:"+relationHash)
			continue
		}
		revertedHashes = append(revertedHashes, relationHash)
		if err := s.appendLog(taskID, queryToolID, "rollback_revert_corrected_relation", relationHash, feedbackMapDict(beforeStatus[relationHash]), afterStatus, reason); err != nil {
			return err
		}
	}
	result["reverted_corrected_relation_hashes"] = revertedHashes

	staleRollbacks := make([]map[string]any, 0)
	cleared := 0
	for _, raw := range asList(rollbackPlan["stale_marks"]) {
		item := asMap(raw)
		if item == nil {
			continue
		}
		paragraphHash := strings.TrimSpace(asString(item["paragraph_hash"]))
		relationHash := strings.TrimSpace(asString(item["relation_hash"]))
		if paragraphHash == "" || relationHash == "" {
			continue
		}
		sourceOperationID := strings.TrimSpace(asString(item["source_operation_id"]))
		if sourceOperationID == "" {
			sourceOperationID = fmt.Sprintf("feedback_correction:%d:%s:%s", taskID, paragraphHash, relationHash)
		}
		expectedSourceType := strings.TrimSpace(asString(item["source_type"]))
		if expectedSourceType == "" {
			expectedSourceType = "feedback_correction"
		}
		expectedSourceID := strings.TrimSpace(asString(item["source_id"]))
		if expectedSourceID == "" {
			expectedSourceID = fmt.Sprint(taskID)
		}
		rollbackMark, err := store.RollbackParagraphStaleRelationMark(storage.ParagraphStaleMarkRollbackOptions{
			ParagraphHash: paragraphHash, RelationHash: relationHash,
			ExpectedSourceType: expectedSourceType, ExpectedSourceID: expectedSourceID,
			ExpectedSourceOperationID: sourceOperationID,
			PreviousMark:              asMap(item["previous_mark"]),
		}, nil)
		if err != nil {
			return err
		}
		if rollbackMark == nil {
			rollbackMark = map[string]any{}
		}
		staleRollbacks = append(staleRollbacks, rollbackMark)
		if asString(rollbackMark["action"]) == "deleted" {
			cleared++
		}
	}
	result["cleared_stale_mark_count"] = cleared
	result["stale_mark_rollbacks"] = staleRollbacks
	for _, rollbackMark := range staleRollbacks {
		paragraphHash := strings.TrimSpace(asString(rollbackMark["paragraph_hash"]))
		relationHash := strings.TrimSpace(asString(rollbackMark["relation_hash"]))
		if err := s.appendLog(taskID, queryToolID, "rollback_clear_stale_mark", paragraphHash, nil, map[string]any{"relation_hash": relationHash, "rollback": rollbackMark}, reason); err != nil {
			return err
		}
	}

	episodeQueued := make([]string, 0)
	for _, source := range tokens(rollbackPlan["episode_sources"]) {
		if store.EnqueueEpisodeSourceRebuild(source, "feedback_correction_rollback", storage.EpisodeEnqueueOptions{}) {
			episodeQueued = append(episodeQueued, source)
			if err := s.appendLog(taskID, queryToolID, "rollback_enqueue_episode_rebuild", source, nil, nil, reason); err != nil {
				return err
			}
		}
	}
	result["episode_sources_queued"] = episodeQueued

	profileQueued := make([]string, 0)
	for _, personID := range tokens(rollbackPlan["profile_person_ids"]) {
		payload, err := store.EnqueuePersonProfileRefresh(storage.PersonProfileRefreshEnqueueInput{
			PersonID: personID, Reason: "feedback_correction_rollback", SourceQueryToolID: queryToolID,
		})
		if err != nil {
			return err
		}
		if payload == nil {
			continue
		}
		profileQueued = append(profileQueued, personID)
		if err := s.appendLog(taskID, queryToolID, "rollback_enqueue_profile_refresh", personID, nil, nil, reason); err != nil {
			return err
		}
	}
	result["profile_person_ids_queued"] = profileQueued
	return nil
}

// ProcessFeedbackProfileRefreshBatch 对齐 _process_feedback_profile_refresh_batch
func (s *FeedbackService) ProcessFeedbackProfileRefreshBatch(ctx context.Context, payload map[string]any) (any, error) {
	store := s.store()
	empty := map[string]any{"processed": 0, "refreshed": 0, "failed": 0, "items": []map[string]any{}, "failures": []map[string]any{}}
	if store == nil || s.deps.ProfileAdmin == nil {
		return empty, nil
	}
	limit := maxInt(1, feedbackInt(payload["limit"], 1))
	maxRetry := 0
	if payload["max_retry"] != nil {
		maxRetry = maxInt(0, feedbackInt(payload["max_retry"], 0))
	} else if s.deps.ProfileAdmin.deps.ProfileRefreshMaxRetry != nil {
		maxRetry = maxInt(0, s.deps.ProfileAdmin.deps.ProfileRefreshMaxRetry())
	} else {
		maxRetry = 3
	}
	rows, err := store.FetchPersonProfileRefreshBatch(limit, maxRetry,
		mathMaxFloat(0, feedbackFloat(payload["debounce_seconds"], 0)),
		mathMaxFloat(0, feedbackFloat(payload["retry_backoff_seconds"], 0)))
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0)
	failures := make([]map[string]any, 0)
	for _, row := range rows {
		personID := strings.TrimSpace(asString(row["person_id"]))
		if personID == "" {
			continue
		}
		requestedAt := optionalFloat(row["requested_at"])
		claimed, err := store.MarkPersonProfileRefreshRunning(personID, requestedAt)
		if err != nil || !claimed {
			continue
		}
		limitValue := maxInt(4, intOr(s.Cfg("person_profile.top_k_evidence", 12), 12, 1))
		profile, refreshErr := s.deps.ProfileAdmin.RefreshPersonProfile(ctx, map[string]any{
			"person_id": personID, "limit": limitValue, "mark_active": false,
		})
		if refreshErr != nil {
			errorText := truncateRunes(refreshErr.Error(), 500)
			_, _ = store.MarkPersonProfileRefreshFailed(personID, errorText, requestedAt)
			failures = append(failures, map[string]any{"person_id": personID, "error": errorText})
			continue
		}
		if profile != nil && pyTruthy(profile["success"]) {
			_, _ = store.MarkPersonProfileRefreshDone(personID, requestedAt)
			items = append(items, map[string]any{
				"person_id": personID, "profile_version": feedbackInt(profile["profile_version"], 0),
				"profile_source": strings.TrimSpace(asString(profile["profile_source"])),
			})
			continue
		}
		errorText := strings.TrimSpace(asString(profile["error"]))
		if errorText == "" {
			errorText = "person profile refresh failed"
		}
		_, _ = store.MarkPersonProfileRefreshFailed(personID, errorText, requestedAt)
		failures = append(failures, map[string]any{"person_id": personID, "error": errorText})
	}
	return map[string]any{
		"processed": len(items) + len(failures), "refreshed": len(items), "failed": len(failures),
		"items": items, "failures": failures,
	}, nil
}

func mathMaxFloat(left float64, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

// ProcessFeedbackEpisodeRebuildBatch 对齐 feedback 归口下的 Episode 重建批处理
func (s *FeedbackService) ProcessFeedbackEpisodeRebuildBatch(ctx context.Context, payload map[string]any) (any, error) {
	if s.deps.ProcessEpisodeSourceRebuildBatch != nil {
		return s.deps.ProcessEpisodeSourceRebuildBatch(ctx, payload)
	}
	if s.deps.Ingest != nil {
		return s.deps.Ingest.ProcessEpisodeSourceRebuildBatch(ctx, payload)
	}
	return nil, fmt.Errorf("episode rebuild service unavailable")
}

// FeedbackCorrectionReconcileLoop 对齐 _feedback_correction_reconcile_loop
func (s *FeedbackService) FeedbackCorrectionReconcileLoop(ctx context.Context, payload map[string]any) (any, error) {
	for !s.stopping() {
		if !s.sleep(ctx, utils.FeedbackCfgReconcileIntervalSeconds(s.feedbackCfg())) {
			break
		}
		if s.stopping() {
			break
		}
		if s.store() == nil || !utils.FeedbackCfgEnabled(s.feedbackCfg()) {
			continue
		}
		batchSize := utils.FeedbackCfgReconcileBatchSize(s.feedbackCfg())
		if utils.FeedbackCfgProfileRefreshEnabled(s.feedbackCfg()) && s.deps.ProfileAdmin != nil {
			if _, err := s.deps.ProfileAdmin.ProcessPersonProfileRefreshQueueBatch(ctx, batchSize); err != nil {
				s.LoggerWarn("feedback_correction_reconcile 处理画像刷新失败: %v", err)
			}
		}
	}
	return nil, nil
}

// sleep 等待指定秒数，ctx 取消或停止时返回 false
func (s *FeedbackService) sleep(ctx context.Context, seconds float64) bool {
	if seconds <= 0 {
		seconds = 1
	}
	timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return !s.stopping()
	}
}

// EnqueueFeedbackTask 对齐 enqueue_feedback_task
func (s *FeedbackService) EnqueueFeedbackTask(ctx context.Context, payload map[string]any) (any, error) {
	if !utils.FeedbackCfgEnabled(s.feedbackCfg()) {
		return map[string]any{"success": false, "queued": false, "reason": "feedback_correction_disabled"}, nil
	}
	store := s.store()
	if store == nil {
		return map[string]any{"success": false, "queued": false, "reason": "metadata_store_unavailable"}, nil
	}
	queryToolID := strings.TrimSpace(asString(payload["query_tool_id"]))
	sessionID := strings.TrimSpace(asString(payload["session_id"]))
	if queryToolID == "" || sessionID == "" {
		return map[string]any{"success": false, "queued": false, "reason": "missing_required_fields"}, nil
	}
	content := feedbackMapDict(payload["structured_content"])
	hits := asList(content["hits"])
	if len(hits) == 0 {
		return map[string]any{"success": false, "queued": false, "reason": "no_hits"}, nil
	}
	queryTime := s.feedbackTimestamp(payload["query_timestamp"])
	dueAt := queryTime.Add(time.Duration(utils.FeedbackCfgWindowHours(s.feedbackCfg()) * float64(time.Hour)))
	saved, err := store.EnqueueFeedbackTask(storage.FeedbackTaskEnqueueOptions{
		QueryToolID: queryToolID, SessionID: sessionID,
		QueryTimestamp: float64(queryTime.UnixNano()) / 1e9,
		DueAt:          float64(dueAt.UnixNano()) / 1e9,
		QuerySnapshot:  content,
	}, nil)
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return map[string]any{"success": false, "queued": false, "reason": "db_save_failed"}, nil
	}
	s.Logger().Debug(fmt.Sprintf("反馈纠错任务入队: query_tool_id=%s due_at=%s", queryToolID, feedbackISOTime(dueAt)))
	return map[string]any{
		"success": true, "queued": true, "query_tool_id": queryToolID,
		"due_at": feedbackISOTime(dueAt), "task": saved,
	}, nil
}

// feedbackISOTime 对齐 Python datetime.isoformat() 的无时区写法
func feedbackISOTime(value time.Time) string {
	return value.Format("2006-01-02T15:04:05.999999")
}

// ClassifyFeedback 对齐 _classify_feedback
func (s *FeedbackService) ClassifyFeedback(ctx context.Context, payload map[string]any) (any, error) {
	queryToolID := strings.TrimSpace(asString(payload["query_tool_id"]))
	queryText := strings.TrimSpace(asString(payload["query_text"]))
	hitBriefs := feedbackMapList(payload["hit_briefs"])
	feedbackMessages := tokens(payload["feedback_messages"])
	prompt := "你是长期记忆纠错分类器。" +
		"你会根据“记忆检索命中列表”和“用户后续反馈”判断是否需要修正记忆。" +
		"请严格输出 JSON 对象，不要输出解释文字。\n\n" +
		"query_tool_id: " + queryToolID + "\n" +
		"原查询: " + queryText + "\n" +
		"候选命中: " + feedbackJSON(hitBriefs) + "\n" +
		"反馈消息: " + feedbackJSON(feedbackMessages) + "\n\n" +
		"输出 JSON schema:\n" +
		"{" + `"decision":"confirm|reject|correct|supplement|none",` +
		`"confidence":0.0,` +
		`"target_hashes":["命中列表中的 hash"],` +
		`"corrected_relations":[{"subject":"","predicate":"","object":"","confidence":1.0}],` +
		`"reason":""` +
		"}\n" +
		"约束:\n" +
		"1. 只有当反馈明确指向错误时才输出 reject/correct。\n" +
		"2. target_hashes 必须来自候选命中 hash。\n" +
		"3. corrected_relations 仅在 decision=correct 时填写，且必须是明确三元组。\n" +
		"4. 不确定时输出 decision=none, confidence<=0.5。"
	if s.deps.LLMGenerate == nil {
		return map[string]any{}, nil
	}
	response, err := s.deps.LLMGenerate(ctx, prompt)
	if err != nil {
		s.LoggerWarn("反馈分类器调用失败: %v", err)
		return map[string]any{}, nil
	}
	return utils.SafeJSONLoads(response), nil
}

// IngestFeedbackRelations 对齐 _ingest_feedback_relations
func (s *FeedbackService) IngestFeedbackRelations(ctx context.Context, payload map[string]any) (any, error) {
	inject := s.deps.Ingest
	if inject == nil {
		return map[string]any{"success": false, "error": "invalid_ingest_payload"}, nil
	}
	queryToolID := strings.TrimSpace(asString(payload["query_tool_id"]))
	sessionID := strings.TrimSpace(asString(payload["session_id"]))
	relationHashes := tokens(payload["relation_hashes"])
	correctedRelations := feedbackMapList(payload["corrected_relations"])
	supersedesHash := ""
	if len(relationHashes) > 0 {
		supersedesHash = relationHashes[0]
	}
	relationRows := make([]map[string]any, 0, len(correctedRelations))
	for _, row := range correctedRelations {
		relationRows = append(relationRows, map[string]any{
			"subject":    strings.TrimSpace(asString(row["subject"])),
			"predicate":  strings.TrimSpace(asString(row["predicate"])),
			"object":     strings.TrimSpace(asString(row["object"])),
			"confidence": feedbackFloat(row["confidence"], 1),
			"metadata": map[string]any{
				"supersedes_hash": supersedesHash, "supersedes_hashes": relationHashes,
				"from_query_tool_id": queryToolID, "feedback_window": utils.FeedbackCfgWindowLabel(s.feedbackCfg()),
			},
		})
	}
	plainParts := make([]string, 0)
	for _, row := range relationRows {
		subject := strings.TrimSpace(asString(row["subject"]))
		predicate := strings.TrimSpace(asString(row["predicate"]))
		object := strings.TrimSpace(asString(row["object"]))
		if subject != "" && predicate != "" && object != "" {
			plainParts = append(plainParts, subject+" "+predicate+" "+object)
		}
	}
	// 对齐 Python json.dumps(..., ensure_ascii=False, sort_keys=True) 默认分隔符
	encoded, err := utils.PyJSONMarshal(relationRows)
	if err != nil {
		return nil, err
	}
	externalID, err := utils.ComputeHash("feedback_correction:" + queryToolID + ":" + encoded)
	if err != nil {
		return nil, err
	}
	result, err := inject.IngestText(ctx, IngestTextParams{
		ExternalID: externalID, SourceType: "chat_summary", Text: strings.Join(plainParts, "；"), ChatID: sessionID,
		Relations: relationRows, RespectFilter: false,
		Metadata: map[string]any{
			"from_query_tool_id": queryToolID, "feedback_window": utils.FeedbackCfgWindowLabel(s.feedbackCfg()),
			"supersedes_hashes": relationHashes, "feedback_correction_source": true,
		},
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return map[string]any{"success": false, "error": "invalid_ingest_payload"}, nil
	}
	storedIDs := tokens(result["stored_ids"])
	correctedHashes := []string{}
	if len(storedIDs) > 1 {
		correctedHashes = storedIDs[1:]
	}
	result["external_id"] = externalID
	result["source"] = s.feedbackChatSource(ctx, sessionID)
	paragraphHashes := []string{}
	if len(storedIDs) > 0 {
		paragraphHashes = storedIDs[:1]
	}
	result["paragraph_hashes"] = paragraphHashes
	result["corrected_relation_hashes"] = correctedHashes
	baseSuccess := true
	if _, ok := result["success"]; ok {
		baseSuccess = pyTruthy(result["success"])
	}
	result["success"] = baseSuccess && len(correctedHashes) > 0
	if !pyTruthy(result["success"]) && strings.TrimSpace(asString(result["error"])) == "" {
		result["error"] = "missing_corrected_relations"
	}
	return result, nil
}

// ApplyFeedbackDecision 对齐 _apply_feedback_decision
func (s *FeedbackService) ApplyFeedbackDecision(ctx context.Context, payload map[string]any) (any, error) {
	store := s.store()
	decision := feedbackTaskPayload(payload)
	hitMap := asMap(payload["hit_map"])
	threshold := utils.FeedbackCfgAutoApplyThreshold(s.feedbackCfg())
	confidence := feedbackFloat(decision["confidence"], 0)
	if confidence < threshold {
		return map[string]any{"applied": false, "reason": "low_confidence", "threshold": threshold, "confidence": confidence}, nil
	}
	decisionType := strings.ToLower(strings.TrimSpace(asString(decision["decision"])))
	if decisionType == "" {
		decisionType = "none"
	}
	if decisionType != "reject" && decisionType != "correct" {
		return map[string]any{"applied": false, "reason": "decision_" + decisionType + "_no_auto_apply"}, nil
	}
	targetHashes := tokens(decision["target_hashes"])
	relationHashes := s.feedbackResolveRelationHashes(targetHashes, hitMap)
	if len(relationHashes) == 0 {
		return map[string]any{"applied": false, "reason": "no_relation_targets"}, nil
	}
	correctedRelations := feedbackMapList(decision["corrected_relations"])
	if decisionType == "correct" && len(correctedRelations) == 0 {
		return map[string]any{
			"applied": false, "reason": "missing_corrected_relations", "relation_hashes": relationHashes,
			"stale_paragraph_hashes": []string{}, "episode_rebuild_sources": []string{},
			"profile_refresh_person_ids": []string{}, "rollback_plan_summary": map[string]any{},
		}, nil
	}
	if store == nil {
		return nil, fmt.Errorf("metadata store unavailable")
	}
	taskID := feedbackInt(payload["task_id"], 0)
	queryToolID := strings.TrimSpace(asString(payload["query_tool_id"]))
	sessionID := strings.TrimSpace(asString(payload["session_id"]))
	reason := strings.TrimSpace(asString(decision["reason"]))

	oldRelationRows := []map[string]any{}
	if s.deps.GraphAdmin != nil {
		oldRelationRows = s.deps.GraphAdmin.queryRelationRowsByHashes(relationHashes, true)
	}
	beforeStatus, err := store.GetRelationStatusBatch(relationHashes)
	if err != nil {
		return nil, err
	}
	forgetResult := map[string]any{"success": false, "error": "v5 admin unavailable"}
	if s.deps.V5Admin != nil {
		forgetResult, err = s.deps.V5Admin.ApplyV5RelationAction(ctx, "forget", relationHashes, 1.0)
		if err != nil {
			return nil, err
		}
	}
	forgetSuccess := pyTruthy(forgetResult["success"])
	afterStatus, err := store.GetRelationStatusBatch(relationHashes)
	if err != nil {
		return nil, err
	}
	for _, hashValue := range relationHashes {
		_ = s.appendLog(taskID, queryToolID, "forget_relation", hashValue,
			feedbackMapDict(beforeStatus[hashValue]), feedbackMapDict(afterStatus[hashValue]), reason)
	}

	var ingestResult map[string]any
	correctedHashCandidates := make([]string, 0)
	correctedSpecs := map[string]map[string]any{}
	if decisionType == "correct" && len(correctedRelations) > 0 {
		for _, item := range correctedRelations {
			subject := strings.TrimSpace(asString(item["subject"]))
			predicate := strings.TrimSpace(asString(item["predicate"]))
			object := strings.TrimSpace(asString(item["object"]))
			hashValue, hashErr := store.ComputeRelationHash(subject, predicate, object)
			if hashErr != nil || hashValue == "" {
				continue
			}
			correctedHashCandidates = append(correctedHashCandidates, hashValue)
			correctedSpecs[hashValue] = map[string]any{"subject": subject, "predicate": predicate, "object": object}
		}
	}
	correctedBeforeStatus := map[string]map[string]any{}
	if len(correctedHashCandidates) > 0 {
		correctedBeforeStatus, err = store.GetRelationStatusBatch(correctedHashCandidates)
		if err != nil {
			return nil, err
		}
	}
	if !forgetSuccess {
		forgetError := strings.TrimSpace(asString(forgetResult["error"]))
		if forgetError == "" {
			forgetError = "forget_failed"
		}
		return map[string]any{
			"applied": false, "reason": "forget_failed",
			"error": forgetError, "forget": forgetResult, "ingest": ingestResult,
			"relation_hashes": relationHashes, "stale_paragraph_hashes": []string{},
			"episode_rebuild_sources": []string{}, "profile_refresh_person_ids": []string{},
			"rollback_plan_summary": map[string]any{},
		}, nil
	}

	staleParagraphMap := map[string][]string{}
	staleParagraphHashes := make([]string, 0)
	previousStaleMarks := map[string]any{}
	episodeSources := make([]string, 0)
	profilePersonIDs := make([]string, 0)
	var rollbackPlan map[string]any
	ingestSuccess := false
	if decisionType == "correct" && len(correctedRelations) > 0 {
		ingestResult, err = s.feedbackIngestRelations(ctx, queryToolID, sessionID, relationHashes, correctedRelations)
		if err != nil {
			return nil, err
		}
		_ = s.appendLog(taskID, queryToolID, "ingest_correction", relationHashes[0],
			map[string]any{"target_hashes": relationHashes}, ingestResult, reason)
		ingestSuccess = ingestResult != nil && pyTruthy(ingestResult["success"])
		if !ingestSuccess {
			errorText := "correction_ingest_failed"
			if ingestResult != nil && strings.TrimSpace(asString(ingestResult["error"])) != "" {
				errorText = strings.TrimSpace(asString(ingestResult["error"]))
			}
			compensationReason := reason
			if compensationReason == "" {
				compensationReason = "feedback_correction_ingest_failed"
			}
			compensation, err := s.RestoreFeedbackRelationsFromSnapshots(ctx, map[string]any{
				"task_id": taskID, "query_tool_id": queryToolID, "relation_hashes": relationHashes,
				"snapshots": beforeStatus, "current_statuses": afterStatus, "reason": compensationReason,
			})
			if err != nil {
				return nil, err
			}
			compensationMap := feedbackMapDict(compensation)
			restored := tokens(compensationMap["restored_hashes"])
			failed := tokens(compensationMap["failed_hashes"])
			resultReason := "correction_ingest_failed"
			if len(failed) > 0 {
				resultReason = "correction_restore_failed"
			}
			return map[string]any{
				"applied": false, "reason": resultReason, "error": errorText,
				"forget": forgetResult, "ingest": ingestResult, "relation_hashes": relationHashes,
				"stale_paragraph_hashes": []string{}, "episode_rebuild_sources": []string{},
				"profile_refresh_person_ids": []string{}, "restored_relation_hashes": restored,
				"restore_failed_hashes": failed, "rollback_plan_summary": map[string]any{},
			}, nil
		}
	}
	applied := forgetSuccess
	if decisionType == "correct" {
		applied = forgetSuccess && ingestSuccess
	}
	if applied {
		candidateStaleMap, err := store.GetParagraphHashesByRelationHashes(relationHashes)
		if err != nil {
			return nil, err
		}
		for relationHash, paragraphHashes := range candidateStaleMap {
			for _, paragraphHash := range paragraphHashes {
				mark, err := store.GetParagraphStaleRelationMark(paragraphHash, relationHash, nil)
				if err != nil {
					return nil, err
				}
				previousStaleMarks[paragraphHash+"\x00"+relationHash] = mark
			}
		}
		markResult, err := s.MarkFeedbackStaleParagraphs(ctx, map[string]any{
			"task_id": taskID, "query_tool_id": queryToolID, "relation_hashes": relationHashes,
			"reason": reason, "paragraph_map": candidateStaleMap,
		})
		if err != nil {
			return nil, err
		}
		if marked, ok := markResult.(map[string][]string); ok {
			staleParagraphMap = marked
		}
		for _, paragraphHashes := range staleParagraphMap {
			staleParagraphHashes = mergeTokens(staleParagraphHashes, paragraphHashes)
		}
		episodeValue, err := s.EnqueueFeedbackEpisodeRebuilds(ctx, map[string]any{
			"paragraph_hashes": staleParagraphHashes, "session_id": sessionID, "include_correction_source": ingestSuccess,
		})
		if err != nil {
			return nil, err
		}
		episodeSources = tokens(episodeValue)
		personValue, err := s.ResolveFeedbackRelatedPersonIDs(ctx, map[string]any{
			"old_relation_rows": oldRelationRows, "corrected_relations": correctedRelations,
		})
		if err != nil {
			return nil, err
		}
		personCandidates := tokens(personValue)
		profileValue, err := s.EnqueueFeedbackProfileRefreshes(ctx, map[string]any{
			"person_ids": personCandidates, "query_tool_id": queryToolID,
		})
		if err != nil {
			return nil, err
		}
		profilePersonIDs = tokens(profileValue)
		for relationHash, paragraphHashes := range staleParagraphMap {
			for _, paragraphHash := range paragraphHashes {
				_ = s.appendLog(taskID, queryToolID, "mark_stale_paragraph", paragraphHash, nil, map[string]any{"relation_hash": relationHash}, reason)
			}
		}
		for _, source := range episodeSources {
			_ = s.appendLog(taskID, queryToolID, "enqueue_episode_rebuild", source, nil, nil, reason)
		}
		for _, personID := range profilePersonIDs {
			_ = s.appendLog(taskID, queryToolID, "enqueue_profile_refresh", personID, nil, nil, reason)
		}
		forgottenRelations := make([]map[string]any, 0, len(oldRelationRows))
		for _, row := range oldRelationRows {
			relationHash := strings.TrimSpace(asString(row["hash"]))
			if relationHash == "" {
				continue
			}
			forgottenRelations = append(forgottenRelations, map[string]any{
				"hash": relationHash, "subject": strings.TrimSpace(asString(row["subject"])),
				"predicate": strings.TrimSpace(asString(row["predicate"])), "object": strings.TrimSpace(asString(row["object"])),
				"before_status": feedbackMapDict(beforeStatus[relationHash]),
			})
		}
		correctedWrite := map[string]any{}
		if ingestResult != nil {
			storedRelationHashes := tokens(ingestResult["corrected_relation_hashes"])
			correctedEntries := make([]map[string]any, 0, len(storedRelationHashes))
			for _, relationHash := range storedRelationHashes {
				entry := map[string]any{
					"hash": relationHash, "existed_before": false, "before_status": map[string]any{},
				}
				for key, value := range correctedSpecs[relationHash] {
					entry[key] = value
				}
				if status, ok := correctedBeforeStatus[relationHash]; ok {
					entry["existed_before"] = true
					entry["before_status"] = status
				}
				correctedEntries = append(correctedEntries, entry)
			}
			correctedWrite = map[string]any{
				"external_id":               strings.TrimSpace(asString(ingestResult["external_id"])),
				"source":                    strings.TrimSpace(asString(ingestResult["source"])),
				"paragraph_hashes":          tokens(ingestResult["paragraph_hashes"]),
				"corrected_relation_hashes": storedRelationHashes, "corrected_relations": correctedEntries,
			}
		}
		staleMarks := make([]map[string]any, 0)
		for relationHash, paragraphHashes := range staleParagraphMap {
			for _, paragraphHash := range paragraphHashes {
				if strings.TrimSpace(paragraphHash) == "" {
					continue
				}
				staleMarks = append(staleMarks, map[string]any{
					"paragraph_hash": paragraphHash, "relation_hash": relationHash,
					"source_type": "feedback_correction", "source_id": fmt.Sprint(taskID),
					"source_operation_id": fmt.Sprintf("feedback_correction:%d:%s:%s", taskID, paragraphHash, relationHash),
					"previous_mark":       previousStaleMarks[paragraphHash+"\x00"+relationHash],
				})
			}
		}
		rollbackPlan = map[string]any{
			"task_id": taskID, "query_tool_id": queryToolID, "session_id": sessionID, "decision_type": decisionType,
			"forgotten_relations": forgottenRelations, "corrected_write": correctedWrite, "stale_marks": staleMarks,
			"episode_sources": episodeSources, "profile_person_ids": profilePersonIDs,
			"created_at": float64(s.now().UnixNano()) / 1e9,
		}
		if _, err := store.UpdateFeedbackTaskRollbackPlan(taskID, rollbackPlan, nil); err != nil {
			return nil, err
		}
	}
	summary := map[string]any{}
	if rollbackPlan != nil {
		summary = s.feedbackRollbackPlanSummary(rollbackPlan)
	}
	return map[string]any{
		"applied": applied, "forget": forgetResult, "ingest": ingestResult,
		"relation_hashes": relationHashes, "stale_paragraph_hashes": staleParagraphHashes,
		"episode_rebuild_sources": episodeSources, "profile_refresh_person_ids": profilePersonIDs,
		"rollback_plan_summary": summary,
	}, nil
}

// feedbackResolveRelationHashes 同步版本
func (s *FeedbackService) feedbackResolveRelationHashes(targetHashes []string, hitMap map[string]any) []string {
	store := s.store()
	resolved := make([]string, 0)
	seen := map[string]struct{}{}
	for _, targetHash := range tokens(targetHashes) {
		hit := feedbackMapDict(hitMap[targetHash])
		itemType := strings.TrimSpace(asString(hit["type"]))
		if itemType == "relation" {
			if _, ok := seen[targetHash]; !ok {
				seen[targetHash] = struct{}{}
				resolved = append(resolved, targetHash)
			}
			continue
		}
		if itemType != "paragraph" {
			continue
		}
		linkedCandidates := tokens(hit["linked_relation_hashes"])
		if len(linkedCandidates) == 0 && store != nil {
			if relations, err := store.GetParagraphRelations(targetHash); err == nil {
				for _, relation := range relations {
					if linkedHash := strings.TrimSpace(asString(relation["hash"])); linkedHash != "" {
						linkedCandidates = append(linkedCandidates, linkedHash)
					}
				}
			}
		}
		for _, linkedHash := range linkedCandidates {
			if _, ok := seen[linkedHash]; ok {
				continue
			}
			seen[linkedHash] = struct{}{}
			resolved = append(resolved, linkedHash)
		}
	}
	return resolved
}

// feedbackIngestRelations 同步版本
func (s *FeedbackService) feedbackIngestRelations(ctx context.Context, queryToolID string, sessionID string, relationHashes []string, correctedRelations []map[string]any) (map[string]any, error) {
	value, err := s.IngestFeedbackRelations(ctx, map[string]any{
		"query_tool_id": queryToolID, "session_id": sessionID,
		"relation_hashes": relationHashes, "corrected_relations": correctedRelations,
	})
	if err != nil {
		return nil, err
	}
	result := asMap(value)
	if result == nil {
		return map[string]any{"success": false, "error": "invalid_ingest_payload"}, nil
	}
	return result, nil
}

// ProcessFeedbackTask 对齐 _process_feedback_task
func (s *FeedbackService) ProcessFeedbackTask(ctx context.Context, payload map[string]any) (any, error) {
	task := feedbackTaskPayload(payload)
	store := s.store()
	if store == nil {
		return nil, nil
	}
	taskID := feedbackInt(task["id"], 0)
	queryToolID := strings.TrimSpace(asString(task["query_tool_id"]))
	if taskID <= 0 || queryToolID == "" {
		return nil, nil
	}
	runningTask, err := store.MarkFeedbackTaskRunning(taskID, nil, nil)
	if err != nil {
		return nil, err
	}
	if runningTask == nil {
		return nil, nil
	}
	task = runningTask
	decisionPayload := map[string]any{}
	sessionID := strings.TrimSpace(asString(task["session_id"]))
	finish := func(status string, lastError string) {
		if lastError != "" {
			_ = s.appendLog(taskID, queryToolID, "error", "", nil, decisionPayloadOrNil(decisionPayload), lastError)
		}
		_, _ = store.FinalizeFeedbackTask(storage.FeedbackTaskFinalizeOptions{
			TaskID: taskID, Status: status, DecisionPayload: decisionPayload, LastError: lastError,
		}, nil)
	}
	structured := feedbackMapDict(task["query_snapshot"])
	if sessionID == "" {
		sessionID = strings.TrimSpace(asString(structured["chat_id"]))
	}
	if sessionID == "" {
		finish("error", "反馈任务缺少 session_id")
		return nil, nil
	}
	hitsRaw := asList(structured["hits"])
	if len(hitsRaw) == 0 {
		decisionPayload = map[string]any{"decision": "none", "confidence": 1.0, "reason": "no_hits"}
		finish("skipped", "")
		return nil, nil
	}
	queryTimestamp := s.feedbackTimestamp(task["query_timestamp"])
	dueAt := s.feedbackTimestamp(task["due_at"])
	if !dueAt.After(queryTimestamp) {
		dueAt = queryTimestamp.Add(time.Duration(utils.FeedbackCfgWindowHours(s.feedbackCfg()) * float64(time.Hour)))
	}
	messagesValue, err := s.ExtractFeedbackMessages(ctx, map[string]any{
		"session_id": sessionID, "query_time": queryTimestamp, "due_time": dueAt,
		"max_messages": utils.FeedbackCfgMaxMessages(s.feedbackCfg()),
	})
	if err != nil {
		finish("error", err.Error())
		return nil, nil
	}
	feedbackMessages := tokens(messagesValue)
	if len(feedbackMessages) == 0 {
		decisionPayload = map[string]any{"decision": "none", "confidence": 1.0, "reason": "no_feedback_messages"}
		finish("skipped", "")
		return nil, nil
	}
	if utils.FeedbackCfgPrefilterEnabled(s.feedbackCfg()) {
		should, err := s.ShouldInvokeFeedbackClassifier(ctx, map[string]any{"feedback_messages": feedbackMessages})
		if err != nil {
			finish("error", err.Error())
			return nil, nil
		}
		if !pyTruthy(should) {
			decisionPayload = map[string]any{"decision": "none", "confidence": 1.0, "reason": "prefilter_skipped"}
			_ = s.appendLog(taskID, queryToolID, "skip", "", nil, map[string]any{"feedback_messages": feedbackMessages}, "prefilter_skipped")
			finish("skipped", "")
			return nil, nil
		}
	}
	briefsValue, err := s.BuildFeedbackHitBriefs(ctx, map[string]any{"hits": hitsRaw})
	if err != nil {
		finish("error", err.Error())
		return nil, nil
	}
	hitBriefs := feedbackMapList(briefsValue)
	hitMap := map[string]any{}
	for _, item := range hitBriefs {
		hashValue := strings.TrimSpace(asString(item["hash"]))
		if hashValue != "" {
			hitMap[hashValue] = item
		}
	}
	rawDecision, err := s.ClassifyFeedback(ctx, map[string]any{
		"query_tool_id": queryToolID, "query_text": asString(structured["query"]),
		"hit_briefs": hitBriefs, "feedback_messages": feedbackMessages,
	})
	if err != nil {
		finish("error", err.Error())
		return nil, nil
	}
	normalizedValue, err := s.NormalizeFeedbackDecision(ctx, map[string]any{
		"payload": asMap(rawDecision), "hit_hashes": feedbackMapKeys(hitMap),
	})
	if err != nil {
		finish("error", err.Error())
		return nil, nil
	}
	decisionPayload = feedbackMapDict(normalizedValue)
	decisionPayload["feedback_message_count"] = len(feedbackMessages)
	_ = s.appendLog(taskID, queryToolID, "classification", "", nil, decisionPayload, strings.TrimSpace(asString(decisionPayload["reason"])))
	applyValue, err := s.ApplyFeedbackDecision(ctx, map[string]any{
		"task_id": taskID, "query_tool_id": queryToolID, "session_id": sessionID,
		"decision": decisionPayload,
		"hit_map":  hitMap,
	})
	if err != nil {
		finish("error", err.Error())
		return nil, nil
	}
	applyResult := feedbackMapDict(applyValue)
	decisionPayload["apply_result"] = applyResult
	finalStatus := feedbackApplyResultStatus(applyResult)
	lastError := ""
	if finalStatus == "error" {
		lastError = strings.TrimSpace(asString(applyResult["error"]))
	}
	finish(finalStatus, lastError)
	return nil, nil
}

// feedbackApplyResultStatus 同步版本
func feedbackApplyResultStatus(applyResult map[string]any) string {
	if pyTruthy(applyResult["applied"]) {
		return "applied"
	}
	reason := strings.ToLower(strings.TrimSpace(asString(applyResult["reason"])))
	if reason == "low_confidence" || reason == "no_relation_targets" || strings.HasPrefix(reason, "decision_") {
		return "skipped"
	}
	return "error"
}

func decisionPayloadOrNil(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	return payload
}

// mapKeys 返回对象键集合
func feedbackMapKeys(source map[string]any) []string {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	return keys
}

// FeedbackCorrectionLoop 对齐 _feedback_correction_loop
func (s *FeedbackService) FeedbackCorrectionLoop(ctx context.Context, payload map[string]any) (any, error) {
	for !s.stopping() {
		interval := utils.FeedbackCfgCheckIntervalSeconds(s.feedbackCfg())
		if !utils.FeedbackCfgEnabled(s.feedbackCfg()) || s.store() == nil {
			if !s.sleep(ctx, interval) {
				break
			}
			continue
		}
		now := float64(s.now().UnixNano()) / 1e9
		tasks, err := s.store().FetchDueFeedbackTasks(storage.FeedbackDueFetchOptions{
			Limit: utils.FeedbackCfgBatchSize(s.feedbackCfg()), Now: &now,
		}, nil)
		if err != nil {
			s.LoggerWarn("feedback_correction 拉取任务失败: %v", err)
			if !s.sleep(ctx, interval) {
				break
			}
			continue
		}
		if len(tasks) == 0 {
			if !s.sleep(ctx, interval) {
				break
			}
			continue
		}
		for _, task := range tasks {
			if s.stopping() {
				break
			}
			if task == nil {
				continue
			}
			if _, err := s.ProcessFeedbackTask(ctx, map[string]any{"task": task}); err != nil {
				s.LoggerWarn("反馈纠错任务处理失败: %v", err)
			}
		}
		if !s.sleep(ctx, 2.0) {
			break
		}
	}
	return nil, nil
}

// MemoryFeedbackAdmin 对齐 memory_feedback_admin
func (s *FeedbackService) MemoryFeedbackAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	act := strings.ToLower(strings.TrimSpace(action))
	store := s.store()
	if store == nil {
		return map[string]any{"success": false, "error": "metadata store unavailable"}, nil
	}
	if act == "list" {
		limit := maxInt(1, feedbackInt(firstAny(payload["limit"], 50), 50))
		items, err := store.ListFeedbackTasks(storage.FeedbackTaskListOptions{
			Limit:            limit,
			Statuses:         tokens(firstAny(payload["status"], payload["statuses"])),
			RollbackStatuses: tokens(firstAny(payload["rollback_status"], payload["rollback_statuses"])),
			Query:            strings.TrimSpace(asString(payload["query"])),
		}, nil)
		if err != nil {
			return nil, err
		}
		summaries := make([]map[string]any, 0, len(items))
		for _, task := range items {
			summaries = append(summaries, s.feedbackTaskSummary(task))
		}
		return map[string]any{"success": true, "items": summaries, "count": len(summaries)}, nil
	}
	if act == "get" {
		taskID := feedbackInt(payload["task_id"], 0)
		task, err := store.GetFeedbackTaskByID(taskID, nil)
		if err != nil {
			return nil, err
		}
		if task == nil {
			return map[string]any{"success": false, "error": "反馈纠错任务不存在"}, nil
		}
		return map[string]any{"success": true, "task": s.feedbackTaskDetail(task)}, nil
	}
	if act == "rollback" {
		return s.RollbackFeedbackTask(ctx, map[string]any{
			"task_id":      payload["task_id"],
			"requested_by": payload["requested_by"],
			"reason":       payload["reason"],
		})
	}
	return map[string]any{"success": false, "error": "不支持的 feedback action: " + act}, nil
}
