package services

// 检索命中处理服务，逐段对齐 search_hit_processing_service.py 的 MemorySearchHitProcessingService

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// SearchHitDeps 检索命中处理依赖端口
type SearchHitDeps struct {
	Metadata                   func() *storage.Store
	Cfg                        func(key string, fallback any) any
	ChatFilterConfigAllows     func(filterConfig map[string]any, streamID string, groupID string, userID string, defaultWhenEmpty bool) bool
	EpisodeQueryBlockEnabled   func() bool
	ParagraphHardFilterEnabled func() bool
	SessionLookup              func(streamID string) (groupID string, userID string)
	ProjectFactParagraph       func(paragraph map[string]any) (map[string]any, error)
	FactRelationHasSupport     func(relationHash string, paragraph map[string]any) (bool, error)
	Now                        func() float64
	// EffectiveFilterCache 对齐内核 _current_effective_filter_cache（共享可变 map）
	EffectiveFilterCache func() map[string]any
}

// SearchHitService 检索命中处理服务
type SearchHitService struct {
	*Base
	deps SearchHitDeps
}

// NewSearchHitService 构造检索命中处理服务
func NewSearchHitService(base *Base, deps SearchHitDeps) *SearchHitService {
	return &SearchHitService{Base: base, deps: deps}
}

func (s *SearchHitService) store() *storage.Store {
	if s.deps.Metadata == nil {
		return nil
	}
	return s.deps.Metadata()
}

func (s *SearchHitService) now() float64 {
	if s.deps.Now != nil {
		return s.deps.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func searchHitList(value any) []map[string]any {
	items := asList(value)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if row := asMap(item); row != nil {
			out = append(out, row)
		}
	}
	return out
}

func searchHitStringValue(value any) any {
	text := strings.TrimSpace(asString(value))
	if text == "" {
		return nil
	}
	return text
}

func searchHitStrings(value any) []string {
	return stringTokens(value)
}

// RelationStatusIsInactive 对齐 _relation_status_is_inactive
func (s *SearchHitService) RelationStatusIsInactive(ctx context.Context, payload map[string]any) (any, error) {
	status := asMap(payload["status"])
	if status == nil {
		return true, nil
	}
	return pyTruthy(status["is_inactive"]), nil
}

// LoadParagraphStaleMarks 对齐 _load_paragraph_stale_marks
func (s *SearchHitService) LoadParagraphStaleMarks(ctx context.Context, payload map[string]any) (any, error) {
	store := s.store()
	hashes := searchHitStrings(payload["paragraph_hashes"])
	result := map[string]any{"marks_by_paragraph": map[string][]map[string]any{}, "relation_status_map": map[string]map[string]any{}}
	if store == nil || len(hashes) == 0 {
		return result, nil
	}
	marks, err := store.GetParagraphStaleRelationMarksBatch(hashes, nil)
	if err != nil {
		return nil, err
	}
	relationHashes := make([]string, 0)
	for _, values := range marks {
		for _, mark := range values {
			if hashValue := strings.TrimSpace(asString(mark["relation_hash"])); hashValue != "" {
				relationHashes = append(relationHashes, hashValue)
			}
		}
	}
	statusMap := map[string]map[string]any{}
	if len(relationHashes) > 0 {
		statusMap, err = store.GetRelationStatusBatch(relationHashes)
		if err != nil {
			return nil, err
		}
	}
	result["marks_by_paragraph"] = marks
	result["relation_status_map"] = statusMap
	return result, nil
}

// ParagraphHiddenByStaleMarks 对齐 _paragraph_hidden_by_stale_marks
func (s *SearchHitService) ParagraphHiddenByStaleMarks(ctx context.Context, payload map[string]any) (any, error) {
	store := s.store()
	hashValue := strings.TrimSpace(asString(payload["paragraph_hash"]))
	if hashValue == "" || store == nil || (s.deps.ParagraphHardFilterEnabled != nil && !s.deps.ParagraphHardFilterEnabled()) {
		return false, nil
	}
	marksMap := map[string][]map[string]any{}
	for key, raw := range asMap(payload["marks_by_paragraph"]) {
		rows := make([]map[string]any, 0)
		for _, item := range asList(raw) {
			if row := asMap(item); row != nil {
				rows = append(rows, row)
			}
		}
		marksMap[key] = rows
	}
	statusMap := map[string]map[string]any{}
	for key, raw := range asMap(payload["relation_status_map"]) {
		statusMap[key] = asMap(raw)
	}
	if len(marksMap) == 0 {
		loaded, err := s.LoadParagraphStaleMarks(ctx, map[string]any{"paragraph_hashes": []string{hashValue}})
		if err != nil {
			return nil, err
		}
		data := asMap(loaded)
		marksMap = map[string][]map[string]any{}
		for key, raw := range asMap(data["marks_by_paragraph"]) {
			rows := make([]map[string]any, 0)
			for _, item := range asList(raw) {
				if row := asMap(item); row != nil {
					rows = append(rows, row)
				}
			}
			marksMap[key] = rows
		}
		statusMap = map[string]map[string]any{}
		for key, raw := range asMap(data["relation_status_map"]) {
			statusMap[key] = asMap(raw)
		}
	} else if len(statusMap) == 0 {
		relationHashes := make([]string, 0)
		for _, mark := range marksMap[hashValue] {
			if relationHash := strings.TrimSpace(asString(mark["relation_hash"])); relationHash != "" {
				relationHashes = append(relationHashes, relationHash)
			}
		}
		if len(relationHashes) > 0 {
			loaded, err := store.GetRelationStatusBatch(relationHashes)
			if err != nil {
				return nil, err
			}
			statusMap = loaded
		}
	}
	for _, mark := range marksMap[hashValue] {
		relationHash := strings.TrimSpace(asString(mark["relation_hash"]))
		if relationHash == "" {
			continue
		}
		status := statusMap[relationHash]
		if status == nil || pyTruthy(status["is_inactive"]) {
			return true, nil
		}
	}
	return false, nil
}

// FilterEpisodeHits 对齐 _filter_episode_hits
func (s *SearchHitService) FilterEpisodeHits(ctx context.Context, payload map[string]any) (any, error) {
	store := s.store()
	hits := searchHitList(payload["hits"])
	if store == nil || (s.deps.EpisodeQueryBlockEnabled != nil && !s.deps.EpisodeQueryBlockEnabled()) {
		return hits, nil
	}
	filtered := make([]map[string]any, 0, len(hits))
	for _, item := range hits {
		if strings.TrimSpace(asString(item["type"])) != "episode" {
			filtered = append(filtered, item)
			continue
		}
		metadata := asMap(item["metadata"])
		if metadata == nil {
			metadata = map[string]any{}
		}
		// 对齐 Python 的 or 短路：metadata.source 非空（含纯空白）时不再回退 item.source
		sourceRaw := ""
		if value, ok := metadata["source"]; ok {
			if text, isText := value.(string); isText && text != "" {
				sourceRaw = text
			}
		}
		if sourceRaw == "" {
			sourceRaw = asString(item["source"])
		}
		source := strings.TrimSpace(sourceRaw)
		if source != "" {
			blocked, err := store.IsEpisodeSourceQueryBlocked(source, nil)
			if err != nil {
				return nil, err
			}
			if blocked {
				continue
			}
		}
		filtered = append(filtered, item)
	}
	return filtered, nil
}

// FilterUserVisibleHits 对齐 _filter_user_visible_hits
func (s *SearchHitService) FilterUserVisibleHits(ctx context.Context, payload map[string]any) (any, error) {
	hits := searchHitList(payload["hits"])
	step, err := s.FilterFactParagraphHits(ctx, map[string]any{"hits": hits})
	if err != nil {
		return nil, err
	}
	episodeValue, err := s.FilterEpisodeHits(ctx, map[string]any{"hits": step})
	if err != nil {
		return nil, err
	}
	active, err := s.FilterActiveRelationHits(ctx, searchHitList(episodeValue))
	if err != nil {
		return nil, err
	}
	effectiveValue, err := s.FilterCurrentEffectiveHits(ctx, map[string]any{"hits": active})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0)
	for _, hit := range searchHitList(effectiveValue) {
		if _, ok := hit["metadata"]; ok {
			next := cloneAnyMap(hit)
			next["metadata"] = utils.VisibleFactMetadata(utils.CoerceMetadataDict(hit["metadata"]))
			out = append(out, next)
			continue
		}
		out = append(out, hit)
	}
	return out, nil
}

// FilterFactParagraphHits 对齐 _filter_fact_paragraph_hits
func (s *SearchHitService) FilterFactParagraphHits(ctx context.Context, payload map[string]any) (any, error) {
	store := s.store()
	hits := searchHitList(payload["hits"])
	if store == nil {
		return hits, nil
	}
	paragraphHashes := make([]string, 0)
	relationHashes := make([]string, 0)
	for _, hit := range hits {
		hashValue := asString(hit["hash"])
		switch asString(hit["type"]) {
		case "paragraph":
			if hashValue != "" {
				paragraphHashes = append(paragraphHashes, hashValue)
			}
		case "relation":
			if hashValue != "" {
				relationHashes = append(relationHashes, hashValue)
			}
		}
	}
	paragraphs := map[string]map[string]any{}
	var err error
	if len(paragraphHashes) > 0 {
		paragraphs, err = store.GetParagraphsByHashes(paragraphHashes)
		if err != nil {
			return nil, err
		}
	}
	relationParagraphs := map[string][]map[string]any{}
	if len(relationHashes) > 0 {
		relationParagraphs, err = store.GetParagraphsByRelationHashes(relationHashes)
		if err != nil {
			return nil, err
		}
	}
	result := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		hashValue := asString(hit["hash"])
		switch asString(hit["type"]) {
		case "paragraph":
			if paragraph, ok := paragraphs[hashValue]; ok {
				if s.deps.ProjectFactParagraph == nil {
					result = append(result, hit)
					continue
				}
				projected, err := s.deps.ProjectFactParagraph(paragraph)
				if err != nil {
					return nil, err
				}
				if projected == nil {
					continue
				}
				next := cloneAnyMap(hit)
				next["content"] = projected["content"]
				next["metadata"] = projected["metadata"]
				result = append(result, next)
				continue
			}
		case "relation":
			if rows := relationParagraphs[hashValue]; len(rows) > 0 {
				if s.deps.FactRelationHasSupport == nil {
					result = append(result, hit)
					continue
				}
				supported := false
				for _, row := range rows {
					ok, err := s.deps.FactRelationHasSupport(hashValue, row)
					if err != nil {
						return nil, err
					}
					if ok {
						supported = true
						break
					}
				}
				if !supported {
					continue
				}
			}
		}
		result = append(result, hit)
	}
	return result, nil
}

// FilterCurrentEffectiveHits 对齐 _filter_current_effective_hits
func (s *SearchHitService) FilterCurrentEffectiveHits(ctx context.Context, payload map[string]any) (any, error) {
	store := s.store()
	hits := searchHitList(payload["hits"])
	if store == nil {
		return s.filterByMemoryChangeMetadata(hits), nil
	}
	neededValue, err := s.CurrentEffectiveFilterStoreCheckNeeded(ctx, map[string]any{"hits": hits})
	if err != nil {
		return nil, err
	}
	if !pyTruthy(neededValue) {
		return s.filterByMemoryChangeMetadata(hits), nil
	}
	paragraphHashes := make([]string, 0)
	relationHashes := make([]string, 0)
	for _, item := range hits {
		hashValue := strings.TrimSpace(asString(item["hash"]))
		switch strings.TrimSpace(asString(item["type"])) {
		case "paragraph":
			if hashValue != "" {
				paragraphHashes = append(paragraphHashes, hashValue)
			}
		case "relation":
			if hashValue != "" {
				relationHashes = append(relationHashes, hashValue)
			}
		}
	}
	paragraphMap := map[string]map[string]any{}
	if len(paragraphHashes) > 0 {
		paragraphMap, err = store.GetParagraphsByHashes(paragraphHashes)
		if err != nil {
			return nil, err
		}
	}
	relationMap := map[string]map[string]any{}
	if len(relationHashes) > 0 {
		relationMap, err = store.GetRelationsByHashes(relationHashes, false)
		if err != nil {
			return nil, err
		}
	}
	now := s.now()
	filtered := make([]map[string]any, 0, len(hits))
	for _, item := range hits {
		metadata := utils.CoerceMetadataDict(item["metadata"])
		hashValue := strings.TrimSpace(asString(item["hash"]))
		var stored map[string]any
		switch strings.TrimSpace(asString(item["type"])) {
		case "paragraph":
			stored = paragraphMap[hashValue]
		case "relation":
			stored = relationMap[hashValue]
		}
		if stored != nil {
			metadata = utils.CoerceMetadataDict(stored["metadata"])
		}
		memoryChange := asMap(metadata["memory_change"])
		if asString(metadata["dedup_phase"]) == "pending" {
			continue
		}
		validTo := utils.OptionalFloat(memoryChange["valid_to"])
		if validTo != nil && *validTo <= now {
			continue
		}
		next := cloneAnyMap(item)
		next["metadata"] = metadata
		filtered = append(filtered, next)
	}
	return filtered, nil
}

// CurrentEffectiveFilterStoreCheckNeeded 对齐 _current_effective_filter_store_check_needed
func (s *SearchHitService) CurrentEffectiveFilterStoreCheckNeeded(ctx context.Context, payload map[string]any) (any, error) {
	hits := searchHitList(payload["hits"])
	for _, item := range hits {
		metadata := utils.CoerceMetadataDict(item["metadata"])
		if asMap(metadata["memory_change"]) != nil || asString(metadata["dedup_phase"]) == "pending" {
			return true, nil
		}
	}
	cache := map[string]any{}
	if s.deps.EffectiveFilterCache != nil {
		if shared := s.deps.EffectiveFilterCache(); shared != nil {
			cache = shared
		}
	}
	now := s.now()
	checkedAt, _ := asFloat(cache["checked_at"])
	if now-checkedAt < 60.0 {
		return pyTruthy(cache["needed"]), nil
	}
	needed := false
	store := s.store()
	if store == nil {
		cache["checked_at"] = now
		cache["needed"] = false
		return false, nil
	}
	plans, err := store.ListFuzzyModifyPlans(1, []string{"executing", "executed", "rolled_back", "rollback_failed"}, "")
	if err != nil {
		s.LoggerWarn("检查当前有效记忆过滤状态失败，将保守启用回表过滤: %v", err)
		needed = true
	} else {
		needed = len(plans) > 0
	}
	cache["checked_at"] = now
	cache["needed"] = needed
	return needed, nil
}

func (s *SearchHitService) filterByMemoryChangeMetadata(hits []map[string]any) []map[string]any {
	now := s.now()
	filtered := make([]map[string]any, 0, len(hits))
	for _, item := range hits {
		metadata := utils.CoerceMetadataDict(item["metadata"])
		memoryChange := asMap(metadata["memory_change"])
		if asString(metadata["dedup_phase"]) == "pending" {
			continue
		}
		validTo := utils.OptionalFloat(memoryChange["valid_to"])
		if validTo != nil && *validTo <= now {
			continue
		}
		next := cloneAnyMap(item)
		next["metadata"] = metadata
		filtered = append(filtered, next)
	}
	return filtered
}

// FilterHitsByMemoryChangeMetadata 对齐 _filter_hits_by_memory_change_metadata
func (s *SearchHitService) FilterHitsByMemoryChangeMetadata(ctx context.Context, payload map[string]any) (any, error) {
	return s.filterByMemoryChangeMetadata(searchHitList(payload["hits"])), nil
}

// ChatSource 对齐 _chat_source
func (s *SearchHitService) ChatSource(ctx context.Context, payload map[string]any) (any, error) {
	chatID := strings.TrimSpace(asString(payload["chat_id"]))
	if chatID == "" {
		return nil, nil
	}
	return "chat_summary:" + chatID, nil
}

// ChatSourceForSearchScope 对齐 _chat_source_for_search_scope
func (s *SearchHitService) ChatSourceForSearchScope(ctx context.Context, payload map[string]any) (any, error) {
	allowed := s.resolveAllowedChatIDs(asString(payload["chat_id"]), searchHitStrings(payload["shared_chat_ids"]))
	if len(allowed) > 1 {
		return nil, nil
	}
	return s.ChatSource(ctx, map[string]any{"chat_id": payload["chat_id"]})
}

// ScopedSearchLimit 对齐 _scoped_search_limit
func (s *SearchHitService) ScopedSearchLimit(ctx context.Context, payload map[string]any) (any, error) {
	limit := intOr(payload["limit"], 5, math.MinInt)
	if limit == 0 {
		// 对齐 Python int(limit or 5)
		limit = 5
	}
	return max(1, limit), nil
}

// ResolveAllowedChatIDs 对齐 _resolve_allowed_chat_ids
func (s *SearchHitService) ResolveAllowedChatIDs(ctx context.Context, payload map[string]any) (any, error) {
	return s.resolveAllowedChatIDs(asString(payload["chat_id"]), searchHitStrings(payload["shared_chat_ids"])), nil
}

func (s *SearchHitService) resolveAllowedChatIDs(chatID string, shared []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, item := range shared {
		token := strings.TrimSpace(item)
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	if clean := strings.TrimSpace(chatID); clean != "" {
		if _, ok := seen[clean]; !ok {
			out = append(out, clean)
		}
	}
	sort.Strings(out)
	return out
}

func (s *SearchHitService) allowedSet(chatID string, shared []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, item := range s.resolveAllowedChatIDs(chatID, shared) {
		out[item] = struct{}{}
	}
	return out
}

// RankScoreFromItem 对齐 _rank_score_from_item
func (s *SearchHitService) RankScoreFromItem(ctx context.Context, payload map[string]any) (any, error) {
	item := asMap(payload["item"])
	if item == nil {
		item = asMap(payload)
	}
	raw, ok := item["score"]
	if !ok {
		raw, ok = item["final_score"]
	}
	if !ok {
		raw, ok = item["relevance"]
	}
	if !ok {
		raw = 0.0
	}
	value, _ := asFloat(raw)
	return value, nil
}

// DedupeRankedItems 对齐 _dedupe_ranked_items
func (s *SearchHitService) DedupeRankedItems(ctx context.Context, payload map[string]any) (any, error) {
	limit := intOr(payload["limit"], 5, math.MinInt)
	if limit == 0 {
		limit = 5
	}
	limit = max(1, limit)
	rawItems := asList(payload["items"])
	type rankedItem struct {
		key   string
		score float64
		item  map[string]any
	}
	ranked := map[string]rankedItem{}
	order := make([]string, 0, len(rawItems))
	for index, raw := range rawItems {
		item := asMap(raw)
		if item == nil {
			continue
		}
		hashValue := strings.TrimSpace(asString(item["hash"]))
		itemType := strings.TrimSpace(asString(item["type"]))
		content := strings.TrimSpace(asString(item["content"]))
		key := hashValue
		if key == "" {
			key = itemType + ":" + content
		}
		if key == "" {
			key = fmt.Sprintf("item:%d", index)
		}
		scoreValue, _ := s.RankScoreFromItem(ctx, map[string]any{"item": item})
		score := 0.0
		if value, ok := scoreValue.(float64); ok {
			score = value
		}
		current, ok := ranked[key]
		if !ok {
			// 保持首次出现顺序，平局时稳定排序与 Python dict 插入序一致
			order = append(order, key)
		}
		if !ok || score > current.score {
			ranked[key] = rankedItem{key: key, score: score, item: item}
		}
	}
	items := make([]rankedItem, 0, len(ranked))
	for _, key := range order {
		items = append(items, ranked[key])
	}
	sort.SliceStable(items, func(i int, j int) bool { return items[i].score > items[j].score })
	out := make([]map[string]any, 0, limit)
	for _, item := range items {
		if len(out) >= limit {
			break
		}
		out = append(out, item.item)
	}
	return out, nil
}

func (s *SearchHitService) paragraphScopeIdentity(paragraph map[string]any) (string, map[string]struct{}) {
	metadata := utils.CoerceMetadataDict(paragraph["metadata"])
	scopeType := strings.ToLower(strings.TrimSpace(asString(metadata["scope_type"])))
	chatIDs := map[string]struct{}{}
	for _, key := range []string{"chat_id", "session_id", "stream_id", "chat_ids", "session_ids", "stream_ids"} {
		if value, ok := metadata[key]; ok {
			extendChatScopeIDs(chatIDs, value)
		}
	}
	source := strings.TrimSpace(firstNonEmptyString(asString(paragraph["source"]), asString(metadata["source"])))
	if scopeType != "" {
		switch scopeType {
		case "global":
			return "global", map[string]struct{}{}
		case "chat":
			if len(chatIDs) == 0 && strings.HasPrefix(source, "chat_summary:") {
				rest := strings.TrimSpace(strings.TrimPrefix(source, "chat_summary:"))
				if rest != "" {
					chatIDs[rest] = struct{}{}
				}
			}
			if len(chatIDs) == 0 {
				return "unknown", map[string]struct{}{}
			}
			return "chat", chatIDs
		default:
			return "unknown", map[string]struct{}{}
		}
	}
	if len(chatIDs) > 0 {
		return "chat", chatIDs
	}
	if strings.HasPrefix(source, "web_import:") {
		return "global", map[string]struct{}{}
	}
	if strings.HasPrefix(source, "chat_summary:") {
		rest := strings.TrimSpace(strings.TrimPrefix(source, "chat_summary:"))
		if rest != "" {
			return "chat", map[string]struct{}{rest: {}}
		}
	}
	return "unknown", map[string]struct{}{}
}

// ResolveRetrievalScopeForStore 对齐 _resolve_retrieval_scope，按当前聊天可见范围计算候选集合
// SQL 失败时返回 error（fail-closed），不再退回 nil 放行全部命中。
func ResolveRetrievalScopeForStore(store *storage.Store, chatID string, shared []string) (*RetrievalScope, error) {
	allowed := (&SearchHitService{}).resolveAllowedChatIDs(chatID, shared)
	if len(allowed) == 0 {
		return nil, nil
	}
	scope := &RetrievalScope{
		Key:          "chat:" + strings.Join(allowed, ","),
		ParagraphIDs: map[string]struct{}{},
		RelationIDs:  map[string]struct{}{},
		EntityIDs:    map[string]struct{}{},
		EpisodeIDs:   map[string]struct{}{},
	}
	if store == nil {
		return scope, nil
	}
	allowedJSON, err := json.Marshal(allowed)
	if err != nil {
		return nil, err
	}
	rows, err := store.Query(`
		WITH allowed_chat_ids(value) AS (
			SELECT CAST(value AS TEXT) FROM json_each(?)
		), paragraph_candidates AS (
			SELECT
				hash,
				source,
				metadata,
				CASE WHEN json_valid(metadata) THEN metadata ELSE '{}' END AS metadata_json
			FROM paragraphs
			WHERE is_deleted IS NULL OR is_deleted = 0
		)
		SELECT hash, source, metadata
		FROM paragraph_candidates p
		WHERE LOWER(COALESCE(json_extract(p.metadata_json, '$.scope_type'), '')) = 'global'
		   OR p.source LIKE 'web_import:%'
		   OR COALESCE(json_extract(p.metadata_json, '$.source'), '') LIKE 'web_import:%'
		   OR EXISTS (
				SELECT 1
				FROM allowed_chat_ids allowed
				WHERE p.source = 'chat_summary:' || allowed.value
		   )
		   OR EXISTS (
				SELECT 1
				FROM json_tree(p.metadata_json) metadata_value
				JOIN allowed_chat_ids allowed
				  ON CAST(metadata_value.value AS TEXT) = allowed.value
				WHERE metadata_value.type IN ('text', 'integer', 'real')
		   )
`, string(allowedJSON))
	if err != nil {
		return nil, err
	}
	allowedSet := map[string]struct{}{}
	for _, item := range allowed {
		allowedSet[item] = struct{}{}
	}
	for _, row := range rows {
		decodeParagraphMetadataRow(row)
		kind, chatIDs := paragraphScopeIdentityOf(row)
		if kind == "global" {
			appendScopeHash(scope.ParagraphIDs, asString(row["hash"]))
			continue
		}
		if kind != "chat" {
			continue
		}
		for id := range chatIDs {
			if _, ok := allowedSet[id]; ok {
				appendScopeHash(scope.ParagraphIDs, asString(row["hash"]))
				break
			}
		}
	}
	if len(scope.ParagraphIDs) == 0 {
		return scope, nil
	}
	paragraphIDs := sortedScopeIDs(scope.ParagraphIDs)
	paragraphJSON, err := json.Marshal(paragraphIDs)
	if err != nil {
		return nil, err
	}
	relationRows, err := store.Query(`
			WITH allowed_paragraphs(value) AS (
				SELECT CAST(value AS TEXT) FROM json_each(?)
			)
			SELECT r.hash, r.source_paragraph, pr.paragraph_hash
			FROM relations r
			LEFT JOIN paragraph_relations pr ON pr.relation_hash = r.hash
			WHERE (r.is_inactive IS NULL OR r.is_inactive = 0)
			  AND (
				   r.source_paragraph IN (SELECT value FROM allowed_paragraphs)
				OR pr.paragraph_hash IN (SELECT value FROM allowed_paragraphs)
			  )
		`, string(paragraphJSON))
	if err != nil {
		return nil, err
	}
	for _, row := range relationRows {
		source := strings.TrimSpace(asString(row["source_paragraph"]))
		attached := strings.TrimSpace(asString(row["paragraph_hash"]))
		if _, ok := scope.ParagraphIDs[source]; ok {
			appendScopeHash(scope.RelationIDs, asString(row["hash"]))
		} else if _, ok := scope.ParagraphIDs[attached]; ok {
			appendScopeHash(scope.RelationIDs, asString(row["hash"]))
		}
	}
	entityRows, err := store.Query(`
			SELECT pe.entity_hash, pe.paragraph_hash
			FROM paragraph_entities pe
			JOIN entities e ON e.hash = pe.entity_hash
			WHERE (e.is_deleted IS NULL OR e.is_deleted = 0)
			  AND pe.paragraph_hash IN (SELECT CAST(value AS TEXT) FROM json_each(?))
		`, string(paragraphJSON))
	if err != nil {
		return nil, err
	}
	for _, row := range entityRows {
		if _, ok := scope.ParagraphIDs[strings.TrimSpace(asString(row["paragraph_hash"]))]; ok {
			appendScopeHash(scope.EntityIDs, asString(row["entity_hash"]))
		}
	}
	episodeRows, err := store.Query(`
			SELECT episode_id, paragraph_hash
			FROM episode_paragraphs
			WHERE paragraph_hash IN (SELECT CAST(value AS TEXT) FROM json_each(?))
		`, string(paragraphJSON))
	if err != nil {
		return nil, err
	}
	for _, row := range episodeRows {
		if _, ok := scope.ParagraphIDs[strings.TrimSpace(asString(row["paragraph_hash"]))]; ok {
			appendScopeHash(scope.EpisodeIDs, asString(row["episode_id"]))
		}
	}
	return scope, nil
}

func paragraphScopeIdentityOf(paragraph map[string]any) (string, map[string]struct{}) {
	return (&SearchHitService{}).paragraphScopeIdentity(paragraph)
}

func decodeParagraphMetadataRow(row map[string]any) {
	if row == nil {
		return
	}
	switch raw := row["metadata"].(type) {
	case string:
		var parsed map[string]any
		if raw != "" && json.Unmarshal([]byte(raw), &parsed) == nil {
			row["metadata"] = parsed
		}
	case []byte:
		var parsed map[string]any
		if len(raw) > 0 && json.Unmarshal(raw, &parsed) == nil {
			row["metadata"] = parsed
		}
	}
}

func appendScopeHash(target map[string]struct{}, value string) {
	if token := strings.TrimSpace(value); token != "" {
		target[token] = struct{}{}
	}
}

func sortedScopeIDs(target map[string]struct{}) []string {
	out := make([]string, 0, len(target))
	for id := range target {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func extendChatScopeIDs(tokens map[string]struct{}, value any) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			extendChatScopeIDs(tokens, item)
		}
		return
	case []string:
		for _, item := range typed {
			extendChatScopeIDs(tokens, item)
		}
		return
	}
	if token := strings.TrimSpace(asString(value)); token != "" {
		tokens[token] = struct{}{}
	}
}

func intersectScopeIDs(left map[string]struct{}, right map[string]struct{}) bool {
	for key := range left {
		if _, ok := right[key]; ok {
			return true
		}
	}
	return false
}

// ParagraphMatchesChatScope 对齐 _paragraph_matches_chat_scope
func (s *SearchHitService) ParagraphMatchesChatScope(ctx context.Context, payload map[string]any) (any, error) {
	paragraph := asMap(payload["paragraph"])
	allowed := map[string]struct{}{}
	for _, item := range searchHitStrings(payload["allowed_chat_ids"]) {
		allowed[item] = struct{}{}
	}
	return s.paragraphMatchesChatScope(paragraph, allowed), nil
}

func (s *SearchHitService) paragraphMatchesChatScope(paragraph map[string]any, allowed map[string]struct{}) bool {
	if paragraph == nil {
		return false
	}
	if len(allowed) == 0 {
		return true
	}
	kind, chatIDs := s.paragraphScopeIdentity(paragraph)
	if kind == "global" {
		return true
	}
	return kind == "chat" && intersectScopeIDs(chatIDs, allowed)
}

// HitMetadataMatchesChatScope 对齐 _hit_metadata_matches_chat_scope
func (s *SearchHitService) HitMetadataMatchesChatScope(ctx context.Context, payload map[string]any) (any, error) {
	hit := asMap(payload["hit"])
	allowed := map[string]struct{}{}
	for _, item := range searchHitStrings(payload["allowed_chat_ids"]) {
		allowed[item] = struct{}{}
	}
	return s.hitMetadataMatchesChatScope(hit, allowed), nil
}

func (s *SearchHitService) hitMetadataMatchesChatScope(hit map[string]any, allowed map[string]struct{}) any {
	if len(allowed) == 0 {
		return true
	}
	metadata := utils.CoerceMetadataDict(hit["metadata"])
	hitType := strings.TrimSpace(asString(hit["type"]))
	chatIDs := metadataChatScopeIDs(metadata)
	if len(chatIDs) > 0 {
		if intersectScopeIDs(chatIDs, allowed) {
			return true
		}
		if hitType == "paragraph" || hitType == "relation" {
			return nil
		}
		return false
	}
	source := strings.TrimSpace(firstNonEmptyString(asString(metadata["source"]), asString(hit["source"])))
	chatSources := map[string]struct{}{}
	for item := range allowed {
		chatSources["chat_summary:"+item] = struct{}{}
	}
	if hitType == "episode" {
		_, ok := chatSources[source]
		return ok
	}
	if strings.HasPrefix(source, "chat_summary:") {
		_, ok := chatSources[source]
		return ok
	}
	return nil
}

// ExtendChatScopeIDs 对齐 _extend_chat_scope_ids
func (s *SearchHitService) ExtendChatScopeIDs(ctx context.Context, payload map[string]any) (any, error) {
	tokens := map[string]struct{}{}
	for _, item := range searchHitStrings(payload["tokens"]) {
		tokens[item] = struct{}{}
	}
	extendChatScopeIDs(tokens, payload["value"])
	out := make([]string, 0, len(tokens))
	for key := range tokens {
		out = append(out, key)
	}
	sort.Strings(out)
	return out, nil
}

func metadataChatScopeIDs(metadata map[string]any) map[string]struct{} {
	tokens := map[string]struct{}{}
	for _, key := range []string{"chat_id", "session_id", "stream_id", "chat_ids", "session_ids", "stream_ids"} {
		extendChatScopeIDs(tokens, metadata[key])
	}
	return tokens
}

// MetadataChatScopeIDs 对齐 _metadata_chat_scope_ids
func (s *SearchHitService) MetadataChatScopeIDs(ctx context.Context, payload map[string]any) (any, error) {
	out := make([]string, 0)
	for item := range metadataChatScopeIDs(utils.CoerceMetadataDict(payload["metadata"])) {
		out = append(out, item)
	}
	sort.Strings(out)
	return out, nil
}

// FilterHitsByChatScope 对齐 _filter_hits_by_chat_scope
func (s *SearchHitService) FilterHitsByChatScope(ctx context.Context, payload map[string]any) (any, error) {
	hits := searchHitList(payload["hits"])
	allowed := s.allowedSet(asString(payload["chat_id"]), searchHitStrings(payload["shared_chat_ids"]))
	store := s.store()
	if len(allowed) == 0 || store == nil {
		return hits, nil
	}
	allowedIndexes := map[int]struct{}{}
	pending := map[int]map[string]string{}
	paragraphHashes := make([]string, 0)
	relationHashes := make([]string, 0)
	for index, item := range hits {
		decision := s.hitMetadataMatchesChatScope(item, allowed)
		if decision == true {
			allowedIndexes[index] = struct{}{}
			continue
		}
		if decision == false {
			continue
		}
		hitHash := strings.TrimSpace(asString(item["hash"]))
		switch strings.TrimSpace(asString(item["type"])) {
		case "paragraph":
			if hitHash != "" {
				paragraphHashes = append(paragraphHashes, hitHash)
				pending[index] = map[string]string{"type": "paragraph", "hash": hitHash}
			}
		case "relation":
			if hitHash != "" {
				relationHashes = append(relationHashes, hitHash)
				pending[index] = map[string]string{"type": "relation", "hash": hitHash}
			}
		}
	}
	var paragraphMap map[string]map[string]any
	var relationParagraphMap map[string][]map[string]any
	var err error
	if len(paragraphHashes) > 0 {
		paragraphMap, err = store.GetParagraphsByHashes(paragraphHashes)
		if err != nil {
			return nil, err
		}
	}
	if len(relationHashes) > 0 {
		relationParagraphMap, err = store.GetParagraphsByRelationHashes(relationHashes)
		if err != nil {
			return nil, err
		}
	}
	for index, item := range pending {
		if item["type"] == "paragraph" {
			if s.paragraphMatchesChatScope(paragraphMap[item["hash"]], allowed) {
				allowedIndexes[index] = struct{}{}
			}
			continue
		}
		for _, paragraph := range relationParagraphMap[item["hash"]] {
			if s.paragraphMatchesChatScope(paragraph, allowed) {
				allowedIndexes[index] = struct{}{}
				break
			}
		}
	}
	filtered := make([]map[string]any, 0, len(allowedIndexes))
	for index, item := range hits {
		if _, ok := allowedIndexes[index]; ok {
			filtered = append(filtered, cloneAnyMap(item))
		}
	}
	return filtered, nil
}

func (s *SearchHitService) retrievalTypeFilterRoot() map[string]any {
	if s.deps.Cfg == nil {
		return map[string]any{}
	}
	filterConfig := asMap(s.deps.Cfg("filter", map[string]any{}))
	if filterConfig == nil {
		return map[string]any{}
	}
	return asMap(filterConfig["retrieval"])
}

func (s *SearchHitService) retrievalTypeFilterConfig(kind string) map[string]any {
	return asMap(s.retrievalTypeFilterRoot()[strings.TrimSpace(kind)])
}

// HasEnabledRetrievalTypeFilter 对齐 _has_enabled_retrieval_type_filter
func (s *SearchHitService) HasEnabledRetrievalTypeFilter(ctx context.Context) bool {
	root := s.retrievalTypeFilterRoot()
	if len(root) == 0 {
		return false
	}
	for _, kind := range []string{"chat_stream", "chat_summary", "episode"} {
		if config := asMap(root[kind]); config != nil && pyTruthy(config["enabled"]) {
			return true
		}
	}
	return false
}

// RetrievalTypeFilterRoot 对齐 _retrieval_type_filter_root
func (s *SearchHitService) RetrievalTypeFilterRoot(ctx context.Context) map[string]any {
	return s.retrievalTypeFilterRoot()
}

// RetrievalTypeFilterConfig 对齐 _retrieval_type_filter_config
func (s *SearchHitService) RetrievalTypeFilterConfig(ctx context.Context, kind string) map[string]any {
	return s.retrievalTypeFilterConfig(kind)
}

// RetrievalFilterKind 对齐 _retrieval_filter_kind
func (s *SearchHitService) RetrievalFilterKind(ctx context.Context, payload map[string]any) (any, error) {
	hitType := strings.TrimSpace(asString(payload["hit_type"]))
	sourceType := strings.TrimSpace(asString(payload["source_type"]))
	source := strings.TrimSpace(asString(payload["source"]))
	if hitType == "episode" {
		return "episode", nil
	}
	if sourceType == "chat_summary" || strings.HasPrefix(source, "chat_summary:") {
		return "chat_summary", nil
	}
	switch sourceType {
	case "chat_history", "chat_stream", "maibot.chat_history":
		return "chat_stream", nil
	}
	if strings.HasPrefix(source, "chat_stream:") || strings.HasPrefix(source, "maibot.chat_history:") {
		return "chat_stream", nil
	}
	return "", nil
}

// SourceStreamID 对齐 _source_stream_id
func (s *SearchHitService) SourceStreamID(ctx context.Context, payload map[string]any) (any, error) {
	token := strings.TrimSpace(asString(payload["source"]))
	for _, prefix := range []string{"chat_summary:", "chat_stream:", "maibot.chat_history:"} {
		if strings.HasPrefix(token, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(token, prefix)), nil
		}
	}
	return "", nil
}

// RetrievalFilterContext 对齐 _retrieval_filter_context
func (s *SearchHitService) RetrievalFilterContext(ctx context.Context, payload map[string]any) (any, error) {
	kind := strings.TrimSpace(asString(payload["kind"]))
	streamID := strings.TrimSpace(asString(payload["stream_id"]))
	groupID, userID := "", ""
	if streamID != "" && s.deps.SessionLookup != nil {
		groupID, userID = s.deps.SessionLookup(streamID)
		groupID = strings.TrimSpace(groupID)
		userID = strings.TrimSpace(userID)
	}
	return map[string]string{"kind": kind, "stream_id": streamID, "group_id": groupID, "user_id": userID}, nil
}

// CurrentRetrievalFilterContext 对齐 _current_retrieval_filter_context
func (s *SearchHitService) CurrentRetrievalFilterContext(ctx context.Context, payload map[string]any) (any, error) {
	value, err := s.RetrievalFilterContext(ctx, map[string]any{"kind": "", "stream_id": payload["stream_id"]})
	if err != nil {
		return nil, err
	}
	contextMap := value.(map[string]string)
	if groupID := strings.TrimSpace(asString(payload["group_id"])); groupID != "" {
		contextMap["group_id"] = groupID
	}
	if userID := strings.TrimSpace(asString(payload["user_id"])); userID != "" {
		contextMap["user_id"] = userID
	}
	return contextMap, nil
}

// RetrievalFilterContextIsCurrentSource 对齐 _retrieval_filter_context_is_current_source
func (s *SearchHitService) RetrievalFilterContextIsCurrentSource(ctx context.Context, payload map[string]any) (any, error) {
	contextMap := asMap(payload["context"])
	current := asMap(payload["current_context"])
	currentStream := strings.TrimSpace(asString(current["stream_id"]))
	sourceStream := strings.TrimSpace(asString(contextMap["stream_id"]))
	if currentStream != "" && sourceStream != "" && currentStream == sourceStream {
		return true, nil
	}
	currentGroup := strings.TrimSpace(asString(current["group_id"]))
	sourceGroup := strings.TrimSpace(asString(contextMap["group_id"]))
	if currentGroup != "" && sourceGroup != "" && currentGroup == sourceGroup {
		return true, nil
	}
	currentUser := strings.TrimSpace(asString(current["user_id"]))
	sourceUser := strings.TrimSpace(asString(contextMap["user_id"]))
	currentPrivate := currentUser != "" && currentGroup == ""
	sourcePrivate := sourceUser != "" && sourceGroup == ""
	return currentPrivate && sourcePrivate && currentUser == sourceUser, nil
}

// RetrievalFilterContextAllowed 对齐 _retrieval_filter_context_allowed
func (s *SearchHitService) RetrievalFilterContextAllowed(ctx context.Context, payload map[string]any) (any, error) {
	contextMap := asMap(payload["context"])
	kind := strings.TrimSpace(asString(contextMap["kind"]))
	if kind == "" {
		return true, nil
	}
	typeConfig := s.retrievalTypeFilterConfig(kind)
	if len(typeConfig) == 0 || !pyTruthy(typeConfig["enabled"]) {
		return true, nil
	}
	if s.deps.ChatFilterConfigAllows == nil {
		return true, nil
	}
	return s.deps.ChatFilterConfigAllows(typeConfig, asString(contextMap["stream_id"]), asString(contextMap["group_id"]), asString(contextMap["user_id"]), true), nil
}

// NormalizeSearchTimeBound 对齐 _normalize_search_time_bound
func (s *SearchHitService) NormalizeSearchTimeBound(ctx context.Context, payload map[string]any) (any, error) {
	value := payload["value"]
	isEnd := pyTruthy(payload["is_end"])
	numeric, query, err := NormalizeSearchTimeBoundValue(value, isEnd)
	if err != nil {
		return nil, err
	}
	return map[string]any{"numeric": numeric, "query": query}, nil
}

// NormalizeSearchTimeBoundValue 对齐 _normalize_search_time_bound；数值输入同时产出 format_timestamp 查询串
func NormalizeSearchTimeBoundValue(value any, isEnd bool) (*float64, *string, error) {
	if value == nil || asString(value) == "" {
		if _, ok := value.(float64); !ok {
			if _, ok := value.(int); !ok {
				return nil, nil, nil
			}
		}
	}
	switch typed := value.(type) {
	case float64:
		formatted, err := utils.FormatTimestamp(&typed)
		if err != nil {
			return nil, nil, err
		}
		return &typed, formatted, nil
	case int:
		number := float64(typed)
		formatted, err := utils.FormatTimestamp(&number)
		if err != nil {
			return nil, nil, err
		}
		return &number, formatted, nil
	case int64:
		number := float64(typed)
		formatted, err := utils.FormatTimestamp(&number)
		if err != nil {
			return nil, nil, err
		}
		return &number, formatted, nil
	}
	text := strings.TrimSpace(asString(value))
	if text == "" {
		return nil, nil, nil
	}
	if numeric := utils.OptionalFloat(text); numeric != nil {
		formatted, err := utils.FormatTimestamp(numeric)
		if err != nil {
			return nil, nil, err
		}
		return numeric, formatted, nil
	}
	timestamp, err := utils.ParseQueryDatetimeToTimestamp(text, isEnd)
	if err != nil {
		return nil, nil, fmt.Errorf("时间参数错误: %v", err)
	}
	return &timestamp, &text, nil
}

// BuildSearchTimeWindow 对齐 _normalize_search_time_window，供 SearchDeps 与命中处理共用
func BuildSearchTimeWindow(timeStart any, timeEnd any) (SearchTimeWindow, error) {
	window := SearchTimeWindow{}
	numericStart, queryStart, err := NormalizeSearchTimeBoundValue(timeStart, false)
	if err != nil {
		return window, err
	}
	numericEnd, queryEnd, err := NormalizeSearchTimeBoundValue(timeEnd, true)
	if err != nil {
		return window, err
	}
	if numericStart != nil && numericEnd != nil && *numericStart > *numericEnd {
		return window, fmt.Errorf("时间参数错误: time_start 不能晚于 time_end")
	}
	window.NumericStart, window.QueryStart = numericStart, queryStart
	window.NumericEnd, window.QueryEnd = numericEnd, queryEnd
	return window, nil
}

// NormalizeSearchTimeWindow 对齐 _normalize_search_time_window
func (s *SearchHitService) NormalizeSearchTimeWindow(ctx context.Context, payload map[string]any) (any, error) {
	window, err := BuildSearchTimeWindow(payload["time_start"], payload["time_end"])
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"numeric_start": window.NumericStart, "numeric_end": window.NumericEnd,
		"query_start": window.QueryStart, "query_end": window.QueryEnd,
	}, nil
}

// RetrievalResultHit 对齐 _retrieval_result_hit
func (s *SearchHitService) RetrievalResultHit(ctx context.Context, payload map[string]any) (any, error) {
	item := asMap(payload["item"])
	if item == nil {
		item = payload
	}
	metadata := asMap(item["metadata"])
	if metadata == nil {
		metadata = map[string]any{}
	}
	return map[string]any{
		"hash": asString(item["hash"]), "content": asString(item["content"]),
		"score": valueOrZero(item["score"]), "type": asString(item["type"]),
		"source": asString(item["source"]), "metadata": metadata,
	}, nil
}

func valueOrZero(value any) any {
	if value == nil {
		return 0.0
	}
	return value
}

// EpisodeHit 对齐 _episode_hit
func (s *SearchHitService) EpisodeHit(ctx context.Context, payload map[string]any) (any, error) {
	row := asMap(payload["row"])
	if row == nil {
		row = payload
	}
	return map[string]any{
		"type": "episode", "episode_id": asString(row["episode_id"]), "title": asString(row["title"]),
		"content": asString(row["summary"]), "score": spaceFloatAny(row["lexical_score"]), "source": "episode",
		"metadata": map[string]any{
			"participants": listOrEmpty(row["participants"]), "keywords": listOrEmpty(row["keywords"]),
			"source": row["source"], "event_time_start": row["event_time_start"], "event_time_end": row["event_time_end"],
		},
	}, nil
}

// listOrEmpty 对齐 Python row.get(key, []) or []：缺失或 null 返回空数组
func listOrEmpty(value any) []any {
	if items := asList(value); items != nil {
		return items
	}
	return []any{}
}

// Summary 对齐 _summary
func (s *SearchHitService) Summary(ctx context.Context, payload map[string]any) (any, error) {
	hits := searchHitList(payload["hits"])
	if len(hits) == 0 {
		return "", nil
	}
	lines := make([]string, 0, 5)
	for index, item := range hits {
		if index >= 5 {
			break
		}
		content := strings.ReplaceAll(strings.TrimSpace(asString(item["content"])), "\n", " ")
		runes := []rune(content)
		if len(runes) > 120 {
			content = string(runes[:120]) + "..."
		}
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, content))
	}
	return strings.Join(lines, "\n"), nil
}

// FilterHits 对齐 _filter_hits
func (s *SearchHitService) FilterHits(ctx context.Context, payload map[string]any) (any, error) {
	hits := searchHitList(payload["hits"])
	personID := asString(payload["person_id"])
	if personID == "" {
		return hits, nil
	}
	filtered := make([]map[string]any, 0)
	for _, item := range hits {
		metadata := asMap(item["metadata"])
		matched := false
		for _, itemID := range stringTokens(metadata["person_ids"]) {
			if itemID == personID {
				matched = true
				break
			}
		}
		if matched || strings.Contains(asString(item["content"]), personID) {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		return hits, nil
	}
	return filtered, nil
}

// FilterActiveRelationHits 对齐 _filter_active_relation_hits
// DB 读失败时返回 error（fail-closed），不再吞错放行非活跃关系。
func (s *SearchHitService) FilterActiveRelationHits(ctx context.Context, hits []map[string]any) ([]map[string]any, error) {
	store := s.store()
	if store == nil {
		return hits, nil
	}
	relationHashes := make([]string, 0)
	paragraphHashes := make([]string, 0)
	paragraphRelationCache := map[string][]string{}
	seenRelations := map[string]struct{}{}
	for _, item := range hits {
		itemType := strings.TrimSpace(asString(item["type"]))
		itemHash := strings.TrimSpace(asString(item["hash"]))
		if itemType == "relation" && itemHash != "" {
			if _, ok := seenRelations[itemHash]; !ok {
				seenRelations[itemHash] = struct{}{}
				relationHashes = append(relationHashes, itemHash)
			}
			continue
		}
		if itemType != "paragraph" || itemHash == "" {
			continue
		}
		paragraphHashes = append(paragraphHashes, itemHash)
		linked, err := store.GetParagraphRelations(itemHash)
		if err != nil {
			return nil, err
		}
		linkedHashes := make([]string, 0)
		for _, relation := range linked {
			linkedHash := strings.TrimSpace(asString(relation["hash"]))
			if linkedHash == "" {
				continue
			}
			linkedHashes = append(linkedHashes, linkedHash)
			if _, ok := seenRelations[linkedHash]; !ok {
				seenRelations[linkedHash] = struct{}{}
				relationHashes = append(relationHashes, linkedHash)
			}
		}
		if len(linkedHashes) > 0 {
			paragraphRelationCache[itemHash] = linkedHashes
		}
	}
	marksByParagraph := map[string][]map[string]any{}
	if len(paragraphHashes) > 0 {
		loaded, err := store.GetParagraphStaleRelationMarksBatch(paragraphHashes, nil)
		if err != nil {
			return nil, err
		}
		marksByParagraph = loaded
	}
	for _, marks := range marksByParagraph {
		for _, mark := range marks {
			relationHash := strings.TrimSpace(asString(mark["relation_hash"]))
			if relationHash == "" {
				continue
			}
			if _, ok := seenRelations[relationHash]; ok {
				continue
			}
			seenRelations[relationHash] = struct{}{}
			relationHashes = append(relationHashes, relationHash)
		}
	}
	if len(relationHashes) == 0 && len(marksByParagraph) == 0 {
		return hits, nil
	}
	statusMap, err := store.GetRelationStatusBatch(relationHashes)
	if err != nil {
		return nil, err
	}
	filtered := make([]map[string]any, 0, len(hits))
	for _, item := range hits {
		itemType := strings.TrimSpace(asString(item["type"]))
		if itemType == "paragraph" {
			paragraphHash := strings.TrimSpace(asString(item["hash"]))
			hiddenValue, err := s.ParagraphHiddenByStaleMarks(ctx, map[string]any{
				"paragraph_hash": paragraphHash, "marks_by_paragraph": marksByParagraph, "relation_status_map": statusMap,
			})
			if err != nil {
				continue
			}
			if pyTruthy(hiddenValue) {
				continue
			}
			linked := paragraphRelationCache[paragraphHash]
			if len(linked) == 0 {
				filtered = append(filtered, item)
				continue
			}
			visible := false
			for _, linkedHash := range linked {
				if !pyTruthy(statusMap[linkedHash]["is_inactive"]) {
					visible = true
					break
				}
			}
			if visible {
				filtered = append(filtered, item)
			}
			continue
		}
		if itemType != "relation" {
			filtered = append(filtered, item)
			continue
		}
		status := statusMap[strings.TrimSpace(asString(item["hash"]))]
		if status == nil || pyTruthy(status["is_inactive"]) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered, nil
}

// RetrievalFilterContextsForHit 对齐 _retrieval_filter_contexts_for_hit
func (s *SearchHitService) RetrievalFilterContextsForHit(ctx context.Context, hit map[string]any, paragraphMap map[string]map[string]any, relationParagraphMap map[string][]map[string]any) []map[string]string {
	hitType := strings.TrimSpace(asString(hit["type"]))
	hitHash := strings.TrimSpace(asString(hit["hash"]))
	if hitType == "paragraph" && paragraphMap[hitHash] != nil {
		return []map[string]string{s.retrievalFilterContextFromParagraph(paragraphMap[hitHash])}
	}
	if hitType == "relation" && len(relationParagraphMap[hitHash]) > 0 {
		contexts := make([]map[string]string, 0)
		for _, paragraph := range relationParagraphMap[hitHash] {
			if paragraph != nil {
				contexts = append(contexts, s.retrievalFilterContextFromParagraph(paragraph))
			}
		}
		if len(contexts) > 0 {
			return contexts
		}
	}
	value, _ := s.retrievalFilterContextFromHit(ctx, hit)
	return []map[string]string{value}
}

func (s *SearchHitService) retrievalFilterContextFromHit(ctx context.Context, hit map[string]any) (map[string]string, error) {
	metadata := utils.CoerceMetadataDict(hit["metadata"])
	source := strings.TrimSpace(firstNonEmptyString(asString(metadata["source"]), asString(hit["source"])))
	sourceType := strings.TrimSpace(asString(metadata["source_type"]))
	streamID := strings.TrimSpace(asString(metadata["chat_id"]))
	if streamID == "" {
		streamID = sourceStreamID(source)
	}
	kindValue, _ := s.RetrievalFilterKind(ctx, map[string]any{"hit_type": hit["type"], "source_type": sourceType, "source": source})
	return s.retrievalFilterContextValue(asString(kindValue), streamID), nil
}

func (s *SearchHitService) retrievalFilterContextFromParagraph(paragraph map[string]any) map[string]string {
	metadata := utils.CoerceMetadataDict(paragraph["metadata"])
	source := strings.TrimSpace(firstNonEmptyString(asString(paragraph["source"]), asString(metadata["source"])))
	sourceType := strings.TrimSpace(asString(metadata["source_type"]))
	streamID := strings.TrimSpace(asString(metadata["chat_id"]))
	if streamID == "" {
		streamID = sourceStreamID(source)
	}
	kind := retrievalFilterKindValue("paragraph", sourceType, source)
	return s.retrievalFilterContextValue(kind, streamID)
}

func sourceStreamID(source string) string {
	for _, prefix := range []string{"chat_summary:", "chat_stream:", "maibot.chat_history:"} {
		if strings.HasPrefix(source, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(source, prefix))
		}
	}
	return ""
}

func retrievalFilterKindValue(hitType string, sourceType string, source string) string {
	if hitType == "episode" {
		return "episode"
	}
	if sourceType == "chat_summary" || strings.HasPrefix(source, "chat_summary:") {
		return "chat_summary"
	}
	switch sourceType {
	case "chat_history", "chat_stream", "maibot.chat_history":
		return "chat_stream"
	}
	if strings.HasPrefix(source, "chat_stream:") || strings.HasPrefix(source, "maibot.chat_history:") {
		return "chat_stream"
	}
	return ""
}

func (s *SearchHitService) retrievalFilterContextValue(kind string, streamID string) map[string]string {
	streamToken := strings.TrimSpace(streamID)
	groupID, userID := "", ""
	if streamToken != "" && s.deps.SessionLookup != nil {
		groupID, userID = s.deps.SessionLookup(streamToken)
		groupID = strings.TrimSpace(groupID)
		userID = strings.TrimSpace(userID)
	}
	return map[string]string{"kind": strings.TrimSpace(kind), "stream_id": streamToken, "group_id": groupID, "user_id": userID}
}

// RetrievalFilterContextFromHit 对齐 _retrieval_filter_context_from_hit
func (s *SearchHitService) RetrievalFilterContextFromHit(ctx context.Context, hit map[string]any) map[string]string {
	value, _ := s.retrievalFilterContextFromHit(ctx, hit)
	return value
}

// RetrievalFilterContextFromParagraph 对齐 _retrieval_filter_context_from_paragraph
func (s *SearchHitService) RetrievalFilterContextFromParagraph(ctx context.Context, paragraph map[string]any) map[string]string {
	return s.retrievalFilterContextFromParagraph(paragraph)
}

// FilterHitsByRetrievalTypeScope 对齐 _filter_hits_by_retrieval_type_scope
func (s *SearchHitService) FilterHitsByRetrievalTypeScope(ctx context.Context, payload map[string]any) (any, error) {
	hits := searchHitList(payload["hits"])
	if len(hits) == 0 || !s.HasEnabledRetrievalTypeFilter(ctx) {
		return hits, nil
	}
	currentContextValue, err := s.CurrentRetrievalFilterContext(ctx, map[string]any{
		"stream_id": payload["current_stream_id"], "group_id": payload["current_group_id"], "user_id": payload["current_user_id"],
	})
	if err != nil {
		return nil, err
	}
	currentContext := currentContextValue.(map[string]string)
	store := s.store()
	paragraphHashes := make([]string, 0)
	relationHashes := make([]string, 0)
	for _, item := range hits {
		itemHash := strings.TrimSpace(asString(item["hash"]))
		if itemHash == "" {
			continue
		}
		switch strings.TrimSpace(asString(item["type"])) {
		case "paragraph":
			paragraphHashes = append(paragraphHashes, itemHash)
		case "relation":
			relationHashes = append(relationHashes, itemHash)
		}
	}
	paragraphMap := map[string]map[string]any{}
	relationParagraphMap := map[string][]map[string]any{}
	if store != nil {
		if len(paragraphHashes) > 0 {
			paragraphMap, err = store.GetParagraphsByHashes(paragraphHashes)
			if err != nil {
				return nil, err
			}
		}
		if len(relationHashes) > 0 {
			relationParagraphMap, err = store.GetParagraphsByRelationHashes(relationHashes)
			if err != nil {
				return nil, err
			}
		}
	}
	filtered := make([]map[string]any, 0)
	for _, item := range hits {
		contexts := s.RetrievalFilterContextsForHit(ctx, item, paragraphMap, relationParagraphMap)
		allowed := false
		for _, contextMap := range contexts {
			current, _ := s.RetrievalFilterContextIsCurrentSource(ctx, map[string]any{"context": contextMap, "current_context": currentContext})
			if pyTruthy(current) {
				allowed = true
				break
			}
		}
		if !allowed {
			for _, contextMap := range contexts {
				value, err := s.RetrievalFilterContextAllowed(ctx, map[string]any{"context": contextMap})
				if err != nil {
					return nil, err
				}
				if pyTruthy(value) {
					allowed = true
					break
				}
			}
		}
		if allowed {
			filtered = append(filtered, cloneAnyMap(item))
		}
	}
	return filtered, nil
}

var _ = json.Marshal
