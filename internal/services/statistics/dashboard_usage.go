package stats

// 小时汇总就绪时，仪表盘的用量部分改读汇总条目，结果与直接扫描 llm_usage 一致

import (
	"context"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

func summaryFromUsageItems(ctx context.Context, db *gorm.DB, start, end time.Time, items []UsageEntry) (StatisticsSummary, error) {
	var requests, inputTokens, outputTokens, hitTokens, missTokens, coreHit, coreMiss int64
	cost := 0.0
	responseCount, responseSum := int64(0), 0.0
	for _, item := range items {
		requests += item.Metrics.RequestCount
		inputTokens += item.Metrics.PromptTokens
		outputTokens += item.Metrics.CompletionTokens
		hitTokens += item.Metrics.CacheHitTokens
		missTokens += item.Metrics.CacheMissTokens
		cost += item.Metrics.Cost
		responseCount += item.Metrics.ResponseTimeCount
		responseSum += item.Metrics.ResponseTimeSum
		if item.Key.TaskName == "planner" {
			coreHit += item.Metrics.CacheHitTokens
			coreMiss += item.Metrics.CacheMissTokens
		}
	}
	summary := StatisticsSummary{
		TotalRequests:       requests,
		TotalCost:           cost,
		InputTokens:         inputTokens,
		OutputTokens:        outputTokens,
		TotalTokens:         inputTokens + outputTokens,
		CacheHitTokens:      hitTokens,
		CacheMissTokens:     missTokens,
		CacheHitRate:        calculateCacheHitRate(hitTokens, missTokens),
		CoreCacheHitTokens:  coreHit,
		CoreCacheMissTokens: coreMiss,
		CoreCacheHitRate:    calculateCacheHitRate(coreHit, coreMiss),
	}
	if responseCount > 0 {
		summary.AvgResponseTime = responseSum / float64(responseCount)
	}
	if err := applySummaryActivity(ctx, db, start, end, &summary); err != nil {
		return summary, err
	}
	return summary, nil
}

func modelStatisticsFromUsage(items []UsageEntry) []ModelStatistics {
	type totals struct {
		requests, input, output, hit, miss int64
		cost, responseSum                  float64
		responseCount                      int64
	}
	grouped := map[string]*totals{}
	for _, item := range items {
		name := firstNonEmpty(item.Key.ModelName, "unknown")
		entry := grouped[name]
		if entry == nil {
			entry = &totals{}
			grouped[name] = entry
		}
		entry.requests += item.Metrics.RequestCount
		entry.input += item.Metrics.PromptTokens
		entry.output += item.Metrics.CompletionTokens
		entry.hit += item.Metrics.CacheHitTokens
		entry.miss += item.Metrics.CacheMissTokens
		entry.cost += item.Metrics.Cost
		entry.responseSum += item.Metrics.ResponseTimeSum
		entry.responseCount += item.Metrics.ResponseTimeCount
	}
	out := make([]ModelStatistics, 0, len(grouped))
	for name, value := range grouped {
		average := 0.0
		if value.responseCount > 0 {
			average = value.responseSum / float64(value.responseCount)
		}
		out = append(out, ModelStatistics{
			ModelName: name, RequestCount: value.requests, TotalCost: value.cost,
			TotalTokens: value.input + value.output, InputTokens: value.input, OutputTokens: value.output,
			CacheHitTokens: value.hit, CacheMissTokens: value.miss,
			CacheHitRate: calculateCacheHitRate(value.hit, value.miss), AvgResponseTime: average,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RequestCount != out[j].RequestCount {
			return out[i].RequestCount > out[j].RequestCount
		}
		return strings.Compare(out[i].ModelName, out[j].ModelName) < 0
	})
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

func usageTimeSeries(items []UsageEntry, layout string) []timeSeriesRow {
	byTimestamp := map[string]*timeSeriesRow{}
	for _, item := range items {
		key := item.Timestamp.Format(layout)
		row := byTimestamp[key]
		if row == nil {
			row = &timeSeriesRow{Timestamp: key}
			byTimestamp[key] = row
		}
		row.Requests += item.Metrics.RequestCount
		row.Cost += item.Metrics.Cost
		row.InputTokens += item.Metrics.PromptTokens
		row.OutputTokens += item.Metrics.CompletionTokens
		row.CacheHitTokens += item.Metrics.CacheHitTokens
		row.CacheMissTokens += item.Metrics.CacheMissTokens
	}
	out := make([]timeSeriesRow, 0, len(byTimestamp))
	for _, row := range byTimestamp {
		out = append(out, *row)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp < out[j].Timestamp })
	return out
}

func hourlyStatisticsFromUsage(items []UsageEntry, start, end time.Time) []TimeSeriesData {
	return fillTimeSeries(usageTimeSeries(items, "2006-01-02T15:00:00"), start, end, time.Hour, "2006-01-02T15:00:00")
}

func dailyStatisticsFromUsage(items []UsageEntry, start, end time.Time) []TimeSeriesData {
	return fillTimeSeries(usageTimeSeries(items, "2006-01-02T00:00:00"), start, end, 24*time.Hour, "2006-01-02T00:00:00")
}
