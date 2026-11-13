package native

// 内核服务接口到 services 实现的薄适配器
// KernelSearchRequest / HostIngest* 与 services 本地参数类型逐字段互转，
// 检索执行直接调用 retrieval.ExecuteSearch，不复制检索内核

import (
	"context"
	"fmt"
	"strings"

	"maibot-go/internal/A_memorix/core/retrieval"
	memruntime "maibot-go/internal/A_memorix/core/runtime"
	"maibot-go/internal/A_memorix/core/runtime/services"
)

// searchAdapter 把内核检索接口转给 services.MemorySearchService
type searchAdapter struct {
	inner        *services.MemorySearchService
	pluginConfig func() map[string]any
}

var _ memruntime.SearchService = (*searchAdapter)(nil)

// SearchMemory 对齐 KernelSearchRequest -> SearchMemoryRequest
func (a *searchAdapter) SearchMemory(ctx context.Context, request memruntime.KernelSearchRequest) (map[string]any, error) {
	return a.inner.SearchMemory(ctx, services.SearchMemoryRequest{
		Query:         request.Query,
		Mode:          request.Mode,
		Limit:         request.Limit,
		ChatID:        request.ChatID,
		GroupID:       request.GroupID,
		UserID:        request.UserID,
		SharedChatIDs: request.SharedChatIDs,
		PersonID:      request.PersonID,
		RespectFilter: respectFilterBool(request.RespectFilter),
		TimeStart:     request.TimeStart,
		TimeEnd:       request.TimeEnd,
	})
}

// AggregateSearch 对齐 _aggregate_search
func (a *searchAdapter) AggregateSearch(ctx context.Context, query string, limit int, request memruntime.KernelSearchRequest) (map[string]any, error) {
	payload, err := a.inner.SearchExecutionOnce(ctx, a.aggregateParams("search", query, limit, request, nil, nil))
	if err != nil {
		return nil, err
	}
	return aggregatePayload(payload, "search"), nil
}

// AggregateTime 对齐 _aggregate_time
func (a *searchAdapter) AggregateTime(ctx context.Context, query string, limit int, request memruntime.KernelSearchRequest, window memruntime.NormalizedSearchTimeWindow) (map[string]any, error) {
	payload, err := a.inner.SearchExecutionOnce(ctx, a.aggregateParams("time", query, limit, request, window.QueryStart, window.QueryEnd))
	if err != nil {
		return nil, err
	}
	return aggregatePayload(payload, "time"), nil
}

// AggregateEpisode 对齐 _aggregate_episode，使用数值时间窗查询 episode
func (a *searchAdapter) AggregateEpisode(ctx context.Context, query string, limit int, request memruntime.KernelSearchRequest, window memruntime.NormalizedSearchTimeWindow) (map[string]any, error) {
	payload, err := a.inner.EpisodeQueryForChatScope(ctx, map[string]any{
		"query": query, "top_k": limit,
		"time_from": window.NumericStart, "time_to": window.NumericEnd,
		"person": request.PersonID, "chat_id": request.ChatID,
		"shared_chat_ids": request.SharedChatIDs,
		"scope":           (*services.RetrievalScope)(nil),
	})
	if err != nil {
		return nil, err
	}
	rows := anyRows(payload)
	return map[string]any{
		"success": true, "results": rows, "count": len(rows), "query_type": "episode",
	}, nil
}

// SearchExecutionOnce 对齐同名内核入口
func (a *searchAdapter) SearchExecutionOnce(ctx context.Context, params map[string]any) (map[string]any, error) {
	return a.inner.SearchExecutionOnce(ctx, a.withPluginConfig(params))
}

// SearchExecutionForChatScope 对齐同名内核入口
func (a *searchAdapter) SearchExecutionForChatScope(ctx context.Context, params map[string]any) (map[string]any, error) {
	return a.inner.SearchExecutionForChatScope(ctx, a.withPluginConfig(params))
}

// EpisodeQueryForChatScope 对齐同名内核入口
func (a *searchAdapter) EpisodeQueryForChatScope(ctx context.Context, params map[string]any) (any, error) {
	return a.inner.EpisodeQueryForChatScope(ctx, params)
}

// aggregateParams 组装聚合检索入口参数
func (a *searchAdapter) aggregateParams(queryType, query string, limit int, request memruntime.KernelSearchRequest, timeFrom, timeTo *string) map[string]any {
	params := map[string]any{
		"caller": "sdk_memory_kernel.aggregate", "query_type": queryType,
		"query": query, "top_k": limit,
		"chat_id": request.ChatID, "group_id": request.GroupID, "user_id": request.UserID,
		"shared_chat_ids": request.SharedChatIDs, "person_id": request.PersonID,
		"enforce_chat_filter": false,
		"scope":               (*services.RetrievalScope)(nil),
	}
	if timeFrom != nil {
		params["time_from"] = *timeFrom
	}
	if timeTo != nil {
		params["time_to"] = *timeTo
	}
	return a.withPluginConfig(params)
}

// withPluginConfig 注入检索运行时配置，已在参数中时保持不变
func (a *searchAdapter) withPluginConfig(params map[string]any) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	if _, exists := params["plugin_config"]; exists || a.pluginConfig == nil {
		return params
	}
	if config := a.pluginConfig(); config != nil {
		params["plugin_config"] = config
	}
	return params
}

// aggregatePayload 把执行结果包装为聚合检索响应
func aggregatePayload(payload map[string]any, queryType string) map[string]any {
	rows := anyRows(payload["results"])
	result := map[string]any{
		"success":    memruntime.PyTruthy(payload["success"]),
		"results":    rows,
		"count":      len(rows),
		"query_type": queryType,
	}
	if text := strings.TrimSpace(memruntime.PyStr(payload["error"])); text != "" {
		result["error"] = text
	}
	return result
}

// anyRows 把 []map[string]any 或 []any 统一为行切片
func anyRows(value any) []map[string]any {
	switch rows := value.(type) {
	case []map[string]any:
		return rows
	case []any:
		out := make([]map[string]any, 0, len(rows))
		for _, item := range rows {
			if mapped, ok := item.(map[string]any); ok {
				out = append(out, mapped)
			}
		}
		return out
	default:
		return []map[string]any{}
	}
}

// ingestAdapter 把内核写入接口转给 services.IngestService
type ingestAdapter struct {
	inner *services.IngestService
}

var _ memruntime.IngestService = (*ingestAdapter)(nil)

// IngestSummary 对齐 IngestSummaryParams
func (a *ingestAdapter) IngestSummary(ctx context.Context, params memruntime.IngestSummaryParams) (map[string]any, error) {
	return a.inner.IngestSummary(ctx, services.IngestSummaryParams{
		ExternalID: params.ExternalID, ChatID: params.ChatID, Text: params.Text,
		Participants: params.Participants, TimeStart: params.TimeStart, TimeEnd: params.TimeEnd,
		Tags: params.Tags, Metadata: params.Metadata, RespectFilter: respectFilterBool(params.RespectFilter),
		UserID: params.UserID, GroupID: params.GroupID,
	})
}

// IngestText 对齐 IngestTextParams
func (a *ingestAdapter) IngestText(ctx context.Context, params memruntime.IngestTextParams) (map[string]any, error) {
	return a.inner.IngestText(ctx, services.IngestTextParams{
		ExternalID: params.ExternalID, SourceType: params.SourceType, Text: params.Text,
		ChatID: params.ChatID, PersonIDs: params.PersonIDs, Participants: params.Participants,
		Timestamp: params.Timestamp, TimeStart: params.TimeStart, TimeEnd: params.TimeEnd,
		Tags: params.Tags, Metadata: params.Metadata, Entities: params.Entities,
		Relations: params.Relations, RespectFilter: respectFilterBool(params.RespectFilter),
		UserID: params.UserID, GroupID: params.GroupID,
	})
}

// ProcessEpisodeSourceRebuildBatch 对齐同名内核入口
func (a *ingestAdapter) ProcessEpisodeSourceRebuildBatch(ctx context.Context, params map[string]any) (map[string]any, error) {
	return a.inner.ProcessEpisodeSourceRebuildBatch(ctx, params)
}

// WriteParagraphVectorOrEnqueue 对齐同名内核入口
func (a *ingestAdapter) WriteParagraphVectorOrEnqueue(ctx context.Context, paragraphHash string, content string, contextText string) (map[string]any, error) {
	return a.inner.WriteParagraphVectorOrEnqueue(ctx, paragraphHash, content, contextText)
}

// EnsureVectorForText 对齐同名内核入口
func (a *ingestAdapter) EnsureVectorForText(ctx context.Context, params map[string]any) (bool, error) {
	return a.inner.EnsureVectorForText(ctx, params)
}

// EnsureRelationVector 对齐同名内核入口
func (a *ingestAdapter) EnsureRelationVector(ctx context.Context, payload map[string]any) (bool, error) {
	return a.inner.EnsureRelationVector(ctx, payload)
}

// EnsureParagraphVector 对齐同名内核入口
func (a *ingestAdapter) EnsureParagraphVector(ctx context.Context, payload map[string]any) (bool, error) {
	return a.inner.EnsureParagraphVector(ctx, payload)
}

// EnsureEntityVector 对齐同名内核入口
func (a *ingestAdapter) EnsureEntityVector(ctx context.Context, payload map[string]any) (bool, error) {
	return a.inner.EnsureEntityVector(ctx, payload)
}

// RestoreRelationHashes 对齐同名内核入口
func (a *ingestAdapter) RestoreRelationHashes(ctx context.Context, payload map[string]any) (map[string]any, error) {
	return a.inner.RestoreRelationHashes(ctx, payload)
}

// toRetrievalScope 转换检索范围，nil 安全
func toRetrievalScope(scope *services.RetrievalScope) *retrieval.RetrievalScope {
	if scope == nil {
		return nil
	}
	convert := func(source map[string]struct{}) map[string]struct{} {
		if source == nil {
			return nil
		}
		out := make(map[string]struct{}, len(source))
		for key := range source {
			out[key] = struct{}{}
		}
		return out
	}
	return &retrieval.RetrievalScope{
		Key:          scope.Key,
		ParagraphIDs: convert(scope.ParagraphIDs),
		RelationIDs:  convert(scope.RelationIDs),
		EntityIDs:    convert(scope.EntityIDs),
		EpisodeIDs:   convert(scope.EpisodeIDs),
	}
}

// toRetrievalRequest 转换检索执行请求
func toRetrievalRequest(request services.SearchExecutionRequest) retrieval.SearchExecutionRequest {
	topK := request.TopK
	candidateTopK := request.CandidateTopK
	useThreshold := request.UseThreshold
	enablePPR := request.EnablePPR
	return retrieval.SearchExecutionRequest{
		Caller: request.Caller, StreamID: request.StreamID, GroupID: request.GroupID,
		UserID: request.UserID, QueryType: request.QueryType, Query: request.Query,
		TopK: &topK, CandidateTopK: &candidateTopK,
		TimeFrom: request.TimeFrom, TimeTo: request.TimeTo,
		Person: request.Person, UseThreshold: &useThreshold, EnablePPR: &enablePPR,
		Scope: toRetrievalScope(request.Scope),
	}
}

// toServicesSearchResult 转换一次检索结果
func toServicesSearchResult(result retrieval.SearchExecutionResult) services.SearchExecutionResult {
	rows := make([]map[string]any, 0, len(result.Results))
	for _, item := range result.Results {
		rows = append(rows, item.ToDict())
	}
	return services.SearchExecutionResult{
		Success: result.Success, Error: result.Error, Results: rows,
		ChatFiltered: result.ChatFiltered,
	}
}

// describeSearchError 生成检索不可用时的真实错误
func describeSearchError(message string) error {
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("检索执行失败")
	}
	return fmt.Errorf("%s", message)
}

// respectFilterBool 内核参数指针归一为 bool，未设置按 Python 默认 true
func respectFilterBool(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}
