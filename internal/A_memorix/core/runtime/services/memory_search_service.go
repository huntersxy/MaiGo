package services

// 统一检索服务，逐段对齐 memory_search_service.py 的 MemorySearchService

import (
	"context"
	"fmt"
	"math"
	"strings"
)

// RetrievalScope 对齐 RetrievalScope，保存聊天可见范围下的候选集合
type RetrievalScope struct {
	Key          string
	ParagraphIDs map[string]struct{}
	RelationIDs  map[string]struct{}
	EntityIDs    map[string]struct{}
	EpisodeIDs   map[string]struct{}
}

// SearchTimeWindow 对齐 _NormalizedSearchTimeWindow
type SearchTimeWindow struct {
	NumericStart *float64
	NumericEnd   *float64
	QueryStart   *string
	QueryEnd     *string
}

// SearchMemoryRequest 对齐 KernelSearchRequest
type SearchMemoryRequest struct {
	Query         string
	Mode          string
	Limit         int
	ChatID        string
	GroupID       string
	UserID        string
	SharedChatIDs []string
	PersonID      string
	RespectFilter bool
	TimeStart     any
	TimeEnd       any
}

// SearchExecutionRequest 对齐 SearchExecutionRequest
type SearchExecutionRequest struct {
	Caller          string
	StreamID        *string
	GroupID         *string
	UserID          *string
	QueryType       string
	Query           string
	TopK            int
	CandidateTopK   int
	TimeFrom        *string
	TimeTo          *string
	Person          *string
	Source          *string
	UseThreshold    bool
	EnablePPR       bool
	Scope           *RetrievalScope
	EnforceChatFilt bool
}

// SearchExecutionResult 对齐 SearchExecutionResult
type SearchExecutionResult struct {
	Success      bool
	Results      []map[string]any
	Error        string
	ChatFiltered bool
}

// AggregateQueryParams 对齐 aggregate_query_service.execute 的关键字参数
type AggregateQueryParams struct {
	Query      string
	TopK       int
	Mix        bool
	MixTopK    int
	TimeFrom   *string
	TimeTo     *string
	Search     func() (map[string]any, error)
	TimeSearch func() (map[string]any, error)
	Episode    func() (map[string]any, error)
}

// SearchDeps 检索服务的依赖端口
type SearchDeps struct {
	// IsChatFiltered 判断聊天流是否被过滤
	IsChatFiltered func(respectFilter bool, streamID string, groupID string, userID string) bool
	// Initialize 确保运行时已初始化
	Initialize func(ctx context.Context) error
	// ResolveRetrievalScope 解析检索范围；SQL 失败返回 error（fail-closed）
	ResolveRetrievalScope func(ctx context.Context, chatID string, sharedChatIDs []string) (*RetrievalScope, error)
	// ExecuteSearch 执行单次检索
	ExecuteSearch func(ctx context.Context, request SearchExecutionRequest) (SearchExecutionResult, error)
	// EpisodeQueryForChatScope 按聊天范围查询 episode
	EpisodeQueryForChatScope func(ctx context.Context, params map[string]any) (any, error)
	// AggregateExecute 执行聚合查询
	AggregateExecute func(ctx context.Context, params AggregateQueryParams) (map[string]any, error)
	// FilterEpisodeHits 过滤 episode 命中
	FilterEpisodeHits func(hits []map[string]any) []map[string]any
	// FilterHits 按人物过滤命中
	FilterHits func(hits []map[string]any, personID string) []map[string]any
	// FilterUserVisibleHits 过滤用户可见命中
	FilterUserVisibleHits func(hits []map[string]any) []map[string]any
	// FilterHitsByRetrievalTypeScope 按检索类型范围过滤命中
	FilterHitsByRetrievalTypeScope func(hits []map[string]any, streamID string, groupID string, userID string) []map[string]any
	// NormalizeTimeWindow 归一化时间窗口
	NormalizeTimeWindow func(timeStart any, timeEnd any) (SearchTimeWindow, error)
	// BuildRuntimeConfig 构建运行时配置
	BuildRuntimeConfig func(ctx context.Context) map[string]any
	// ReinforceAccess 对最终采用的关系命中提交一次 ACCESS
	ReinforceAccess func(ctx context.Context, relationHashes []string)
	// RuntimeCapabilityStatus 读取运行时能力状态
	RuntimeCapabilityStatus func() map[string]any
}

// MemorySearchService 统一检索服务
type MemorySearchService struct {
	*Base
	deps SearchDeps
}

// NewMemorySearchService 构造统一检索服务
func NewMemorySearchService(base *Base, deps SearchDeps) *MemorySearchService {
	return &MemorySearchService{Base: base, deps: deps}
}

// SearchMemory 对齐 search_memory
func (s *MemorySearchService) SearchMemory(ctx context.Context, request SearchMemoryRequest) (map[string]any, error) {
	if s.deps.IsChatFiltered != nil && s.deps.IsChatFiltered(request.RespectFilter, request.ChatID, request.GroupID, request.UserID) {
		return s.withStatus(map[string]any{"summary": "", "hits": []map[string]any{}, "filtered": true}), nil
	}
	if s.deps.Initialize != nil {
		if err := s.deps.Initialize(ctx); err != nil {
			return nil, err
		}
	}
	mode := strings.ToLower(strings.TrimSpace(request.Mode))
	if mode == "" {
		mode = "search"
	}
	query := strings.TrimSpace(request.Query)
	limit := request.Limit
	if limit < 1 {
		limit = 5
	}
	sharedChatIDs := make([]string, 0, len(request.SharedChatIDs))
	for _, item := range request.SharedChatIDs {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			sharedChatIDs = append(sharedChatIDs, trimmed)
		}
	}
	var scope *RetrievalScope
	if s.deps.ResolveRetrievalScope != nil {
		var scopeErr error
		scope, scopeErr = s.deps.ResolveRetrievalScope(ctx, request.ChatID, sharedChatIDs)
		if scopeErr != nil {
			return s.withStatus(map[string]any{"summary": "", "hits": []map[string]any{}, "error": scopeErr.Error()}), nil
		}
	}
	supported := map[string]struct{}{"search": {}, "time": {}, "hybrid": {}, "episode": {}, "aggregate": {}}
	if _, ok := supported[mode]; !ok {
		return s.withStatus(map[string]any{
			"summary": "",
			"hits":    []map[string]any{},
			"error":   fmt.Sprintf("不支持的检索模式: %s（仅支持 search/time/hybrid/episode/aggregate，semantic 已移除）", mode),
		}), nil
	}
	timeWindow, err := s.normalizeTimeWindow(request.TimeStart, request.TimeEnd)
	if err != nil {
		return s.withStatus(map[string]any{"summary": "", "hits": []map[string]any{}, "error": err.Error()}), nil
	}

	if mode == "episode" {
		rows, err := s.episodeQueryForChatScope(ctx, query, limit, timeWindow.NumericStart, timeWindow.NumericEnd, request.PersonID, request.ChatID, sharedChatIDs, scope)
		if err != nil {
			return nil, err
		}
		hits := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			hits = append(hits, episodeHit(row))
		}
		hits = s.filterEpisodeHits(hits)
		hits = filterHitsByRetrievalScope(hits, scope)
		if request.RespectFilter {
			hits = s.filterHitsByRetrievalTypeScope(hits, request.ChatID, request.GroupID, request.UserID)
		}
		hits = limitHits(hits, limit)
		return s.finalizeSearchResponse(ctx, hits)
	}

	if mode == "aggregate" {
		searchRunner := func() (map[string]any, error) {
			return s.aggregateSearch(ctx, query, limit, request, scope)
		}
		timeRunner := func() (map[string]any, error) {
			return s.aggregateTime(ctx, query, limit, request, timeWindow, scope)
		}
		episodeRunner := func() (map[string]any, error) {
			return s.aggregateEpisode(ctx, query, limit, request, timeWindow, scope)
		}
		payload := map[string]any{}
		if s.deps.AggregateExecute != nil {
			payload, err = s.deps.AggregateExecute(ctx, AggregateQueryParams{
				Query: query, TopK: limit, Mix: true, MixTopK: limit,
				TimeFrom: timeWindow.QueryStart, TimeTo: timeWindow.QueryEnd,
				Search: searchRunner, TimeSearch: timeRunner, Episode: episodeRunner,
			})
			if err != nil {
				return nil, err
			}
		} else {
			payload, err = searchRunner()
			if err != nil {
				return nil, err
			}
		}
		hits := make([]map[string]any, 0)
		for _, item := range asMapList(payload["mixed_results"]) {
			if item == nil {
				continue
			}
			if _, ok := item["metadata"]; !ok {
				item["metadata"] = map[string]any{}
			}
			hits = append(hits, item)
		}
		filtered := s.filterHits(hits, request.PersonID)
		filtered = s.filterUserVisibleHits(filtered)
		filtered = filterHitsByRetrievalScope(filtered, scope)
		if request.RespectFilter {
			filtered = s.filterHitsByRetrievalTypeScope(filtered, request.ChatID, request.GroupID, request.UserID)
		}
		return s.finalizeSearchResponse(ctx, limitHits(filtered, limit))
	}

	pluginConfig := map[string]any{}
	if s.deps.BuildRuntimeConfig != nil {
		pluginConfig = s.deps.BuildRuntimeConfig(ctx)
	}
	result, err := s.searchExecutionForChatScope(ctx, SearchExecutionRequest{
		Caller: "sdk_memory_kernel", QueryType: mode, Query: query, TopK: limit,
		TimeFrom: timeWindow.QueryStart, TimeTo: timeWindow.QueryEnd,
		Scope: scope, EnforceChatFilt: request.RespectFilter,
		Person:   stringPtrOrNil(request.PersonID),
		StreamID: stringPtrOrNil(request.ChatID),
		GroupID:  stringPtrOrNil(request.GroupID),
		UserID:   stringPtrOrNil(request.UserID),
	}, request, pluginConfig)
	if err != nil {
		return nil, err
	}
	if !result.Success {
		return s.withStatus(map[string]any{"summary": "", "hits": []map[string]any{}, "error": result.Error}), nil
	}
	if result.ChatFiltered {
		return s.withStatus(map[string]any{"summary": "", "hits": []map[string]any{}, "filtered": true}), nil
	}
	hits := make([]map[string]any, 0, len(result.Results))
	for _, item := range result.Results {
		hits = append(hits, retrievalResultHit(item))
	}
	filtered := s.filterHits(hits, request.PersonID)
	filtered = s.filterUserVisibleHits(filtered)
	filtered = filterHitsByRetrievalScope(filtered, scope)
	if request.RespectFilter {
		filtered = s.filterHitsByRetrievalTypeScope(filtered, request.ChatID, request.GroupID, request.UserID)
	}
	return s.finalizeSearchResponse(ctx, limitHits(filtered, limit))
}

// retrievalScopeFromValue 安全地把 payload 值还原为检索范围，缺失或类型不符返回 nil
func retrievalScopeFromValue(value any) *RetrievalScope {
	switch typed := value.(type) {
	case nil:
		return nil
	case *RetrievalScope:
		return typed
	case RetrievalScope:
		return &typed
	default:
		return nil
	}
}

// SearchExecutionOnce 对齐 _search_execution_once 的 payload 入口
func (s *MemorySearchService) SearchExecutionOnce(ctx context.Context, params map[string]any) (map[string]any, error) {
	request := requestFromPayload(params)
	execRequest := SearchExecutionRequest{
		Caller:          asString(params["caller"]),
		QueryType:       asString(params["query_type"]),
		Query:           asString(params["query"]),
		TopK:            intOr(params["top_k"], 5, 1),
		TimeFrom:        stringPayloadPtr(params["time_from"]),
		TimeTo:          stringPayloadPtr(params["time_to"]),
		EnforceChatFilt: pyTruthy(params["enforce_chat_filter"]),
		Scope:           retrievalScopeFromValue(params["scope"]),
	}
	execRequest.Person = stringPtrOrNil(request.PersonID)
	execRequest.StreamID = stringPtrOrNil(request.ChatID)
	execRequest.GroupID = stringPtrOrNil(request.GroupID)
	execRequest.UserID = stringPtrOrNil(request.UserID)
	result, err := s.searchExecutionForChatScope(ctx, execRequest, request, asMap(params["plugin_config"]))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"success":       result.Success,
		"results":       result.Results,
		"error":         result.Error,
		"chat_filtered": result.ChatFiltered,
	}, nil
}

// SearchExecutionForChatScope 对齐 _search_execution_for_chat_scope 的 payload 入口
func (s *MemorySearchService) SearchExecutionForChatScope(ctx context.Context, params map[string]any) (map[string]any, error) {
	return s.SearchExecutionOnce(ctx, params)
}

// EpisodeQueryForChatScope 对齐 _episode_query_for_chat_scope 的 payload 入口
func (s *MemorySearchService) EpisodeQueryForChatScope(ctx context.Context, params map[string]any) (any, error) {
	if s.deps.EpisodeQueryForChatScope != nil {
		return s.deps.EpisodeQueryForChatScope(ctx, params)
	}
	rows, err := s.episodeQueryForChatScope(ctx,
		asString(params["query"]),
		intOr(params["top_k"], 5, 1),
		optionalFloat(params["time_from"]),
		optionalFloat(params["time_to"]),
		asString(params["person"]),
		asString(params["chat_id"]),
		asStringList(params["shared_chat_ids"]),
		retrievalScopeFromValue(params["scope"]),
	)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *MemorySearchService) searchExecutionForChatScope(ctx context.Context, request SearchExecutionRequest, kernelRequest SearchMemoryRequest, pluginConfig map[string]any) (SearchExecutionResult, error) {
	topK := request.TopK
	candidateTopK := topK
	paragraphs := pyIntOr(s.Cfg("retrieval.top_k_paragraphs", 20), 20, math.MinInt)
	relations := pyIntOr(s.Cfg("retrieval.top_k_relations", 10), 10, math.MinInt)
	if paragraphs+relations > candidateTopK {
		candidateTopK = paragraphs + relations
	}
	request.CandidateTopK = candidateTopK
	request.UseThreshold = true
	request.EnablePPR = pyTruthy(s.Cfg("retrieval.enable_ppr", true))
	if request.Scope == nil && s.deps.ResolveRetrievalScope != nil {
		scope, scopeErr := s.deps.ResolveRetrievalScope(ctx, kernelRequest.ChatID, kernelRequest.SharedChatIDs)
		if scopeErr != nil {
			return SearchExecutionResult{Success: false, Error: scopeErr.Error()}, nil
		}
		request.Scope = scope
	}
	if s.deps.ExecuteSearch == nil {
		return SearchExecutionResult{Success: false, Error: "检索执行器未注入"}, nil
	}
	return s.deps.ExecuteSearch(ctx, request)
}

func (s *MemorySearchService) aggregateSearch(ctx context.Context, query string, limit int, request SearchMemoryRequest, scope *RetrievalScope) (map[string]any, error) {
	pluginConfig := map[string]any{}
	if s.deps.BuildRuntimeConfig != nil {
		pluginConfig = s.deps.BuildRuntimeConfig(ctx)
	}
	result, err := s.searchExecutionForChatScope(ctx, SearchExecutionRequest{
		Caller: "sdk_memory_kernel.aggregate", QueryType: "search", Query: query, TopK: limit,
		Scope: scope, Person: stringPtrOrNil(request.PersonID),
		StreamID: stringPtrOrNil(request.ChatID),
		GroupID:  stringPtrOrNil(request.GroupID),
		UserID:   stringPtrOrNil(request.UserID),
	}, request, pluginConfig)
	if err != nil {
		return nil, err
	}
	results := make([]map[string]any, 0)
	if result.Success {
		for _, item := range result.Results {
			results = append(results, retrievalResultHit(item))
		}
	}
	results = filterHitsByRetrievalScope(results, scope)
	results = limitHits(results, limit)
	return map[string]any{"success": result.Success, "results": results, "count": len(results), "query_type": "search", "error": result.Error}, nil
}

func (s *MemorySearchService) aggregateTime(ctx context.Context, query string, limit int, request SearchMemoryRequest, window SearchTimeWindow, scope *RetrievalScope) (map[string]any, error) {
	pluginConfig := map[string]any{}
	if s.deps.BuildRuntimeConfig != nil {
		pluginConfig = s.deps.BuildRuntimeConfig(ctx)
	}
	result, err := s.searchExecutionForChatScope(ctx, SearchExecutionRequest{
		Caller: "sdk_memory_kernel.aggregate", QueryType: "time", Query: query, TopK: limit,
		TimeFrom: window.QueryStart, TimeTo: window.QueryEnd, Scope: scope, Person: stringPtrOrNil(request.PersonID),
		StreamID: stringPtrOrNil(request.ChatID),
		GroupID:  stringPtrOrNil(request.GroupID),
		UserID:   stringPtrOrNil(request.UserID),
	}, request, pluginConfig)
	if err != nil {
		return nil, err
	}
	results := make([]map[string]any, 0)
	if result.Success {
		for _, item := range result.Results {
			results = append(results, retrievalResultHit(item))
		}
	}
	results = filterHitsByRetrievalScope(results, scope)
	results = limitHits(results, limit)
	return map[string]any{"success": result.Success, "results": results, "count": len(results), "query_type": "time", "error": result.Error}, nil
}

func (s *MemorySearchService) aggregateEpisode(ctx context.Context, query string, limit int, request SearchMemoryRequest, window SearchTimeWindow, scope *RetrievalScope) (map[string]any, error) {
	rows, err := s.episodeQueryForChatScope(ctx, query, limit, window.NumericStart, window.NumericEnd, request.PersonID, request.ChatID, request.SharedChatIDs, scope)
	if err != nil {
		return nil, err
	}
	hits := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, episodeHit(row))
	}
	hits = s.filterEpisodeHits(hits)
	hits = filterHitsByRetrievalScope(hits, scope)
	return map[string]any{"success": true, "results": hits, "count": len(hits), "query_type": "episode"}, nil
}

func (s *MemorySearchService) episodeQueryForChatScope(ctx context.Context, query string, topK int, timeFrom *float64, timeTo *float64, person string, chatID string, sharedChatIDs []string, scope *RetrievalScope) ([]map[string]any, error) {
	if scope == nil && s.deps.ResolveRetrievalScope != nil {
		var scopeErr error
		scope, scopeErr = s.deps.ResolveRetrievalScope(ctx, chatID, sharedChatIDs)
		if scopeErr != nil {
			return nil, scopeErr
		}
	}
	if s.deps.EpisodeQueryForChatScope == nil {
		return nil, fmt.Errorf("episode 检索器未注入")
	}
	payload, err := s.deps.EpisodeQueryForChatScope(ctx, map[string]any{
		"query": query, "top_k": topK,
		"time_from": timeFrom, "time_to": timeTo,
		"person": person, "source": nil, "scope": scope,
	})
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0)
	for _, item := range asMapList(payload) {
		if item != nil {
			rows = append(rows, item)
		}
	}
	return rows, nil
}

// finalizeSearchResponse 对齐 _finalize_search_response
func (s *MemorySearchService) finalizeSearchResponse(ctx context.Context, hits []map[string]any) (map[string]any, error) {
	relationHashes := make([]string, 0)
	seen := make(map[string]struct{})
	for _, item := range hits {
		if strings.TrimSpace(asString(item["type"])) != "relation" {
			continue
		}
		relationHash := strings.TrimSpace(asString(item["hash"]))
		if relationHash == "" {
			continue
		}
		if _, ok := seen[relationHash]; ok {
			continue
		}
		seen[relationHash] = struct{}{}
		relationHashes = append(relationHashes, relationHash)
	}
	if len(relationHashes) > 0 && s.deps.ReinforceAccess != nil {
		s.deps.ReinforceAccess(ctx, relationHashes)
	}
	return s.withStatus(map[string]any{"summary": summarizeHits(hits), "hits": hits}), nil
}

func (s *MemorySearchService) withStatus(payload map[string]any) map[string]any {
	if s.deps.RuntimeCapabilityStatus == nil {
		payload["degraded"] = false
		payload["retrieval_ready"] = false
		payload["retrieval_mode"] = "sparse"
		payload["available_channels"] = []string{}
		payload["unavailable_channels"] = []string{}
		return payload
	}
	status := s.deps.RuntimeCapabilityStatus()
	payload["degraded"] = pyTruthy(status["degraded"])
	payload["retrieval_ready"] = pyTruthy(status["retrieval_ready"])
	payload["retrieval_mode"] = asString(status["retrieval_mode"])
	payload["available_channels"] = asStringList(status["available_channels"])
	payload["unavailable_channels"] = asStringList(status["unavailable_channels"])
	return payload
}

func (s *MemorySearchService) normalizeTimeWindow(timeStart any, timeEnd any) (SearchTimeWindow, error) {
	if s.deps.NormalizeTimeWindow != nil {
		return s.deps.NormalizeTimeWindow(timeStart, timeEnd)
	}
	return BuildSearchTimeWindow(timeStart, timeEnd)
}

func (s *MemorySearchService) filterHits(hits []map[string]any, personID string) []map[string]any {
	if s.deps.FilterHits != nil {
		return s.deps.FilterHits(hits, personID)
	}
	return filterHitsByPerson(hits, personID)
}

func (s *MemorySearchService) filterUserVisibleHits(hits []map[string]any) []map[string]any {
	if s.deps.FilterUserVisibleHits != nil {
		return s.deps.FilterUserVisibleHits(hits)
	}
	return hits
}

func (s *MemorySearchService) filterEpisodeHits(hits []map[string]any) []map[string]any {
	if s.deps.FilterEpisodeHits != nil {
		return s.deps.FilterEpisodeHits(hits)
	}
	return hits
}

func (s *MemorySearchService) filterHitsByRetrievalTypeScope(hits []map[string]any, streamID string, groupID string, userID string) []map[string]any {
	if s.deps.FilterHitsByRetrievalTypeScope != nil {
		return s.deps.FilterHitsByRetrievalTypeScope(hits, streamID, groupID, userID)
	}
	return hits
}

// filterHitsByPerson 对齐 _filter_hits
func filterHitsByPerson(hits []map[string]any, personID string) []map[string]any {
	if personID == "" {
		return hits
	}
	filtered := make([]map[string]any, 0)
	for _, item := range hits {
		metadata := asMap(item["metadata"])
		if searchContainsString(asStringList(metadata["person_ids"]), personID) {
			filtered = append(filtered, item)
			continue
		}
		if strings.Contains(asString(item["content"]), personID) {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		return hits
	}
	return filtered
}

// filterHitsByRetrievalScope 对齐 _filter_hits_by_retrieval_scope
func filterHitsByRetrievalScope(hits []map[string]any, scope *RetrievalScope) []map[string]any {
	if scope == nil {
		return hits
	}
	filtered := make([]map[string]any, 0, len(hits))
	for _, item := range hits {
		hitType := strings.TrimSpace(asString(item["type"]))
		hitHash := strings.TrimSpace(asString(item["hash"]))
		switch hitType {
		case "paragraph":
			if _, ok := scope.ParagraphIDs[hitHash]; ok {
				filtered = append(filtered, item)
			}
		case "episode":
			if _, ok := scope.EpisodeIDs[strings.TrimSpace(asString(item["episode_id"]))]; ok {
				filtered = append(filtered, item)
			}
		case "relation":
			if _, ok := scope.RelationIDs[hitHash]; ok {
				filtered = append(filtered, item)
				continue
			}
			metadata := asMap(item["metadata"])
			rawHashes, ok := metadata["relation_hashes"]
			if !ok {
				continue
			}
			candidates := asStringList(rawHashes)
			if len(candidates) == 0 {
				continue
			}
			allInScope := true
			for _, hashValue := range candidates {
				if _, ok := scope.RelationIDs[strings.TrimSpace(hashValue)]; !ok {
					allInScope = false
					break
				}
			}
			if allInScope {
				filtered = append(filtered, item)
			}
		}
	}
	return filtered
}

// retrievalResultHit 对齐 _retrieval_result_hit
func retrievalResultHit(item map[string]any) map[string]any {
	return map[string]any{
		"hash":     item["hash"],
		"content":  item["content"],
		"score":    item["score"],
		"type":     item["type"],
		"source":   item["source"],
		"metadata": asMap(item["metadata"]),
	}
}

// episodeHit 对齐 _episode_hit
func episodeHit(row map[string]any) map[string]any {
	return map[string]any{
		"type":       "episode",
		"episode_id": asString(row["episode_id"]),
		"title":      asString(row["title"]),
		"content":    asString(row["summary"]),
		"score":      spaceFloatAny(row["lexical_score"]),
		"source":     "episode",
		"metadata": map[string]any{
			"participants":     asList(row["participants"]),
			"keywords":         asList(row["keywords"]),
			"source":           row["source"],
			"event_time_start": row["event_time_start"],
			"event_time_end":   row["event_time_end"],
		},
	}
}

// summarizeHits 对齐 _summary
func summarizeHits(hits []map[string]any) string {
	if len(hits) == 0 {
		return ""
	}
	lines := make([]string, 0, 5)
	for index, item := range hits {
		if index >= 5 {
			break
		}
		content := strings.ReplaceAll(strings.TrimSpace(asString(item["content"])), "\n", " ")
		if len([]rune(content)) > 120 {
			content = string([]rune(content)[:120]) + "..."
		}
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, content))
	}
	return strings.Join(lines, "\n")
}

func limitHits(hits []map[string]any, limit int) []map[string]any {
	if limit < 0 {
		limit = 0
	}
	if len(hits) > limit {
		return hits[:limit]
	}
	return hits
}

func searchContainsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func stringPtrOrNil(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(value)
	return &trimmed
}

func stringPayloadPtr(value any) *string {
	if value == nil {
		return nil
	}
	text := strings.TrimSpace(asString(value))
	if text == "" {
		return nil
	}
	return &text
}

func requestFromPayload(params map[string]any) SearchMemoryRequest {
	request := SearchMemoryRequest{
		Query:         asString(params["query"]),
		Mode:          asString(params["mode"]),
		Limit:         intOr(params["limit"], 5, 1),
		ChatID:        asString(params["chat_id"]),
		GroupID:       asString(params["group_id"]),
		UserID:        asString(params["user_id"]),
		SharedChatIDs: asStringList(params["shared_chat_ids"]),
		PersonID:      asString(params["person_id"]),
		RespectFilter: pyTruthy(params["respect_filter"]),
		TimeStart:     params["time_start"],
		TimeEnd:       params["time_end"],
	}
	if raw, ok := params["request"].(*SearchMemoryRequest); ok && raw != nil {
		return *raw
	}
	return request
}

func asMapList(value any) []map[string]any {
	items, ok := value.([]map[string]any)
	if ok {
		return items
	}
	rawItems, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(rawItems))
	for _, item := range rawItems {
		if mapped := asMap(item); mapped != nil {
			out = append(out, mapped)
		}
	}
	return out
}

func asStringList(value any) []string {
	items, ok := value.([]string)
	if ok {
		return items
	}
	rawItems, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(rawItems))
	for _, item := range rawItems {
		if text := asString(item); text != "" {
			out = append(out, text)
		}
	}
	return out
}
