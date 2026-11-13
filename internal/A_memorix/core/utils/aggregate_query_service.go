// Package utils 的聚合查询服务，对齐 A_memorix/core/utils/aggregate_query_service.py
//
// 职责：并发执行 search/time/episode 三个分支，统一分支结果结构，
// 并在 mix 时按 Weighted RRF 融合排序。分支执行器由调用方注入，
// 因此本文件不依赖具体检索实现。
package utils

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// AggregateBranchRunner 对应 Python BranchRunner，返回该分支的原始 payload
type AggregateBranchRunner func(ctx context.Context) (map[string]any, error)

// AggregateConfigSource 对应带 get_config 方法的插件配置对象
type AggregateConfigSource interface {
	GetConfig(key string, defaultValue any) any
}

// AggregateQueryService 聚合查询执行服务
type AggregateQueryService struct {
	config any
}

// NewAggregateQueryService 构造服务；config 可为 nil、map[string]any 或 AggregateConfigSource
func NewAggregateQueryService(config any) *AggregateQueryService {
	return &AggregateQueryService{config: config}
}

func (s *AggregateQueryService) cfg(key string, defaultValue any) any {
	if s == nil || s.config == nil {
		return defaultValue
	}
	if source, ok := s.config.(AggregateConfigSource); ok {
		return source.GetConfig(key, defaultValue)
	}
	current := s.config
	for _, part := range strings.Split(key, ".") {
		mapping, ok := current.(map[string]any)
		if !ok {
			return defaultValue
		}
		next, exists := mapping[part]
		if !exists {
			return defaultValue
		}
		current = next
	}
	return current
}

func aggregateAsFloat(value any, defaultValue float64) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int8:
		return float64(v)
	case int16:
		return float64(v)
	case int32:
		return float64(v)
	case int64:
		return float64(v)
	case uint:
		return float64(v)
	case uint8:
		return float64(v)
	case uint16:
		return float64(v)
	case uint32:
		return float64(v)
	case uint64:
		return float64(v)
	case bool:
		if v {
			return 1
		}
		return 0
	case string:
		var parsed float64
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%g", &parsed); err == nil {
			return parsed
		}
	}
	return defaultValue
}

func aggregateAsInt(value any, defaultValue int) int {
	switch v := value.(type) {
	case int:
		return v
	case int8:
		return int(v)
	case int16:
		return int(v)
	case int32:
		return int(v)
	case int64:
		return int(v)
	case uint:
		return int(v)
	case uint8:
		return int(v)
	case uint16:
		return int(v)
	case uint32:
		return int(v)
	case uint64:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	case bool:
		if v {
			return 1
		}
		return 0
	case string:
		var parsed int
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &parsed); err == nil {
			return parsed
		}
	}
	return defaultValue
}

func (s *AggregateQueryService) rrfK() float64 {
	value := aggregateAsFloat(s.cfg("retrieval.aggregate.rrf_k", 60.0), 60.0)
	if value < 1.0 {
		return 1.0
	}
	return value
}

func (s *AggregateQueryService) weights() map[string]float64 {
	weights := map[string]float64{"search": 1.0, "time": 1.0, "episode": 1.0}
	raw, ok := s.cfg("retrieval.aggregate.weights", map[string]any{}).(map[string]any)
	if !ok {
		return weights
	}
	for _, key := range []string{"search", "time", "episode"} {
		if value, exists := raw[key]; exists {
			weight := aggregateAsFloat(value, weights[key])
			if weight < 0 {
				weight = 0
			}
			weights[key] = weight
		}
	}
	return weights
}

// aggregateFalsy 对齐 Python `value or ""` 的常见标量真值
func aggregateFalsy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case bool:
		return !typed
	case string:
		return typed == ""
	case int:
		return typed == 0
	case int64:
		return typed == 0
	case float32:
		return typed == 0
	case float64:
		return typed == 0
	default:
		return false
	}
}

func aggregateString(value any) string {
	if aggregateFalsy(value) {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func aggregateRawString(value any) string {
	if aggregateFalsy(value) {
		return ""
	}
	return fmt.Sprint(value)
}

// NormalizeAggregateBranchPayload 对齐 _normalize_branch_payload
func NormalizeAggregateBranchPayload(name string, payload map[string]any) map[string]any {
	if payload == nil {
		payload = map[string]any{}
	}
	results := aggregateResultMaps(payload["results"])
	if results == nil {
		results = []map[string]any{}
	}
	countValue, exists := payload["count"]
	if !exists || countValue == nil {
		countValue = len(results)
	}
	count := aggregateAsInt(countValue, 0)
	if count < 0 {
		count = 0
	}
	elapsed := aggregateAsFloat(payload["elapsed_ms"], 0.0)
	if elapsed < 0 {
		elapsed = 0
	}
	queryType := aggregateString(payload["query_type"])
	if queryType == "" {
		queryType = name
	}
	return map[string]any{
		"name":        name,
		"success":     aggregateBool(payload["success"]),
		"skipped":     aggregateBool(payload["skipped"]),
		"skip_reason": aggregateString(payload["skip_reason"]),
		"error":       aggregateString(payload["error"]),
		"results":     results,
		"count":       count,
		"elapsed_ms":  elapsed,
		"content":     aggregateRawString(payload["content"]),
		"query_type":  queryType,
	}
}

func aggregateBool(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case nil:
		return false
	case string:
		return v != ""
	case int:
		return v != 0
	case float64:
		return v != 0
	}
	return false
}

func aggregateMixKey(item map[string]any, branch string, rank int) string {
	itemType := strings.ToLower(aggregateString(item["type"]))
	if itemType == "episode" {
		if episodeID := aggregateString(item["episode_id"]); episodeID != "" {
			return "episode:" + episodeID
		}
	}
	if itemHash := aggregateString(item["hash"]); itemHash != "" {
		return itemType + ":" + itemHash
	}
	// 对齐 Python str(...)[:80]：不 strip，按字符截断
	content := aggregateRawString(item["content"])
	if runes := []rune(content); len(runes) > 80 {
		content = string(runes[:80])
	}
	return fmt.Sprintf("%s:%s:%d:%s", branch, itemType, rank, content)
}

// aggregateBranchOrder 对齐 Python dict 插入序：标准分支 search/time/episode 优先，其余按名称稳定排列
func aggregateBranchOrder(branches map[string]map[string]any) []string {
	names := make([]string, 0, len(branches))
	seen := make(map[string]struct{}, len(branches))
	for _, name := range []string{"search", "time", "episode"} {
		if _, ok := branches[name]; ok {
			names = append(names, name)
			seen[name] = struct{}{}
		}
	}
	rest := make([]string, 0, len(branches)-len(names))
	for name := range branches {
		if _, ok := seen[name]; ok {
			continue
		}
		rest = append(rest, name)
	}
	sort.Strings(rest)
	return append(names, rest...)
}

// BuildAggregateMixedResults 对齐 _build_mixed_results
func (s *AggregateQueryService) BuildAggregateMixedResults(branches map[string]map[string]any, topK int) []map[string]any {
	rrfK := s.rrfK()
	weights := s.weights()
	type mixedEntry struct {
		key  string
		item map[string]any
	}
	entries := map[string]*mixedEntry{}
	var entryOrder []string
	sourceBranches := map[string]map[string]struct{}{}

	names := aggregateBranchOrder(branches)
	for _, branchName := range names {
		branch := branches[branchName]
		if !aggregateBool(branch["success"]) {
			continue
		}
		results := aggregateResultMaps(branch["results"])
		weight := weights[branchName]
		if weight < 0 {
			weight = 0
		}
		for index, item := range results {
			key := aggregateMixKey(item, branchName, index+1)
			score := weight / (rrfK + float64(index+1))
			entry, exists := entries[key]
			if !exists {
				merged := map[string]any{}
				for k, v := range item {
					merged[k] = v
				}
				merged["fusion_score"] = 0.0
				entry = &mixedEntry{key: key, item: merged}
				entries[key] = entry
				entryOrder = append(entryOrder, key)
				sourceBranches[key] = map[string]struct{}{}
			}
			entry.item["fusion_score"] = aggregateAsFloat(entry.item["fusion_score"], 0) + score
			sourceBranches[key][branchName] = struct{}{}
		}
	}

	mixed := make([]*mixedEntry, 0, len(entryOrder))
	for _, key := range entryOrder {
		if entry := entries[key]; entry != nil {
			mixed = append(mixed, entry)
		}
	}
	sort.SliceStable(mixed, func(i, j int) bool {
		leftScore := aggregateAsFloat(mixed[i].item["fusion_score"], 0)
		rightScore := aggregateAsFloat(mixed[j].item["fusion_score"], 0)
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		leftType := aggregateString(mixed[i].item["type"])
		rightType := aggregateString(mixed[j].item["type"])
		if leftType != rightType {
			return leftType < rightType
		}
		leftKey := aggregateString(mixed[i].item["hash"])
		if leftKey == "" {
			leftKey = aggregateString(mixed[i].item["episode_id"])
		}
		rightKey := aggregateString(mixed[j].item["hash"])
		if rightKey == "" {
			rightKey = aggregateString(mixed[j].item["episode_id"])
		}
		return leftKey < rightKey
	})

	limit := topK
	if limit < 1 {
		limit = 1
	}
	if limit > len(mixed) {
		limit = len(mixed)
	}
	out := make([]map[string]any, 0, limit)
	for index, entry := range mixed[:limit] {
		merged := map[string]any{}
		for k, v := range entry.item {
			merged[k] = v
		}
		sources := make([]string, 0, len(sourceBranches[entry.key]))
		for source := range sourceBranches[entry.key] {
			sources = append(sources, source)
		}
		sort.Strings(sources)
		merged["source_branches"] = sources
		merged["rank"] = index + 1
		out = append(out, merged)
	}
	return out
}

func aggregateResultMaps(value any) []map[string]any {
	switch results := value.(type) {
	case []map[string]any:
		return results
	case []any:
		out := make([]map[string]any, 0, len(results))
		for _, item := range results {
			if mapping, ok := item.(map[string]any); ok {
				out = append(out, mapping)
			}
		}
		return out
	}
	return nil
}

// AggregateStatus 对齐 _status
func AggregateStatus(branch map[string]any) string {
	if aggregateBool(branch["skipped"]) {
		return "skipped"
	}
	if aggregateBool(branch["success"]) {
		return "success"
	}
	return "failed"
}

// BuildAggregateSummary 对齐 _build_summary
func (s *AggregateQueryService) BuildAggregateSummary(branches map[string]map[string]any) map[string]map[string]any {
	summary := map[string]map[string]any{}
	for name, branch := range branches {
		status := AggregateStatus(branch)
		entry := map[string]any{
			"status": status,
			"count":  aggregateAsInt(branch["count"], 0),
		}
		if status == "skipped" {
			entry["reason"] = aggregateString(branch["skip_reason"])
		}
		if status == "failed" {
			entry["error"] = aggregateString(branch["error"])
		}
		summary[name] = entry
	}
	return summary
}

// ExecuteAggregateQuery 对齐 execute
func (s *AggregateQueryService) ExecuteAggregateQuery(
	ctx context.Context,
	query string,
	topK int,
	mix bool,
	mixTopK *int,
	timeFrom string,
	timeTo string,
	searchRunner AggregateBranchRunner,
	timeRunner AggregateBranchRunner,
	episodeRunner AggregateBranchRunner,
) (map[string]any, error) {
	cleanQuery := strings.TrimSpace(query)
	safeTopK := topK
	if safeTopK < 1 {
		safeTopK = 1
	}
	safeMixTopK := safeTopK
	if mixTopK != nil {
		safeMixTopK = *mixTopK
		if safeMixTopK < 1 {
			safeMixTopK = 1
		}
	}

	branches := map[string]map[string]any{}
	errorsList := []map[string]any{}
	type scheduledBranch struct {
		name   string
		runner AggregateBranchRunner
	}
	scheduled := []scheduledBranch{}

	if cleanQuery != "" {
		if searchRunner != nil {
			scheduled = append(scheduled, scheduledBranch{"search", searchRunner})
		} else {
			branches["search"] = NormalizeAggregateBranchPayload("search", map[string]any{
				"success": false, "error": "search runner unavailable", "results": []map[string]any{},
			})
		}
	} else {
		branches["search"] = NormalizeAggregateBranchPayload("search", map[string]any{
			"success": false, "skipped": true, "skip_reason": "missing_query",
			"results": []map[string]any{}, "count": 0,
		})
	}

	if timeFrom != "" || timeTo != "" {
		if timeRunner != nil {
			scheduled = append(scheduled, scheduledBranch{"time", timeRunner})
		} else {
			branches["time"] = NormalizeAggregateBranchPayload("time", map[string]any{
				"success": false, "error": "time runner unavailable", "results": []map[string]any{},
			})
		}
	} else {
		branches["time"] = NormalizeAggregateBranchPayload("time", map[string]any{
			"success": false, "skipped": true, "skip_reason": "missing_time_window",
			"results": []map[string]any{}, "count": 0,
		})
	}

	if episodeRunner != nil {
		scheduled = append(scheduled, scheduledBranch{"episode", episodeRunner})
	} else {
		branches["episode"] = NormalizeAggregateBranchPayload("episode", map[string]any{
			"success": false, "error": "episode runner unavailable", "results": []map[string]any{},
		})
	}

	if len(scheduled) > 0 {
		type branchResult struct {
			name    string
			payload map[string]any
			err     error
		}
		results := make(chan branchResult, len(scheduled))
		for _, item := range scheduled {
			go func(item scheduledBranch) {
				payload, err := item.runner(ctx)
				results <- branchResult{name: item.name, payload: payload, err: err}
			}(item)
		}
		for range scheduled {
			result := <-results
			if result.err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, ctxErr
				}
				branches[result.name] = NormalizeAggregateBranchPayload(result.name, map[string]any{
					"success": false, "error": result.err.Error(), "results": []map[string]any{},
				})
			} else {
				branches[result.name] = NormalizeAggregateBranchPayload(result.name, result.payload)
			}
		}
	}

	for _, name := range []string{"search", "time", "episode"} {
		branch, exists := branches[name]
		if !exists {
			continue
		}
		if aggregateBool(branch["skipped"]) {
			continue
		}
		if !aggregateBool(branch["success"]) {
			errText := aggregateString(branch["error"])
			if errText == "" {
				errText = "unknown error"
			}
			errorsList = append(errorsList, map[string]any{"branch": name, "error": errText})
		}
	}

	success := false
	for _, name := range []string{"search", "time", "episode"} {
		if branch, exists := branches[name]; exists && aggregateBool(branch["success"]) {
			success = true
		}
	}
	var mixedResults []map[string]any
	if mix {
		mixedResults = s.BuildAggregateMixedResults(branches, safeMixTopK)
	}

	payload := map[string]any{
		"success":    success,
		"query_type": "aggregate",
		"query":      cleanQuery,
		"top_k":      safeTopK,
		"mix":        mix,
		"mix_top_k":  safeMixTopK,
		"branches":   branches,
		"errors":     errorsList,
		"summary":    s.BuildAggregateSummary(branches),
	}
	if mixedResults != nil {
		payload["mixed_results"] = mixedResults
	}
	payload["content"] = s.BuildAggregateContent(cleanQuery, branches, errorsList, mixedResults)
	return payload, nil
}
