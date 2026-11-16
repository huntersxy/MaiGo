package stats

// 详细统计的可靠性、时段趋势与上一周期环比，对齐 statistic.py 的 c50b 增量

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"MaiGo/internal/webui/schemas"
)

const (
	detailedTaskUsageRetention = 7 * 24 * time.Hour
	detailedTrendMaxRange      = 30 * 24 * time.Hour
)

var detailedPeriodBucketMinutes = map[string]int{
	"all_time":        1440,
	"last_30_days":    1440,
	"last_7_days":     360,
	"last_3_days":     180,
	"last_24_hours":   60,
	"last_3_hours":    10,
	"last_hour":       5,
	"last_15_minutes": 1,
}

var detailedEmbeddingTaskNames = map[string]struct{}{"embedding": {}, "image_embedding": {}}

type detailedFailureTally struct {
	attempts int64
	failures int64
}

func detailedFailureKey(kind, name string) string {
	return kind + "\x00" + name
}

// enrichDetailedPeriods 为每个时段追加可靠性、趋势与上一周期环比
func enrichDetailedPeriods(
	ctx context.Context, provider DetailedProvider, snapshot *DetailedStatisticsData,
	starts []time.Time, now time.Time, usage []DetailedUsage, messages []DetailedMessage, intervals []OnlineInterval,
) {
	retentionStart := now.Add(-detailedTaskUsageRetention)
	taskUsage, err := provider.TaskUsageSince(ctx, retentionStart)
	if err != nil {
		taskUsage = nil
	}
	for index := range snapshot.Periods {
		period := &snapshot.Periods[index]
		key := period.Key
		periodStart := starts[index]
		reliability, modelFailures, taskFailures := buildDetailedReliability(periodStart, now, retentionStart, taskUsage)
		period.Reliability = reliability
		applyDetailedFailureCounts(period.Models, "model", modelFailures)
		applyDetailedFailureCounts(period.TaskGroups, "task", taskFailures)
		if key != "all_time" {
			previousStart := periodStart.Add(-detailedPeriodDuration(key))
			summary := buildDetailedPreviousSummary(ctx, provider, previousStart, periodStart, usage, intervals)
			period.PreviousSummary = &summary
		}
		period.Trend = buildDetailedPeriodTrend(periodStart, now, detailedPeriodBucketMinutes[key], usage, messages, taskUsage)
	}
}

func detailedPeriodDuration(key string) time.Duration {
	for _, definition := range detailedPeriodDefinitions {
		if definition.key == key {
			return definition.duration
		}
	}
	return 0
}

func buildDetailedReliability(periodStart, now, retentionStart time.Time, rows []DetailedTaskUsage) (schemas.DetailedReliabilityData, map[string]detailedFailureTally, map[string]detailedFailureTally) {
	coverageStart := periodStart
	if retentionStart.After(coverageStart) {
		coverageStart = retentionStart
	}
	type tally struct {
		attempts int64
		failures int64
		duration float64
	}
	grouped := map[string]map[string]*tally{
		"model": {}, "provider": {}, "task": {},
	}
	modelLookup := map[string]detailedFailureTally{}
	taskLookup := map[string]detailedFailureTally{}
	totalAttempts, totalFailures := int64(0), int64(0)
	totalDuration := 0.0
	for _, row := range rows {
		if row.BucketTime.Before(coverageStart) || row.BucketTime.After(now) {
			continue
		}
		for dimension, name := range map[string]string{
			"model": row.ModelName, "provider": row.ProviderName, "task": row.TaskName,
		} {
			entry := grouped[dimension][name]
			if entry == nil {
				entry = &tally{}
				grouped[dimension][name] = entry
			}
			entry.attempts += row.RequestCount
			entry.failures += row.FailureCount
			entry.duration += row.SuccessDuration
		}
		kind := "generation"
		if _, ok := detailedEmbeddingTaskNames[row.TaskName]; ok {
			kind = "embedding"
		}
		for _, item := range []struct {
			lookup map[string]detailedFailureTally
			name   string
		}{{modelLookup, row.ModelName}, {taskLookup, row.TaskName}} {
			key := detailedFailureKey(kind, item.name)
			tally := item.lookup[key]
			tally.attempts += row.RequestCount
			tally.failures += row.FailureCount
			item.lookup[key] = tally
		}
		totalAttempts += row.RequestCount
		totalFailures += row.FailureCount
		totalDuration += row.SuccessDuration
	}
	buildItems := func(dimension string) []schemas.DetailedReliabilityItem {
		items := make([]schemas.DetailedReliabilityItem, 0, len(grouped[dimension]))
		for name, value := range grouped[dimension] {
			successes := value.attempts - value.failures
			item := schemas.DetailedReliabilityItem{
				Name: name, AttemptCount: value.attempts, FailureCount: value.failures,
			}
			if value.attempts > 0 {
				rate := float64(value.failures) / float64(value.attempts)
				item.FailureRate = &rate
			}
			if successes > 0 {
				average := value.duration / float64(successes)
				item.AvgSuccessDuration = &average
			}
			items = append(items, item)
		}
		sort.SliceStable(items, func(i, j int) bool {
			leftRate, rightRate := 0.0, 0.0
			if items[i].FailureRate != nil {
				leftRate = *items[i].FailureRate
			}
			if items[j].FailureRate != nil {
				rightRate = *items[j].FailureRate
			}
			if leftRate != rightRate {
				return leftRate > rightRate
			}
			if items[i].AttemptCount != items[j].AttemptCount {
				return items[i].AttemptCount > items[j].AttemptCount
			}
			return items[i].Name < items[j].Name
		})
		return items
	}
	data := schemas.DetailedReliabilityData{
		CoverageStart: formatDetailedTime(coverageStart),
		Truncated:     periodStart.Before(retentionStart),
		AttemptCount:  totalAttempts,
		FailureCount:  totalFailures,
		ByModel:       buildItems("model"),
		ByProvider:    buildItems("provider"),
		ByTask:        buildItems("task"),
	}
	if totalAttempts > 0 {
		rate := float64(totalFailures) / float64(totalAttempts)
		data.FailureRate = &rate
	}
	if successes := totalAttempts - totalFailures; successes > 0 {
		average := totalDuration / float64(successes)
		data.AvgSuccessDuration = &average
	}
	return data, modelLookup, taskLookup
}

func applyDetailedFailureCounts(rows []schemas.DetailedStatisticsBreakdown, dimension string, lookup map[string]detailedFailureTally) {
	for index := range rows {
		kind := "generation"
		if rows[index].IsEmbedding {
			kind = "embedding"
		}
		tally, ok := lookup[detailedFailureKey(kind, rows[index].Name)]
		if !ok {
			continue
		}
		attempts, failures := tally.attempts, tally.failures
		rows[index].AttemptCount = &attempts
		rows[index].FailureCount = &failures
	}
}

func buildDetailedPreviousSummary(ctx context.Context, provider DetailedProvider, start, end time.Time, usage []DetailedUsage, intervals []OnlineInterval) schemas.DetailedStatisticsSummary {
	var requests, inputTokens, outputTokens, hitTokens, missTokens int64
	cost := 0.0
	for _, record := range usage {
		if record.Timestamp.Before(start) || !record.Timestamp.Before(end) {
			continue
		}
		requests++
		inputTokens += record.PromptTokens
		outputTokens += record.CompletionTokens
		cost += record.Cost
		hit, miss := int64(0), int64(0)
		if record.PromptCacheEnabled {
			hit, miss = normalizeCacheTokens(record.PromptTokens, true, record.PromptCacheHitTokens, record.PromptCacheMissTokens)
		}
		hitTokens += hit
		missTokens += miss
	}
	messages, err := provider.MessageCountBetween(ctx, start, end)
	if err != nil {
		messages = 0
	}
	replies, err := provider.ReplyCountBetween(ctx, start, end, detailedReplyToolName)
	if err != nil {
		replies = 0
	}
	onlineSeconds := detailedOnlineSeconds(intervals, start, end)
	return buildDetailedSummaryTotals(requests, cost, inputTokens, outputTokens, hitTokens, missTokens, onlineSeconds, messages, replies)
}

func buildDetailedSummaryTotals(requests int64, cost float64, inputTokens, outputTokens, hitTokens, missTokens int64, onlineSeconds float64, messages, replies int64) schemas.DetailedStatisticsSummary {
	totalTokens := inputTokens + outputTokens
	onlineHours := onlineSeconds / 3600
	receivedMessages := messages - replies
	summary := schemas.DetailedStatisticsSummary{
		OnlineTime:         onlineSeconds,
		TotalMessages:      messages,
		TotalReplies:       replies,
		TotalRequests:      requests,
		TotalTokens:        totalTokens,
		InputTokens:        inputTokens,
		OutputTokens:       outputTokens,
		CacheHitTokens:     hitTokens,
		CacheMissTokens:    missTokens,
		CacheHitRate:       calculateCacheHitRate(hitTokens, missTokens),
		TotalCost:          cost,
		CostPer100Messages: ratioOrZero(cost, float64(messages)) * 100,
		CostPer100Replies:  ratioOrZero(cost, float64(replies)) * 100,
		CostPerHour:        ratioOrZero(cost, onlineHours),
		TokensPerHour:      ratioOrZero(float64(totalTokens), onlineHours),
	}
	if receivedMessages > 0 {
		summary.CostPer100MessagesExcludingReplies = cost / float64(receivedMessages) * 100
	}
	return summary
}

func buildDetailedPeriodTrend(periodStart, now time.Time, bucketMinutes int, usage []DetailedUsage, messages []DetailedMessage, taskUsage []DetailedTaskUsage) schemas.DetailedPeriodTrend {
	trendStart := periodStart
	truncated := false
	if earliest := now.Add(-detailedTrendMaxRange); trendStart.Before(earliest) {
		trendStart = earliest
		truncated = true
	}
	if bucketMinutes <= 0 {
		bucketMinutes = 1440
	}
	seconds := float64(bucketMinutes * 60)
	size := int(math.Ceil(now.Sub(trendStart).Seconds() / seconds))
	if size < 1 {
		size = 1
	}
	trend := schemas.DetailedPeriodTrend{
		StartTime:       formatDetailedTime(trendStart),
		BucketMinutes:   bucketMinutes,
		Truncated:       truncated,
		TimeLabels:      make([]string, size),
		Costs:           make([]float64, size),
		Tokens:          make([]int64, size),
		Requests:        make([]int64, size),
		Messages:        make([]int64, size),
		Attempts:        make([]int64, size),
		Failures:        make([]int64, size),
		CostByModel:     map[string][]float64{},
		TokensByModel:   map[string][]int64{},
		RequestsByModel: map[string][]int64{},
		CostByModule:    map[string][]float64{},
		MessageByChat:   map[string][]int64{},
		HeatmapMessages: makeMatrixInt(),
		HeatmapCosts:    makeMatrixFloat(),
	}
	format := "%m-%d"
	switch {
	case bucketMinutes < 60:
		format = "%H:%M"
	case bucketMinutes < 1440:
		format = "%m-%d %H:%M"
	}
	for index := range trend.TimeLabels {
		trend.TimeLabels[index] = trendStart.Add(time.Duration(index*bucketMinutes) * time.Minute).Format(format)
	}
	indexFor := func(timestamp time.Time) (int, bool) {
		if timestamp.Before(trendStart) || timestamp.After(now) {
			return 0, false
		}
		index := int(timestamp.Sub(trendStart).Seconds() / seconds)
		if index >= size {
			index = size - 1
		}
		return index, true
	}
	for _, record := range usage {
		index, ok := indexFor(record.Timestamp)
		if !ok {
			continue
		}
		requestType := firstNonEmpty(record.RequestType, "unknown")
		modelName := firstNonEmpty(record.ModelAssignName, record.ModelName, "unknown")
		moduleName := requestType
		if position := strings.Index(requestType, "."); position >= 0 {
			moduleName = requestType[:position]
		}
		cost := record.Cost
		tokens := record.PromptTokens + record.CompletionTokens
		trend.Costs[index] += cost
		trend.Tokens[index] += tokens
		trend.Requests[index]++
		ensureFloatSeries(trend.CostByModel, modelName, size)[index] += cost
		ensureIntSeries(trend.TokensByModel, modelName, size)[index] += tokens
		ensureIntSeries(trend.RequestsByModel, modelName, size)[index]++
		ensureFloatSeries(trend.CostByModule, moduleName, size)[index] += cost
		weekday := (int(record.Timestamp.Weekday()) + 6) % 7
		trend.HeatmapCosts[weekday][record.Timestamp.Hour()] += cost
	}
	for _, message := range messages {
		if message.ChatName == "" {
			continue
		}
		index, ok := indexFor(message.StatisticsTime)
		if !ok {
			continue
		}
		trend.Messages[index]++
		ensureIntSeries(trend.MessageByChat, message.ChatName, size)[index]++
		weekday := (int(message.StatisticsTime.Weekday()) + 6) % 7
		trend.HeatmapMessages[weekday][message.StatisticsTime.Hour()]++
	}
	for _, row := range taskUsage {
		index, ok := indexFor(row.BucketTime)
		if !ok {
			continue
		}
		trend.Attempts[index] += row.RequestCount
		trend.Failures[index] += row.FailureCount
	}
	return trend
}

func makeMatrixInt() [][]int64 {
	matrix := make([][]int64, 7)
	for day := range matrix {
		matrix[day] = make([]int64, 24)
	}
	return matrix
}

func makeMatrixFloat() [][]float64 {
	matrix := make([][]float64, 7)
	for day := range matrix {
		matrix[day] = make([]float64, 24)
	}
	return matrix
}

func emptyDetailedReliability() schemas.DetailedReliabilityData {
	return schemas.DetailedReliabilityData{
		ByModel:    []schemas.DetailedReliabilityItem{},
		ByProvider: []schemas.DetailedReliabilityItem{},
		ByTask:     []schemas.DetailedReliabilityItem{},
	}
}

func emptyDetailedTrend() schemas.DetailedPeriodTrend {
	return schemas.DetailedPeriodTrend{
		TimeLabels:      []string{},
		Costs:           []float64{},
		Tokens:          []int64{},
		Requests:        []int64{},
		Messages:        []int64{},
		Attempts:        []int64{},
		Failures:        []int64{},
		CostByModel:     map[string][]float64{},
		TokensByModel:   map[string][]int64{},
		RequestsByModel: map[string][]int64{},
		CostByModule:    map[string][]float64{},
		MessageByChat:   map[string][]int64{},
		HeatmapMessages: makeMatrixInt(),
		HeatmapCosts:    makeMatrixFloat(),
	}
}

func ensureFloatSeries(series map[string][]float64, name string, size int) []float64 {
	if values, ok := series[name]; ok {
		return values
	}
	values := make([]float64, size)
	series[name] = values
	return values
}

func ensureIntSeries(series map[string][]int64, name string, size int) []int64 {
	if values, ok := series[name]; ok {
		return values
	}
	values := make([]int64, size)
	series[name] = values
	return values
}
