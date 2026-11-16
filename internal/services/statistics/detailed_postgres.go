package stats

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

type postgresDetailedProvider struct {
	db *gorm.DB
}

// NewPostgresDetailedProvider 创建读取 MaiBot 业务表的详细统计查询器
func NewPostgresDetailedProvider(db *gorm.DB) DetailedProvider {
	return &postgresDetailedProvider{db: db}
}

func (p *postgresDetailedProvider) EarliestStatisticsTime(ctx context.Context) (time.Time, bool, error) {
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
		var value sql.NullTime
		if err := p.db.WithContext(ctx).Raw(item.query).Scan(&value).Error; err != nil {
			return time.Time{}, false, fmt.Errorf("查询 %s 最早时间失败: %w", item.table, err)
		}
		if !value.Valid || value.Time.IsZero() {
			continue
		}
		if !found || value.Time.Before(earliest) {
			earliest = value.Time
			found = true
		}
	}
	return earliest, found, nil
}

func (p *postgresDetailedProvider) UsageSince(ctx context.Context, start time.Time) ([]DetailedUsage, error) {
	if p == nil || p.db == nil {
		return nil, nil
	}
	query := "SELECT id, timestamp, request_type, " +
		"model_api_provider_name AS provider_name, model_assign_name, model_name, " +
		"session_id, task_name, prompt_tokens, completion_tokens, " +
		"prompt_cache_enabled, prompt_cache_hit_tokens, prompt_cache_miss_tokens, " +
		"cost, time_cost FROM llm_usage WHERE timestamp >= ? ORDER BY id"
	var rows []DetailedUsage
	if err := p.db.WithContext(ctx).Raw(query, start).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (p *postgresDetailedProvider) UsageHourlyBetween(ctx context.Context, startBucket, endBucket time.Time) ([]UsageEntry, error) {
	if p == nil || p.db == nil {
		return nil, nil
	}
	return UsageHourlyRows(ctx, p.db, startBucket, endBucket)
}

func (p *postgresDetailedProvider) MessageChatsSince(ctx context.Context, start time.Time) ([]DetailedMessageChat, error) {
	if p == nil || p.db == nil {
		return nil, nil
	}
	query := "WITH scoped AS (" +
		"SELECT ml.id, " +
		"CASE WHEN ml.conversation_kind = 'group' THEN 'g' || ml.target_id::text ELSE 'u' || ml.target_id::text END AS chat_id, " +
		"COALESCE(NULLIF(CASE WHEN ml.conversation_kind = 'group' THEN ct.name ELSE ml.user_nickname END, ''), " +
		"CASE WHEN ml.conversation_kind = 'group' THEN '群' || ml.target_id::text ELSE '用户' || ml.target_id::text END) AS chat_name, " +
		"COALESCE(ml.received_at, ml.timestamp) AS statistics_time " +
		"FROM mai_messages ml LEFT JOIN conversation_targets ct " +
		"ON ct.conversation_kind = ml.conversation_kind AND ct.target_id = ml.target_id " +
		"WHERE COALESCE(ml.received_at, ml.timestamp) >= ?" +
		"), aggregated AS (" +
		"SELECT chat_id, COUNT(*) AS message_count, MAX(statistics_time) AS latest_timestamp " +
		"FROM scoped GROUP BY chat_id" +
		"), latest AS (" +
		"SELECT DISTINCT ON (chat_id) chat_id, chat_name FROM scoped " +
		"ORDER BY chat_id, statistics_time DESC, id DESC" +
		") SELECT a.chat_id, latest.chat_name, a.message_count, a.latest_timestamp " +
		"FROM aggregated a JOIN latest USING (chat_id) ORDER BY a.chat_id"
	var rows []DetailedMessageChat
	if err := p.db.WithContext(ctx).Raw(query, start).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (p *postgresDetailedProvider) ReplyCountSince(ctx context.Context, start time.Time, toolName string) (int64, error) {
	if p == nil || p.db == nil || !p.db.Migrator().HasTable("tool_records") {
		return 0, nil
	}
	var count int64
	if err := p.db.WithContext(ctx).Raw("SELECT COUNT(*) FROM tool_records WHERE timestamp >= ? AND tool_name = ?", start, toolName).Scan(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (p *postgresDetailedProvider) OnlineIntervalsSince(ctx context.Context, start time.Time) ([]OnlineInterval, error) {
	if p == nil || p.db == nil || !p.db.Migrator().HasTable("online_time") {
		return nil, nil
	}
	var rows []OnlineInterval
	if err := p.db.WithContext(ctx).Raw("SELECT start_timestamp, end_timestamp FROM online_time WHERE end_timestamp >= ? ORDER BY start_timestamp", start).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (p *postgresDetailedProvider) MessagesSince(ctx context.Context, start time.Time) ([]DetailedMessage, error) {
	if p == nil || p.db == nil {
		return nil, nil
	}
	query := "SELECT COALESCE(ml.received_at, ml.timestamp) AS statistics_time, ml.platform, " +
		"COALESCE(NULLIF(ml.external_user_id, ''), ml.user_id::text) AS user_id, " +
		"ml.conversation_kind, ml.target_id, ml.nickname, COALESCE(ct.name, '') AS group_name " +
		"FROM mai_messages ml LEFT JOIN conversation_targets ct " +
		"ON ct.conversation_kind = ml.conversation_kind AND ct.target_id = ml.target_id " +
		"WHERE COALESCE(ml.received_at, ml.timestamp) >= ?"
	var rows []struct {
		StatisticsTime   time.Time
		Platform         string
		UserID           string
		ConversationKind string
		TargetID         int64
		Nickname         string
		GroupName        string
	}
	if err := p.db.WithContext(ctx).Raw(query, start).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]DetailedMessage, 0, len(rows))
	for _, row := range rows {
		item := DetailedMessage{StatisticsTime: row.StatisticsTime, Platform: row.Platform, UserID: row.UserID, IsGroup: row.ConversationKind == "group"}
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

func (p *postgresDetailedProvider) BotAccounts(ctx context.Context) ([]DetailedBotAccount, error) {
	if p == nil || p.db == nil || !p.db.Migrator().HasTable("bot_platform_accounts") {
		return nil, nil
	}
	var rows []DetailedBotAccount
	if err := p.db.WithContext(ctx).Raw("SELECT platform, account_id FROM bot_platform_accounts WHERE disabled = false").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (p *postgresDetailedProvider) TaskUsageSince(ctx context.Context, start time.Time) ([]DetailedTaskUsage, error) {
	if p == nil || p.db == nil || !p.db.Migrator().HasTable("model_task_usage") {
		return nil, nil
	}
	query := `SELECT bucket_time, task_name, model_name, provider_name,
		request_count, failure_count, success_duration
		FROM model_task_usage WHERE bucket_time >= ? ORDER BY bucket_time`
	var rows []struct {
		BucketTime      time.Time
		TaskName        string
		ModelName       string
		ProviderName    string
		RequestCount    int64
		FailureCount    int64
		SuccessDuration float64
	}
	if err := p.db.WithContext(ctx).Raw(query, start).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]DetailedTaskUsage, 0, len(rows))
	for _, row := range rows {
		out = append(out, DetailedTaskUsage{
			BucketTime: row.BucketTime, TaskName: row.TaskName, ModelName: row.ModelName, ProviderName: row.ProviderName,
			RequestCount: row.RequestCount, FailureCount: row.FailureCount, SuccessDuration: row.SuccessDuration,
		})
	}
	return out, nil
}

func (p *postgresDetailedProvider) MessageCountBetween(ctx context.Context, start, end time.Time) (int64, error) {
	if p == nil || p.db == nil || !p.db.Migrator().HasTable("mai_messages") {
		return 0, nil
	}
	var count int64
	query := `SELECT COUNT(*) FROM mai_messages
		WHERE COALESCE(received_at, timestamp) >= ? AND COALESCE(received_at, timestamp) < ?
			AND (COALESCE(group_id, '') <> '' OR user_id <> 0)`
	if err := p.db.WithContext(ctx).Raw(query, start, end).Scan(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (p *postgresDetailedProvider) ReplyCountBetween(ctx context.Context, start, end time.Time, toolName string) (int64, error) {
	if p == nil || p.db == nil || !p.db.Migrator().HasTable("tool_records") {
		return 0, nil
	}
	var count int64
	if err := p.db.WithContext(ctx).Raw(
		"SELECT COUNT(*) FROM tool_records WHERE timestamp >= ? AND timestamp < ? AND tool_name = ?",
		start, end, toolName).Scan(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}
