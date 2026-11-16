package stats

// 模型调用小时汇总，对齐 services/statistics_usage_hourly.py
//
// 每条 llm_usage 记录按统计口径归一化后累加到 (小时, 请求类型, 提供商, 模型, 任务, 聊天) 一行。
// 回填与增量共用游标；汇总写入与游标前进同一事务提交。汇总追上最新记录前统计仍读明细。

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	usageHourlyCursorSource = "usage_hourly_v1"
	usageHourlyChunkSize    = 20000
	usageHourlyTimeBudget   = 20 * time.Second
)

// UsageHourlyState ready 表示汇总已追上本次刷新时的最新记录；cursor 为已汇总到的最大 id
type UsageHourlyState struct {
	Ready  bool
	Cursor int64
}

var (
	usageHourlyRefreshMu sync.Mutex
	usageHourlyRetryMu   sync.Mutex
	usageHourlyFailures  int
	usageHourlyRetryAt   time.Time
)

// RefreshUsageHourly 把游标之后的新记录汇总进小时表；失败不抛出，按指数退避推迟重试
func RefreshUsageHourly(ctx context.Context, db *gorm.DB, timeBudget time.Duration) UsageHourlyState {
	if db == nil {
		return UsageHourlyState{}
	}
	usageHourlyRefreshMu.Lock()
	defer usageHourlyRefreshMu.Unlock()

	usageHourlyRetryMu.Lock()
	retryAt := usageHourlyRetryAt
	usageHourlyRetryMu.Unlock()
	if time.Now().Before(retryAt) {
		return UsageHourlyState{Ready: false, Cursor: safeReadUsageHourlyCursor(ctx, db)}
	}

	if timeBudget <= 0 {
		timeBudget = usageHourlyTimeBudget
	}
	deadline := time.Now().Add(timeBudget)
	processedTotal := 0
	for {
		processed, err := aggregateUsageNextChunk(ctx, db, usageHourlyChunkSize)
		if err != nil {
			recordUsageHourlyFailure(err)
			return UsageHourlyState{Ready: false, Cursor: safeReadUsageHourlyCursor(ctx, db)}
		}
		processedTotal += processed
		if processed < usageHourlyChunkSize || time.Now().After(deadline) {
			break
		}
	}
	cursor, maxID, err := readUsageHourlyProgress(ctx, db)
	if err != nil {
		recordUsageHourlyFailure(err)
		return UsageHourlyState{Ready: false, Cursor: safeReadUsageHourlyCursor(ctx, db)}
	}
	usageHourlyRetryMu.Lock()
	usageHourlyFailures = 0
	usageHourlyRetryAt = time.Time{}
	usageHourlyRetryMu.Unlock()
	ready := cursor >= maxID
	if processedTotal > 0 && !ready {
		zap.L().Info("模型调用小时汇总回填中",
			zap.Int64("cursor", cursor), zap.Int64("max_id", maxID))
	}
	return UsageHourlyState{Ready: ready, Cursor: cursor}
}

func recordUsageHourlyFailure(err error) {
	usageHourlyRetryMu.Lock()
	usageHourlyFailures++
	failures := usageHourlyFailures
	delay := 30 * time.Second * time.Duration(1<<(min(failures-1, 5)))
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	usageHourlyRetryAt = time.Now().Add(delay)
	usageHourlyRetryMu.Unlock()
	zap.L().Warn("模型调用小时汇总失败，本轮统计改读明细",
		zap.Int("failures", failures), zap.Duration("retry_after", delay), zap.Error(err))
}

func usageTimestampSelect(db *gorm.DB) string {
	if timeSeriesDialect(db) == timeSeriesDialectSQLite {
		// 取原始墙钟文本，避免驱动把无时区时间当成 UTC 再格式化
		return sqliteTimestampExpression("timestamp") + " AS timestamp"
	}
	return "timestamp"
}

func aggregateUsageNextChunk(ctx context.Context, db *gorm.DB, chunkSize int) (int, error) {
	if err := EnsureSchema(ctx, db); err != nil {
		return 0, err
	}
	processed := 0
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cursor, err := loadAggregationCursor(tx, usageHourlyCursorSource)
		if err != nil {
			return err
		}
		var rows []usageRecord
		query := `SELECT id, ` + usageTimestampSelect(tx) + `, request_type, model_api_provider_name AS provider_name,
			model_assign_name, model_name, session_id, prompt_tokens, completion_tokens,
			prompt_cache_enabled, prompt_cache_hit_tokens, prompt_cache_miss_tokens, cost, time_cost, task_name
			FROM llm_usage WHERE id > ? ORDER BY id LIMIT ?`
		if err := tx.Raw(query, cursor, chunkSize).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		type bucketKey struct {
			Bucket time.Time
			Key    UsageKey
		}
		grouped := map[bucketKey]*UsageMetrics{}
		for _, row := range rows {
			timestamp, err := parseSQLiteDetailedTime(row.Timestamp)
			if err != nil {
				// 没有时间戳的记录不进入任何统计时段
				continue
			}
			key, metrics := normalizeUsageRecord(row, timestamp)
			bucket := usageBucketStart(timestamp)
			groupKey := bucketKey{Bucket: bucket, Key: key}
			total := grouped[groupKey]
			if total == nil {
				copy := metrics
				grouped[groupKey] = &copy
				continue
			}
			addUsageMetrics(total, metrics)
		}
		for groupKey, metrics := range grouped {
			if err := upsertUsageHourly(tx, groupKey.Bucket, groupKey.Key, *metrics); err != nil {
				return err
			}
		}
		lastID := rows[len(rows)-1].ID
		if err := writeAggregationCursor(tx, usageHourlyCursorSource, lastID); err != nil {
			return err
		}
		processed = len(rows)
		return nil
	})
	return processed, err
}

func upsertUsageHourly(tx *gorm.DB, bucket time.Time, key UsageKey, metrics UsageMetrics) error {
	query := `INSERT INTO statistics_usage_hourly(
		bucket_time, request_type, provider_name, model_name, task_name, session_id,
		request_count, prompt_tokens, completion_tokens, cache_hit_tokens, cache_miss_tokens, cost,
		duration_count, duration_sum, duration_sum_sq, response_time_count, response_time_sum
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(bucket_time, request_type, provider_name, model_name, task_name, session_id) DO UPDATE SET
		request_count = statistics_usage_hourly.request_count + excluded.request_count,
		prompt_tokens = statistics_usage_hourly.prompt_tokens + excluded.prompt_tokens,
		completion_tokens = statistics_usage_hourly.completion_tokens + excluded.completion_tokens,
		cache_hit_tokens = statistics_usage_hourly.cache_hit_tokens + excluded.cache_hit_tokens,
		cache_miss_tokens = statistics_usage_hourly.cache_miss_tokens + excluded.cache_miss_tokens,
		cost = statistics_usage_hourly.cost + excluded.cost,
		duration_count = statistics_usage_hourly.duration_count + excluded.duration_count,
		duration_sum = statistics_usage_hourly.duration_sum + excluded.duration_sum,
		duration_sum_sq = statistics_usage_hourly.duration_sum_sq + excluded.duration_sum_sq,
		response_time_count = statistics_usage_hourly.response_time_count + excluded.response_time_count,
		response_time_sum = statistics_usage_hourly.response_time_sum + excluded.response_time_sum`
	bucketValue := any(bucket)
	if timeSeriesDialect(tx) == timeSeriesDialectSQLite {
		// SQLite 统计表沿用 Python 的本地墙钟文本，避免驱动按系统时区重解释
		bucketValue = bucket.In(time.Local).Format("2006-01-02 15:04:05")
	}
	return tx.Exec(query,
		bucketValue, key.RequestType, key.ProviderName, key.ModelName, key.TaskName, key.SessionID,
		metrics.RequestCount, metrics.PromptTokens, metrics.CompletionTokens, metrics.CacheHitTokens,
		metrics.CacheMissTokens, metrics.Cost, metrics.DurationCount, metrics.DurationSum, metrics.DurationSumSq,
		metrics.ResponseTimeCount, metrics.ResponseTimeSum,
	).Error
}

func writeAggregationCursor(tx *gorm.DB, source string, lastID int64) error {
	return tx.Exec(`INSERT INTO statistics_aggregation_cursors(source_name, last_processed_id, updated_at)
		VALUES(?, ?, ?)
		ON CONFLICT(source_name) DO UPDATE SET
			last_processed_id = excluded.last_processed_id,
			updated_at = excluded.updated_at`, source, lastID, time.Now().UTC()).Error
}

func readUsageHourlyProgress(ctx context.Context, db *gorm.DB) (int64, int64, error) {
	cursor, err := loadAggregationCursor(db.WithContext(ctx), usageHourlyCursorSource)
	if err != nil {
		return 0, 0, err
	}
	var maxID int64
	if err := db.WithContext(ctx).Raw(`SELECT COALESCE(MAX(id), 0) FROM llm_usage`).Scan(&maxID).Error; err != nil {
		return 0, 0, err
	}
	return cursor, maxID, nil
}

func safeReadUsageHourlyCursor(ctx context.Context, db *gorm.DB) int64 {
	cursor, _, err := readUsageHourlyProgress(ctx, db)
	if err != nil {
		return 0
	}
	return cursor
}

// UsageRecordsBetween 读取 [start, end) 的调用明细，供时间段两端不满一小时的部分使用
func UsageRecordsBetween(ctx context.Context, db *gorm.DB, start, end time.Time) ([]UsageEntry, error) {
	where, args := usageTimeRangeClause(db, start, end)
	query := `SELECT id, ` + usageTimestampSelect(db) + `, request_type, model_api_provider_name AS provider_name,
		model_assign_name, model_name, session_id, prompt_tokens, completion_tokens,
		prompt_cache_enabled, prompt_cache_hit_tokens, prompt_cache_miss_tokens, cost, time_cost, task_name
		FROM llm_usage WHERE ` + where + ` ORDER BY id`
	return queryUsageEntries(ctx, db, query, args...)
}

// UsageRecordsThrough 读取 [start, end] 的调用明细，供闭区间末端使用
func UsageRecordsThrough(ctx context.Context, db *gorm.DB, start, end time.Time) ([]UsageEntry, error) {
	where, args := usageTimeRangeThroughClause(db, start, end)
	query := `SELECT id, ` + usageTimestampSelect(db) + `, request_type, model_api_provider_name AS provider_name,
		model_assign_name, model_name, session_id, prompt_tokens, completion_tokens,
		prompt_cache_enabled, prompt_cache_hit_tokens, prompt_cache_miss_tokens, cost, time_cost, task_name
		FROM llm_usage WHERE ` + where + ` ORDER BY id`
	return queryUsageEntries(ctx, db, query, args...)
}

func usageTimeRangeClause(db *gorm.DB, start, end time.Time) (string, []any) {
	if timeSeriesDialect(db) == timeSeriesDialectSQLite {
		expression := sqliteTimestampExpression("timestamp")
		return expression + " >= ? AND " + expression + " < ?", []any{
			start.In(time.Local).Format("2006-01-02 15:04:05"),
			end.In(time.Local).Format("2006-01-02 15:04:05"),
		}
	}
	return "timestamp >= ? AND timestamp < ?", []any{start, end}
}

func usageTimeRangeThroughClause(db *gorm.DB, start, end time.Time) (string, []any) {
	if timeSeriesDialect(db) == timeSeriesDialectSQLite {
		expression := sqliteTimestampExpression("timestamp")
		return expression + " >= ? AND " + expression + " <= ?", []any{
			start.In(time.Local).Format("2006-01-02 15:04:05"),
			end.In(time.Local).Format("2006-01-02 15:04:05"),
		}
	}
	return "timestamp >= ? AND timestamp <= ?", []any{start, end}
}

func usageBucketRangeClause(db *gorm.DB, startBucket, endBucket time.Time) (string, []any) {
	if timeSeriesDialect(db) == timeSeriesDialectSQLite {
		return "bucket_time >= ? AND bucket_time < ?", []any{
			startBucket.In(time.Local).Format("2006-01-02 15:04:05"),
			endBucket.In(time.Local).Format("2006-01-02 15:04:05"),
		}
	}
	return "bucket_time >= ? AND bucket_time < ?", []any{startBucket, endBucket}
}

// UsageRecordsAfterID 读取游标之后、还没进入汇总的记录
func UsageRecordsAfterID(ctx context.Context, db *gorm.DB, cursor int64) ([]UsageEntry, error) {
	query := `SELECT id, ` + usageTimestampSelect(db) + `, request_type, model_api_provider_name AS provider_name,
		model_assign_name, model_name, session_id, prompt_tokens, completion_tokens,
		prompt_cache_enabled, prompt_cache_hit_tokens, prompt_cache_miss_tokens, cost, time_cost, task_name
		FROM llm_usage WHERE id > ? ORDER BY id`
	return queryUsageEntries(ctx, db, query, cursor)
}

func queryUsageEntries(ctx context.Context, db *gorm.DB, query string, args ...any) ([]UsageEntry, error) {
	var rows []usageRecord
	if err := db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	entries := make([]UsageEntry, 0, len(rows))
	for _, row := range rows {
		timestamp, err := parseSQLiteDetailedTime(row.Timestamp)
		if err != nil {
			continue
		}
		key, metrics := normalizeUsageRecord(row, timestamp)
		entries = append(entries, UsageEntry{Timestamp: timestamp, Key: key, Metrics: metrics})
	}
	return entries, nil
}

func usageBucketSelect(db *gorm.DB) string {
	if timeSeriesDialect(db) == timeSeriesDialectSQLite {
		return "strftime('%Y-%m-%d %H:%M:%S', bucket_time)"
	}
	if zone := postgresTimeZoneName(time.Local); zone != "" {
		return "to_char(bucket_time AT TIME ZONE '" + zone + "', 'YYYY-MM-DD\"T\"HH24:MI:SS')"
	}
	return "to_char(bucket_time, 'YYYY-MM-DD\"T\"HH24:MI:SS')"
}

// UsageHourlyRows 读取 [startBucket, endBucket) 的小时汇总行
func UsageHourlyRows(ctx context.Context, db *gorm.DB, startBucket, endBucket time.Time) ([]UsageEntry, error) {
	bucketRange, args := usageBucketRangeClause(db, startBucket, endBucket)
	query := `SELECT ` + usageBucketSelect(db) + ` AS bucket_time, request_type, provider_name, model_name, task_name, session_id,
		request_count, prompt_tokens, completion_tokens, cache_hit_tokens, cache_miss_tokens, cost,
		duration_count, duration_sum, duration_sum_sq, response_time_count, response_time_sum
		FROM statistics_usage_hourly WHERE ` + bucketRange + ` ORDER BY bucket_time`
	var rows []struct {
		BucketTime        string
		RequestType       string
		ProviderName      string
		ModelName         string
		TaskName          string
		SessionID         string
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
	if err := db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	entries := make([]UsageEntry, 0, len(rows))
	for _, row := range rows {
		timestamp, err := parseSQLiteDetailedTime(row.BucketTime)
		if err != nil {
			continue
		}
		entries = append(entries, UsageEntry{
			Timestamp: timestamp,
			Key: UsageKey{
				RequestType: row.RequestType, ProviderName: row.ProviderName, ModelName: row.ModelName,
				TaskName: row.TaskName, SessionID: row.SessionID,
			},
			Metrics: UsageMetrics{
				RequestCount: row.RequestCount, PromptTokens: row.PromptTokens, CompletionTokens: row.CompletionTokens,
				CacheHitTokens: row.CacheHitTokens, CacheMissTokens: row.CacheMissTokens, Cost: row.Cost,
				DurationCount: row.DurationCount, DurationSum: row.DurationSum, DurationSumSq: row.DurationSumSq,
				ResponseTimeCount: row.ResponseTimeCount, ResponseTimeSum: row.ResponseTimeSum,
			},
		})
	}
	return entries, nil
}

// UsageDurationMoment 按请求类型、提供商、模型、任务汇总的有效耗时矩
type UsageDurationMoment struct {
	RequestType  string
	ProviderName string
	ModelName    string
	TaskName     string
	Count        int64
	Sum          float64
	SumSq        float64
}

// UsageDurationMoments 汇总 startBucket 之后全部小时桶的有效耗时矩
func UsageDurationMoments(ctx context.Context, db *gorm.DB, startBucket time.Time) ([]UsageDurationMoment, error) {
	query := `SELECT request_type, provider_name, model_name, task_name,
		COALESCE(SUM(duration_count),0) AS count, COALESCE(SUM(duration_sum),0) AS sum,
		COALESCE(SUM(duration_sum_sq),0) AS sum_sq
		FROM statistics_usage_hourly WHERE bucket_time >= ? AND duration_count > 0
		GROUP BY request_type, provider_name, model_name, task_name`
	if err := EnsureSchema(ctx, db); err != nil {
		return nil, err
	}
	var rows []UsageDurationMoment
	bound := any(startBucket)
	if timeSeriesDialect(db) == timeSeriesDialectSQLite {
		bound = startBucket.In(time.Local).Format("2006-01-02 15:04:05")
	}
	if err := db.WithContext(ctx).Raw(query, bound).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// WindowUsage 时间窗口内的全部用量：整点区间读汇总，两端与刷新后新记录读明细
func WindowUsage(ctx context.Context, db *gorm.DB, start, end time.Time, endInclusive bool, cursor int64) ([]UsageEntry, error) {
	upper := end
	if endInclusive {
		upper = end.Add(time.Microsecond)
	}
	first, last, ok := clampHourRange(start, upper)
	if !ok {
		if endInclusive {
			return UsageRecordsThrough(ctx, db, start, end)
		}
		return UsageRecordsBetween(ctx, db, start, upper)
	}
	items, err := UsageHourlyRows(ctx, db, first, last)
	if err != nil {
		return nil, err
	}
	if start.Before(first) {
		records, err := UsageRecordsBetween(ctx, db, start, first)
		if err != nil {
			return nil, err
		}
		items = append(items, records...)
	}
	if endInclusive {
		records, err := UsageRecordsThrough(ctx, db, last, end)
		if err != nil {
			return nil, err
		}
		items = append(items, records...)
	} else {
		records, err := UsageRecordsBetween(ctx, db, last, upper)
		if err != nil {
			return nil, err
		}
		items = append(items, records...)
	}
	after, err := UsageRecordsAfterID(ctx, db, cursor)
	if err != nil {
		return nil, err
	}
	for _, entry := range after {
		if !entry.Timestamp.Before(first) && entry.Timestamp.Before(last) {
			items = append(items, entry)
		}
	}
	return items, nil
}
