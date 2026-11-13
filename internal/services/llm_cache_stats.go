package services

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	llmCacheFocusedTask    = "replyer"
	llmCacheFocusedPlanner = "planner"
	llmCachePromptPoolSize = 128
	llmCacheSnippetLimit   = 160
	llmCacheSummaryLimit   = 5
)

var llmCacheExcludedRequestTypes = map[string]struct{}{
	"A_Memorix.ChatSummarization": {},
	"expression.learner":          {},
	"jargon.learner":              {},
	"reply.effect_judge":          {},
}

// LLMCacheStat 对齐 Python LLMCacheStat 的聚合字段
type LLMCacheStat struct {
	TaskName                         string         `json:"task_name"`
	RequestType                      string         `json:"request_type"`
	ModelName                        string         `json:"model_name"`
	SessionID                        string         `json:"session_id"`
	Calls                            int            `json:"calls"`
	CacheReportedCalls               int            `json:"cache_reported_calls"`
	PromptTokens                     int64          `json:"prompt_tokens"`
	PromptCacheHitTokens             int64          `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens            int64          `json:"prompt_cache_miss_tokens"`
	TheoreticalPromptCacheHitTokens  int64          `json:"theoretical_prompt_cache_hit_tokens"`
	TheoreticalPromptCacheMissTokens int64          `json:"theoretical_prompt_cache_miss_tokens"`
	TheoreticalComparedCalls         int            `json:"theoretical_compared_calls"`
	TheoreticalCachePoolHits         int            `json:"theoretical_cache_pool_hits"`
	CommonPrefixRateTotal            float64        `json:"-"`
	SuspectedContextSlidingCalls     int            `json:"suspected_context_sliding_calls"`
	SlidingDroppedMessagesTotal      int            `json:"sliding_dropped_messages_total"`
	SlidingAlignedMessagesTotal      int            `json:"sliding_aligned_messages_total"`
	DynamicDiffCounts                map[string]int `json:"dynamic_diff_counts"`
}

func (s *LLMCacheStat) promptCacheTotalTokens() int64 {
	return s.PromptCacheHitTokens + s.PromptCacheMissTokens
}

func (s *LLMCacheStat) promptCacheHitRate() float64 {
	total := s.promptCacheTotalTokens()
	if total <= 0 {
		return 0
	}
	return float64(s.PromptCacheHitTokens) / float64(total) * 100
}

func (s *LLMCacheStat) theoreticalTotalTokens() int64 {
	return s.TheoreticalPromptCacheHitTokens + s.TheoreticalPromptCacheMissTokens
}

func (s *LLMCacheStat) theoreticalHitRate() float64 {
	total := s.theoreticalTotalTokens()
	if total <= 0 {
		return 0
	}
	return float64(s.TheoreticalPromptCacheHitTokens) / float64(total) * 100
}

func round2(value float64) float64 { return math.Round(value*100) / 100 }

func (s *LLMCacheStat) topDynamicDiffPaths() string {
	if len(s.DynamicDiffCounts) == 0 {
		return ""
	}
	type pair struct {
		Path  string
		Count int
	}
	items := make([]pair, 0, len(s.DynamicDiffCounts))
	for path, count := range s.DynamicDiffCounts {
		items = append(items, pair{Path: path, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].Path < items[j].Path
	})
	if len(items) > llmCacheSummaryLimit {
		items = items[:llmCacheSummaryLimit]
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, item.Path+" ("+strconv.Itoa(item.Count)+")")
	}
	return strings.Join(parts, "; ")
}

// ToMap 对齐 Python LLMCacheStat.to_dict
func (s *LLMCacheStat) ToMap() map[string]any {
	avgCommonPrefix := 0.0
	if s.Calls > 0 {
		avgCommonPrefix = s.CommonPrefixRateTotal / float64(s.Calls)
	}
	droppedAvg := 0.0
	alignedAvg := 0.0
	if s.SuspectedContextSlidingCalls > 0 {
		droppedAvg = float64(s.SlidingDroppedMessagesTotal) / float64(s.SuspectedContextSlidingCalls)
		alignedAvg = float64(s.SlidingAlignedMessagesTotal) / float64(s.SuspectedContextSlidingCalls)
	}
	return map[string]any{
		"task_name": s.TaskName, "request_type": s.RequestType, "model_name": s.ModelName, "session_id": s.SessionID,
		"calls": s.Calls, "cache_reported_calls": s.CacheReportedCalls, "prompt_tokens": s.PromptTokens,
		"prompt_cache_hit_tokens": s.PromptCacheHitTokens, "prompt_cache_miss_tokens": s.PromptCacheMissTokens,
		"prompt_cache_hit_rate":                round2(s.promptCacheHitRate()),
		"theoretical_prompt_cache_hit_tokens":  s.TheoreticalPromptCacheHitTokens,
		"theoretical_prompt_cache_miss_tokens": s.TheoreticalPromptCacheMissTokens,
		"theoretical_compared_calls":           s.TheoreticalComparedCalls,
		"theoretical_cache_pool_hits":          s.TheoreticalCachePoolHits,
		"theoretical_prompt_cache_hit_rate":    round2(s.theoreticalHitRate()),
		"prompt_cache_hit_rate_delta":          round2(s.promptCacheHitRate() - s.theoreticalHitRate()),
		"avg_common_prefix_rate":               round2(avgCommonPrefix),
		"suspected_context_sliding_calls":      s.SuspectedContextSlidingCalls,
		"avg_sliding_dropped_messages":         round2(droppedAvg),
		"avg_sliding_aligned_messages":         round2(alignedAvg),
		"top_dynamic_diff_paths":               s.topDynamicDiffPaths(),
	}
}

type llmCacheKey struct {
	TaskName, RequestType, ModelName, SessionID string
}

type theoreticalMatch struct {
	HitTokens         int64
	MissTokens        int64
	HitRate           float64
	Compared          bool
	PoolSize          int
	BestMatchRank     int
	BestPromptText    string
	CommonPrefixChars int
}

type dynamicDiff struct {
	Path, Previous, Current string
}

type promptDiagnostics struct {
	CommonPrefixRate           float64
	CurrentMessageCount        int
	BestMatchMessageCount      int
	CommonPrefixMessages       int
	CommonSuffixMessages       int
	PromptGrowthChars          int
	LongestAlignedOverlap      int
	AlignedPreviousStartIndex  int
	AlignedCurrentStartIndex   int
	SuspectedContextSliding    bool
	SlidingDroppedHeadMessages int
	SlidingAlignedMessages     int
	SlidingNewTailMessages     int
	CurrentFirstRole           string
	BestFirstRole              string
	CurrentLastRole            string
	BestLastRole               string
}

type llmCacheStore struct {
	mu               sync.Mutex
	stats            map[llmCacheKey]*LLMCacheStat
	promptPools      map[llmCacheKey][]string
	enabled          func() bool
	eventSink        func(map[string]any)
	totalCalls       int
	callsInRun       int
	callsSinceReport int
	lastReportAt     time.Time
	reportDir        string
}

var (
	llmCacheProcessStartedAt = time.Now()
	llmCacheRunID            = newLLMCacheRunID()
	globalLLMCacheStore      = &llmCacheStore{
		stats: map[llmCacheKey]*LLMCacheStat{}, promptPools: map[llmCacheKey][]string{},
		enabled: func() bool { return false },
	}
)

// newLLMCacheRunID 对齐 Python RUN_ID：yyyyMMddHHmmss-8hex
func newLLMCacheRunID() string {
	stamp := time.Now().Format("20060102150405")
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return stamp + "-00000000"
	}
	return stamp + "-" + hex.EncodeToString(suffix)
}

// ConfigureLLMCacheStats 注入启用判定与事件落盘回调
func ConfigureLLMCacheStats(enabled func() bool, eventSink func(map[string]any)) {
	globalLLMCacheStore.mu.Lock()
	defer globalLLMCacheStore.mu.Unlock()
	if enabled == nil {
		enabled = func() bool { return false }
	}
	globalLLMCacheStore.enabled = enabled
	globalLLMCacheStore.eventSink = eventSink
}

func normalizeCacheText(value, fallback string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return fallback
	}
	return text
}

func normalizeLLMCacheTokens(promptTokens, hitTokens, missTokens int64) (int64, int64, bool) {
	if hitTokens < 0 {
		hitTokens = 0
	}
	if missTokens < 0 {
		missTokens = 0
	}
	reported := hitTokens > 0 || missTokens > 0
	if missTokens == 0 && hitTokens > 0 {
		missTokens = promptTokens - hitTokens
		if missTokens < 0 {
			missTokens = 0
		}
	} else if hitTokens == 0 && missTokens == 0 && promptTokens > 0 {
		missTokens = promptTokens
	}
	return hitTokens, missTokens, reported
}

func longestCommonPrefix(left, right string) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for i := 0; i < limit; i++ {
		if left[i] != right[i] {
			return i
		}
	}
	return limit
}

func calculateTheoreticalMatch(promptTokens int64, promptText string, pool []string) theoreticalMatch {
	if promptText == "" {
		return theoreticalMatch{}
	}
	if len(pool) == 0 {
		return theoreticalMatch{MissTokens: promptTokens, Compared: true}
	}
	bestPrefix := 0
	bestRank := 0
	bestText := ""
	for index := len(pool) - 1; index >= 0; index-- {
		rank := len(pool) - index
		prefix := longestCommonPrefix(pool[index], promptText)
		if prefix > bestPrefix {
			bestPrefix = prefix
			bestRank = rank
			bestText = pool[index]
		}
	}
	rate := float64(bestPrefix) / float64(len(promptText))
	hit := int64(math.Round(float64(promptTokens) * rate))
	if hit > promptTokens {
		hit = promptTokens
	}
	miss := promptTokens - hit
	if miss < 0 {
		miss = 0
	}
	return theoreticalMatch{HitTokens: hit, MissTokens: miss, HitRate: rate * 100, Compared: true, PoolSize: len(pool), BestMatchRank: bestRank, BestPromptText: bestText, CommonPrefixChars: bestPrefix}
}

func summarizeCacheValue(value any) string {
	var text string
	switch typed := value.(type) {
	case string:
		text = strings.ReplaceAll(typed, "\n", "\\n")
	case nil:
		text = "null"
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			text = fmt.Sprint(typed)
		} else {
			text = string(raw)
		}
	}
	if len(text) > llmCacheSnippetLimit {
		return text[:llmCacheSnippetLimit] + "..."
	}
	return text
}

func findFirstStructuralDiff(previous, current any, path string) *dynamicDiff {
	switch p := previous.(type) {
	case map[string]any:
		c, ok := current.(map[string]any)
		if !ok {
			return &dynamicDiff{Path: path + ".__type__", Previous: "dict", Current: fmt.Sprintf("%T", current)}
		}
		keys := map[string]struct{}{}
		for key := range p {
			keys[key] = struct{}{}
		}
		for key := range c {
			keys[key] = struct{}{}
		}
		ordered := make([]string, 0, len(keys))
		for key := range keys {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)
		for _, key := range ordered {
			keyPath := path + "." + key
			pv, pok := p[key]
			cv, cok := c[key]
			if !pok {
				return &dynamicDiff{Path: keyPath, Previous: "<missing>", Current: summarizeCacheValue(cv)}
			}
			if !cok {
				return &dynamicDiff{Path: keyPath, Previous: summarizeCacheValue(pv), Current: "<missing>"}
			}
			if diff := findFirstStructuralDiff(pv, cv, keyPath); diff != nil {
				return diff
			}
		}
		return nil
	case []any:
		c, ok := current.([]any)
		if !ok {
			return &dynamicDiff{Path: path + ".__type__", Previous: "list", Current: fmt.Sprintf("%T", current)}
		}
		limit := len(p)
		if len(c) > limit {
			limit = len(c)
		}
		for index := 0; index < limit; index++ {
			indexPath := fmt.Sprintf("%s[%d]", path, index)
			if index >= len(p) {
				return &dynamicDiff{Path: indexPath, Previous: "<missing>", Current: summarizeCacheValue(c[index])}
			}
			if index >= len(c) {
				return &dynamicDiff{Path: indexPath, Previous: summarizeCacheValue(p[index]), Current: "<missing>"}
			}
			if diff := findFirstStructuralDiff(p[index], c[index], indexPath); diff != nil {
				return diff
			}
		}
		return nil
	}
	if fmt.Sprintf("%T", previous) != fmt.Sprintf("%T", current) {
		return &dynamicDiff{Path: path + ".__type__", Previous: fmt.Sprintf("%T", previous), Current: fmt.Sprintf("%T", current)}
	}
	if fmt.Sprint(previous) == fmt.Sprint(current) {
		return nil
	}
	if p, ok := previous.(string); ok {
		c, ok := current.(string)
		if ok {
			index := longestCommonPrefix(p, c)
			return &dynamicDiff{Path: fmt.Sprintf("%s@char%d", path, index), Previous: summarizeCacheValue(p[index:]), Current: summarizeCacheValue(c[index:])}
		}
	}
	return &dynamicDiff{Path: path, Previous: summarizeCacheValue(previous), Current: summarizeCacheValue(current)}
}

func diagnoseDynamicDiff(previous, current string) dynamicDiff {
	if current == "" {
		return dynamicDiff{Path: "prompt_text.unavailable"}
	}
	if previous == "" {
		return dynamicDiff{Path: "cache_pool.empty", Current: summarizeCacheValue(current)}
	}
	var previousPayload, currentPayload any
	if err := json.Unmarshal([]byte(previous), &previousPayload); err != nil {
		index := longestCommonPrefix(previous, current)
		return dynamicDiff{Path: fmt.Sprintf("raw_prompt@char%d", index), Previous: summarizeCacheValue(previous[index:]), Current: summarizeCacheValue(current[index:])}
	}
	if err := json.Unmarshal([]byte(current), &currentPayload); err != nil {
		index := longestCommonPrefix(previous, current)
		return dynamicDiff{Path: fmt.Sprintf("raw_prompt@char%d", index), Previous: summarizeCacheValue(previous[index:]), Current: summarizeCacheValue(current[index:])}
	}
	if diff := findFirstStructuralDiff(previousPayload, currentPayload, "root"); diff != nil {
		return *diff
	}
	return dynamicDiff{Path: "identical"}
}

func promptMessages(raw string) ([]map[string]any, []string) {
	if raw == "" {
		return nil, nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, nil
	}
	list, _ := payload["messages"].([]any)
	messages := make([]map[string]any, 0, len(list))
	fingerprints := make([]string, 0, len(list))
	for _, item := range list {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		messages = append(messages, message)
		raw, _ := json.Marshal(message)
		fingerprints = append(fingerprints, string(raw))
	}
	return messages, fingerprints
}

func messageRole(messages []map[string]any, index int) string {
	if len(messages) == 0 {
		return ""
	}
	if index < 0 {
		index += len(messages)
	}
	if index < 0 || index >= len(messages) {
		return ""
	}
	role, _ := messages[index]["role"].(string)
	return role
}

type alignment struct{ PreviousStart, CurrentStart, Length int }

func longestMessageAlignment(previous, current []string) alignment {
	best := alignment{}
	for i := 0; i < len(previous); i++ {
		for j := 0; j < len(current); j++ {
			length := 0
			for i+length < len(previous) && j+length < len(current) && previous[i+length] == current[j+length] {
				length++
			}
			if length > best.Length {
				best = alignment{PreviousStart: i, CurrentStart: j, Length: length}
			}
		}
	}
	return best
}

func diagnosePrompt(previous, current string, prefixChars int) promptDiagnostics {
	bestMessages, bestPrints := promptMessages(previous)
	currentMessages, currentPrints := promptMessages(current)
	diag := promptDiagnostics{CurrentMessageCount: len(currentMessages), BestMatchMessageCount: len(bestMessages)}
	if current == "" {
		return diag
	}
	diag.CommonPrefixRate = float64(prefixChars) / float64(len(current)) * 100
	diag.PromptGrowthChars = len(current) - len(previous)
	commonPrefix := 0
	for commonPrefix < len(bestPrints) && commonPrefix < len(currentPrints) && bestPrints[commonPrefix] == currentPrints[commonPrefix] {
		commonPrefix++
	}
	commonSuffix := 0
	for commonSuffix < len(bestPrints)-commonPrefix && commonSuffix < len(currentPrints)-commonPrefix &&
		bestPrints[len(bestPrints)-1-commonSuffix] == currentPrints[len(currentPrints)-1-commonSuffix] {
		commonSuffix++
	}
	diag.CommonPrefixMessages = commonPrefix
	diag.CommonSuffixMessages = commonSuffix
	align := longestMessageAlignment(bestPrints, currentPrints)
	diag.LongestAlignedOverlap = align.Length
	diag.AlignedPreviousStartIndex = align.PreviousStart
	diag.AlignedCurrentStartIndex = align.CurrentStart
	if align.PreviousStart > align.CurrentStart && align.Length > commonPrefix {
		diag.SuspectedContextSliding = true
		diag.SlidingDroppedHeadMessages = align.PreviousStart - align.CurrentStart
		diag.SlidingAlignedMessages = align.Length
		diag.SlidingNewTailMessages = len(currentPrints) - align.CurrentStart - align.Length
		if diag.SlidingNewTailMessages < 0 {
			diag.SlidingNewTailMessages = 0
		}
	}
	diag.CurrentFirstRole = messageRole(currentMessages, 0)
	diag.BestFirstRole = messageRole(bestMessages, 0)
	diag.CurrentLastRole = messageRole(currentMessages, len(currentMessages)-1)
	diag.BestLastRole = messageRole(bestMessages, len(bestMessages)-1)
	return diag
}

// RecordLLMCacheUsage 记录一次 replyer/planner prompt cache 使用事件
func RecordLLMCacheUsage(taskName, requestType, modelName, sessionID string, promptTokens, hitTokens, missTokens int64, promptText string) {
	store := globalLLMCacheStore
	store.mu.Lock()
	enabled := store.enabled
	store.mu.Unlock()
	if enabled == nil || !enabled() {
		return
	}
	normalizedTask := strings.TrimSpace(taskName)
	if normalizedTask != llmCacheFocusedTask && normalizedTask != llmCacheFocusedPlanner {
		return
	}
	normalizedRequest := normalizeCacheText(requestType, "unknown")
	if _, excluded := llmCacheExcludedRequestTypes[normalizedRequest]; excluded {
		return
	}
	key := llmCacheKey{TaskName: normalizedTask, RequestType: normalizedRequest, ModelName: normalizeCacheText(modelName, "unknown"), SessionID: normalizeCacheText(sessionID, "unknown")}
	if promptTokens < 0 {
		promptTokens = 0
	}
	hitTokens, missTokens, reported := normalizeLLMCacheTokens(promptTokens, hitTokens, missTokens)

	store.mu.Lock()
	pool := append([]string(nil), store.promptPools[key]...)
	match := calculateTheoreticalMatch(promptTokens, promptText, pool)
	diff := diagnoseDynamicDiff(match.BestPromptText, promptText)
	diag := diagnosePrompt(match.BestPromptText, promptText, match.CommonPrefixChars)
	if promptText != "" {
		next := append(pool, promptText)
		if len(next) > llmCachePromptPoolSize {
			next = next[len(next)-llmCachePromptPoolSize:]
		}
		store.promptPools[key] = next
	}
	stat := store.stats[key]
	if stat == nil {
		stat = &LLMCacheStat{TaskName: key.TaskName, RequestType: key.RequestType, ModelName: key.ModelName, SessionID: key.SessionID, DynamicDiffCounts: map[string]int{}}
		store.stats[key] = stat
	}
	stat.Calls++
	stat.PromptTokens += promptTokens
	stat.PromptCacheHitTokens += hitTokens
	stat.PromptCacheMissTokens += missTokens
	stat.TheoreticalPromptCacheHitTokens += match.HitTokens
	stat.TheoreticalPromptCacheMissTokens += match.MissTokens
	stat.CommonPrefixRateTotal += diag.CommonPrefixRate
	if diag.SuspectedContextSliding {
		stat.SuspectedContextSlidingCalls++
		stat.SlidingDroppedMessagesTotal += diag.SlidingDroppedHeadMessages
		stat.SlidingAlignedMessagesTotal += diag.SlidingAlignedMessages
	}
	stat.DynamicDiffCounts[diff.Path]++
	if reported {
		stat.CacheReportedCalls++
	}
	if match.Compared {
		stat.TheoreticalComparedCalls++
	}
	if match.HitTokens > 0 {
		stat.TheoreticalCachePoolHits++
	}
	store.totalCalls++
	store.callsInRun++
	store.callsSinceReport++
	callIndex := store.callsInRun
	sink := store.eventSink
	apiRate := cacheRate(hitTokens, missTokens)
	event := map[string]any{
		"created_at": time.Now().Format(llmCacheReportTimeLayout), "run_id": llmCacheRunID,
		"process_started_at": llmCacheProcessStartedAt.Format(llmCacheReportTimeLayout), "call_index_in_run": callIndex,
		"task_name": key.TaskName, "request_type": key.RequestType,
		"model_name": key.ModelName, "session_id": key.SessionID, "prompt_tokens": promptTokens,
		"prompt_chars": len(promptText), "prompt_cache_hit_tokens": hitTokens, "prompt_cache_miss_tokens": missTokens,
		"prompt_cache_hit_rate":               round2(apiRate),
		"theoretical_prompt_cache_hit_tokens": match.HitTokens, "theoretical_prompt_cache_miss_tokens": match.MissTokens,
		"theoretical_prompt_cache_hit_rate": round2(match.HitRate), "theoretical_cache_pool_size": match.PoolSize,
		"theoretical_best_match_rank": match.BestMatchRank, "theoretical_common_prefix_chars": match.CommonPrefixChars,
		"theoretical_common_prefix_rate": round2(diag.CommonPrefixRate),
		"current_message_count":          diag.CurrentMessageCount, "best_match_message_count": diag.BestMatchMessageCount,
		"common_prefix_messages": diag.CommonPrefixMessages, "common_suffix_messages": diag.CommonSuffixMessages,
		"prompt_growth_chars": diag.PromptGrowthChars, "longest_aligned_message_overlap": diag.LongestAlignedOverlap,
		"aligned_previous_start_index": diag.AlignedPreviousStartIndex, "aligned_current_start_index": diag.AlignedCurrentStartIndex,
		"suspected_context_sliding":     diag.SuspectedContextSliding,
		"sliding_dropped_head_messages": diag.SlidingDroppedHeadMessages, "sliding_aligned_messages": diag.SlidingAlignedMessages,
		"sliding_new_tail_messages":  diag.SlidingNewTailMessages,
		"current_first_message_role": diag.CurrentFirstRole, "best_first_message_role": diag.BestFirstRole,
		"current_last_message_role": diag.CurrentLastRole, "best_last_message_role": diag.BestLastRole,
		"prompt_cache_hit_rate_delta": round2(apiRate - match.HitRate), "dynamic_diff_path": diff.Path,
		"dynamic_diff_previous": diff.Previous, "dynamic_diff_current": diff.Current,
		"cache_reported": reported, "theoretical_compared": match.Compared,
	}
	store.mu.Unlock()
	if sink != nil {
		sink(event)
	}
	appendLLMCacheUsageEvent(event)
	maybeWriteLLMCacheReports(GetLLMCacheStatsSnapshot())
}

// GetLLMCacheStatsSnapshot 返回当前进程内快照
func GetLLMCacheStatsSnapshot() []map[string]any {
	store := globalLLMCacheStore
	store.mu.Lock()
	defer store.mu.Unlock()
	out := make([]map[string]any, 0, len(store.stats))
	for _, stat := range store.stats {
		out = append(out, stat.ToMap())
	}
	return out
}

// ResetLLMCacheStats 清空进程内统计
func ResetLLMCacheStats() {
	store := globalLLMCacheStore
	store.mu.Lock()
	defer store.mu.Unlock()
	store.stats = map[llmCacheKey]*LLMCacheStat{}
	store.promptPools = map[llmCacheKey][]string{}
	store.totalCalls = 0
	store.callsInRun = 0
	store.callsSinceReport = 0
	store.lastReportAt = time.Time{}
}

// LogLLMCacheStatsSummary 返回高低命中率摘要，供调用方记录日志
func LogLLMCacheStatsSummary(snapshot []map[string]any) (low, high []map[string]any) {
	if snapshot == nil {
		snapshot = GetLLMCacheStatsSnapshot()
	}
	if len(snapshot) == 0 {
		return nil, nil
	}
	sorted := append([]map[string]any(nil), snapshot...)
	sort.Slice(sorted, func(i, j int) bool {
		left, _ := sorted[i]["prompt_cache_hit_rate"].(float64)
		right, _ := sorted[j]["prompt_cache_hit_rate"].(float64)
		if left != right {
			return left < right
		}
		return toInt64(sorted[i]["prompt_cache_miss_tokens"]) > toInt64(sorted[j]["prompt_cache_miss_tokens"])
	})
	limit := llmCacheSummaryLimit
	if len(sorted) < limit {
		limit = len(sorted)
	}
	low = append(low, sorted[:limit]...)
	for index := len(sorted) - 1; index >= len(sorted)-limit; index-- {
		high = append(high, sorted[index])
	}
	return low, high
}

func toInt64(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	}
	return 0
}

// formatLLMCacheStatsLine 对齐 Python _format_stat 的日志行
func formatLLMCacheStatsLine(item map[string]any) string {
	return fmt.Sprintf(
		"%s/%s/%s: api_hit_rate=%.2f%%, theory_hit_rate=%.2f%%, delta=%.2f%%, avg_prefix=%.2f%%, sliding_calls=%d, top_dynamic=%s, hit=%d, miss=%d, prompt=%d, calls=%d, reported=%d",
		cacheString(item, "task_name"), cacheString(item, "request_type"), cacheString(item, "model_name"),
		cacheFloat(item, "prompt_cache_hit_rate"), cacheFloat(item, "theoretical_prompt_cache_hit_rate"),
		cacheFloat(item, "prompt_cache_hit_rate_delta"), cacheFloat(item, "avg_common_prefix_rate"),
		cacheInt(item, "suspected_context_sliding_calls"), cacheString(item, "top_dynamic_diff_paths"),
		cacheInt(item, "prompt_cache_hit_tokens"), cacheInt(item, "prompt_cache_miss_tokens"),
		cacheInt(item, "prompt_tokens"), cacheInt(item, "calls"), cacheInt(item, "cache_reported_calls"),
	)
}

// logLLMCacheStatsSummary 对齐 Python log_llm_cache_stats_summary 的日志内容
func logLLMCacheStatsSummary(snapshot []map[string]any) {
	if snapshot == nil {
		snapshot = GetLLMCacheStatsSnapshot()
	}
	if len(snapshot) == 0 {
		return
	}
	low, high := LogLLMCacheStatsSummary(snapshot)
	lowLines := make([]string, 0, len(low))
	for _, item := range low {
		lowLines = append(lowLines, formatLLMCacheStatsLine(item))
	}
	highLines := make([]string, 0, len(high))
	for _, item := range high {
		highLines = append(highLines, formatLLMCacheStatsLine(item))
	}
	zap.L().Info("LLM prompt cache 统计摘要\n低命中调用点:\n- " + strings.Join(lowLines, "\n- ") +
		"\n高命中调用点:\n- " + strings.Join(highLines, "\n- "))
}
