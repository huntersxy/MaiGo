package stats

// 模型调用统计口径，逐条统计与小时汇总共用，对齐 services/statistics_usage_rules.py

import (
	"strings"
	"time"
)

const unrecordedTaskName = "未记录"

// UsageKey 一条调用在统计里的全部维度，已归一化
type UsageKey struct {
	RequestType  string
	ProviderName string
	ModelName    string
	TaskName     string
	SessionID    string
}

// UsageMetrics 次数、输入、输出、缓存命中、缓存未命中、花费、有效耗时条数、耗时和、耗时平方和、
// 非空耗时条数、非空耗时和（首页平均响应时间沿用 SQL AVG 口径：含 0 与负数，只忽略空值）
type UsageMetrics struct {
	RequestCount      int64
	PromptTokens      int64
	CompletionTokens  int64
	CacheHitTokens    int64
	CacheMissTokens   int64
	Cost              float64
	DurationCount     int64
	DurationSum       float64
	DurationSumSq     float64
	ResponseTimeCount int64
	ResponseTimeSum   float64
}

// UsageEntry 时间窗口内的一条用量（明细记录或同一小时同维度的汇总行）
type UsageEntry struct {
	Timestamp time.Time
	Key       UsageKey
	Metrics   UsageMetrics
}

func usageBucketStart(value time.Time) time.Time {
	local := value.In(time.Local)
	return time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, time.Local)
}

func ceilHour(value time.Time) time.Time {
	first := usageBucketStart(value)
	if first.Equal(value) {
		return first
	}
	return first.Add(time.Hour)
}

// clampHourRange 时间段 [start, now] 中可以直接用小时汇总的整点区间 [ceil(start), floor(now))；不足一小时返回 false
func clampHourRange(start, now time.Time) (time.Time, time.Time, bool) {
	first := ceilHour(start)
	last := usageBucketStart(now)
	return first, last, first.Before(last)
}

func normalizePromptCacheTokens(promptTokens, hitTokens, missTokens int64) (int64, int64) {
	return normalizeCacheTokens(promptTokens, true, hitTokens, missTokens)
}

type usageRecord struct {
	ID                    int64
	Timestamp             string
	RequestType           string
	ProviderName          string
	ModelAssignName       string
	ModelName             string
	SessionID             string
	PromptTokens          int64
	CompletionTokens      int64
	PromptCacheEnabled    bool
	PromptCacheHitTokens  int64
	PromptCacheMissTokens int64
	Cost                  float64
	TimeCost              *float64
	TaskName              string
}

// normalizeUsageRecord 逐条统计与小时汇总共用的唯一归一化规则
func normalizeUsageRecord(record usageRecord, timestamp time.Time) (UsageKey, UsageMetrics) {
	hitTokens, missTokens := int64(0), int64(0)
	if record.PromptCacheEnabled {
		hitTokens, missTokens = normalizePromptCacheTokens(record.PromptTokens, record.PromptCacheHitTokens, record.PromptCacheMissTokens)
	}
	timeCost := 0.0
	if record.TimeCost != nil {
		timeCost = *record.TimeCost
	}
	validDuration := timeCost > 0
	key := UsageKey{
		RequestType:  firstNonEmpty(record.RequestType, "unknown"),
		ProviderName: firstNonEmpty(record.ProviderName, "unknown"),
		ModelName:    firstNonEmpty(record.ModelAssignName, record.ModelName, "unknown"),
		TaskName:     firstNonEmpty(record.TaskName, unrecordedTaskName),
		SessionID:    strings.TrimSpace(record.SessionID),
	}
	metrics := UsageMetrics{
		RequestCount:      1,
		PromptTokens:      record.PromptTokens,
		CompletionTokens:  record.CompletionTokens,
		CacheHitTokens:    hitTokens,
		CacheMissTokens:   missTokens,
		Cost:              record.Cost,
		ResponseTimeSum:   0,
		ResponseTimeCount: 0,
	}
	if validDuration {
		metrics.DurationCount = 1
		metrics.DurationSum = timeCost
		metrics.DurationSumSq = timeCost * timeCost
	}
	if record.TimeCost != nil {
		metrics.ResponseTimeCount = 1
		metrics.ResponseTimeSum = *record.TimeCost
	}
	return key, metrics
}

func addUsageMetrics(target *UsageMetrics, source UsageMetrics) {
	target.RequestCount += source.RequestCount
	target.PromptTokens += source.PromptTokens
	target.CompletionTokens += source.CompletionTokens
	target.CacheHitTokens += source.CacheHitTokens
	target.CacheMissTokens += source.CacheMissTokens
	target.Cost += source.Cost
	target.DurationCount += source.DurationCount
	target.DurationSum += source.DurationSum
	target.DurationSumSq += source.DurationSumSq
	target.ResponseTimeCount += source.ResponseTimeCount
	target.ResponseTimeSum += source.ResponseTimeSum
}
