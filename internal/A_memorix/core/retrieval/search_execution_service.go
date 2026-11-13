package retrieval

// 统一检索执行服务，对齐 A_memorix core/utils/search_execution_service.py
// 负责参数归一化、聊天过滤、同请求合并、检索执行与阈值/后处理

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"maibot-go/internal/A_memorix/core/utils"
)

// SearchExecutionRequest 对齐 SearchExecutionRequest 数据类
type SearchExecutionRequest struct {
	Caller        string
	StreamID      *string
	GroupID       *string
	UserID        *string
	QueryType     string
	Query         string
	TopK          *int
	CandidateTopK *int
	TimeFrom      *string
	TimeTo        *string
	Person        *string
	Source        *string
	UseThreshold  *bool
	EnablePPR     *bool
	Scope         *RetrievalScope
}

// NewSearchExecutionRequest 返回与 Python 默认值一致的请求
func NewSearchExecutionRequest(caller string) SearchExecutionRequest {
	useThreshold := true
	enablePPR := true
	return SearchExecutionRequest{
		Caller:       caller,
		QueryType:    "search",
		UseThreshold: &useThreshold,
		EnablePPR:    &enablePPR,
	}
}

func (r SearchExecutionRequest) useThreshold() bool {
	return r.UseThreshold == nil || *r.UseThreshold
}

func (r SearchExecutionRequest) enablePPR() bool {
	return r.EnablePPR == nil || *r.EnablePPR
}

// SearchExecutionResult 对齐 SearchExecutionResult 数据类
type SearchExecutionResult struct {
	Success      bool
	Error        string
	QueryType    string
	Query        string
	TopK         int
	TimeFrom     *string
	TimeTo       *string
	Person       *string
	Source       *string
	Temporal     *TemporalQueryOptions
	Results      []RetrievalResult
	ElapsedMS    float64
	ChatFiltered bool
	DedupHit     bool
}

// Count 返回结果数量
func (r SearchExecutionResult) Count() int { return len(r.Results) }

// SearchThresholdFilter 阈值过滤器最小接口
type SearchThresholdFilter interface {
	Filter(results []RetrievalResult) []RetrievalResult
}

// SearchChatFilter 宿主聊天开关过滤能力
type SearchChatFilter interface {
	IsChatEnabled(streamID, groupID, userID *string) bool
}

// SearchRequestDeduper 宿主同请求合并能力
type SearchRequestDeduper interface {
	ExecuteRequestWithDedup(ctx context.Context, requestKey string, executor func(context.Context) (map[string]any, error)) (bool, map[string]any, error)
}

func searchConfigValue(config map[string]any, key string, defaultValue any) any {
	if config == nil {
		return defaultValue
	}
	var current any = config
	for _, part := range strings.Split(key, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return defaultValue
		}
		value, exists := object[part]
		if !exists {
			return defaultValue
		}
		current = value
	}
	return current
}

func searchConfigInt(config map[string]any, key string, defaultValue int) int {
	value := searchConfigValue(config, key, defaultValue)
	if _, ok := value.(int); ok {
		return utils.PyIntOrZero(value)
	}
	return utils.PyIntOrZero(value)
}

func searchConfigFloat(config map[string]any, key string, defaultValue float64) float64 {
	value := searchConfigValue(config, key, defaultValue)
	if number, err := parseSearchFloat(value); err == nil {
		return number
	}
	return defaultValue
}

func searchConfigBool(config map[string]any, key string, defaultValue bool) bool {
	value := searchConfigValue(config, key, defaultValue)
	if value == nil {
		return defaultValue
	}
	return utils.PyBool(value)
}

func parseSearchFloat(value any) (float64, error) {
	switch typed := value.(type) {
	case nil:
		return 0, fmt.Errorf("invalid float")
	default:
		number := utils.PyFloatOrZero(typed)
		return number, nil
	}
}

func searchSanitize(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func searchStringPtr(value string) *string {
	return &value
}

func searchNormalizeQueryType(raw string) string {
	token := strings.ToLower(strings.TrimSpace(raw))
	if token == "" {
		return "search"
	}
	return token
}

func searchResolveTopK(config map[string]any, queryType string, raw *int) (bool, int, string) {
	temporalDefault := searchConfigInt(config, "retrieval.temporal.default_top_k", 10)
	defaultTopK := 10
	if queryType == "time" || queryType == "hybrid" {
		defaultTopK = temporalDefault
	}
	if raw == nil {
		return true, searchClampTopK(defaultTopK), ""
	}
	value := *raw
	return true, searchClampTopK(value), ""
}

func searchClampTopK(value int) int {
	if value < 1 {
		return 1
	}
	if value > 50 {
		return 50
	}
	return value
}

func searchBuildTemporal(
	config map[string]any,
	queryType string,
	timeFromRaw, timeToRaw *string,
	person, source *string,
) (bool, *TemporalQueryOptions, string) {
	if queryType != "time" && queryType != "hybrid" {
		return true, nil, ""
	}
	if !searchConfigBool(config, "retrieval.temporal.enabled", true) {
		return false, nil, "时序检索已禁用（retrieval.temporal.enabled=false）"
	}
	if searchSanitize(timeFromRaw) == "" && searchSanitize(timeToRaw) == "" {
		return false, nil, "time/hybrid 模式至少需要 time_from 或 time_to"
	}
	tsFrom, tsTo, err := utils.ParseQueryTimeRange(timeFromRaw, timeToRaw)
	if err != nil {
		return false, nil, fmt.Sprintf("时间参数错误: %v", err)
	}
	temporal := &TemporalQueryOptions{
		TimeFrom:             tsFrom,
		TimeTo:               tsTo,
		Person:               searchSanitize(person),
		Source:               searchSanitize(source),
		AllowCreatedFallback: searchConfigBool(config, "retrieval.temporal.allow_created_fallback", true),
		CandidateMultiplier:  searchConfigInt(config, "retrieval.temporal.candidate_multiplier", 8),
		MaxScan:              searchConfigInt(config, "retrieval.temporal.max_scan", 1000),
	}
	return true, temporal, ""
}

func searchScopeIdentity(scope *RetrievalScope) string {
	if scope == nil {
		return ""
	}
	digest := sha1.New()
	groups := []struct {
		label  string
		values map[string]struct{}
	}{
		{"paragraph", scope.ParagraphIDs},
		{"relation", scope.RelationIDs},
		{"entity", scope.EntityIDs},
		{"episode", scope.EpisodeIDs},
	}
	for _, group := range groups {
		digest.Write([]byte(group.label))
		digest.Write([]byte{0})
		keys := make([]string, 0, len(group.values))
		for key := range group.values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			encoded := []byte(key)
			var length [8]byte
			binary.BigEndian.PutUint64(length[:], uint64(len(encoded)))
			digest.Write(length[:])
			digest.Write(encoded)
		}
	}
	return scope.Key + ":" + hex.EncodeToString(digest.Sum(nil))
}

func searchBuildRequestKey(
	request SearchExecutionRequest,
	queryType string,
	topK int,
	temporal *TemporalQueryOptions,
	candidateTopK int,
) (string, error) {
	resolvedCandidate := topK
	if candidateTopK > topK {
		resolvedCandidate = candidateTopK
	}
	var timeFromTS, timeToTS any
	if temporal != nil {
		timeFromTS = temporal.TimeFrom
		timeToTS = temporal.TimeTo
	}
	payload := map[string]any{
		"stream_id":       searchSanitize(request.StreamID),
		"query_type":      queryType,
		"query":           searchSanitize(&request.Query),
		"time_from":       searchSanitize(request.TimeFrom),
		"time_to":         searchSanitize(request.TimeTo),
		"time_from_ts":    timeFromTS,
		"time_to_ts":      timeToTS,
		"person":          searchSanitize(request.Person),
		"source":          searchSanitize(request.Source),
		"scope":           searchScopeIdentity(request.Scope),
		"top_k":           topK,
		"candidate_top_k": resolvedCandidate,
		"use_threshold":   request.useThreshold(),
		"enable_ppr":      request.enablePPR(),
	}
	text, err := utils.PyJSONMarshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha1.Sum([]byte(text))
	return hex.EncodeToString(sum[:]), nil
}

// ExecuteSearch 执行一次 search、time 或 hybrid 检索
func ExecuteSearch(
	ctx context.Context,
	retriever EpisodeEvidenceRetriever,
	thresholdFilter SearchThresholdFilter,
	pluginConfig map[string]any,
	request SearchExecutionRequest,
	enforceChatFilter bool,
) SearchExecutionResult {
	if retriever == nil {
		return SearchExecutionResult{Success: false, Error: "知识检索器未初始化"}
	}
	queryType := searchNormalizeQueryType(request.QueryType)
	query := strings.TrimSpace(request.Query)
	if queryType != "search" && queryType != "time" && queryType != "hybrid" {
		return SearchExecutionResult{Success: false, Error: fmt.Sprintf("query_type 无效: %s（仅支持 search/time/hybrid）", queryType)}
	}
	if (queryType == "search" || queryType == "hybrid") && query == "" {
		return SearchExecutionResult{Success: false, Error: "search/hybrid 模式必须提供 query"}
	}
	if request.TopK == nil {
		// 默认值在下面统一解析
	}
	topKOK, topK, topKError := searchResolveTopK(pluginConfig, queryType, request.TopK)
	if !topKOK {
		return SearchExecutionResult{Success: false, Error: topKError}
	}
	candidateTopK := topK
	if request.CandidateTopK != nil {
		candidateTopK = *request.CandidateTopK
		if candidateTopK < topK {
			candidateTopK = topK
		}
	}
	temporalOK, temporal, temporalError := searchBuildTemporal(
		pluginConfig, queryType, request.TimeFrom, request.TimeTo, request.Person, request.Source,
	)
	if !temporalOK {
		return SearchExecutionResult{Success: false, Error: temporalError}
	}
	pluginInstance := searchResolvePluginInstance(pluginConfig)
	if enforceChatFilter && pluginInstance != nil {
		if chatFilter, ok := pluginInstance.(SearchChatFilter); ok {
			if !chatFilter.IsChatEnabled(request.StreamID, request.GroupID, request.UserID) {
				return SearchExecutionResult{
					Success:      true,
					QueryType:    queryType,
					Query:        query,
					TopK:         topK,
					TimeFrom:     request.TimeFrom,
					TimeTo:       request.TimeTo,
					Person:       request.Person,
					Source:       request.Source,
					Temporal:     temporal,
					Results:      []RetrievalResult{},
					ElapsedMS:    0,
					ChatFiltered: true,
					DedupHit:     false,
				}
			}
		}
	}
	requestKey, err := searchBuildRequestKey(request, queryType, topK, temporal, candidateTopK)
	if err != nil {
		return SearchExecutionResult{Success: false, Error: fmt.Sprintf("知识检索失败: %v", err)}
	}
	executor := func(ctx context.Context) (map[string]any, error) {
		startedAt := time.Now()
		options := &RetrieveOptions{
			TopK:      &candidateTopK,
			Temporal:  temporal,
			EnablePPR: searchBoolPtr(request.enablePPR()),
			Scope:     request.Scope,
		}
		retrieved, retrieveErr := retriever.Retrieve(ctx, query, options)
		if retrieveErr != nil {
			return nil, retrieveErr
		}
		shouldApplyThreshold := request.useThreshold() && thresholdFilter != nil
		if queryType == "time" && query == "" &&
			searchConfigBool(pluginConfig, "retrieval.time.skip_threshold_when_query_empty", true) {
			shouldApplyThreshold = false
		}
		if shouldApplyThreshold {
			retrieved = thresholdFilter.Filter(retrieved)
		}
		if queryType == "search" {
			graphStore := searchResolveRuntimeComponent(pluginConfig, pluginInstance, "graph_store")
			metadataStore := searchResolveRuntimeComponent(pluginConfig, pluginInstance, "metadata_store")
			fallbackEnabled := searchConfigBool(pluginConfig, "retrieval.search.smart_fallback.enabled", true)
			fallbackThreshold := searchConfigFloat(pluginConfig, "retrieval.search.smart_fallback.threshold", 0.6)
			var allowedRelationIDs []string
			if request.Scope != nil {
				for key := range request.Scope.RelationIDs {
					allowedRelationIDs = append(allowedRelationIDs, key)
				}
				sort.Strings(allowedRelationIDs)
			}
			merged, _, _, fallbackErr := utils.MaybeApplySmartPathFallback(
				query,
				toUtilsRetrievalResults(retrieved),
				graphStore,
				metadataStore,
				fallbackEnabled,
				fallbackThreshold,
				allowedRelationIDs,
				3,
				5,
			)
			if fallbackErr != nil {
				return nil, fallbackErr
			}
			retrieved = fromUtilsRetrievalResults(merged)
		}
		if searchConfigBool(pluginConfig, "retrieval.search.safe_content_dedup.enabled", true) {
			deduped, _ := utils.ApplySafeContentDedup(toUtilsRetrievalResults(retrieved))
			retrieved = fromUtilsRetrievalResults(deduped)
		}
		if retrieved == nil {
			retrieved = []RetrievalResult{}
		}
		return map[string]any{
			"results":    retrieved,
			"elapsed_ms": float64(time.Since(startedAt)) / float64(time.Millisecond),
		}, nil
	}
	dedupHit := false
	var payload map[string]any
	bypassDedup := strings.ToLower(strings.TrimSpace(request.Caller)) == "retrieval_tuning"
	if !bypassDedup && pluginInstance != nil {
		if deduper, ok := pluginInstance.(SearchRequestDeduper); ok {
			var dedupErr error
			dedupHit, payload, dedupErr = deduper.ExecuteRequestWithDedup(ctx, requestKey, executor)
			if dedupErr != nil {
				return SearchExecutionResult{Success: false, Error: fmt.Sprintf("知识检索失败: %v", dedupErr)}
			}
		}
	}
	if payload == nil {
		var execErr error
		payload, execErr = executor(ctx)
		if execErr != nil {
			return SearchExecutionResult{Success: false, Error: fmt.Sprintf("知识检索失败: %v", execErr)}
		}
	}
	results, _ := payload["results"].([]RetrievalResult)
	if results == nil {
		results = []RetrievalResult{}
	}
	elapsedMS := utils.PyFloatOrZero(payload["elapsed_ms"])
	return SearchExecutionResult{
		Success:      true,
		QueryType:    queryType,
		Query:        query,
		TopK:         topK,
		TimeFrom:     request.TimeFrom,
		TimeTo:       request.TimeTo,
		Person:       request.Person,
		Source:       request.Source,
		Temporal:     temporal,
		Results:      results,
		ElapsedMS:    elapsedMS,
		ChatFiltered: false,
		DedupHit:     dedupHit,
	}
}

func searchBoolPtr(value bool) *bool { return &value }

func searchResolvePluginInstance(pluginConfig map[string]any) any {
	if pluginConfig == nil {
		return nil
	}
	return pluginConfig["plugin_instance"]
}

func searchResolveRuntimeComponent(pluginConfig map[string]any, pluginInstance any, key string) any {
	if pluginConfig != nil {
		if value, exists := pluginConfig[key]; exists && value != nil {
			return value
		}
	}
	type componentProvider interface {
		RuntimeComponent(key string) any
	}
	if provider, ok := pluginInstance.(componentProvider); ok {
		return provider.RuntimeComponent(key)
	}
	return nil
}

// ToSerializableResults 对齐 to_serializable_results
func ToSerializableResults(results []RetrievalResult) []map[string]any {
	serialized := make([]map[string]any, 0, len(results))
	for _, item := range results {
		metadata := make(map[string]any, len(item.Metadata)+1)
		for key, value := range item.Metadata {
			metadata[key] = value
		}
		if _, exists := metadata["time_meta"]; !exists {
			metadata["time_meta"] = map[string]any{}
		}
		serialized = append(serialized, map[string]any{
			"hash":     item.HashValue,
			"type":     item.ResultType,
			"score":    item.Score,
			"content":  item.Content,
			"metadata": metadata,
		})
	}
	return serialized
}

func toUtilsRetrievalResults(results []RetrievalResult) []utils.RetrievalResult {
	out := make([]utils.RetrievalResult, 0, len(results))
	for _, item := range results {
		out = append(out, utils.RetrievalResult{
			HashValue:  item.HashValue,
			Content:    item.Content,
			Score:      item.Score,
			ResultType: item.ResultType,
			Source:     item.Source,
			Metadata:   item.Metadata,
		})
	}
	return out
}

func fromUtilsRetrievalResults(results []utils.RetrievalResult) []RetrievalResult {
	out := make([]RetrievalResult, 0, len(results))
	for _, item := range results {
		out = append(out, RetrievalResult{
			HashValue:  item.HashValue,
			Content:    item.Content,
			Score:      item.Score,
			ResultType: item.ResultType,
			Source:     item.Source,
			Metadata:   item.Metadata,
		})
	}
	return out
}
