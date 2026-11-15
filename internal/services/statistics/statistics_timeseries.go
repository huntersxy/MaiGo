package stats

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"MaiGo/internal/webui/schemas"
)

const (
	timeSeriesDialectPostgres = "postgres"
	timeSeriesDialectSQLite   = "sqlite"
)

// TimeSeriesData 定义在 internal/webui/schemas，这里保留别名
type TimeSeriesData = schemas.TimeSeriesData

type timeSeriesRow struct {
	Timestamp       string
	Requests        int64
	Cost            float64
	InputTokens     int64
	OutputTokens    int64
	CacheHitTokens  int64
	CacheMissTokens int64
}

type timeSeriesQuery struct {
	SQL  string
	Args []any
}

// GetHourlyStatistics 按本地小时聚合 LLM 请求并补齐空桶
func GetHourlyStatistics(ctx context.Context, db *gorm.DB, start, end time.Time) ([]TimeSeriesData, error) {
	if db == nil {
		return nil, nil
	}
	if err := EnsureSchema(ctx, db); err != nil {
		return nil, err
	}
	query := buildTimeSeriesQuery(timeSeriesDialect(db), start.Location(), start, end, true)
	rows, err := queryTimeSeries(ctx, db, query)
	if err != nil {
		return nil, err
	}
	return fillTimeSeries(rows, start, end, time.Hour, "2006-01-02T15:00:00"), nil
}

// GetDailyStatistics 按本地天聚合 LLM 请求并补齐空桶
func GetDailyStatistics(ctx context.Context, db *gorm.DB, start, end time.Time) ([]TimeSeriesData, error) {
	if db == nil {
		return nil, nil
	}
	if err := EnsureSchema(ctx, db); err != nil {
		return nil, err
	}
	query := buildTimeSeriesQuery(timeSeriesDialect(db), start.Location(), start, end, false)
	rows, err := queryTimeSeries(ctx, db, query)
	if err != nil {
		return nil, err
	}
	return fillTimeSeries(rows, start, end, 24*time.Hour, "2006-01-02T00:00:00"), nil
}

func timeSeriesDialect(db *gorm.DB) string {
	if db == nil || db.Dialector == nil {
		return timeSeriesDialectPostgres
	}
	if strings.EqualFold(strings.TrimSpace(db.Dialector.Name()), timeSeriesDialectSQLite) {
		return timeSeriesDialectSQLite
	}
	return timeSeriesDialectPostgres
}

// buildTimeSeriesQuery 按方言生成时间序列聚合 SQL
// PostgreSQL 用 to_char 将 timestamptz 逐行转换到目标时区，SQLite 按存储的本地墙钟直接格式化
func buildTimeSeriesQuery(dialect string, loc *time.Location, start, end time.Time, hourly bool) timeSeriesQuery {
	if loc == nil {
		loc = time.Local
	}
	args := make([]any, 0, 3)
	var bucket string
	where := "timestamp >= ? AND timestamp <= ?"
	if dialect == timeSeriesDialectSQLite {
		sqliteFormat := "%Y-%m-%dT00:00:00"
		if hourly {
			sqliteFormat = "%Y-%m-%dT%H:00:00"
		}
		// modernc 把 time.Time 写成 "2026-11-05 10:30:00 +0800 CST"，先截取可解析的墙钟前缀
		sqliteTimestamp := sqliteTimestampExpression("timestamp")
		bucket = "strftime('" + sqliteFormat + "', " + sqliteTimestamp + ")"
		where = sqliteTimestamp + " >= ? AND " + sqliteTimestamp + " <= ?"
		args = append(args,
			start.In(loc).Format("2006-01-02 15:04:05"),
			end.In(loc).Format("2006-01-02 15:04:05"),
		)
	} else {
		format := "YYYY-MM-DD\"T00:00:00\""
		if hourly {
			format = "YYYY-MM-DD\"T\"HH24:00:00"
		}
		if zone := postgresTimeZoneName(loc); zone != "" {
			// 时区按行生效，跨 DST 时不会像整段偏移那样把所有时间戳当成固定偏移
			bucket = "to_char(timestamp AT TIME ZONE (?::text), '" + format + "')"
			args = append(args, zone)
		} else {
			// 拿不到 IANA 名称时退回数据库会话时区，仍由 PostgreSQL 逐行处理 DST
			bucket = "to_char(timestamp, '" + format + "')"
		}
		args = append(args, start, end)
	}
	query := `SELECT ` + bucket + ` AS timestamp,
		COUNT(*) AS requests,
		COALESCE(SUM(cost), 0) AS cost,
		COALESCE(SUM(prompt_tokens), 0) AS input_tokens,
		COALESCE(SUM(completion_tokens), 0) AS output_tokens,
		COALESCE(SUM(` + timeSeriesCacheHitSQL(dialect) + `), 0) AS cache_hit_tokens,
		COALESCE(SUM(` + timeSeriesCacheMissSQL(dialect) + `), 0) AS cache_miss_tokens
	FROM llm_usage WHERE ` + where + `
	GROUP BY 1 ORDER BY 1`
	return timeSeriesQuery{SQL: query, Args: args}
}

func sqliteTimestampExpression(column string) string {
	return "replace(substr(" + column + ", 1, 19), 'T', ' ')"
}

func postgresTimeZoneName(loc *time.Location) string {
	if loc == nil {
		return ""
	}
	name := strings.TrimSpace(loc.String())
	if name == "" || strings.EqualFold(name, "Local") {
		return ""
	}
	if _, err := time.LoadLocation(name); err != nil {
		return ""
	}
	return name
}

func timeSeriesCacheHitSQL(dialect string) string {
	if dialect == timeSeriesDialectSQLite {
		return `CASE WHEN prompt_cache_enabled THEN max(prompt_cache_hit_tokens, 0) ELSE 0 END`
	}
	return cacheHitSQL
}

func timeSeriesCacheMissSQL(dialect string) string {
	if dialect == timeSeriesDialectSQLite {
		return `CASE WHEN prompt_cache_enabled THEN
			CASE
				WHEN max(prompt_cache_miss_tokens, 0) > 0 THEN max(prompt_cache_miss_tokens, 0)
				WHEN max(prompt_cache_hit_tokens, 0) > 0
					THEN max(prompt_tokens - max(prompt_cache_hit_tokens, 0), 0)
				WHEN prompt_tokens > 0 THEN prompt_tokens
				ELSE 0
			END
		ELSE 0 END`
	}
	return cacheMissSQL
}

func queryTimeSeries(ctx context.Context, db *gorm.DB, query timeSeriesQuery) ([]timeSeriesRow, error) {
	var rows []timeSeriesRow
	if err := db.WithContext(ctx).Raw(query.SQL, query.Args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询时间序列失败: %w", err)
	}
	return rows, nil
}

// fillTimeSeries 按调用方时区的本地墙钟补桶
// Python 用 datetime + timedelta 推进墙钟，跨 DST 不会跳过不存在的整点，也不会重复回拨的整点
func fillTimeSeries(rows []timeSeriesRow, start, end time.Time, step time.Duration, layout string) []TimeSeriesData {
	byTimestamp := make(map[string]timeSeriesRow, len(rows))
	for _, row := range rows {
		byTimestamp[row.Timestamp] = row
	}
	loc := start.Location()
	if loc == nil {
		loc = time.Local
	}
	startLocal := start.In(loc)
	endLocal := end.In(loc)
	var cursor, endCursor time.Time
	if step >= 24*time.Hour {
		cursor = time.Date(startLocal.Year(), startLocal.Month(), startLocal.Day(), 0, 0, 0, 0, time.UTC)
		endCursor = time.Date(endLocal.Year(), endLocal.Month(), endLocal.Day(), 0, 0, 0, 0, time.UTC)
	} else {
		cursor = time.Date(startLocal.Year(), startLocal.Month(), startLocal.Day(), startLocal.Hour(), 0, 0, 0, time.UTC)
		endCursor = time.Date(endLocal.Year(), endLocal.Month(), endLocal.Day(), endLocal.Hour(), 0, 0, 0, time.UTC)
	}
	out := make([]TimeSeriesData, 0)
	for !cursor.After(endCursor) {
		key := cursor.Format(layout)
		item := TimeSeriesData{Timestamp: key}
		if row, ok := byTimestamp[key]; ok {
			item.Requests = row.Requests
			item.Cost = row.Cost
			item.Tokens = row.InputTokens + row.OutputTokens
			item.InputTokens = row.InputTokens
			item.OutputTokens = row.OutputTokens
			item.CacheHitTokens = row.CacheHitTokens
			item.CacheMissTokens = row.CacheMissTokens
		}
		out = append(out, item)
		cursor = cursor.Add(step)
	}
	return out
}
