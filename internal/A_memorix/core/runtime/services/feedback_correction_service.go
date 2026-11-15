package services

// 反馈纠错服务，逐段对齐 feedback_correction_service.py 的 MemoryFeedbackCorrectionService
// 任务状态机与回滚补偿计划写库逻辑分别由 storage.metadata_feedback 与 metadata_feedback_marks 承载

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// FeedbackDeps 反馈纠错依赖端口
type FeedbackDeps struct {
	Metadata                         func() *storage.Store
	FeedbackConfig                   func() *utils.FeedbackIntegrationConfig
	Initialize                       func(ctx context.Context) error
	BackgroundStopping               func() bool
	Persist                          func()
	DeleteAdmin                      *DeleteAdminService
	Ingest                           *IngestService
	V5Admin                          *V5AdminService
	ProfileAdmin                     *ProfileAdminService
	GraphAdmin                       *GraphAdminService
	SearchHit                        *SearchHitService
	ResolvePersonID                  func(ctx context.Context, identifier string) (string, error)
	Messages                         func(ctx context.Context, chatID string, startTime float64, endTime float64, limit int, limitMode string) ([]utils.SessionMessage, error)
	LLMGenerate                      func(ctx context.Context, prompt string) (string, error)
	ProcessEpisodeSourceRebuildBatch func(ctx context.Context, params map[string]any) (map[string]any, error)
	Now                              func() time.Time
}

// FeedbackService 反馈纠错服务
type FeedbackService struct {
	*Base
	deps FeedbackDeps
}

// NewFeedbackService 构造反馈纠错服务
func NewFeedbackService(base *Base, deps FeedbackDeps) *FeedbackService {
	return &FeedbackService{Base: base, deps: deps}
}

func (s *FeedbackService) store() *storage.Store {
	if s == nil || s.deps.Metadata == nil {
		return nil
	}
	return s.deps.Metadata()
}

func (s *FeedbackService) feedbackCfg() *utils.FeedbackIntegrationConfig {
	if s == nil || s.deps.FeedbackConfig == nil {
		return nil
	}
	return s.deps.FeedbackConfig()
}

func (s *FeedbackService) now() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now()
	}
	return time.Now()
}

func (s *FeedbackService) stopping() bool {
	return s.deps.BackgroundStopping != nil && s.deps.BackgroundStopping()
}

func (s *FeedbackService) initialize(ctx context.Context) error {
	if s.deps.Initialize != nil {
		return s.deps.Initialize(ctx)
	}
	return nil
}

func (s *FeedbackService) persist() {
	if s.deps.Persist != nil {
		s.deps.Persist()
	}
}

// feedbackTaskPayload 读取 payload 中的 task，缺省时把 payload 本身当任务字典
func feedbackTaskPayload(payload map[string]any) map[string]any {
	if task := asMap(payload["task"]); task != nil {
		return task
	}
	return payload
}

// feedbackMapList 把 []any 收敛为对象列表
func feedbackMapList(value any) []map[string]any {
	items := asList(value)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if row := asMap(item); row != nil {
			out = append(out, row)
		}
	}
	return out
}

// feedbackMapDict 把对象值收敛为 map，非对象折叠为空对象
func feedbackMapDict(value any) map[string]any {
	if row := asMap(value); row != nil {
		return row
	}
	return map[string]any{}
}

// feedbackJSON 对齐 Python json.dumps(..., ensure_ascii=False)
func feedbackJSON(value any) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "null"
	}
	return strings.TrimSuffix(buffer.String(), "\n")
}

// feedbackTimestamp 对齐 Python coerce_datetime(value) or datetime.now()
func (s *FeedbackService) feedbackTimestamp(value any) time.Time {
	if parsed := utils.CoerceDatetime(value); parsed != nil {
		return *parsed
	}
	return s.now()
}

// feedbackFloat 读取浮点值，非法时返回默认值
func feedbackFloat(value any, fallback float64) float64 {
	if parsed, ok := asFloat(value); ok {
		return parsed
	}
	return fallback
}

// feedbackInt 对齐 Python int(value or fallback)
func feedbackInt(value any, fallback int) int {
	parsed := intOr(value, fallback, math.MinInt)
	return parsed
}

// feedbackClamp 把浮点值收敛到 [low, high]
func feedbackClamp(value float64, low float64, high float64) float64 {
	if math.IsNaN(value) {
		return low
	}
	return math.Min(high, math.Max(low, value))
}

// feedbackSessionMessagesText 从会话消息中提取文本，替代 Python processed_plain_text
func feedbackSessionMessagesText(message utils.SessionMessage) string {
	parts := make([]string, 0, len(message.RawMessage.Components))
	for _, component := range message.RawMessage.Components {
		if component.Kind != utils.ComponentText {
			continue
		}
		token := strings.TrimSpace(component.Text)
		if token != "" {
			parts = append(parts, token)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// feedbackChatSource 对齐 _chat_source
func (s *FeedbackService) feedbackChatSource(ctx context.Context, sessionID string) string {
	if s.deps.SearchHit != nil {
		value, err := s.deps.SearchHit.ChatSource(ctx, map[string]any{"chat_id": sessionID})
		if err == nil {
			return strings.TrimSpace(asString(value))
		}
	}
	token := strings.TrimSpace(sessionID)
	if token == "" {
		return ""
	}
	return "chat_summary:" + token
}

// feedbackLoadParagraphRows 对齐 _load_paragraph_rows
func (s *FeedbackService) feedbackLoadParagraphRows(hashes []string) []map[string]any {
	store := s.store()
	tokenList := tokens(hashes)
	if store == nil || len(tokenList) == 0 {
		return nil
	}
	rows, err := store.GetParagraphsByHashes(tokenList)
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(rows))
	for _, token := range tokenList {
		if row, ok := rows[token]; ok && row != nil {
			out = append(out, row)
		}
	}
	return out
}

// ResolveFeedbackRelatedPersonIDs 对齐 _resolve_feedback_related_person_ids
func (s *FeedbackService) ResolveFeedbackRelatedPersonIDs(ctx context.Context, payload map[string]any) (any, error) {
	rows := append(feedbackMapList(payload["old_relation_rows"]), feedbackMapList(payload["corrected_relations"])...)
	candidates := make([]string, 0)
	for _, row := range rows {
		for _, value := range []any{row["subject"], row["object"]} {
			token := strings.TrimSpace(asString(value))
			if token != "" {
				candidates = append(candidates, token)
			}
		}
	}
	resolved := make([]string, 0)
	seen := map[string]struct{}{}
	for _, candidate := range tokens(candidates) {
		if s.deps.ResolvePersonID == nil {
			continue
		}
		personID, err := s.deps.ResolvePersonID(ctx, candidate)
		if err != nil {
			continue
		}
		token := strings.TrimSpace(personID)
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		resolved = append(resolved, token)
	}
	return resolved, nil
}

// MarkFeedbackStaleParagraphs 对齐 _mark_feedback_stale_paragraphs
func (s *FeedbackService) MarkFeedbackStaleParagraphs(ctx context.Context, payload map[string]any) (any, error) {
	store := s.store()
	if store == nil || !utils.FeedbackCfgParagraphMarkEnabled(s.feedbackCfg()) {
		return map[string][]string{}, nil
	}
	taskID := feedbackInt(payload["task_id"], 0)
	queryToolID := strings.TrimSpace(asString(payload["query_tool_id"]))
	reason := strings.TrimSpace(asString(payload["reason"]))
	relationHashes := tokens(payload["relation_hashes"])
	var paragraphMap map[string][]string
	if raw := asMap(payload["paragraph_map"]); raw != nil {
		paragraphMap = make(map[string][]string, len(raw))
		for key, value := range raw {
			paragraphMap[key] = tokens(value)
		}
	} else {
		loaded, err := store.GetParagraphHashesByRelationHashes(relationHashes)
		if err != nil {
			return nil, err
		}
		paragraphMap = loaded
	}
	for relationHash, paragraphHashes := range paragraphMap {
		for _, paragraphHash := range paragraphHashes {
			if _, err := store.UpsertParagraphStaleRelationMark(storage.ParagraphStaleMarkUpsertOptions{
				ParagraphHash:     paragraphHash,
				RelationHash:      relationHash,
				QueryToolID:       queryToolID,
				TaskID:            taskID,
				Reason:            reason,
				SourceType:        "feedback_correction",
				SourceID:          fmt.Sprint(taskID),
				SourceOperationID: fmt.Sprintf("feedback_correction:%d:%s:%s", taskID, paragraphHash, relationHash),
			}, nil); err != nil {
				return nil, err
			}
		}
	}
	return paragraphMap, nil
}

// EnqueueFeedbackEpisodeRebuilds 对齐 _enqueue_feedback_episode_rebuilds
func (s *FeedbackService) EnqueueFeedbackEpisodeRebuilds(ctx context.Context, payload map[string]any) (any, error) {
	store := s.store()
	if store == nil || !utils.FeedbackCfgEpisodeRebuildEnabled(s.feedbackCfg()) {
		return []string{}, nil
	}
	paragraphHashes := tokens(payload["paragraph_hashes"])
	sources := make([]string, 0)
	for _, row := range s.feedbackLoadParagraphRows(paragraphHashes) {
		token := strings.TrimSpace(asString(row["source"]))
		if token != "" {
			sources = append(sources, token)
		}
	}
	sources = tokens(sources)
	sessionID := strings.TrimSpace(asString(payload["session_id"]))
	correctionSource := s.feedbackChatSource(ctx, sessionID)
	if pyTruthy(payload["include_correction_source"]) && correctionSource != "" {
		sources = mergeTokens(sources, []string{correctionSource})
	}
	queued := make([]string, 0)
	for _, source := range sources {
		if store.EnqueueEpisodeSourceRebuild(source, "feedback_correction", storage.EpisodeEnqueueOptions{}) {
			queued = append(queued, source)
		}
	}
	return queued, nil
}

// EnqueueFeedbackProfileRefreshes 对齐 _enqueue_feedback_profile_refreshes
func (s *FeedbackService) EnqueueFeedbackProfileRefreshes(ctx context.Context, payload map[string]any) (any, error) {
	store := s.store()
	if store == nil || !utils.FeedbackCfgProfileRefreshEnabled(s.feedbackCfg()) {
		return []string{}, nil
	}
	queryToolID := strings.TrimSpace(asString(payload["query_tool_id"]))
	queued := make([]string, 0)
	for _, personID := range tokens(payload["person_ids"]) {
		result, err := store.EnqueuePersonProfileRefresh(storage.PersonProfileRefreshEnqueueInput{
			PersonID: personID, Reason: "feedback_correction", SourceQueryToolID: queryToolID,
		})
		if err != nil {
			return nil, err
		}
		if result != nil {
			queued = append(queued, personID)
		}
	}
	return queued, nil
}

// FeedbackAffectedCounts 对齐 _feedback_affected_counts
func (s *FeedbackService) FeedbackAffectedCounts(ctx context.Context, payload map[string]any) (any, error) {
	task := feedbackTaskPayload(payload)
	decisionPayload := feedbackMapDict(task["decision_payload"])
	applyResult := feedbackMapDict(decisionPayload["apply_result"])
	rollbackPlan := feedbackMapDict(task["rollback_plan"])
	correctedWrite := feedbackMapDict(rollbackPlan["corrected_write"])
	countList := func(primary any, fallback any) int {
		if items := asList(primary); len(items) > 0 {
			return len(items)
		}
		return len(asList(fallback))
	}
	return map[string]any{
		"relations":             countList(applyResult["relation_hashes"], rollbackPlan["forgotten_relations"]),
		"stale_paragraphs":      countList(applyResult["stale_paragraph_hashes"], rollbackPlan["stale_marks"]),
		"episode_sources":       countList(applyResult["episode_rebuild_sources"], rollbackPlan["episode_sources"]),
		"profile_person_ids":    countList(applyResult["profile_refresh_person_ids"], rollbackPlan["profile_person_ids"]),
		"correction_paragraphs": len(asList(correctedWrite["paragraph_hashes"])),
		"corrected_relations":   len(asList(correctedWrite["corrected_relations"])),
	}, nil
}

// BuildFeedbackRollbackPlanSummary 对齐 _build_feedback_rollback_plan_summary
func (s *FeedbackService) BuildFeedbackRollbackPlanSummary(ctx context.Context, payload map[string]any) (any, error) {
	rollbackPlan := feedbackTaskPayload(payload)
	correctedWrite := feedbackMapDict(rollbackPlan["corrected_write"])
	forgottenRelations := asList(rollbackPlan["forgotten_relations"])
	correctedRelations := asList(correctedWrite["corrected_relations"])
	paragraphHashes := asList(correctedWrite["paragraph_hashes"])
	staleMarks := asList(rollbackPlan["stale_marks"])
	episodeSources := tokens(rollbackPlan["episode_sources"])
	profilePersonIDs := tokens(rollbackPlan["profile_person_ids"])
	return map[string]any{
		"forgotten_relations": forgottenRelations,
		"corrected_write":     correctedWrite,
		"stale_marks":         staleMarks,
		"episode_sources":     episodeSources,
		"profile_person_ids":  profilePersonIDs,
		"affected_counts": map[string]any{
			"forgotten_relations":   len(forgottenRelations),
			"corrected_relations":   len(correctedRelations),
			"correction_paragraphs": len(paragraphHashes),
			"stale_marks":           len(staleMarks),
			"episode_sources":       len(episodeSources),
			"profile_person_ids":    len(profilePersonIDs),
		},
	}, nil
}

// BuildFeedbackTaskSummary 对齐 _build_feedback_task_summary
func (s *FeedbackService) BuildFeedbackTaskSummary(ctx context.Context, payload map[string]any) (any, error) {
	task := feedbackTaskPayload(payload)
	querySnapshot := feedbackMapDict(task["query_snapshot"])
	decisionPayload := feedbackMapDict(task["decision_payload"])
	rollbackStatus := strings.ToLower(strings.TrimSpace(asString(task["rollback_status"])))
	if rollbackStatus == "" {
		rollbackStatus = "none"
	}
	return map[string]any{
		"task_id":                feedbackInt(task["id"], 0),
		"query_tool_id":          strings.TrimSpace(asString(task["query_tool_id"])),
		"session_id":             strings.TrimSpace(asString(task["session_id"])),
		"query_text":             strings.TrimSpace(asString(querySnapshot["query"])),
		"query_timestamp":        task["query_timestamp"],
		"task_status":            strings.ToLower(strings.TrimSpace(asString(task["status"]))),
		"decision":               strings.ToLower(strings.TrimSpace(asString(decisionPayload["decision"]))),
		"decision_confidence":    feedbackFloat(decisionPayload["confidence"], 0),
		"feedback_message_count": feedbackInt(decisionPayload["feedback_message_count"], 0),
		"rollback_status":        rollbackStatus,
		"affected_counts":        s.feedbackAffectedCounts(task),
		"created_at":             task["created_at"],
		"updated_at":             task["updated_at"],
	}, nil
}

// feedbackAffectedCounts 同步版本，供摘要与详情复用
func (s *FeedbackService) feedbackAffectedCounts(task map[string]any) map[string]any {
	decisionPayload := feedbackMapDict(task["decision_payload"])
	applyResult := feedbackMapDict(decisionPayload["apply_result"])
	rollbackPlan := feedbackMapDict(task["rollback_plan"])
	correctedWrite := feedbackMapDict(rollbackPlan["corrected_write"])
	countList := func(primary any, fallback any) int {
		if items := asList(primary); len(items) > 0 {
			return len(items)
		}
		return len(asList(fallback))
	}
	return map[string]any{
		"relations":             countList(applyResult["relation_hashes"], rollbackPlan["forgotten_relations"]),
		"stale_paragraphs":      countList(applyResult["stale_paragraph_hashes"], rollbackPlan["stale_marks"]),
		"episode_sources":       countList(applyResult["episode_rebuild_sources"], rollbackPlan["episode_sources"]),
		"profile_person_ids":    countList(applyResult["profile_refresh_person_ids"], rollbackPlan["profile_person_ids"]),
		"correction_paragraphs": len(asList(correctedWrite["paragraph_hashes"])),
		"corrected_relations":   len(asList(correctedWrite["corrected_relations"])),
	}
}

// BuildFeedbackTaskDetail 对齐 _build_feedback_task_detail
func (s *FeedbackService) BuildFeedbackTaskDetail(ctx context.Context, payload map[string]any) (any, error) {
	task := feedbackTaskPayload(payload)
	summary := s.feedbackTaskSummary(task)
	taskID := feedbackInt(task["id"], 0)
	actionLogs := []map[string]any{}
	if store := s.store(); store != nil {
		if rows, err := store.ListFeedbackActionLogs(taskID, nil); err == nil && rows != nil {
			actionLogs = rows
		}
	}
	summary["query_snapshot"] = feedbackMapDict(task["query_snapshot"])
	summary["decision_payload"] = feedbackMapDict(task["decision_payload"])
	summary["rollback_plan_summary"] = s.feedbackRollbackPlanSummary(feedbackMapDict(task["rollback_plan"]))
	summary["rollback_result"] = feedbackMapDict(task["rollback_result"])
	summary["rollback_error"] = strings.TrimSpace(asString(task["rollback_error"]))
	summary["rollback_requested_by"] = strings.TrimSpace(asString(task["rollback_requested_by"]))
	summary["rollback_reason"] = strings.TrimSpace(asString(task["rollback_reason"]))
	summary["rollback_requested_at"] = task["rollback_requested_at"]
	summary["rolled_back_at"] = task["rolled_back_at"]
	summary["action_logs"] = actionLogs
	return summary, nil
}

// feedbackTaskSummary 同步版本，供详情复用
func (s *FeedbackService) feedbackTaskSummary(task map[string]any) map[string]any {
	querySnapshot := feedbackMapDict(task["query_snapshot"])
	decisionPayload := feedbackMapDict(task["decision_payload"])
	rollbackStatus := strings.ToLower(strings.TrimSpace(asString(task["rollback_status"])))
	if rollbackStatus == "" {
		rollbackStatus = "none"
	}
	return map[string]any{
		"task_id":                feedbackInt(task["id"], 0),
		"query_tool_id":          strings.TrimSpace(asString(task["query_tool_id"])),
		"session_id":             strings.TrimSpace(asString(task["session_id"])),
		"query_text":             strings.TrimSpace(asString(querySnapshot["query"])),
		"query_timestamp":        task["query_timestamp"],
		"task_status":            strings.ToLower(strings.TrimSpace(asString(task["status"]))),
		"decision":               strings.ToLower(strings.TrimSpace(asString(decisionPayload["decision"]))),
		"decision_confidence":    feedbackFloat(decisionPayload["confidence"], 0),
		"feedback_message_count": feedbackInt(decisionPayload["feedback_message_count"], 0),
		"rollback_status":        rollbackStatus,
		"affected_counts":        s.feedbackAffectedCounts(task),
		"created_at":             task["created_at"],
		"updated_at":             task["updated_at"],
	}
}

// feedbackRollbackPlanSummary 同步版本
func (s *FeedbackService) feedbackRollbackPlanSummary(rollbackPlan map[string]any) map[string]any {
	correctedWrite := feedbackMapDict(rollbackPlan["corrected_write"])
	forgottenRelations := asList(rollbackPlan["forgotten_relations"])
	correctedRelations := asList(correctedWrite["corrected_relations"])
	paragraphHashes := asList(correctedWrite["paragraph_hashes"])
	staleMarks := asList(rollbackPlan["stale_marks"])
	episodeSources := tokens(rollbackPlan["episode_sources"])
	profilePersonIDs := tokens(rollbackPlan["profile_person_ids"])
	return map[string]any{
		"forgotten_relations": forgottenRelations,
		"corrected_write":     correctedWrite,
		"stale_marks":         staleMarks,
		"episode_sources":     episodeSources,
		"profile_person_ids":  profilePersonIDs,
		"affected_counts": map[string]any{
			"forgotten_relations":   len(forgottenRelations),
			"corrected_relations":   len(correctedRelations),
			"correction_paragraphs": len(paragraphHashes),
			"stale_marks":           len(staleMarks),
			"episode_sources":       len(episodeSources),
			"profile_person_ids":    len(profilePersonIDs),
		},
	}
}

// SoftDeleteFeedbackCorrectionParagraphs 对齐 _soft_delete_feedback_correction_paragraphs
func (s *FeedbackService) SoftDeleteFeedbackCorrectionParagraphs(ctx context.Context, payload map[string]any) (any, error) {
	store := s.store()
	hashes := tokens(payload["paragraph_hashes"])
	result := map[string]any{"deleted_hashes": []string{}, "deleted_external_refs": []any{}}
	if store == nil || len(hashes) == 0 {
		return result, nil
	}
	paragraphRows := map[string]any{}
	for _, hashValue := range hashes {
		row, err := store.GetParagraph(hashValue)
		if err != nil {
			return nil, err
		}
		paragraphRows[hashValue] = row
	}
	externalRefs, err := store.ListExternalMemoryRefsByParagraphs(hashes)
	if err != nil {
		return nil, err
	}
	if s.deps.DeleteAdmin == nil {
		return nil, fmt.Errorf("删除管理服务未初始化")
	}
	deleteResult, err := s.deps.DeleteAdmin.executeDeleteAction(ctx, "paragraph", map[string]any{"hashes": hashes}, "feedback_correction_retracted", "feedback_correction", nil)
	if err != nil {
		return nil, err
	}
	if !pyTruthy(deleteResult["success"]) {
		message := strings.TrimSpace(asString(deleteResult["error"]))
		if message == "" {
			message = "反馈纠错段落删除失败"
		}
		return nil, fmt.Errorf("%s", message)
	}
	return map[string]any{
		"deleted_hashes":        hashes,
		"paragraph_rows":        paragraphRows,
		"deleted_external_refs": externalRefs,
		"delete_operation_id":   deleteResult["operation_id"],
	}, nil
}

// ExtractFeedbackMessages 对齐 _extract_feedback_messages
func (s *FeedbackService) ExtractFeedbackMessages(ctx context.Context, payload map[string]any) (any, error) {
	sessionID := strings.TrimSpace(asString(payload["session_id"]))
	maxMessages := max(1, feedbackInt(payload["max_messages"], 1))
	queryTime := s.feedbackTimestamp(payload["query_time"])
	dueTime := s.feedbackTimestamp(payload["due_time"])
	collected := make([]string, 0)
	if s.deps.Messages == nil || sessionID == "" {
		return collected, nil
	}
	rows, err := s.deps.Messages(ctx, sessionID, float64(queryTime.UnixNano())/1e9, float64(dueTime.UnixNano())/1e9, max(1, maxMessages*4), "latest")
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, item := range rows {
		text := strings.TrimSpace(feedbackSessionMessagesText(item))
		if utils.FeedbackNoise(text) {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		collected = append(collected, text)
	}
	if len(collected) > maxMessages {
		collected = collected[len(collected)-maxMessages:]
	}
	return collected, nil
}

// BuildFeedbackHitBriefs 对齐 _build_feedback_hit_briefs
func (s *FeedbackService) BuildFeedbackHitBriefs(ctx context.Context, payload map[string]any) (any, error) {
	hits := feedbackMapList(payload["hits"])
	limit := max(1, feedbackInt(payload["limit"], 12))
	store := s.store()
	briefs := make([]map[string]any, 0, limit)
	for index, raw := range hits {
		if index >= limit {
			break
		}
		metadata := feedbackMapDict(raw["metadata"])
		subject := strings.TrimSpace(asString(metadata["subject"]))
		predicate := strings.TrimSpace(asString(metadata["predicate"]))
		object := strings.TrimSpace(asString(metadata["object"]))
		linkedHashes := make([]string, 0)
		linkedTexts := make([]string, 0)
		itemType := strings.TrimSpace(asString(raw["type"]))
		itemHash := strings.TrimSpace(asString(raw["hash"]))
		if itemType == "paragraph" && itemHash != "" && store != nil {
			relations, err := store.GetParagraphRelations(itemHash)
			if err == nil {
				for _, relation := range relations {
					relationHash := strings.TrimSpace(asString(relation["hash"]))
					if relationHash == "" || containsValue(linkedHashes, relationHash) {
						continue
					}
					linkedHashes = append(linkedHashes, relationHash)
					relSubject := strings.TrimSpace(asString(relation["subject"]))
					relPredicate := strings.TrimSpace(asString(relation["predicate"]))
					relObject := strings.TrimSpace(asString(relation["object"]))
					if text := formatRelationText(relSubject, relPredicate, relObject); text != "" {
						linkedTexts = append(linkedTexts, text)
					}
					if subject == "" || predicate == "" || object == "" {
						subject, predicate, object = relSubject, relPredicate, relObject
					}
				}
			}
		}
		briefs = append(briefs, map[string]any{
			"hash":                   itemHash,
			"type":                   itemType,
			"content":                strings.TrimSpace(asString(raw["content"])),
			"subject":                subject,
			"predicate":              predicate,
			"object":                 object,
			"linked_relation_hashes": linkedHashes[:min(6, len(linkedHashes))],
			"linked_relation_texts":  linkedTexts[:min(3, len(linkedTexts))],
		})
	}
	return briefs, nil
}

// ShouldInvokeFeedbackClassifier 对齐 _should_invoke_feedback_classifier
func (s *FeedbackService) ShouldInvokeFeedbackClassifier(ctx context.Context, payload map[string]any) (any, error) {
	messages := tokens(payload["feedback_messages"])
	if len(messages) == 0 {
		return false, nil
	}
	lowered := strings.ToLower(strings.Join(messages, "\n"))
	for _, token := range utils.FeedbackSignalTokens() {
		if strings.Contains(lowered, strings.ToLower(token)) {
			return true, nil
		}
	}
	return false, nil
}

// NormalizeFeedbackDecision 对齐 _normalize_feedback_decision
func (s *FeedbackService) NormalizeFeedbackDecision(ctx context.Context, payload map[string]any) (any, error) {
	raw := payload
	if nested := asMap(payload["payload"]); nested != nil {
		raw = nested
	}
	allowed := map[string]struct{}{"confirm": {}, "reject": {}, "correct": {}, "supplement": {}, "none": {}}
	decision := strings.ToLower(strings.TrimSpace(asString(raw["decision"])))
	if _, ok := allowed[decision]; !ok {
		decision = "none"
	}
	confidence := feedbackClamp(feedbackFloat(raw["confidence"], 0), 0, 1)
	validHashes := map[string]struct{}{}
	for _, item := range tokens(payload["hit_hashes"]) {
		validHashes[item] = struct{}{}
	}
	targetCandidates := []any{}
	switch value := raw["target_hashes"].(type) {
	case string:
		targetCandidates = []any{value}
	case []any:
		targetCandidates = value
	case []string:
		for _, item := range value {
			targetCandidates = append(targetCandidates, item)
		}
	}
	targetHashes := make([]string, 0)
	for _, item := range targetCandidates {
		token := strings.TrimSpace(asString(item))
		if _, ok := validHashes[token]; ok && token != "" {
			targetHashes = append(targetHashes, token)
		}
	}
	correctedRelations := make([]map[string]any, 0)
	for _, item := range asList(raw["corrected_relations"]) {
		row := asMap(item)
		if row == nil {
			continue
		}
		subject := strings.TrimSpace(asString(row["subject"]))
		predicate := strings.TrimSpace(asString(row["predicate"]))
		object := strings.TrimSpace(asString(row["object"]))
		if subject == "" || predicate == "" || object == "" {
			continue
		}
		correctedRelations = append(correctedRelations, map[string]any{
			"subject": subject, "predicate": predicate, "object": object,
			"confidence": feedbackClamp(feedbackFloat(row["confidence"], 1), 0, 1),
		})
	}
	if len(correctedRelations) > 6 {
		correctedRelations = correctedRelations[:6]
	}
	return map[string]any{
		"decision":            decision,
		"confidence":          confidence,
		"target_hashes":       targetHashes,
		"corrected_relations": correctedRelations,
		"reason":              strings.TrimSpace(asString(raw["reason"])),
		"raw":                 raw,
	}, nil
}

// FeedbackApplyResultStatus 对齐 _feedback_apply_result_status
func (s *FeedbackService) FeedbackApplyResultStatus(ctx context.Context, payload map[string]any) (any, error) {
	applyResult := payload
	if nested := asMap(payload["apply_result"]); nested != nil {
		applyResult = nested
	}
	if pyTruthy(applyResult["applied"]) {
		return "applied", nil
	}
	reason := strings.ToLower(strings.TrimSpace(asString(applyResult["reason"])))
	if reason == "low_confidence" || reason == "no_relation_targets" || strings.HasPrefix(reason, "decision_") {
		return "skipped", nil
	}
	return "error", nil
}

// RestoreFeedbackRelationsFromSnapshots 对齐 _restore_feedback_relations_from_snapshots
func (s *FeedbackService) RestoreFeedbackRelationsFromSnapshots(ctx context.Context, payload map[string]any) (any, error) {
	store := s.store()
	restored := make([]string, 0)
	failed := make([]string, 0)
	if store == nil {
		return map[string]any{"restored_hashes": restored, "failed_hashes": failed}, nil
	}
	taskID := feedbackInt(payload["task_id"], 0)
	queryToolID := strings.TrimSpace(asString(payload["query_tool_id"]))
	reason := strings.TrimSpace(asString(payload["reason"]))
	snapshots := asMap(payload["snapshots"])
	statusMap := asMap(payload["current_statuses"])
	for _, relationHash := range tokens(payload["relation_hashes"]) {
		snapshot := feedbackMapDict(snapshots[relationHash])
		if len(snapshot) == 0 {
			failed = append(failed, relationHash)
			continue
		}
		afterStatus, err := store.RestoreRelationStatusFromSnapshot(relationHash, snapshot)
		if err != nil {
			return nil, err
		}
		if afterStatus == nil {
			failed = append(failed, relationHash)
			continue
		}
		restored = append(restored, relationHash)
		before := map[string]any{}
		if statusMap != nil {
			before = feedbackMapDict(statusMap[relationHash])
		}
		if _, err := store.AppendFeedbackActionLog(storage.FeedbackActionLogAppendOptions{
			TaskID: taskID, QueryToolID: queryToolID, ActionType: "compensate_restore_relation",
			TargetHash: relationHash, BeforePayload: before, AfterPayload: afterStatus, Reason: reason,
		}, nil); err != nil {
			return nil, err
		}
	}
	if len(restored) > 0 || len(failed) > 0 {
		s.rebuildGraph()
		s.persist()
	}
	return map[string]any{"restored_hashes": restored, "failed_hashes": failed}, nil
}

// rebuildGraph 对齐 _rebuild_graph_from_metadata
func (s *FeedbackService) rebuildGraph() {
	if s.deps.GraphAdmin != nil {
		_, _ = s.deps.GraphAdmin.rebuildGraphFromMetadata()
	}
}

// ResolveFeedbackRelationHashes 对齐 _resolve_feedback_relation_hashes
func (s *FeedbackService) ResolveFeedbackRelationHashes(ctx context.Context, payload map[string]any) (any, error) {
	store := s.store()
	hitMap := asMap(payload["hit_map"])
	resolved := make([]string, 0)
	seen := map[string]struct{}{}
	for _, targetHash := range tokens(payload["target_hashes"]) {
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
			relations, err := store.GetParagraphRelations(targetHash)
			if err == nil {
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
	return resolved, nil
}
