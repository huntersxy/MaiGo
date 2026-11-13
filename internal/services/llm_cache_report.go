package services

// LLM 缓存统计报告
// 对齐 MaiBot src/services/llm_cache_stats.py 的落盘面：
//   - usage_YYYYMMDD.jsonl 逐事件追加
//   - report.html 汇总 run/调用点/显著性/低高命中率
//   - sessions.html 追加 session 维度
// 报告是只读快照，不参与请求路径；写失败不影响生成。

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	llmCacheReportFileName        = "report.html"
	llmCacheSessionReportFileName = "sessions.html"
	llmCacheReportInterval        = 300 * time.Second
	llmCacheReportCalls           = 50
	llmCacheReportRunLimit        = 12
	llmCacheReportTimeLayout      = "2006-01-02T15:04:05"
)

// ConfigureLLMCacheStatsReportDir 设置报告目录；空串恢复默认 logs/llm_cache_stats
func ConfigureLLMCacheStatsReportDir(dir string) {
	globalLLMCacheStore.mu.Lock()
	defer globalLLMCacheStore.mu.Unlock()
	globalLLMCacheStore.reportDir = strings.TrimSpace(dir)
}

func llmCacheReportDir() string {
	globalLLMCacheStore.mu.Lock()
	dir := globalLLMCacheStore.reportDir
	globalLLMCacheStore.mu.Unlock()
	if dir != "" {
		return dir
	}
	return filepath.Join("logs", "llm_cache_stats")
}

func appendLLMCacheUsageEvent(event map[string]any) {
	dir := llmCacheReportDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("usage_%s.jsonl", time.Now().Format("20060102")))
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(event); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	_, _ = file.Write(buffer.Bytes())
	_ = file.Close()
}

// readLLMCacheUsageEvents 对齐 _read_usage_events：坏编码行整行跳过，坏 JSON 静默跳过
func readLLMCacheUsageEvents(dir string) []map[string]any {
	paths, err := filepath.Glob(filepath.Join(dir, "usage_*.jsonl"))
	if err != nil {
		return nil
	}
	sort.Strings(paths)
	out := make([]map[string]any, 0)
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			if !utf8.Valid(line) {
				continue
			}
			var event map[string]any
			if json.Unmarshal(line, &event) == nil && event != nil {
				out = append(out, event)
			}
		}
		_ = file.Close()
	}
	return out
}

func llmCacheEventRunID(event map[string]any) string {
	runID := strings.TrimSpace(stringFromCacheAny(event["run_id"]))
	if runID == "" {
		return "legacy"
	}
	return runID
}

func stringFromCacheAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func int64FromCacheAny(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil {
			return parsed
		}
		return 0
	default:
		return 0
	}
}

func float64FromCacheAny(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	default:
		return 0
	}
}

func boolFromCacheAny(value any) bool {
	typed, _ := value.(bool)
	return typed
}

func cacheRate(hit, miss int64) float64 {
	if hit+miss <= 0 {
		return 0
	}
	return float64(hit) / float64(hit+miss) * 100
}

func cacheSampleVariance(valueTotal, squareTotal float64, count int64) float64 {
	if count <= 1 {
		return 0
	}
	variance := (squareTotal - (valueTotal*valueTotal)/float64(count)) / float64(count-1)
	if variance < 0 {
		return 0
	}
	return variance
}

// AggregateLLMCacheUsageByRun 对齐 _aggregate_usage_events_by_run
func AggregateLLMCacheUsageByRun(events []map[string]any) []map[string]any {
	grouped := map[string]map[string]any{}
	order := make([]string, 0)
	for _, event := range events {
		runID := llmCacheEventRunID(event)
		item := grouped[runID]
		if item == nil {
			item = map[string]any{
				"run_id": runID, "process_started_at": stringFromCacheAny(event["process_started_at"]),
				"first_seen_at": stringFromCacheAny(event["created_at"]), "last_seen_at": stringFromCacheAny(event["created_at"]),
				"calls": int64(0), "prompt_tokens": int64(0), "prompt_cache_hit_tokens": int64(0), "prompt_cache_miss_tokens": int64(0),
				"theoretical_prompt_cache_hit_tokens": int64(0), "theoretical_prompt_cache_miss_tokens": int64(0),
				"common_prefix_rate_total": 0.0, "common_prefix_rate_square_total": 0.0, "suspected_context_sliding_calls": int64(0),
			}
			grouped[runID] = item
			order = append(order, runID)
		}
		createdAt := stringFromCacheAny(event["created_at"])
		if createdAt != "" {
			if first, _ := item["first_seen_at"].(string); first == "" || createdAt < first {
				item["first_seen_at"] = createdAt
			}
			if last, _ := item["last_seen_at"].(string); createdAt > last {
				item["last_seen_at"] = createdAt
			}
		}
		item["calls"] = int64FromCacheAny(item["calls"]) + 1
		item["prompt_tokens"] = int64FromCacheAny(item["prompt_tokens"]) + int64FromCacheAny(event["prompt_tokens"])
		item["prompt_cache_hit_tokens"] = int64FromCacheAny(item["prompt_cache_hit_tokens"]) + int64FromCacheAny(event["prompt_cache_hit_tokens"])
		item["prompt_cache_miss_tokens"] = int64FromCacheAny(item["prompt_cache_miss_tokens"]) + int64FromCacheAny(event["prompt_cache_miss_tokens"])
		item["theoretical_prompt_cache_hit_tokens"] = int64FromCacheAny(item["theoretical_prompt_cache_hit_tokens"]) + int64FromCacheAny(event["theoretical_prompt_cache_hit_tokens"])
		item["theoretical_prompt_cache_miss_tokens"] = int64FromCacheAny(item["theoretical_prompt_cache_miss_tokens"]) + int64FromCacheAny(event["theoretical_prompt_cache_miss_tokens"])
		prefixRate := float64FromCacheAny(event["theoretical_common_prefix_rate"])
		item["common_prefix_rate_total"] = float64FromCacheAny(item["common_prefix_rate_total"]) + prefixRate
		// Python 的 run 聚合虽然初始化了 square_total，但未累加，保持 0.0 以对齐
		if boolFromCacheAny(event["suspected_context_sliding"]) {
			item["suspected_context_sliding_calls"] = int64FromCacheAny(item["suspected_context_sliding_calls"]) + 1
		}
	}
	out := make([]map[string]any, 0, len(grouped))
	for _, runID := range order {
		item := grouped[runID]
		calls := int64FromCacheAny(item["calls"])
		item["prompt_cache_hit_rate"] = round2(cacheRate(int64FromCacheAny(item["prompt_cache_hit_tokens"]), int64FromCacheAny(item["prompt_cache_miss_tokens"])))
		item["theoretical_prompt_cache_hit_rate"] = round2(cacheRate(int64FromCacheAny(item["theoretical_prompt_cache_hit_tokens"]), int64FromCacheAny(item["theoretical_prompt_cache_miss_tokens"])))
		if calls > 0 {
			item["avg_common_prefix_rate"] = round2(float64FromCacheAny(item["common_prefix_rate_total"]) / float64(calls))
		} else {
			item["avg_common_prefix_rate"] = 0.0
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return stringFromCacheAny(out[i]["first_seen_at"]) < stringFromCacheAny(out[j]["first_seen_at"])
	})
	return out
}

type llmCacheCallSiteKey struct {
	TaskName    string
	RequestType string
	ModelName   string
	SessionID   string
}

func (k llmCacheCallSiteKey) id() string {
	return strings.Join([]string{k.TaskName, k.RequestType, k.ModelName, k.SessionID}, "\x00")
}

func (k llmCacheCallSiteKey) less(other llmCacheCallSiteKey) bool {
	if k.TaskName != other.TaskName {
		return k.TaskName < other.TaskName
	}
	if k.RequestType != other.RequestType {
		return k.RequestType < other.RequestType
	}
	if k.ModelName != other.ModelName {
		return k.ModelName < other.ModelName
	}
	return k.SessionID < other.SessionID
}

func aggregateLLMCacheCallSiteMap(events []map[string]any, runID string, includeSession bool) map[string]map[string]any {
	grouped := map[string]map[string]any{}
	for _, event := range events {
		if llmCacheEventRunID(event) != runID {
			continue
		}
		key := llmCacheCallSiteKey{
			TaskName:    stringFromCacheAny(event["task_name"]),
			RequestType: stringFromCacheAny(event["request_type"]),
			ModelName:   stringFromCacheAny(event["model_name"]),
		}
		if includeSession {
			key.SessionID = normalizeCacheText(stringFromCacheAny(event["session_id"]), "unknown")
		}
		item := grouped[key.id()]
		if item == nil {
			item = map[string]any{
				"task_name": key.TaskName, "request_type": key.RequestType, "model_name": key.ModelName, "session_id": key.SessionID,
				"calls": int64(0), "prompt_cache_hit_tokens": int64(0), "prompt_cache_miss_tokens": int64(0),
				"theoretical_prompt_cache_hit_tokens": int64(0), "theoretical_prompt_cache_miss_tokens": int64(0),
				"common_prefix_rate_total": 0.0, "common_prefix_rate_square_total": 0.0, "suspected_context_sliding_calls": int64(0),
			}
			grouped[key.id()] = item
		}
		item["calls"] = int64FromCacheAny(item["calls"]) + 1
		item["prompt_cache_hit_tokens"] = int64FromCacheAny(item["prompt_cache_hit_tokens"]) + int64FromCacheAny(event["prompt_cache_hit_tokens"])
		item["prompt_cache_miss_tokens"] = int64FromCacheAny(item["prompt_cache_miss_tokens"]) + int64FromCacheAny(event["prompt_cache_miss_tokens"])
		item["theoretical_prompt_cache_hit_tokens"] = int64FromCacheAny(item["theoretical_prompt_cache_hit_tokens"]) + int64FromCacheAny(event["theoretical_prompt_cache_hit_tokens"])
		item["theoretical_prompt_cache_miss_tokens"] = int64FromCacheAny(item["theoretical_prompt_cache_miss_tokens"]) + int64FromCacheAny(event["theoretical_prompt_cache_miss_tokens"])
		prefixRate := float64FromCacheAny(event["theoretical_common_prefix_rate"])
		item["common_prefix_rate_total"] = float64FromCacheAny(item["common_prefix_rate_total"]) + prefixRate
		item["common_prefix_rate_square_total"] = float64FromCacheAny(item["common_prefix_rate_square_total"]) + prefixRate*prefixRate
		if boolFromCacheAny(event["suspected_context_sliding"]) {
			item["suspected_context_sliding_calls"] = int64FromCacheAny(item["suspected_context_sliding_calls"]) + 1
		}
	}
	for _, item := range grouped {
		calls := int64FromCacheAny(item["calls"])
		prefixTotal := float64FromCacheAny(item["common_prefix_rate_total"])
		prefixSquareTotal := float64FromCacheAny(item["common_prefix_rate_square_total"])
		item["prompt_cache_hit_rate"] = round2(cacheRate(int64FromCacheAny(item["prompt_cache_hit_tokens"]), int64FromCacheAny(item["prompt_cache_miss_tokens"])))
		item["theoretical_prompt_cache_hit_rate"] = round2(cacheRate(int64FromCacheAny(item["theoretical_prompt_cache_hit_tokens"]), int64FromCacheAny(item["theoretical_prompt_cache_miss_tokens"])))
		if calls > 0 {
			item["avg_common_prefix_rate"] = round2(prefixTotal / float64(calls))
		} else {
			item["avg_common_prefix_rate"] = 0.0
		}
		item["common_prefix_rate_variance"] = round4(cacheSampleVariance(prefixTotal, prefixSquareTotal, calls))
	}
	return grouped
}

func sortedLLMCacheCallSiteKeys(grouped map[string]map[string]any) []llmCacheCallSiteKey {
	keys := make([]llmCacheCallSiteKey, 0, len(grouped))
	for _, item := range grouped {
		keys = append(keys, llmCacheCallSiteKey{
			TaskName:    stringFromCacheAny(item["task_name"]),
			RequestType: stringFromCacheAny(item["request_type"]),
			ModelName:   stringFromCacheAny(item["model_name"]),
			SessionID:   stringFromCacheAny(item["session_id"]),
		})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].less(keys[j]) })
	return keys
}

// AggregateLLMCacheUsageByCallSite 对齐 _aggregate_usage_events_by_call_site
func AggregateLLMCacheUsageByCallSite(events []map[string]any, runID string, includeSession bool) []map[string]any {
	grouped := aggregateLLMCacheCallSiteMap(events, runID, includeSession)
	keys := sortedLLMCacheCallSiteKeys(grouped)
	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		out = append(out, grouped[key.id()])
	}
	return out
}

func round4(value float64) float64 { return math.Round(value*10000) / 10000 }

// AggregateLLMCacheStatsSnapshot 对齐 _aggregate_stats_snapshot
func AggregateLLMCacheStatsSnapshot(snapshot []map[string]any, includeSession bool) []map[string]any {
	grouped := map[string]map[string]any{}
	order := make([]string, 0)
	for _, item := range snapshot {
		taskName := stringFromCacheAny(item["task_name"])
		requestType := stringFromCacheAny(item["request_type"])
		modelName := stringFromCacheAny(item["model_name"])
		sessionID := ""
		if includeSession {
			sessionID = stringFromCacheAny(item["session_id"])
		}
		key := strings.Join([]string{taskName, requestType, modelName, sessionID}, "\x00")
		target := grouped[key]
		if target == nil {
			target = map[string]any{
				"task_name": taskName, "request_type": requestType, "model_name": modelName, "session_id": sessionID,
				"calls": int64(0), "cache_reported_calls": int64(0), "prompt_tokens": int64(0), "prompt_cache_hit_tokens": int64(0),
				"prompt_cache_miss_tokens": int64(0), "theoretical_prompt_cache_hit_tokens": int64(0),
				"theoretical_prompt_cache_miss_tokens": int64(0), "theoretical_compared_calls": int64(0),
				"theoretical_cache_pool_hits": int64(0), "common_prefix_rate_weighted_total": 0.0,
				"suspected_context_sliding_calls": int64(0), "sliding_dropped_weighted_total": 0.0,
				"sliding_aligned_weighted_total": 0.0, "top_dynamic_diff_paths": "",
			}
			grouped[key] = target
			order = append(order, key)
		}
		calls := int64FromCacheAny(item["calls"])
		slidingCalls := int64FromCacheAny(item["suspected_context_sliding_calls"])
		target["calls"] = int64FromCacheAny(target["calls"]) + calls
		target["cache_reported_calls"] = int64FromCacheAny(target["cache_reported_calls"]) + int64FromCacheAny(item["cache_reported_calls"])
		target["prompt_tokens"] = int64FromCacheAny(target["prompt_tokens"]) + int64FromCacheAny(item["prompt_tokens"])
		target["prompt_cache_hit_tokens"] = int64FromCacheAny(target["prompt_cache_hit_tokens"]) + int64FromCacheAny(item["prompt_cache_hit_tokens"])
		target["prompt_cache_miss_tokens"] = int64FromCacheAny(target["prompt_cache_miss_tokens"]) + int64FromCacheAny(item["prompt_cache_miss_tokens"])
		target["theoretical_prompt_cache_hit_tokens"] = int64FromCacheAny(target["theoretical_prompt_cache_hit_tokens"]) + int64FromCacheAny(item["theoretical_prompt_cache_hit_tokens"])
		target["theoretical_prompt_cache_miss_tokens"] = int64FromCacheAny(target["theoretical_prompt_cache_miss_tokens"]) + int64FromCacheAny(item["theoretical_prompt_cache_miss_tokens"])
		target["theoretical_compared_calls"] = int64FromCacheAny(target["theoretical_compared_calls"]) + int64FromCacheAny(item["theoretical_compared_calls"])
		target["theoretical_cache_pool_hits"] = int64FromCacheAny(target["theoretical_cache_pool_hits"]) + int64FromCacheAny(item["theoretical_cache_pool_hits"])
		target["common_prefix_rate_weighted_total"] = float64FromCacheAny(target["common_prefix_rate_weighted_total"]) + float64FromCacheAny(item["avg_common_prefix_rate"])*float64(calls)
		target["suspected_context_sliding_calls"] = int64FromCacheAny(target["suspected_context_sliding_calls"]) + slidingCalls
		target["sliding_dropped_weighted_total"] = float64FromCacheAny(target["sliding_dropped_weighted_total"]) + float64FromCacheAny(item["avg_sliding_dropped_messages"])*float64(slidingCalls)
		target["sliding_aligned_weighted_total"] = float64FromCacheAny(target["sliding_aligned_weighted_total"]) + float64FromCacheAny(item["avg_sliding_aligned_messages"])*float64(slidingCalls)
		if includeSession {
			target["top_dynamic_diff_paths"] = stringFromCacheAny(item["top_dynamic_diff_paths"])
		}
	}
	out := make([]map[string]any, 0, len(grouped))
	for _, key := range order {
		item := grouped[key]
		calls := int64FromCacheAny(item["calls"])
		slidingCalls := int64FromCacheAny(item["suspected_context_sliding_calls"])
		item["prompt_cache_hit_rate"] = round2(cacheRate(int64FromCacheAny(item["prompt_cache_hit_tokens"]), int64FromCacheAny(item["prompt_cache_miss_tokens"])))
		item["theoretical_prompt_cache_hit_rate"] = round2(cacheRate(int64FromCacheAny(item["theoretical_prompt_cache_hit_tokens"]), int64FromCacheAny(item["theoretical_prompt_cache_miss_tokens"])))
		item["prompt_cache_hit_rate_delta"] = round2(float64FromCacheAny(item["prompt_cache_hit_rate"]) - float64FromCacheAny(item["theoretical_prompt_cache_hit_rate"]))
		if calls > 0 {
			item["avg_common_prefix_rate"] = round2(float64FromCacheAny(item["common_prefix_rate_weighted_total"]) / float64(calls))
		} else {
			item["avg_common_prefix_rate"] = 0.0
		}
		if slidingCalls > 0 {
			item["avg_sliding_dropped_messages"] = round2(float64FromCacheAny(item["sliding_dropped_weighted_total"]) / float64(slidingCalls))
			item["avg_sliding_aligned_messages"] = round2(float64FromCacheAny(item["sliding_aligned_weighted_total"]) / float64(slidingCalls))
		} else {
			item["avg_sliding_dropped_messages"] = 0.0
			item["avg_sliding_aligned_messages"] = 0.0
		}
		out = append(out, item)
	}
	return out
}

func getPreviousLLMCacheRunID(runStats []map[string]any, currentRunID string) string {
	ids := make([]string, 0, len(runStats))
	for _, item := range runStats {
		ids = append(ids, stringFromCacheAny(item["run_id"]))
	}
	for index, id := range ids {
		if id == currentRunID {
			if index <= 0 {
				return ""
			}
			return ids[index-1]
		}
	}
	return ""
}

func getPreviousLLMCacheRunStats(runStats []map[string]any, currentRunID string) []map[string]any {
	out := make([]map[string]any, 0, len(runStats))
	for _, item := range runStats {
		if stringFromCacheAny(item["run_id"]) != currentRunID {
			out = append(out, item)
		}
	}
	return out
}

func formatLLMCacheInt(value any) string {
	text := strconv.FormatInt(int64FromCacheAny(value), 10)
	sign := ""
	if strings.HasPrefix(text, "-") {
		sign = "-"
		text = text[1:]
	}
	var builder strings.Builder
	builder.WriteString(sign)
	for index, char := range text {
		if index > 0 && (len(text)-index)%3 == 0 {
			builder.WriteByte(',')
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

func formatLLMCacheRate(value any) string {
	return fmt.Sprintf("%.2f%%", float64FromCacheAny(value))
}

func formatLLMCacheRunTimeLabel(runStat map[string]any) string {
	firstSeen := strings.TrimSpace(stringFromCacheAny(runStat["first_seen_at"]))
	lastSeen := strings.TrimSpace(stringFromCacheAny(runStat["last_seen_at"]))
	processStarted := strings.TrimSpace(stringFromCacheAny(runStat["process_started_at"]))
	if firstSeen != "" && lastSeen != "" && firstSeen != lastSeen {
		return firstSeen + " -> " + lastSeen
	}
	if firstSeen != "" {
		return firstSeen
	}
	return processStarted
}

func normalCDF(value float64) float64 {
	return 0.5 * (1 + math.Erf(value/math.Sqrt2))
}

func confidenceFromZScore(zScore float64) float64 {
	pValue := 2 * (1 - normalCDF(math.Abs(zScore)))
	confidence := (1 - pValue) * 100
	if confidence < 0 {
		return 0
	}
	if confidence > 100 {
		return 100
	}
	return confidence
}

func calculateTwoProportionConfidence(currentHit, currentTotal, baselineHit, baselineTotal int64) float64 {
	if currentTotal <= 0 || baselineTotal <= 0 {
		return 0
	}
	currentRate := float64(currentHit) / float64(currentTotal)
	baselineRate := float64(baselineHit) / float64(baselineTotal)
	pooledRate := float64(currentHit+baselineHit) / float64(currentTotal+baselineTotal)
	standardError := math.Sqrt(pooledRate * (1 - pooledRate) * (1/float64(currentTotal) + 1/float64(baselineTotal)))
	if standardError <= 0 {
		return 0
	}
	return confidenceFromZScore((currentRate - baselineRate) / standardError)
}

func calculateMeanDifferenceConfidence(currentMean, currentVariance float64, currentCount int64, baselineMean, baselineVariance float64, baselineCount int64) float64 {
	if currentCount <= 1 || baselineCount <= 1 {
		return 0
	}
	standardError := math.Sqrt(currentVariance/float64(currentCount) + baselineVariance/float64(baselineCount))
	if standardError <= 0 {
		return 0
	}
	return confidenceFromZScore((currentMean - baselineMean) / standardError)
}

func formatSignificanceLabel(confidence float64) string {
	if confidence >= 95 {
		return "显著"
	}
	return "不显著"
}

// writeLLMCacheReports 生成 report.html 与 sessions.html
func writeLLMCacheReports(snapshot []map[string]any) {
	dir := llmCacheReportDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	events := readLLMCacheUsageEvents(dir)
	runStats := AggregateLLMCacheUsageByRun(events)
	overview := renderLLMCacheHTMLReport(snapshot, events, runStats, false)
	sessions := renderLLMCacheHTMLReport(snapshot, events, runStats, true)
	_ = os.WriteFile(filepath.Join(dir, llmCacheReportFileName), []byte(overview), 0o644)
	_ = os.WriteFile(filepath.Join(dir, llmCacheSessionReportFileName), []byte(sessions), 0o644)
}

func maybeWriteLLMCacheReports(snapshot []map[string]any) {
	store := globalLLMCacheStore
	store.mu.Lock()
	now := time.Now()
	due := store.lastReportAt.IsZero() || store.callsSinceReport >= llmCacheReportCalls || now.Sub(store.lastReportAt) >= llmCacheReportInterval
	if due {
		store.lastReportAt = now
		store.callsSinceReport = 0
	}
	store.mu.Unlock()
	if due {
		writeLLMCacheReports(snapshot)
		logLLMCacheStatsSummary(snapshot)
	}
}
