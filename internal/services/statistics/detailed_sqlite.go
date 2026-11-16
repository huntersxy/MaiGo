package stats

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// sqliteDetailedProvider 使用 SQLite 兼容 SQL 读取 MaiBot 业务表
type sqliteDetailedProvider struct {
	db *gorm.DB
}

// NewSQLiteDetailedProvider 创建 SQLite 详细统计查询器
func NewSQLiteDetailedProvider(db *gorm.DB) DetailedProvider {
	return &sqliteDetailedProvider{db: db}
}

func sqliteBoundTime(value time.Time) string {
	return value.In(time.Local).Format("2006-01-02 15:04:05")
}

func sqliteTimeCompare(column string) string {
	// modernc 把时间写成 "2026-11-05 10:30:00 +0800 CST"，截取墙钟前缀后按文本比较
	return "replace(substr(COALESCE(" + column + ", ''), 1, 19), 'T', ' ')"
}

func parseSQLiteDetailedTime(raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, fmt.Errorf("SQLite 时间字段为空")
	}
	layouts := []string{
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02T15:04:05.999999999-07:00",
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析 SQLite 时间 %q", raw)
}

func (p *sqliteDetailedProvider) EarliestStatisticsTime(ctx context.Context) (time.Time, bool, error) {
	if p == nil || p.db == nil {
		return time.Time{}, false, nil
	}
	queries := []struct {
		table string
		query string
	}{
		{"llm_usage", "SELECT MIN(timestamp) FROM llm_usage"},
		{"mai_messages", "SELECT MIN(COALESCE(received_at, timestamp)) FROM mai_messages"},
		{"online_time", "SELECT MIN(start_timestamp) FROM online_time"},
		{"tool_records", "SELECT MIN(timestamp) FROM tool_records"},
	}
	var earliest time.Time
	found := false
	for _, item := range queries {
		if !p.db.Migrator().HasTable(item.table) {
			continue
		}
		var raw sql.NullString
		if err := p.db.WithContext(ctx).Raw(item.query).Scan(&raw).Error; err != nil {
			return time.Time{}, false, fmt.Errorf("查询 %s 最早时间失败: %w", item.table, err)
		}
		if !raw.Valid || strings.TrimSpace(raw.String) == "" {
			continue
		}
		parsed, err := parseSQLiteDetailedTime(raw.String)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("解析 %s 最早时间失败: %w", item.table, err)
		}
		if !found || parsed.Before(earliest) {
			earliest = parsed
			found = true
		}
	}
	return earliest, found, nil
}

func (p *sqliteDetailedProvider) UsageSince(ctx context.Context, start time.Time) ([]DetailedUsage, error) {
	if p == nil || p.db == nil {
		return nil, nil
	}
	query := `SELECT id, ` + sqliteTimestampExpression("timestamp") + ` AS timestamp, request_type, model_api_provider_name AS provider_name,
		model_assign_name, model_name, session_id, task_name, prompt_tokens, completion_tokens,
		prompt_cache_enabled, prompt_cache_hit_tokens, prompt_cache_miss_tokens, cost, time_cost
		FROM llm_usage
		WHERE ` + sqliteTimeCompare("timestamp") + ` >= ?
		ORDER BY id`
	var rows []struct {
		ID                    int64
		Timestamp             string
		RequestType           string
		ProviderName          string
		ModelAssignName       string
		ModelName             string
		SessionID             string
		TaskName              string
		PromptTokens          int64
		CompletionTokens      int64
		PromptCacheEnabled    bool
		PromptCacheHitTokens  int64
		PromptCacheMissTokens int64
		Cost                  float64
		TimeCost              float64
	}
	if err := p.db.WithContext(ctx).Raw(query, sqliteBoundTime(start)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]DetailedUsage, 0, len(rows))
	for _, row := range rows {
		timestamp, err := parseSQLiteDetailedTime(row.Timestamp)
		if err != nil {
			return nil, err
		}
		out = append(out, DetailedUsage{
			ID:                    row.ID,
			Timestamp:             timestamp,
			RequestType:           row.RequestType,
			ProviderName:          row.ProviderName,
			ModelAssignName:       row.ModelAssignName,
			ModelName:             row.ModelName,
			SessionID:             row.SessionID,
			TaskName:              row.TaskName,
			PromptTokens:          row.PromptTokens,
			CompletionTokens:      row.CompletionTokens,
			PromptCacheEnabled:    row.PromptCacheEnabled,
			PromptCacheHitTokens:  row.PromptCacheHitTokens,
			PromptCacheMissTokens: row.PromptCacheMissTokens,
			Cost:                  row.Cost,
			TimeCost:              row.TimeCost,
		})
	}
	return out, nil
}

func (p *sqliteDetailedProvider) UsageHourlyBetween(ctx context.Context, startBucket, endBucket time.Time) ([]UsageEntry, error) {
	if p == nil || p.db == nil {
		return nil, nil
	}
	return UsageHourlyRows(ctx, p.db, startBucket, endBucket)
}

func (p *sqliteDetailedProvider) MessageChatsSince(ctx context.Context, start time.Time) ([]DetailedMessageChat, error) {
	if p == nil || p.db == nil {
		return nil, nil
	}
	query := `WITH scoped AS (
		SELECT ml.id,
			CASE WHEN ml.conversation_kind = 'group' THEN 'g' || CAST(ml.target_id AS TEXT) ELSE 'u' || CAST(ml.target_id AS TEXT) END AS chat_id,
			CASE WHEN ml.conversation_kind = 'group'
				THEN COALESCE(NULLIF(ct.name, ''), '群' || CAST(ml.target_id AS TEXT))
				ELSE COALESCE(NULLIF(ml.user_nickname, ''), NULLIF(ct.name, ''), '用户' || CAST(ml.target_id AS TEXT))
			END AS chat_name,
			COALESCE(ml.received_at, ml.timestamp) AS statistics_time
		FROM mai_messages ml
		LEFT JOIN conversation_targets ct
			ON ct.conversation_kind = ml.conversation_kind AND ct.target_id = ml.target_id
		WHERE (` + sqliteTimeCompare("ml.received_at") + ` >= ?
			OR ` + sqliteTimeCompare("ml.timestamp") + ` >= ?)
	), aggregated AS (
		SELECT chat_id, COUNT(*) AS message_count, MAX(statistics_time) AS latest_timestamp
		FROM scoped GROUP BY chat_id
	)
	SELECT a.chat_id,
		(SELECT s2.chat_name FROM scoped s2 WHERE s2.chat_id = a.chat_id
			ORDER BY s2.statistics_time DESC, s2.id DESC LIMIT 1) AS chat_name,
		a.message_count, ` + sqliteTimestampExpression("a.latest_timestamp") + ` AS latest_timestamp
	FROM aggregated a ORDER BY a.chat_id`
	var rows []struct {
		ChatID          string
		ChatName        string
		MessageCount    int64
		LatestTimestamp string
	}
	bound := sqliteBoundTime(start)
	if err := p.db.WithContext(ctx).Raw(query, bound, bound).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]DetailedMessageChat, 0, len(rows))
	for _, row := range rows {
		latest, err := parseSQLiteDetailedTime(row.LatestTimestamp)
		if err != nil {
			return nil, err
		}
		out = append(out, DetailedMessageChat{ChatID: row.ChatID, ChatName: row.ChatName, MessageCount: row.MessageCount, LatestTimestamp: latest})
	}
	return out, nil
}

func (p *sqliteDetailedProvider) ReplyCountSince(ctx context.Context, start time.Time, toolName string) (int64, error) {
	if p == nil || p.db == nil || !p.db.Migrator().HasTable("tool_records") {
		return 0, nil
	}
	var count int64
	if err := p.db.WithContext(ctx).Raw(
		"SELECT COUNT(*) FROM tool_records WHERE "+sqliteTimeCompare("timestamp")+" >= ? AND tool_name = ?",
		sqliteBoundTime(start), toolName).Scan(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (p *sqliteDetailedProvider) OnlineIntervalsSince(ctx context.Context, start time.Time) ([]OnlineInterval, error) {
	if p == nil || p.db == nil || !p.db.Migrator().HasTable("online_time") {
		return nil, nil
	}
	var rows []struct {
		StartTimestamp string
		EndTimestamp   string
	}
	if err := p.db.WithContext(ctx).Raw(
		"SELECT "+sqliteTimestampExpression("start_timestamp")+" AS start_timestamp, "+
			sqliteTimestampExpression("end_timestamp")+" AS end_timestamp FROM online_time WHERE "+
			sqliteTimeCompare("end_timestamp")+" >= ? ORDER BY start_timestamp",
		sqliteBoundTime(start)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]OnlineInterval, 0, len(rows))
	for _, row := range rows {
		startTime, err := parseSQLiteDetailedTime(row.StartTimestamp)
		if err != nil {
			return nil, err
		}
		endTime, err := parseSQLiteDetailedTime(row.EndTimestamp)
		if err != nil {
			return nil, err
		}
		out = append(out, OnlineInterval{Start: startTime, End: endTime})
	}
	return out, nil
}

func (p *sqliteDetailedProvider) MessagesSince(ctx context.Context, start time.Time) ([]DetailedMessage, error) {
	if p == nil || p.db == nil {
		return nil, nil
	}
	query := `SELECT ` + sqliteTimestampExpression("COALESCE(ml.received_at, ml.timestamp)") + ` AS statistics_time, ml.platform,
		COALESCE(NULLIF(ml.external_user_id, ''), CAST(ml.user_id AS TEXT)) AS user_id,
		ml.conversation_kind, ml.target_id, ml.user_nickname, COALESCE(ct.name, '') AS group_name
		FROM mai_messages ml
		LEFT JOIN conversation_targets ct
			ON ct.conversation_kind = ml.conversation_kind AND ct.target_id = ml.target_id
		WHERE (` + sqliteTimeCompare("ml.received_at") + ` >= ?
			OR ` + sqliteTimeCompare("ml.timestamp") + ` >= ?)`
	var rows []struct {
		StatisticsTime   string
		Platform         string
		UserID           string
		ConversationKind string
		TargetID         int64
		Nickname         string
		GroupName        string
	}
	bound := sqliteBoundTime(start)
	if err := p.db.WithContext(ctx).Raw(query, bound, bound).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]DetailedMessage, 0, len(rows))
	for _, row := range rows {
		timestamp, err := parseSQLiteDetailedTime(row.StatisticsTime)
		if err != nil {
			return nil, err
		}
		item := DetailedMessage{StatisticsTime: timestamp, Platform: row.Platform, UserID: row.UserID, IsGroup: row.ConversationKind == "group"}
		if item.IsGroup {
			item.ChatID = fmt.Sprintf("g%d", row.TargetID)
			item.ChatName = strings.TrimSpace(row.GroupName)
			if item.ChatName == "" {
				item.ChatName = fmt.Sprintf("群%d", row.TargetID)
			}
		} else {
			item.ChatID = fmt.Sprintf("u%d", row.TargetID)
			item.ChatName = strings.TrimSpace(row.Nickname)
			if item.ChatName == "" {
				item.ChatName = fmt.Sprintf("用户%d", row.TargetID)
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func (p *sqliteDetailedProvider) BotAccounts(ctx context.Context) ([]DetailedBotAccount, error) {
	if p == nil || p.db == nil || !p.db.Migrator().HasTable("bot_platform_accounts") {
		return nil, nil
	}
	var rows []DetailedBotAccount
	if err := p.db.WithContext(ctx).Raw(
		"SELECT platform, account_id FROM bot_platform_accounts WHERE COALESCE(disabled, 0) = 0").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (p *sqliteDetailedProvider) TaskUsageSince(ctx context.Context, start time.Time) ([]DetailedTaskUsage, error) {
	if p == nil || p.db == nil || !p.db.Migrator().HasTable("model_task_usage") {
		return nil, nil
	}
	query := `SELECT ` + sqliteTimestampExpression("bucket_time") + ` AS bucket_time, task_name, model_name, provider_name,
		request_count, failure_count, success_duration
		FROM model_task_usage WHERE ` + sqliteTimeCompare("bucket_time") + ` >= ? ORDER BY bucket_time`
	var rows []struct {
		BucketTime      string
		TaskName        string
		ModelName       string
		ProviderName    string
		RequestCount    int64
		FailureCount    int64
		SuccessDuration float64
	}
	if err := p.db.WithContext(ctx).Raw(query, sqliteBoundTime(start)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]DetailedTaskUsage, 0, len(rows))
	for _, row := range rows {
		bucket, err := parseSQLiteDetailedTime(row.BucketTime)
		if err != nil {
			continue
		}
		out = append(out, DetailedTaskUsage{
			BucketTime: bucket, TaskName: row.TaskName, ModelName: row.ModelName, ProviderName: row.ProviderName,
			RequestCount: row.RequestCount, FailureCount: row.FailureCount, SuccessDuration: row.SuccessDuration,
		})
	}
	return out, nil
}

func (p *sqliteDetailedProvider) MessageCountBetween(ctx context.Context, start, end time.Time) (int64, error) {
	if p == nil || p.db == nil || !p.db.Migrator().HasTable("mai_messages") {
		return 0, nil
	}
	var count int64
	query := `SELECT COUNT(*) FROM mai_messages
		WHERE COALESCE(received_at, timestamp) >= ? AND COALESCE(received_at, timestamp) < ?
			AND (COALESCE(group_id, '') <> '' OR user_id <> 0)`
	if err := p.db.WithContext(ctx).Raw(query, sqliteBoundTime(start), sqliteBoundTime(end)).Scan(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (p *sqliteDetailedProvider) ReplyCountBetween(ctx context.Context, start, end time.Time, toolName string) (int64, error) {
	if p == nil || p.db == nil || !p.db.Migrator().HasTable("tool_records") {
		return 0, nil
	}
	var count int64
	query := "SELECT COUNT(*) FROM tool_records WHERE " + sqliteTimeCompare("timestamp") + " >= ? AND " +
		sqliteTimeCompare("timestamp") + " < ? AND tool_name = ?"
	if err := p.db.WithContext(ctx).Raw(query, sqliteBoundTime(start), sqliteBoundTime(end), toolName).Scan(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}
