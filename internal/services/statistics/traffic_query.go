package stats

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"maibot-go/internal/chat/session"

	"gorm.io/gorm"
)

// TopChatRow 按会话聚合的消息量（入站+出站）
type TopChatRow struct {
	ConversationKey  string `json:"conversation_key"`
	ConversationKind string `json:"conversation_kind"`
	TargetID         int64  `json:"target_id"`
	Name             string `json:"name"`
	MessageCount     int64  `json:"message_count"`
}

type chatAggRow struct {
	ConversationKey string
	MessageCount    int64
}

type targetNameRow struct {
	ConversationKind string
	TargetID         int64
	Name             string
}

// QueryTopChats 从 message_traffic_chat_hourly 聚合活跃会话；pending 合并尚未 flush 的进程内计数
func QueryTopChats(ctx context.Context, db *gorm.DB, since time.Time, limit int, pending map[string]int64) ([]TopChatRow, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	totals := make(map[string]int64)
	names := make(map[string]string)
	if db != nil {
		since = since.UTC()
		statsRows, statsNames, statsErr := queryStatisticsChatTotals(ctx, db, since)
		if statsErr != nil {
			return nil, statsErr
		}
		for key, count := range statsRows {
			totals[key] += count
		}
		for key, name := range statsNames {
			names[key] = name
		}
		if len(totals) == 0 {
			var rows []chatAggRow
			err := db.WithContext(ctx).Raw(`
				SELECT conversation_key, SUM(message_count) AS message_count
				FROM message_traffic_chat_hourly
				WHERE bucket_start >= ?
				GROUP BY conversation_key`,
				since).Scan(&rows).Error
			if err != nil {
				return nil, fmt.Errorf("查询按会话消息统计失败: %w", err)
			}
			for _, row := range rows {
				key := strings.TrimSpace(row.ConversationKey)
				if key == "" {
					continue
				}
				totals[key] += row.MessageCount
			}
		}
	}
	for key, count := range pending {
		key = strings.TrimSpace(key)
		if key == "" || count <= 0 {
			continue
		}
		totals[key] += count
	}
	if len(totals) == 0 {
		return nil, nil
	}

	type ranked struct {
		key   string
		count int64
	}
	list := make([]ranked, 0, len(totals))
	for key, count := range totals {
		list = append(list, ranked{key: key, count: count})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].count != list[j].count {
			return list[i].count > list[j].count
		}
		return list[i].key < list[j].key
	})
	if len(list) > limit {
		list = list[:limit]
	}

	keys := make([]string, len(list))
	for i := range list {
		keys[i] = list[i].key
	}
	fallbackNames, err := loadConversationNames(ctx, db, keys)
	if err != nil {
		return nil, err
	}
	for key, name := range fallbackNames {
		if strings.TrimSpace(names[key]) == "" {
			names[key] = name
		}
	}

	out := make([]TopChatRow, 0, len(list))
	for _, item := range list {
		row := TopChatRow{
			ConversationKey: item.key,
			MessageCount:    item.count,
			Name:            names[item.key],
		}
		if kind, targetID, parseErr := chatsession.ParseConversationKey(item.key); parseErr == nil {
			row.ConversationKind = kind
			row.TargetID = targetID
		}
		out = append(out, row)
	}
	return out, nil
}

func loadConversationNames(ctx context.Context, db *gorm.DB, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	if db == nil || len(keys) == 0 {
		return out, nil
	}
	type pair struct {
		kind     string
		targetID int64
		key      string
	}
	pairs := make([]pair, 0, len(keys))
	for _, key := range keys {
		kind, targetID, err := chatsession.ParseConversationKey(key)
		if err != nil {
			continue
		}
		pairs = append(pairs, pair{kind: kind, targetID: targetID, key: key})
	}
	if len(pairs) == 0 {
		return out, nil
	}

	args := make([]any, 0, len(pairs)*2)
	var b strings.Builder
	b.WriteString(`SELECT conversation_kind, target_id, name FROM conversation_targets WHERE `)
	for i, p := range pairs {
		if i > 0 {
			b.WriteString(" OR ")
		}
		b.WriteString("(conversation_kind = ? AND target_id = ?)")
		args = append(args, p.kind, p.targetID)
	}
	var nameRows []targetNameRow
	if err := db.WithContext(ctx).Raw(b.String(), args...).Scan(&nameRows).Error; err != nil {
		return nil, fmt.Errorf("读取会话名称失败: %w", err)
	}
	for _, row := range nameRows {
		key := chatsession.ConversationKey(row.ConversationKind, row.TargetID)
		out[key] = strings.TrimSpace(row.Name)
	}
	return out, nil
}

// queryStatisticsChatTotals 从 statistics_message_hourly 读取 chat_id 与 chat_name
func queryStatisticsChatTotals(ctx context.Context, db *gorm.DB, since time.Time) (map[string]int64, map[string]string, error) {
	if err := EnsureSchema(ctx, db); err != nil {
		return nil, nil, fmt.Errorf("初始化统计表失败: %w", err)
	}
	var rows []struct {
		ChatID       string
		ChatName     string
		MessageCount int64
	}
	err := db.WithContext(ctx).Raw(`
		SELECT chat_id, MAX(chat_name) AS chat_name, COALESCE(SUM(message_count), 0) AS message_count
		FROM statistics_message_hourly
		WHERE bucket_time >= ?
		GROUP BY chat_id`, since).Scan(&rows).Error
	if err != nil {
		return nil, nil, fmt.Errorf("查询统计消息汇总失败: %w", err)
	}
	totals := make(map[string]int64, len(rows))
	names := make(map[string]string, len(rows))
	for _, row := range rows {
		key, ok := statsChatIDToConversationKey(row.ChatID)
		if !ok {
			continue
		}
		totals[key] += row.MessageCount
		if name := strings.TrimSpace(row.ChatName); name != "" {
			names[key] = name
		}
	}
	return totals, names, nil
}

func statsChatIDToConversationKey(chatID string) (string, bool) {
	chatID = strings.TrimSpace(chatID)
	if len(chatID) < 2 {
		return "", false
	}
	prefix, raw := chatID[0], chatID[1:]
	targetID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || targetID <= 0 {
		return "", false
	}
	switch prefix {
	case 'g':
		return chatsession.ConversationKey("group", targetID), true
	case 'u':
		return chatsession.ConversationKey("private", targetID), true
	default:
		return "", false
	}
}
