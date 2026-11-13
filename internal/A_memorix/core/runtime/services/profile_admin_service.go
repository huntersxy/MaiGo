package services

// 人物画像管理服务，逐段对齐 profile_admin_service.py 的 MemoryProfileAdminService

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// ProfileAdminDeps 画像服务的依赖端口
type ProfileAdminDeps struct {
	Metadata                          func() *storage.Store
	PersonProfile                     func() *utils.PersonProfileService
	Initialize                        func(ctx context.Context) error
	Cfg                               func(key string, fallback any) any
	MarkPersonActive                  func(personID string)
	FeedbackProfileRefreshEnabled     func() bool
	FeedbackProfileForceRefreshOnRead func() bool
	FeedbackReconcileBatchSize        func() int
	ProfileRefreshMaxRetry            func() int
	DeleteAction                      func(ctx context.Context, mode string, selector map[string]any, requestedBy string, reason string) (map[string]any, error)
	InvalidateImportManifest          func(ctx context.Context, result map[string]any)
	ProcessProfileRefreshBatch        func(ctx context.Context, limit int) (map[string]any, error)
	FilterUserVisibleHits             func(items []map[string]any) []map[string]any
	TrimText                          func(value string, limit int) string
	ObserveIdentity                   func(platform string, userID string, aliases []string, groupID string) (map[string]any, error)
	ProfileTTLSeconds                 func() float64
}

// ProfileAdminService 人物画像管理服务
type ProfileAdminService struct {
	*Base
	deps ProfileAdminDeps
}

// NewProfileAdminService 构造人物画像管理服务
func NewProfileAdminService(base *Base, deps ProfileAdminDeps) *ProfileAdminService {
	return &ProfileAdminService{Base: base, deps: deps}
}

func (s *ProfileAdminService) store() *storage.Store {
	if s.deps.Metadata == nil {
		return nil
	}
	return s.deps.Metadata()
}

func (s *ProfileAdminService) profileService() *utils.PersonProfileService {
	if s.deps.PersonProfile == nil {
		return nil
	}
	return s.deps.PersonProfile()
}

func (s *ProfileAdminService) cfg(key string, fallback any) any {
	if s.deps.Cfg != nil {
		return s.deps.Cfg(key, fallback)
	}
	return s.Cfg("person_profile."+key, fallback)
}

func (s *ProfileAdminService) initialize(ctx context.Context) error {
	if s.deps.Initialize != nil {
		return s.deps.Initialize(ctx)
	}
	return nil
}

// MarkPersonActive 记录人物活跃时间
func (s *ProfileAdminService) MarkPersonActive(ctx context.Context, personID string) {
	token := strings.TrimSpace(personID)
	if token == "" {
		return
	}
	if s.deps.MarkPersonActive != nil {
		s.deps.MarkPersonActive(token)
	}
}

// EnqueuePersonProfileRefresh 入队画像刷新
func (s *ProfileAdminService) EnqueuePersonProfileRefresh(ctx context.Context, personID string, reason string) bool {
	store := s.store()
	if store == nil || !pyTruthy(s.cfg("enabled", true)) {
		return false
	}
	token := strings.TrimSpace(reason)
	if token == "" {
		token = "memory_ingest"
	}
	payload, err := store.EnqueuePersonProfileRefresh(storage.PersonProfileRefreshEnqueueInput{PersonID: personID, Reason: token})
	if err != nil {
		s.LoggerWarn("人物画像刷新入队失败: %v", err)
		return false
	}
	return payload != nil
}

// HasPendingPersonProfileRefresh 判断是否存在待处理刷新
func (s *ProfileAdminService) HasPendingPersonProfileRefresh(ctx context.Context, personID string) bool {
	store := s.store()
	if store == nil {
		return false
	}
	request, err := store.GetPersonProfileRefreshRequest(personID)
	if err != nil || request == nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(asString(request["status"])))
	if status == "pending" || status == "running" {
		return true
	}
	if status != "failed" {
		return false
	}
	return intOr(request["retry_count"], 0, math.MinInt) < s.refreshMaxRetry()
}

func (s *ProfileAdminService) refreshMaxRetry() int {
	if s.deps.ProfileRefreshMaxRetry != nil {
		return s.deps.ProfileRefreshMaxRetry()
	}
	return 3
}

func emptyPersonProfileResponse(personID string, personName string) map[string]any {
	return map[string]any{
		"summary": "", "traits": []any{}, "evidence": []any{},
		"person_id": strings.TrimSpace(personID), "person_name": strings.TrimSpace(personName),
		"profile_source": "", "has_manual_override": false,
	}
}

func (s *ProfileAdminService) queryWithFeedbackRefresh(ctx context.Context, personID string, personKeyword string, limit int, forceRefresh bool, sourceNote string) (map[string]any, error) {
	store := s.store()
	service := s.profileService()
	if store == nil || service == nil {
		return nil, fmt.Errorf("人物画像服务未初始化")
	}
	pid := strings.TrimSpace(personID)
	if pid == "" && strings.TrimSpace(personKeyword) != "" {
		resolved, err := service.ResolvePersonID(ctx, strings.TrimSpace(personKeyword))
		if err == nil {
			pid = resolved
		}
	}
	var dirtyRequest map[string]any
	if pid != "" {
		request, err := store.GetPersonProfileRefreshRequest(pid)
		if err != nil {
			return nil, err
		}
		dirtyRequest = request
	}
	shouldForce := forceRefresh
	if pid != "" && s.feedbackRefreshEnabled() && s.feedbackForceOnRead() && dirtyRequest != nil {
		status := strings.ToLower(strings.TrimSpace(asString(dirtyRequest["status"])))
		if status == "pending" || status == "running" || status == "failed" {
			shouldForce = true
		}
	}
	ttl := 0.0
	if s.deps.ProfileTTLSeconds != nil {
		ttl = s.deps.ProfileTTLSeconds()
	}
	profile, err := service.QueryPersonProfile(ctx, pid, strings.TrimSpace(personKeyword), maxInt(1, limit), ttl, shouldForce, sourceNote)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		profile = map[string]any{"success": false, "error": "invalid profile payload"}
	}
	if dirtyRequest != nil {
		profile["feedback_refresh_request"] = dirtyRequest
	}
	if shouldForce && dirtyRequest != nil && !pyTruthy(profile["success"]) {
		if _, ok := profile["error"]; !ok {
			profile["error"] = "feedback_refresh_failed"
		}
		profile["feedback_refresh_failed"] = true
	}
	return profile, nil
}

func (s *ProfileAdminService) feedbackRefreshEnabled() bool {
	if s.deps.FeedbackProfileRefreshEnabled != nil {
		return s.deps.FeedbackProfileRefreshEnabled()
	}
	return true
}

func (s *ProfileAdminService) feedbackForceOnRead() bool {
	if s.deps.FeedbackProfileForceRefreshOnRead != nil {
		return s.deps.FeedbackProfileForceRefreshOnRead()
	}
	return false
}

func (s *ProfileAdminService) reconcileBatchSize() int {
	if s.deps.FeedbackReconcileBatchSize != nil {
		return s.deps.FeedbackReconcileBatchSize()
	}
	return 20
}

// BuildPersonProfileResponse 对齐 _build_person_profile_response
func (s *ProfileAdminService) BuildPersonProfileResponse(ctx context.Context, payload map[string]any) (map[string]any, error) {
	return s.buildPersonProfileResponse(payload,
		asString(payload["requested_person_id"]), intOr(payload["limit"], 10, 1)), nil
}

func (s *ProfileAdminService) buildPersonProfileResponse(profile map[string]any, requestedPersonID string, limit int) map[string]any {
	store := s.store()
	if store == nil {
		return emptyPersonProfileResponse(requestedPersonID, "")
	}
	if !pyTruthy(profile["success"]) {
		return emptyPersonProfileResponse(asString(profile["person_id"]), asString(profile["person_name"]))
	}
	evidence := make([]map[string]any, 0)
	evidenceLimit := maxInt(1, limit)
	for _, raw := range asList(profile["evidence_ids"]) {
		hashValue := asString(raw)
		if len(evidence) >= evidenceLimit {
			break
		}
		paragraph, err := store.GetParagraph(hashValue)
		if err == nil && paragraph != nil {
			content := truncateRunes(asString(paragraph["content"]), 220)
			evidence = append(evidence, map[string]any{
				"hash": hashValue, "content": content,
				"metadata": asMap(paragraph["metadata"]), "type": "paragraph",
			})
			continue
		}
		relation, err := store.GetRelation(hashValue, false)
		if err == nil && relation != nil {
			content := strings.TrimSpace(strings.Join([]string{
				strings.TrimSpace(asString(relation["subject"])),
				strings.TrimSpace(asString(relation["predicate"])),
				strings.TrimSpace(asString(relation["object"])),
			}, " "))
			evidence = append(evidence, map[string]any{
				"hash": hashValue, "content": content,
				"metadata": map[string]any{"confidence": relation["confidence"], "source_paragraph": relation["source_paragraph"]},
				"type":     "relation",
			})
		}
	}
	evidence = s.filterVisible(evidence)
	text := strings.TrimSpace(asString(profile["profile_text"]))
	traits := make([]string, 0)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "- "))
		if line == "" {
			continue
		}
		traits = append(traits, line)
		if len(traits) >= 8 {
			break
		}
	}
	return map[string]any{
		"summary": text, "traits": traits, "evidence": evidence,
		"person_id":           firstNonEmptyString(asString(profile["person_id"]), requestedPersonID),
		"person_name":         asString(profile["person_name"]),
		"profile_source":      firstNonEmptyString(asString(profile["profile_source"]), "auto_snapshot"),
		"has_manual_override": pyTruthy(profile["has_manual_override"]),
	}
}

func (s *ProfileAdminService) filterVisible(items []map[string]any) []map[string]any {
	if s.deps.FilterUserVisibleHits == nil {
		return items
	}
	return s.deps.FilterUserVisibleHits(items)
}

func (s *ProfileAdminService) trim(value string, limit int) string {
	if s.deps.TrimText != nil {
		return s.deps.TrimText(value, limit)
	}
	return truncateRunes(value, limit)
}

// GetPersonProfile 对齐 get_person_profile
func (s *ProfileAdminService) GetPersonProfile(ctx context.Context, personID string, chatID string, limit int) (map[string]any, error) {
	if err := s.initialize(ctx); err != nil {
		return nil, err
	}
	s.MarkPersonActive(ctx, personID)
	profile, err := s.queryWithFeedbackRefresh(ctx, personID, "", maxInt(4, limit), false, "sdk_memory_kernel.get_person_profile")
	if err != nil {
		return nil, err
	}
	return s.buildPersonProfileResponse(profile, personID, limit), nil
}

// RefreshPersonProfile 对齐 refresh_person_profile
func (s *ProfileAdminService) RefreshPersonProfile(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if err := s.initialize(ctx); err != nil {
		return nil, err
	}
	service := s.profileService()
	if service == nil {
		return nil, fmt.Errorf("人物画像服务未初始化")
	}
	personID := asString(payload["person_id"])
	limit := intOr(payload["limit"], 10, 1)
	markActive := true
	if raw, ok := payload["mark_active"]; ok && raw != nil {
		markActive = pyTruthy(raw)
	}
	if markActive {
		s.MarkPersonActive(ctx, personID)
	}
	ttl := 0.0
	if s.deps.ProfileTTLSeconds != nil {
		ttl = s.deps.ProfileTTLSeconds()
	}
	profile, err := service.QueryPersonProfile(ctx, personID, "", maxInt(4, limit), ttl, true, "sdk_memory_kernel.refresh_person_profile")
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return map[string]any{}, nil
	}
	return profile, nil
}

// QueryPersonProfileWithFeedbackRefresh 对齐 _query_person_profile_with_feedback_refresh 的公开入口
func (s *ProfileAdminService) QueryPersonProfileWithFeedbackRefresh(ctx context.Context, personID string, chatID string, limit int) (map[string]any, error) {
	if err := s.initialize(ctx); err != nil {
		return nil, err
	}
	return s.queryWithFeedbackRefresh(ctx, personID, "", maxInt(1, limit), false, "sdk_memory_kernel.query_person_profile")
}

// ProcessPersonProfileRefreshQueueBatch 对齐 _process_feedback_profile_refresh_batch 的公开入口
func (s *ProfileAdminService) ProcessPersonProfileRefreshQueueBatch(ctx context.Context, limit int) (map[string]any, error) {
	if s.deps.ProcessProfileRefreshBatch == nil {
		return map[string]any{"processed": 0, "refreshed": 0, "failed": 0}, nil
	}
	return s.deps.ProcessProfileRefreshBatch(ctx, maxInt(1, limit))
}

// MemoryProfileAdmin 对齐 memory_profile_admin
func (s *ProfileAdminService) MemoryProfileAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if err := s.initialize(ctx); err != nil {
		return nil, err
	}
	store := s.store()
	service := s.profileService()
	if store == nil || service == nil {
		return nil, fmt.Errorf("人物画像服务未初始化")
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "resolve_identity":
		personID, err := service.ResolvePersonID(ctx, strings.TrimSpace(asString(payload["identifier"])))
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "person_id": personID}, nil
	case "observe_identity":
		if s.deps.ObserveIdentity == nil {
			return map[string]any{"success": false, "error": "身份观察器未注入"}, nil
		}
		identity, err := s.deps.ObserveIdentity(asString(payload["platform"]), asString(payload["user_id"]), stringTokens(payload["aliases"]), asString(payload["group_id"]))
		if err != nil {
			return nil, err
		}
		result := map[string]any{"success": true}
		for key, value := range identity {
			result[key] = value
		}
		return result, nil
	case "query":
		profile, err := s.queryWithFeedbackRefresh(ctx, asString(payload["person_id"]), firstNonEmptyString(asString(payload["person_keyword"]), asString(payload["keyword"])), maxInt(1, intOr(firstAny(payload["limit"], payload["top_k"]), 12, 1)), pyTruthy(payload["force_refresh"]), "sdk_memory_kernel.memory_profile_admin.query")
		if err != nil {
			return nil, err
		}
		if profile == nil {
			return map[string]any{"success": false, "error": "invalid profile payload"}, nil
		}
		personID := strings.TrimSpace(asString(profile["person_id"]))
		if pyTruthy(profile["success"]) && personID != "" {
			for key, value := range s.uncertainProfileCandidates(personID, asString(payload["context_text"])) {
				profile[key] = value
			}
			if pyTruthy(profile["has_manual_override"]) {
				profile["uncertain_candidates"] = []any{}
			}
		}
		return profile, nil
	case "evidence":
		return s.profileEvidenceAdmin(ctx, asString(payload["person_id"]), firstNonEmptyString(asString(payload["person_keyword"]), asString(payload["keyword"])), maxInt(1, intOr(firstAny(payload["limit"], payload["top_k"]), 12, 1)), pyTruthy(payload["force_refresh"]))
	case "correct_evidence":
		return s.ProfileCorrectEvidenceAdmin(ctx, payload)
	case "status":
		summary, err := store.GetPersonProfileRefreshSummary(maxInt(1, intOr(payload["limit"], 20, 1)))
		if err != nil {
			return nil, err
		}
		result := map[string]any{"success": true}
		for key, value := range summary {
			result[key] = value
		}
		return result, nil
	case "process_pending":
		result, err := s.ProcessPersonProfileRefreshQueueBatch(ctx, maxInt(1, intOr(payload["limit"], s.reconcileBatchSize(), 1)))
		if err != nil {
			return nil, err
		}
		out := map[string]any{"success": true}
		for key, value := range result {
			out[key] = value
		}
		return out, nil
	case "list":
		limit := maxInt(1, intOr(payload["limit"], 50, 1))
		rows, err := store.Query(`
			SELECT s.person_id, s.profile_version, s.profile_text, s.updated_at, s.expires_at, s.source_note
			FROM person_profile_snapshots s
			JOIN (
				SELECT person_id, MAX(profile_version) AS max_version
				FROM person_profile_snapshots
				GROUP BY person_id
			) latest
			  ON latest.person_id = s.person_id
			 AND latest.max_version = s.profile_version
			ORDER BY s.updated_at DESC
			LIMIT ?`, limit)
		if err != nil {
			return nil, err
		}
		items := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			personID := strings.TrimSpace(asString(row["person_id"]))
			override, err := store.GetPersonProfileOverride(personID)
			if err != nil {
				return nil, err
			}
			items = append(items, map[string]any{
				"person_id": personID, "profile_version": intOr(row["profile_version"], 0, math.MinInt),
				"profile_text": asString(row["profile_text"]), "updated_at": row["updated_at"], "expires_at": row["expires_at"],
				"source_note": asString(row["source_note"]), "has_manual_override": override != nil, "manual_override": override,
			})
		}
		return map[string]any{"success": true, "items": items, "count": len(items)}, nil
	case "get_aliases":
		personID := strings.TrimSpace(asString(payload["person_id"]))
		if personID == "" {
			return map[string]any{"success": false, "error": "person_id 不能为空"}, nil
		}
		result := map[string]any{"success": true}
		for key, value := range service.GetPersonAliasDetails(ctx, personID) {
			result[key] = value
		}
		return result, nil
	case "set_aliases":
		personID := strings.TrimSpace(asString(payload["person_id"]))
		rawAliases, ok := payload["aliases"].([]any)
		aliases := make([]string, 0)
		if ok {
			for _, item := range rawAliases {
				aliases = append(aliases, asString(item))
			}
		} else if list, ok := payload["aliases"].([]string); ok {
			aliases = list
		} else {
			return map[string]any{"success": false, "error": "aliases 必须是字符串列表"}, nil
		}
		override, err := store.SetPersonProfileAliasOverride(storage.PersonProfileAliasOverrideInput{
			PersonID: personID, Aliases: aliases, UpdatedBy: asString(payload["updated_by"]),
			Source: firstNonEmptyString(asString(payload["source"]), "memory_profile_admin"),
		})
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}, nil
		}
		refreshQueued := false
		if pyTruthy(s.cfg("enabled", true)) {
			refreshQueued = s.EnqueuePersonProfileRefresh(ctx, personID, "person_aliases_updated")
		}
		result := map[string]any{"success": true, "override": override, "refresh_queued": refreshQueued}
		for key, value := range service.GetPersonAliasDetails(ctx, personID) {
			result[key] = value
		}
		return result, nil
	case "delete_aliases":
		personID := strings.TrimSpace(asString(payload["person_id"]))
		if personID == "" {
			return map[string]any{"success": false, "error": "person_id 不能为空"}, nil
		}
		deleted, err := store.DeletePersonProfileAliasOverride(personID, nil)
		if err != nil {
			return nil, err
		}
		refreshQueued := false
		if pyTruthy(s.cfg("enabled", true)) {
			refreshQueued = s.EnqueuePersonProfileRefresh(ctx, personID, "person_aliases_override_deleted")
		}
		result := map[string]any{"success": true, "deleted": deleted, "refresh_queued": refreshQueued}
		for key, value := range service.GetPersonAliasDetails(ctx, personID) {
			result[key] = value
		}
		return result, nil
	case "set_override":
		personID := strings.TrimSpace(asString(payload["person_id"]))
		override, err := store.SetPersonProfileOverride(personID,
			firstNonEmptyString(asString(payload["override_text"]), asString(payload["text"])),
			asString(payload["updated_by"]), firstNonEmptyString(asString(payload["source"]), "memory_profile_admin"), nil)
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}, nil
		}
		return map[string]any{"success": true, "override": override}, nil
	case "delete_override":
		personID := strings.TrimSpace(asString(payload["person_id"]))
		deleted, err := store.DeletePersonProfileOverride(personID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": deleted, "deleted": deleted, "person_id": personID}, nil
	default:
		return map[string]any{"success": false, "error": fmt.Sprintf("不支持的 profile action: %s", strings.ToLower(strings.TrimSpace(action)))}, nil
	}
}

// uncertainProfileCandidates 对齐 _uncertain_profile_candidates
func (s *ProfileAdminService) uncertainProfileCandidates(personID string, contextText string) map[string]any {
	store := s.store()
	count := 0
	claims := []map[string]any{}
	if store != nil {
		if value, err := store.CountUncertainPersonFactClaims(personID); err == nil {
			count = value
		}
		if strings.TrimSpace(contextText) != "" {
			if rows, err := store.ListUncertainPersonFactClaims(personID, intPtr(512)); err == nil {
				claims = rows
			}
		}
	}
	queryGrams := profileRelevanceGrams(contextText)
	type rankedClaim struct {
		overlap   int
		confirmed float64
		claim     map[string]any
	}
	ranked := make([]rankedClaim, 0)
	if len(queryGrams) > 0 {
		for _, claim := range claims {
			overlap := intersectCount(queryGrams, profileRelevanceGrams(asString(claim["value_text"])))
			if overlap > 0 {
				confirmed, _ := asFloat(claim["last_confirmed_at"])
				ranked = append(ranked, rankedClaim{overlap: overlap, confirmed: confirmed, claim: claim})
			}
		}
	}
	sort.SliceStable(ranked, func(i int, j int) bool {
		if ranked[i].overlap != ranked[j].overlap {
			return ranked[i].overlap > ranked[j].overlap
		}
		if ranked[i].confirmed != ranked[j].confirmed {
			return ranked[i].confirmed > ranked[j].confirmed
		}
		return asString(ranked[i].claim["claim_id"]) < asString(ranked[j].claim["claim_id"])
	})
	candidates := make([]map[string]any, 0, 2)
	for index, item := range ranked {
		if index >= 2 {
			break
		}
		candidates = append(candidates, map[string]any{"claim_id": asString(item.claim["claim_id"]), "text": asString(item.claim["value_text"])})
	}
	return map[string]any{"uncertain_fact_count": count, "uncertain_candidates": candidates}
}

func profileRelevanceGrams(value string) map[string]struct{} {
	normalized := cases.Fold().String(norm.NFKC.String(value))
	var builder strings.Builder
	for _, char := range normalized {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
		}
	}
	token := []rune(builder.String())
	ignored := map[string]struct{}{"用户": {}, "这个": {}, "那个": {}, "自己": {}, "喜欢": {}}
	grams := make(map[string]struct{})
	for index := 0; index+1 < len(token); index++ {
		gram := string(token[index : index+2])
		if _, ok := ignored[gram]; ok {
			continue
		}
		grams[gram] = struct{}{}
	}
	return grams
}

func intersectCount(left map[string]struct{}, right map[string]struct{}) int {
	count := 0
	for key := range left {
		if _, ok := right[key]; ok {
			count++
		}
	}
	return count
}

// BuildProfileRelationEvidenceItem 对齐 _build_profile_relation_evidence_item
func (s *ProfileAdminService) BuildProfileRelationEvidenceItem(ctx context.Context, relation map[string]any, index int) map[string]any {
	hashValue := strings.TrimSpace(asString(relation["hash"]))
	metadata := utils.CoerceMetadataDict(relation["metadata"])
	content := utils.ProfileRelationContent(relation)
	source := strings.TrimSpace(firstNonEmptyString(asString(relation["source_paragraph"]), asString(metadata["source"])))
	reason := ""
	if hashValue == "" {
		reason = "缺少关系 hash"
	}
	return map[string]any{
		"evidence_key":  fmt.Sprintf("relation:%s", firstNonEmptyString(hashValue, fmt.Sprintf("%d", index))),
		"evidence_type": "relation", "hash": hashValue, "content": content, "source": source,
		"source_type": "relation", "metadata": metadata, "score": nil,
		"confidence": relation["confidence"], "correction_mode": "delete_relation",
		"deletable": hashValue != "", "not_deletable_reason": reason, "raw": relation,
	}
}

// BuildProfileParagraphEvidenceItem 对齐 _build_profile_paragraph_evidence_item
func (s *ProfileAdminService) BuildProfileParagraphEvidenceItem(ctx context.Context, item map[string]any, index int, fallbackHash string) map[string]any {
	hashValue := strings.TrimSpace(firstNonEmptyString(asString(item["hash"]), fallbackHash))
	metadata := utils.CoerceMetadataDict(item["metadata"])
	source := strings.TrimSpace(firstNonEmptyString(asString(item["source"]), asString(metadata["source"])))
	content := strings.TrimSpace(asString(item["content"]))
	sourceType := utils.ProfileEvidenceTypeFromSource(source, metadata)
	isDeleted := false
	if hashValue != "" {
		if store := s.store(); store != nil {
			if paragraph, err := store.GetParagraph(hashValue); err == nil && paragraph != nil {
				paragraphMeta := utils.CoerceMetadataDict(paragraph["metadata"])
				merged := map[string]any{}
				for key, value := range paragraphMeta {
					merged[key] = value
				}
				for key, value := range metadata {
					merged[key] = value
				}
				metadata = merged
				source = firstNonEmptyString(source, asString(paragraph["source"]))
				content = firstNonEmptyString(content, asString(paragraph["content"]))
				sourceType = utils.ProfileEvidenceTypeFromSource(source, metadata)
				isDeleted = pyTruthy(paragraph["is_deleted"])
			}
		}
	}
	deletable := hashValue != "" && !isDeleted
	reason := ""
	if !deletable {
		if isDeleted {
			reason = "证据已删除"
		} else {
			reason = "缺少段落 hash"
		}
	}
	return map[string]any{
		"evidence_key":  fmt.Sprintf("%s:%s", sourceType, firstNonEmptyString(hashValue, fmt.Sprintf("%d", index))),
		"evidence_type": sourceType, "hash": hashValue, "content": truncateRunes(content, 260),
		"source": source, "source_type": sourceType, "metadata": metadata, "score": item["score"],
		"confidence": nil, "correction_mode": "delete_paragraph",
		"deletable": deletable, "not_deletable_reason": reason, "raw": item,
	}
}

// BuildProfileEvidenceItems 对齐 _build_profile_evidence_items
func (s *ProfileAdminService) BuildProfileEvidenceItems(ctx context.Context, profile map[string]any) []map[string]any {
	store := s.store()
	evidence := make([]map[string]any, 0)
	seen := make(map[string]struct{})
	appendItem := func(item map[string]any) {
		evidenceType := strings.TrimSpace(asString(item["evidence_type"]))
		hashValue := strings.TrimSpace(asString(item["hash"]))
		key := evidenceType + "|" + firstNonEmptyString(hashValue, asString(item["evidence_key"]))
		if evidenceType == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		evidence = append(evidence, item)
	}
	if store == nil {
		return evidence
	}
	for index, raw := range asList(profile["relation_edges"]) {
		if relation := asMap(raw); relation != nil {
			appendItem(s.BuildProfileRelationEvidenceItem(ctx, relation, index+1))
		}
	}
	for index, raw := range asList(profile["vector_evidence"]) {
		if item := asMap(raw); item != nil {
			appendItem(s.BuildProfileParagraphEvidenceItem(ctx, item, index+1, ""))
		}
	}
	for index, raw := range stringTokens(profile["evidence_ids"]) {
		hashValue := raw
		exists := false
		for _, item := range evidence {
			if strings.TrimSpace(asString(item["hash"])) == hashValue {
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		paragraph, err := store.GetParagraph(hashValue)
		if err == nil && paragraph != nil {
			appendItem(s.BuildProfileParagraphEvidenceItem(ctx, map[string]any{
				"hash": hashValue, "content": asString(paragraph["content"]),
				"source": asString(paragraph["source"]), "metadata": utils.CoerceMetadataDict(paragraph["metadata"]),
			}, index+1, ""))
			continue
		}
		relation, err := store.GetRelation(hashValue, false)
		if err == nil && relation != nil {
			appendItem(s.BuildProfileRelationEvidenceItem(ctx, relation, index+1))
		}
	}
	return evidence
}

// ProfileEvidenceResponse 对齐 _profile_evidence_response
func (s *ProfileAdminService) ProfileEvidenceResponse(ctx context.Context, profile map[string]any, requestedPersonID string, limit int) map[string]any {
	store := s.store()
	if !pyTruthy(profile["success"]) {
		errText := orDefaultString(asString(profile["error"]), "人物画像查询失败")
		return map[string]any{
			"success": false, "error": errText,
			"person_id": firstNonEmptyString(asString(profile["person_id"]), requestedPersonID),
			"evidence":  []any{},
		}
	}
	evidence := s.BuildProfileEvidenceItems(ctx, profile)
	limit = maxInt(1, limit)
	if len(evidence) > limit {
		evidence = evidence[:limit]
	}
	uncertainCount := 0
	if store != nil {
		if value, err := store.CountUncertainPersonFactClaims(firstNonEmptyString(asString(profile["person_id"]), requestedPersonID)); err == nil {
			uncertainCount = value
		}
	}
	return map[string]any{
		"success":           true,
		"person_id":         firstNonEmptyString(asString(profile["person_id"]), requestedPersonID),
		"person_name":       asString(profile["person_name"]),
		"profile_text":      asString(profile["profile_text"]),
		"auto_profile_text": firstNonEmptyString(asString(profile["auto_profile_text"]), asString(profile["profile_text"])),
		"profile_version":   profile["profile_version"], "updated_at": profile["updated_at"], "expires_at": profile["expires_at"],
		"profile_source":       firstNonEmptyString(asString(profile["profile_source"]), "auto_snapshot"),
		"has_manual_override":  pyTruthy(profile["has_manual_override"]),
		"manual_override_text": asString(profile["manual_override_text"]),
		"evidence":             evidence, "evidence_count": len(evidence),
		"uncertain_fact_count": uncertainCount, "raw_profile": profile,
	}
}

// ProfileEvidenceAdmin 对齐 _profile_evidence_admin
func (s *ProfileAdminService) ProfileEvidenceAdmin(ctx context.Context, personID string, personKeyword string, limit int, forceRefresh bool) (map[string]any, error) {
	return s.profileEvidenceAdmin(ctx, personID, personKeyword, limit, forceRefresh)
}

func (s *ProfileAdminService) profileEvidenceAdmin(ctx context.Context, personID string, personKeyword string, limit int, forceRefresh bool) (map[string]any, error) {
	profile, err := s.queryWithFeedbackRefresh(ctx, personID, personKeyword, maxInt(1, limit), forceRefresh, "sdk_memory_kernel.memory_profile_admin.evidence")
	if err != nil {
		return nil, err
	}
	requestedPersonID := strings.TrimSpace(firstNonEmptyString(asString(profile["person_id"]), personID))
	return s.ProfileEvidenceResponse(ctx, profile, requestedPersonID, limit), nil
}

// ProfileCorrectEvidenceAdmin 对齐 _profile_correct_evidence_admin
func (s *ProfileAdminService) ProfileCorrectEvidenceAdmin(ctx context.Context, payload map[string]any) (map[string]any, error) {
	personID := asString(payload["person_id"])
	personKeyword := firstNonEmptyString(asString(payload["person_keyword"]), asString(payload["keyword"]))
	evidenceType := strings.ToLower(strings.TrimSpace(asString(payload["evidence_type"])))
	hashValue := strings.TrimSpace(firstNonEmptyString(asString(payload["hash"]), asString(payload["hash_value"])))
	requestedBy := firstNonEmptyString(strings.TrimSpace(asString(payload["requested_by"])), "webui")
	reason := firstNonEmptyString(strings.TrimSpace(asString(payload["reason"])), "profile_evidence_correction")
	refresh := true
	if raw, ok := payload["refresh"]; ok && raw != nil {
		refresh = pyTruthy(raw)
	}
	limit := maxInt(1, intOr(firstAny(payload["limit"], payload["top_k"]), 12, 1))
	switch evidenceType {
	case "relation", "paragraph", "person_fact", "chat_summary":
	default:
		return map[string]any{"success": false, "error": "不支持的画像证据类型"}, nil
	}
	if hashValue == "" {
		return map[string]any{"success": false, "error": "画像证据 hash 不能为空"}, nil
	}
	evidencePayload, err := s.profileEvidenceAdmin(ctx, personID, personKeyword, maxInt(50, limit), false)
	if err != nil {
		return nil, err
	}
	if !pyTruthy(evidencePayload["success"]) {
		return evidencePayload, nil
	}
	var matched map[string]any
	for _, raw := range asList(evidencePayload["evidence"]) {
		item := asMap(raw)
		if item == nil || strings.TrimSpace(asString(item["hash"])) != hashValue {
			continue
		}
		itemType := strings.ToLower(strings.TrimSpace(asString(item["evidence_type"])))
		if evidenceType == itemType || (evidenceType == "paragraph" && (itemType == "person_fact" || itemType == "chat_summary")) {
			matched = item
			break
		}
	}
	if matched == nil {
		return map[string]any{"success": false, "error": "当前画像证据中未找到目标 hash"}, nil
	}
	if !pyTruthy(matched["deletable"]) {
		return map[string]any{
			"success": false, "error": orDefaultString(asString(matched["not_deletable_reason"]), "该画像证据不可纠错"),
			"evidence": matched,
		}, nil
	}
	deleteMode := "paragraph"
	if evidenceType == "relation" {
		deleteMode = "relation"
	}
	deleteResult := map[string]any{}
	if s.deps.DeleteAction != nil {
		value, err := s.deps.DeleteAction(ctx, deleteMode, map[string]any{"hashes": []string{hashValue}}, requestedBy, reason)
		if err != nil {
			return nil, err
		}
		deleteResult = value
	}
	if pyTruthy(deleteResult["success"]) && s.deps.InvalidateImportManifest != nil {
		s.deps.InvalidateImportManifest(ctx, deleteResult)
	}
	refreshedProfile := map[string]any{}
	refreshedEvidence := map[string]any{}
	if refresh && pyTruthy(deleteResult["success"]) {
		service := s.profileService()
		if service == nil {
			return nil, fmt.Errorf("人物画像服务未初始化")
		}
		ttl := 0.0
		if s.deps.ProfileTTLSeconds != nil {
			ttl = s.deps.ProfileTTLSeconds()
		}
		value, err := service.QueryPersonProfile(ctx, firstNonEmptyString(asString(evidencePayload["person_id"]), personID), "", maxInt(4, limit), ttl, true, "sdk_memory_kernel.memory_profile_admin.correct_evidence")
		if err != nil {
			return nil, err
		}
		if value == nil {
			value = map[string]any{}
		}
		refreshedProfile = value
		refreshedEvidence = s.ProfileEvidenceResponse(ctx, value, firstNonEmptyString(asString(evidencePayload["person_id"]), personID), limit)
	}
	return map[string]any{
		"success":   pyTruthy(deleteResult["success"]),
		"person_id": firstNonEmptyString(asString(evidencePayload["person_id"]), personID),
		"evidence":  matched, "delete_result": deleteResult,
		"operation_id":      asString(deleteResult["operation_id"]),
		"refreshed_profile": refreshedProfile, "refreshed_evidence": refreshedEvidence,
		"error": asString(deleteResult["error"]),
	}, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func orDefaultString(value string, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func firstAny(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func intPtr(value int) *int {
	return &value
}

var _ = time.Now
