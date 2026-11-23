package stats

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"MaiGo/internal/webui/schemas"
)

// 统计响应类型定义在 internal/webui/schemas（对齐 Python src/webui/schemas/statistics.py），
// 这里保留别名，使服务层与 WebUI 共用同一份契约，且 services 包对外 API 不变。
type (
	StatisticsSummary = schemas.StatisticsSummary
	ModelStatistics   = schemas.ModelStatistics
	RecentActivity    = schemas.RecentActivity
	DashboardData     = schemas.DashboardData
)

type summaryRow struct {
	TotalRequests       int64
	TotalCost           float64
	InputTokens         int64
	OutputTokens        int64
	CacheHitTokens      int64
	CacheMissTokens     int64
	CoreCacheHitTokens  int64
	CoreCacheMissTokens int64
	AvgResponseTime     float64
}

type modelStatsRow struct {
	ModelName       string
	RequestCount    int64
	TotalCost       float64
	InputTokens     int64
	OutputTokens    int64
	CacheHitTokens  int64
	CacheMissTokens int64
	AvgResponseTime float64
}

// noticeMessageFilterSQL 排除平台通知行；Go 的 NapCat 戳一戳通知 id 为 napcat-notice-*
const noticeMessageFilterSQL = " AND COALESCE(external_message_id, '') <> 'notice'" +
	" AND COALESCE(external_message_id, '') NOT LIKE 'napcat-notice-%'"

// GetStatisticsSummary 查询时间范围内的统计摘要
func GetStatisticsSummary(ctx context.Context, db *gorm.DB, start, end time.Time) (StatisticsSummary, error) {
	var summary StatisticsSummary
	if db == nil {
		return summary, nil
	}
	if err := EnsureSchema(ctx, db); err != nil {
		return summary, err
	}
	row, err := querySummaryRow(ctx, db, start, end)
	if err != nil {
		return summary, err
	}
	summary.TotalRequests = row.TotalRequests
	summary.TotalCost = row.TotalCost
	summary.InputTokens = row.InputTokens
	summary.OutputTokens = row.OutputTokens
	summary.TotalTokens = row.InputTokens + row.OutputTokens
	summary.CacheHitTokens = row.CacheHitTokens
	summary.CacheMissTokens = row.CacheMissTokens
	summary.CacheHitRate = calculateCacheHitRate(row.CacheHitTokens, row.CacheMissTokens)
	summary.CoreCacheHitTokens = row.CoreCacheHitTokens
	summary.CoreCacheMissTokens = row.CoreCacheMissTokens
	summary.CoreCacheHitRate = calculateCacheHitRate(row.CoreCacheHitTokens, row.CoreCacheMissTokens)
	summary.AvgResponseTime = row.AvgResponseTime
	if err := applySummaryActivity(ctx, db, start, end, &summary); err != nil {
		return summary, err
	}
	return summary, nil
}

// applySummaryActivity 填充在线时长、每小时均值与消息计数，供摘要 SQL 与小时汇总路径共用
func applySummaryActivity(ctx context.Context, db *gorm.DB, start, end time.Time, summary *StatisticsSummary) error {
	online, err := statisticsOnlineSeconds(ctx, db, start, end)
	if err != nil {
		return err
	}
	summary.OnlineTime = online
	if summary.OnlineTime > 0 {
		hours := summary.OnlineTime / 3600
		summary.CostPerHour = summary.TotalCost / hours
		summary.TokensPerHour = float64(summary.TotalTokens) / hours
	}
	where, args := statisticsRangeClause(db, "COALESCE(received_at, timestamp)", start, end)
	var counts struct {
		TotalMessages int64
		TotalReplies  int64
	}
	if err := db.WithContext(ctx).Raw(`SELECT COUNT(*) AS total_messages, COUNT(reply_to) AS total_replies
		FROM mai_messages WHERE `+where+noticeMessageFilterSQL, args...).Scan(&counts).Error; err != nil {
		return fmt.Errorf("查询消息与回复总数失败: %w", err)
	}
	summary.TotalMessages = counts.TotalMessages
	summary.TotalReplies = counts.TotalReplies
	return nil
}

func querySummaryRow(ctx context.Context, db *gorm.DB, start, end time.Time) (summaryRow, error) {
	var row summaryRow
	hitSQL, missSQL := summaryCacheSQL(db)
	where, args := statisticsRangeClause(db, "timestamp", start, end)
	query := `SELECT
		COUNT(*) AS total_requests,
		COALESCE(SUM(cost), 0) AS total_cost,
		COALESCE(SUM(prompt_tokens), 0) AS input_tokens,
		COALESCE(SUM(completion_tokens), 0) AS output_tokens,
		COALESCE(SUM(` + hitSQL + `), 0) AS cache_hit_tokens,
		COALESCE(SUM(` + missSQL + `), 0) AS cache_miss_tokens,
		COALESCE(SUM(CASE WHEN task_name = 'planner' THEN ` + hitSQL + ` ELSE 0 END), 0) AS core_cache_hit_tokens,
		COALESCE(SUM(CASE WHEN task_name = 'planner' THEN ` + missSQL + ` ELSE 0 END), 0) AS core_cache_miss_tokens,
		COALESCE(AVG(time_cost), 0) AS avg_response_time
	FROM llm_usage WHERE ` + where
	if err := db.WithContext(ctx).Raw(query, args...).Scan(&row).Error; err != nil {
		return row, fmt.Errorf("查询统计摘要失败: %w", err)
	}
	return row, nil
}

func summaryCacheSQL(db *gorm.DB) (string, string) {
	dialect := timeSeriesDialect(db)
	return timeSeriesCacheHitSQL(dialect), timeSeriesCacheMissSQL(dialect)
}

func statisticsRangeClause(db *gorm.DB, column string, start, end time.Time) (string, []any) {
	if timeSeriesDialect(db) == timeSeriesDialectSQLite {
		expression := sqliteTimestampExpression(column)
		return expression + " >= ? AND " + expression + " <= ?", []any{
			start.In(time.Local).Format("2006-01-02 15:04:05"),
			end.In(time.Local).Format("2006-01-02 15:04:05"),
		}
	}
	return column + " >= ? AND " + column + " <= ?", []any{start, end}
}

const (
	cacheHitSQL  = `CASE WHEN prompt_cache_enabled THEN GREATEST(prompt_cache_hit_tokens, 0) ELSE 0 END`
	cacheMissSQL = `CASE WHEN prompt_cache_enabled THEN
		CASE
			WHEN GREATEST(prompt_cache_miss_tokens, 0) > 0 THEN GREATEST(prompt_cache_miss_tokens, 0)
			WHEN GREATEST(prompt_cache_hit_tokens, 0) > 0
				THEN GREATEST(prompt_tokens - GREATEST(prompt_cache_hit_tokens, 0), 0)
			WHEN prompt_tokens > 0 THEN prompt_tokens
			ELSE 0
		END
	ELSE 0 END`
)

// modelStatisticsOrderClause 生成模型统计排序：请求数降序，同次数按模型名升序
//
// SQLite 的 BINARY collation 就是 UTF-8 字节序；PG 默认 collation 对大小写/非 ASCII 排序
// 可能与字节序不同，因此 PG 显式 COLLATE "C"，与 SQLite BINARY 保持一致
func modelStatisticsOrderClause(db *gorm.DB) string {
	expression := "COALESCE(model_assign_name, model_name, 'unknown')"
	if timeSeriesDialect(db) == timeSeriesDialectSQLite {
		return "ORDER BY request_count DESC, " + expression + " ASC"
	}
	return "ORDER BY request_count DESC, " + expression + ` COLLATE "C" ASC`
}

// GetModelStatistics 按模型聚合最近时间范围内的调用，取调用次数前 10
func GetModelStatistics(ctx context.Context, db *gorm.DB, start, end time.Time) ([]ModelStatistics, error) {
	if db == nil {
		return nil, nil
	}
	if err := EnsureSchema(ctx, db); err != nil {
		return nil, err
	}
	var rows []modelStatsRow
	hitSQL, missSQL := summaryCacheSQL(db)
	where, args := statisticsRangeClause(db, "timestamp", start, end)
	// 对齐 Python statistics_service.py：GROUP BY COALESCE(model_assign_name, model_name, 'unknown')，
	// 按调用次数降序、模型名升序取前 10，小时汇总路径使用同一排序
	query := `SELECT
		COALESCE(model_assign_name, model_name, 'unknown') AS model_name,
		COUNT(*) AS request_count,
		COALESCE(SUM(cost), 0) AS total_cost,
		COALESCE(SUM(prompt_tokens), 0) AS input_tokens,
		COALESCE(SUM(completion_tokens), 0) AS output_tokens,
		COALESCE(SUM(` + hitSQL + `), 0) AS cache_hit_tokens,
		COALESCE(SUM(` + missSQL + `), 0) AS cache_miss_tokens,
		COALESCE(AVG(time_cost), 0) AS avg_response_time
	FROM llm_usage WHERE ` + where + `
	GROUP BY 1
	` + modelStatisticsOrderClause(db) + `
	LIMIT 10`
	if err := db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询模型统计失败: %w", err)
	}
	out := make([]ModelStatistics, 0, len(rows))
	for _, row := range rows {
		modelName := row.ModelName
		if strings.TrimSpace(modelName) == "" {
			modelName = "unknown"
		}
		out = append(out, ModelStatistics{
			ModelName:       modelName,
			RequestCount:    row.RequestCount,
			TotalCost:       row.TotalCost,
			TotalTokens:     row.InputTokens + row.OutputTokens,
			InputTokens:     row.InputTokens,
			OutputTokens:    row.OutputTokens,
			CacheHitTokens:  row.CacheHitTokens,
			CacheMissTokens: row.CacheMissTokens,
			CacheHitRate:    calculateCacheHitRate(row.CacheHitTokens, row.CacheMissTokens),
			AvgResponseTime: row.AvgResponseTime,
		})
	}
	return out, nil
}

// GetRecentActivity 查询最近 limit 条 LLM 调用
func GetRecentActivity(ctx context.Context, db *gorm.DB, start, end time.Time, limit int) ([]RecentActivity, error) {
	if db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	if err := EnsureSchema(ctx, db); err != nil {
		return nil, err
	}
	var rows []struct {
		Timestamp        time.Time
		ModelName        string
		ModelAssignName  string
		RequestType      string
		PromptTokens     int64
		CompletionTokens int64
		CacheEnabled     bool  `gorm:"column:prompt_cache_enabled"`
		CacheHitTokens   int64 `gorm:"column:prompt_cache_hit_tokens"`
		CacheMissTokens  int64 `gorm:"column:prompt_cache_miss_tokens"`
		Cost             float64
		TimeCost         float64
	}
	where, args := statisticsRangeClause(db, "timestamp", start, end)
	args = append(args, limit)
	err := db.WithContext(ctx).Raw(`SELECT timestamp, model_name, model_assign_name, request_type,
		prompt_tokens, completion_tokens, prompt_cache_enabled, prompt_cache_hit_tokens,
		prompt_cache_miss_tokens, cost, time_cost
	FROM llm_usage WHERE `+where+`
	ORDER BY timestamp DESC, id DESC LIMIT ?`, args...).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询最近模型调用失败: %w", err)
	}
	out := make([]RecentActivity, 0, len(rows))
	for _, row := range rows {
		hit, miss := normalizeCacheTokens(row.PromptTokens, row.CacheEnabled, row.CacheHitTokens, row.CacheMissTokens)
		model := strings.TrimSpace(row.ModelAssignName)
		if model == "" {
			model = row.ModelName
		}
		out = append(out, RecentActivity{
			"timestamp":         formatPythonNaiveTime(row.Timestamp),
			"model":             model,
			"request_type":      row.RequestType,
			"tokens":            row.PromptTokens + row.CompletionTokens,
			"input_tokens":      row.PromptTokens,
			"output_tokens":     row.CompletionTokens,
			"cache_hit_tokens":  hit,
			"cache_miss_tokens": miss,
			"cost":              row.Cost,
			"time_cost":         row.TimeCost,
			"status":            nil,
		})
	}
	return out, nil
}

// GetDashboardStatistics 读取缓存或实时计算 WebUI 仪表盘数据
func GetDashboardStatistics(ctx context.Context, db *gorm.DB, hours int, useCache bool, instanceDir string) (DashboardData, error) {
	return GetDashboardStatisticsAt(ctx, db, hours, useCache, instanceDir, time.Now())
}

// GetDashboardStatisticsAt 按给定时刻读取缓存或实时计算仪表盘数据
func GetDashboardStatisticsAt(ctx context.Context, db *gorm.DB, hours int, useCache bool, instanceDir string, now time.Time) (DashboardData, error) {
	if hours <= 0 {
		hours = 24
	}
	if useCache {
		if data, ok := loadDashboardStatisticsCache(instanceDir, hours, now); ok {
			return data, nil
		}
	}
	data, err := computeDashboardStatisticsAt(ctx, db, hours, now, UsageHourlyState{})
	if err != nil {
		return data, err
	}
	if useCache {
		_ = updateDashboardStatisticsCacheEntry(instanceDir, hours, data, now)
	}
	return data, nil
}

// computeDashboardStatisticsAt 按给定时刻计算仪表盘数据；小时汇总就绪时用量部分读汇总
func computeDashboardStatisticsAt(ctx context.Context, db *gorm.DB, hours int, now time.Time, usageHourly UsageHourlyState) (DashboardData, error) {
	start := now.Add(-time.Duration(hours) * time.Hour)
	var usageItems []UsageEntry
	useUsageItems := false
	if usageHourly.Ready && db != nil {
		items, err := WindowUsage(ctx, db, start, now, true, usageHourly.Cursor)
		if err != nil {
			return DashboardData{}, err
		}
		usageItems = items
		useUsageItems = true
	}
	var summary StatisticsSummary
	var err error
	if useUsageItems {
		summary, err = summaryFromUsageItems(ctx, db, start, now, usageItems)
	} else {
		summary, err = GetStatisticsSummary(ctx, db, start, now)
	}
	if err != nil {
		return DashboardData{}, err
	}
	var models []ModelStatistics
	if useUsageItems {
		models = modelStatisticsFromUsage(usageItems)
	} else {
		models, err = GetModelStatistics(ctx, db, start, now)
	}
	if err != nil {
		return DashboardData{}, err
	}
	var hourly []TimeSeriesData
	if useUsageItems {
		hourly = hourlyStatisticsFromUsage(usageItems, start, now)
	} else {
		hourly, err = GetHourlyStatistics(ctx, db, start, now)
	}
	if err != nil {
		return DashboardData{}, err
	}
	online, err := hourlyOnlineSecondsInRange(ctx, db, start, now)
	if err != nil {
		return DashboardData{}, err
	}
	for i := range hourly {
		hourly[i].OnlineSeconds = online[hourly[i].Timestamp]
	}
	var daily []TimeSeriesData
	if useUsageItems {
		daily = dailyStatisticsFromUsage(usageItems, start, now)
	} else {
		daily, err = GetDailyStatistics(ctx, db, start, now)
	}
	if err != nil {
		return DashboardData{}, err
	}
	recent, err := GetRecentActivity(ctx, db, start, now, 10)
	if err != nil {
		return DashboardData{}, err
	}
	return DashboardData{
		Summary:        summary,
		ModelStats:     models,
		HourlyData:     hourly,
		DailyData:      daily,
		RecentActivity: recent,
	}, nil
}

// EmptyDashboardStatistics 构造空仪表盘数据
func EmptyDashboardStatistics() DashboardData {
	return DashboardData{
		ModelStats:     []ModelStatistics{},
		HourlyData:     []TimeSeriesData{},
		DailyData:      []TimeSeriesData{},
		RecentActivity: []RecentActivity{},
	}
}

func calculateCacheHitRate(hit, miss int64) *float64 {
	total := hit + miss
	if total <= 0 {
		return nil
	}
	rate := float64(hit) / float64(total)
	return &rate
}

func normalizeCacheTokens(promptTokens int64, cacheEnabled bool, hitTokens, missTokens int64) (int64, int64) {
	if !cacheEnabled {
		return 0, 0
	}
	hit := max(hitTokens, 0)
	miss := max(missTokens, 0)
	if miss == 0 && hit > 0 {
		miss = max(promptTokens-hit, 0)
	}
	if hit+miss == 0 && promptTokens > 0 {
		miss = promptTokens
	}
	return hit, miss
}

// formatPythonNaiveTime 输出 Python datetime.isoformat() 的 naive 本地时间
// 微秒为零时省略，否则固定六位小数
func formatPythonNaiveTime(t time.Time) string {
	local := t.In(time.Local)
	if local.Nanosecond()/1000 == 0 {
		return local.Format("2006-01-02T15:04:05")
	}
	return local.Format("2006-01-02T15:04:05.000000")
}

// statisticsOnlineSeconds 优先使用 online_time 区间；旧库只有日表时按日折算
func statisticsOnlineSeconds(ctx context.Context, db *gorm.DB, start, end time.Time) (float64, error) {
	if onlineIntervalsEmpty(ctx, db) {
		return dailyOnlineSeconds(ctx, db, start, end)
	}
	intervals, err := queryOnlineIntervals(ctx, db, start, end)
	if err != nil {
		return 0, fmt.Errorf("查询在线区间失败: %w", err)
	}
	return onlineSecondsInRange(intervals, start, end), nil
}

func dailyOnlineSeconds(ctx context.Context, db *gorm.DB, start, end time.Time) (float64, error) {
	startDay := start.In(time.Local).Format("2006-01-02")
	endDay := end.In(time.Local).Format("2006-01-02")
	// 用 map 承接驱动原始值：SQLite 的 DATE 列扫描为 time.Time，PG 的 ::text 为日期字符串
	var rows []map[string]any
	var err error
	if timeSeriesDialect(db) == timeSeriesDialectSQLite {
		err = db.WithContext(ctx).Raw(`SELECT day_date, online_seconds FROM online_time_daily
			WHERE day_date >= ? AND day_date <= ? ORDER BY day_date`, startDay, endDay).Scan(&rows).Error
	} else {
		err = db.WithContext(ctx).Raw(`SELECT day_date::text AS day_date, online_seconds FROM online_time_daily
			WHERE day_date >= ?::date AND day_date <= ?::date ORDER BY day_date`, startDay, endDay).Scan(&rows).Error
	}
	if err != nil {
		return 0, fmt.Errorf("查询按日在线时长失败: %w", err)
	}
	var total float64
	for _, row := range rows {
		dayStart, err := parseDailyOnlineDate(row["day_date"])
		if err != nil {
			return 0, fmt.Errorf("解析按日在线日期失败: %w", err)
		}
		onlineSeconds, err := dailyOnlineSecondsValue(row["online_seconds"])
		if err != nil {
			return 0, fmt.Errorf("解析按日在线秒数失败: %w", err)
		}
		if onlineSeconds <= 0 {
			continue
		}
		overlap := overlapSeconds(dayStart, dayStart.Add(24*time.Hour), start, end)
		if overlap <= 0 {
			continue
		}
		if overlap >= statsSecondsPerDay {
			total += float64(onlineSeconds)
			continue
		}
		total += math.Round(float64(onlineSeconds) * float64(overlap) / statsSecondsPerDay)
	}
	return total, nil
}

// dailyOnlineSecondsValue 把驱动返回的 online_seconds（整数列）转换为 int64
func dailyOnlineSecondsValue(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case float64:
		return int64(typed), nil
	case []byte:
		return strconv.ParseInt(strings.TrimSpace(string(typed)), 10, 64)
	case string:
		return strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
	case nil:
		return 0, fmt.Errorf("online_seconds 为空")
	default:
		return 0, fmt.Errorf("不支持的 online_seconds 类型 %T", value)
	}
}

// parseDailyOnlineDate 把 online_time_daily.day_date 的驱动扫描值规范化为本地日零点
//
// PG 分支用 day_date::text 得到 "2006-01-02"；SQLite 的 DATE 列会被 glebarez/sqlite 扫描成
// time.Time，把它转成 string 会得到 RFC3339，随后解析失败被静默跳过导致按日在线=0
func parseDailyOnlineDate(value any) (time.Time, error) {
	switch typed := value.(type) {
	case time.Time:
		year, month, day := typed.Date()
		return time.Date(year, month, day, 0, 0, 0, 0, time.Local), nil
	case string:
		return parseDailyOnlineDateText(typed)
	case []byte:
		return parseDailyOnlineDateText(string(typed))
	case nil:
		return time.Time{}, fmt.Errorf("day_date 为空")
	default:
		return time.Time{}, fmt.Errorf("不支持的 day_date 类型 %T", value)
	}
}

func parseDailyOnlineDateText(raw string) (time.Time, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return time.Time{}, fmt.Errorf("day_date 为空")
	}
	layouts := []string{
		"2006-01-02",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04:05.999999999",
		time.RFC3339Nano,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999 -0700 MST",
	}
	for _, layout := range layouts {
		parsed, err := time.ParseInLocation(layout, text, time.Local)
		if err != nil {
			continue
		}
		year, month, day := parsed.Date()
		return time.Date(year, month, day, 0, 0, 0, 0, time.Local), nil
	}
	return time.Time{}, fmt.Errorf("无法解析日期 %q", raw)
}

func hourlyOnlineSecondsInRange(ctx context.Context, db *gorm.DB, start, end time.Time) (map[string]float64, error) {
	if onlineIntervalsEmpty(ctx, db) {
		return legacyHourlyOnlineSeconds(ctx, db, start, end)
	}
	intervals, err := queryOnlineIntervals(ctx, db, start, end)
	if err != nil {
		return nil, fmt.Errorf("查询在线区间失败: %w", err)
	}
	return hourlyOnlineSeconds(intervals, start, end), nil
}

func legacyHourlyOnlineSeconds(ctx context.Context, db *gorm.DB, start, end time.Time) (map[string]float64, error) {
	seconds, err := dailyOnlineSeconds(ctx, db, start, end)
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64)
	if seconds <= 0 {
		return out, nil
	}
	duration := end.Sub(start).Hours()
	if duration <= 0 {
		return out, nil
	}
	perHour := seconds / duration
	for cursor := floorLocalHour(start); cursor.Before(end); cursor = cursor.Add(time.Hour) {
		out[cursor.In(time.Local).Format("2006-01-02T15:00:00")] = perHour
	}
	return out, nil
}
