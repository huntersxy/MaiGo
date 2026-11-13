package stats

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type telemetryMessageRow struct {
	Platform         string
	UserID           string
	ConversationKind string
	ReplyFrequency   sql.NullFloat64
}

const telemetryMessageLogQuery = "SELECT\n" +
	"\t\tCOALESCE(NULLIF(platform, ''), 'unknown') AS platform,\n" +
	"\t\tCAST(user_id AS TEXT) AS user_id,\n" +
	"\t\tconversation_kind,\n" +
	"\t\treply_frequency\n" +
	"\tFROM mai_messages\n" +
	"\tWHERE COALESCE(received_at, timestamp) >= ? AND COALESCE(received_at, timestamp) < ?"

// collectMessagesFromLogs 对齐 MaiBot 按消息明细统计方向与回复频率
func (u *statsUploader) collectMessagesFromLogs(ctx context.Context, periodStart, periodEnd time.Time, accounts []BotAccount) (telemetryStatsMessages, error) {
	directions := map[string]*telemetryStatsDirection{
		statsDirectionReceived: newDirectionStats(),
		statsDirectionBotSent:  newDirectionStats(),
	}
	var rows []telemetryMessageRow
	err := u.db.WithContext(ctx).Raw(telemetryMessageLogQuery, periodStart, periodEnd).Scan(&rows).Error
	if err != nil {
		return telemetryStatsMessages{}, fmt.Errorf("查询消息明细失败: %w", err)
	}
	botAccounts := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		platform := strings.ToLower(strings.TrimSpace(account.Platform))
		userID := strings.TrimSpace(account.UserID)
		if platform == "" || userID == "" {
			continue
		}
		botAccounts[platform+"\x00"+userID] = struct{}{}
	}
	for _, row := range rows {
		platform := strings.ToLower(strings.TrimSpace(row.Platform))
		if platform == "" {
			platform = statsUnknownLabel
		}
		direction := statsDirectionReceived
		if _, ok := botAccounts[platform+"\x00"+strings.TrimSpace(row.UserID)]; ok {
			direction = statsDirectionBotSent
		}
		chatType := statsChatTypePrivate
		if strings.EqualFold(strings.TrimSpace(row.ConversationKind), statsChatTypeGroup) {
			chatType = statsChatTypeGroup
		}
		addMessageStat(directions[direction], platform, chatType, replyFrequencyBucket(row.ReplyFrequency))
	}
	return telemetryStatsMessages{ByDirection: directions}, nil
}

func addMessageStat(stats *telemetryStatsDirection, platform, chatType, bucket string) {
	if stats == nil {
		return
	}
	stats.Total++
	item := stats.ByPlatform[platform]
	if item == nil {
		item = &telemetryStatsPlatform{
			ByReplyFrequency: map[string]map[string]int64{
				statsChatTypePrivate: newFrequencyBuckets(0),
				statsChatTypeGroup:   newFrequencyBuckets(0),
			},
		}
		stats.ByPlatform[platform] = item
	}
	item.Total++
	switch chatType {
	case statsChatTypeGroup:
		item.Group++
	case statsChatTypePrivate:
		item.Private++
	default:
		return
	}
	item.ByReplyFrequency[chatType][bucket]++
}

// replyFrequencyBucket 对齐 MaiBot _get_frequency_bucket
func replyFrequencyBucket(value sql.NullFloat64) string {
	if !value.Valid {
		return statsUnknownLabel
	}
	frequency := value.Float64
	switch {
	case frequency <= 0:
		return "0"
	case frequency <= 0.05:
		return "0.05"
	case frequency <= 0.1:
		return "0.1"
	case frequency <= 0.25:
		return "0.25"
	case frequency <= 0.5:
		return "0.5"
	case frequency <= 0.75:
		return "0.75"
	default:
		return "1"
	}
}

func (u *statsUploader) botAccounts() []BotAccount {
	if u == nil || u.tele == nil || u.tele.botAccounts == nil {
		return nil
	}
	return u.tele.botAccounts()
}
