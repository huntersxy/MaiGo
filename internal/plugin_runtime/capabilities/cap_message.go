package capabilities

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/common/database"
	commonutils "MaiGo/internal/common/utils"
)

func pluginTimeRange(args map[string]any) (time.Time, time.Time) {
	start := time.Unix(int64(coalesceFloat(args, "start_time", 0)), 0)
	endUnix := coalesceFloat(args, "end_time", 0)
	var end time.Time
	if endUnix > 0 {
		end = time.Unix(int64(endUnix), 0)
	}
	return start, end
}

func pluginLimit(args map[string]any, def int) int {
	limit := int(coalesceFloat(args, "limit", float64(def)))
	if limit < 0 {
		return 0
	}
	return limit
}

func filterPluginMessages(rows []memory.MessageLog, args map[string]any) []memory.MessageLog {
	// 机器人消息仍以 user_id=0 落库；命令标记已入库，filter_command 与 Python 同义
	filterMai := truthyPayloadValue(args["filter_mai"])
	filterCommand := truthyPayloadValue(args["filter_command"])
	if !filterMai && !filterCommand {
		return rows
	}
	out := make([]memory.MessageLog, 0, len(rows))
	for _, row := range rows {
		if filterMai && row.UserID == 0 {
			continue
		}
		if filterCommand && row.IsCommand {
			continue
		}
		out = append(out, row)
	}
	return out
}

func latestPluginMessages(rows []memory.MessageLog, limit int) []memory.MessageLog {
	sort.Slice(rows, func(i, j int) bool { return rows[i].Timestamp.Before(rows[j].Timestamp) })
	if limit <= 0 || len(rows) <= limit {
		return rows
	}
	return rows[len(rows)-limit:]
}

// limitPluginMessages 按 limit_mode 截断，对齐 Python find_messages 的 earliest/latest
// earliest 取最早 limit 条，latest（默认）取最近 limit 条；两者结果都按时间升序返回。
func limitPluginMessages(rows []memory.MessageLog, limit int, limitMode string) []memory.MessageLog {
	sort.Slice(rows, func(i, j int) bool { return rows[i].Timestamp.Before(rows[j].Timestamp) })
	if limit <= 0 || len(rows) <= limit {
		return rows
	}
	if strings.EqualFold(strings.TrimSpace(limitMode), "earliest") {
		return rows[:limit]
	}
	return rows[len(rows)-limit:]
}

// pluginLimitMode 读取 limit_mode，非法值回退 latest（对齐 Python 默认值）
func pluginLimitMode(args map[string]any) string {
	if strings.EqualFold(strings.TrimSpace(coalesceString(args, "limit_mode")), "earliest") {
		return "earliest"
	}
	return "latest"
}

func queryMessagesByTime(ctx context.Context, mem *memory.Manager, args map[string]any) ([]memory.MessageLog, error) {
	start, end := pluginTimeRange(args)
	limit := pluginLimit(args, 0)
	chatID := strings.TrimSpace(coalesceString(args, "chat_id", "stream_id", "session_id"))
	if chatID != "" {
		sessionID, err := resolvePluginChatSessionID(ctx, mem, chatID)
		if err != nil {
			return nil, err
		}
		rows, err := mem.ListMessagesBySessionIDTimeRange(ctx, sessionID, start, end, 0)
		if err != nil {
			return nil, err
		}
		rows = filterPluginMessages(rows, args)
		return limitPluginMessages(rows, limit, pluginLimitMode(args)), nil
	}
	db := mem.GetDB()
	query := db.WithContext(ctx).Where("timestamp >= ?", start).
		Where(memory.NoticeExternalMessageIDPredicate("external_message_id"))
	if !end.IsZero() {
		query = query.Where("timestamp <= ?", end)
	}
	var rows []memory.MessageLog
	if err := query.Order("timestamp ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	rows = filterPluginMessages(rows, args)
	return limitPluginMessages(rows, limit, pluginLimitMode(args)), nil
}

func pluginMessageResult(rows []memory.MessageLog) map[string]any {
	return map[string]any{"success": true, "messages": serializePluginMessages(rows)}
}

func makeMessageGetByTimeHandler(mem *memory.Manager) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		if mem == nil {
			return capabilityFailure("消息存储未就绪"), nil
		}
		rows, err := queryMessagesByTime(ctx, mem, args)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		return pluginMessageResult(rows), nil
	}
}

func makeMessageGetByIDHandler(mem *memory.Manager) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		if mem == nil {
			return capabilityFailure("消息存储未就绪"), nil
		}
		rawID := strings.TrimSpace(coalesceString(args, "message_id", "msg_id"))
		if rawID == "" {
			return capabilityFailure("缺少必要参数 message_id"), nil
		}
		messageID, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			return map[string]any{"success": true, "message": nil}, nil
		}
		db := mem.GetDB()
		query := db.WithContext(ctx).Where("message_id = ?", messageID).
			Where(memory.NoticeExternalMessageIDPredicate("external_message_id"))
		if chatID := strings.TrimSpace(coalesceString(args, "chat_id", "stream_id", "session_id")); chatID != "" {
			sessionID, err := resolvePluginChatSessionID(ctx, mem, chatID)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			query = query.Where("session_id = ?", sessionID)
		}
		var row memory.MessageLog
		if err := query.First(&row).Error; err != nil {
			return map[string]any{"success": true, "message": nil}, nil
		}
		return map[string]any{"success": true, "message": serializePluginMessages([]memory.MessageLog{row})[0]}, nil
	}
}

func makeMessageGetByTimeInChatHandler(mem *memory.Manager) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		if mem == nil {
			return capabilityFailure("消息存储未就绪"), nil
		}
		chatID := strings.TrimSpace(coalesceString(args, "chat_id", "stream_id", "session_id"))
		if chatID == "" {
			return capabilityFailure("缺少必要参数 chat_id"), nil
		}
		sessionID, err := resolvePluginChatSessionID(ctx, mem, chatID)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		start, end := pluginTimeRange(args)
		rows, err := mem.ListMessagesBySessionIDTimeRange(ctx, sessionID, start, end, 0)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		rows = limitPluginMessages(filterPluginMessages(rows, args), pluginLimit(args, 0), pluginLimitMode(args))
		return pluginMessageResult(rows), nil
	}
}

func makeMessageGetRecentHandler(mem *memory.Manager) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		if mem == nil {
			return capabilityFailure("消息存储未就绪"), nil
		}
		chatID := strings.TrimSpace(coalesceString(args, "chat_id", "stream_id", "session_id"))
		if chatID == "" {
			return capabilityFailure("缺少必要参数 chat_id"), nil
		}
		hours := coalesceFloat(args, "hours", 24)
		if hours < 0 {
			return capabilityFailure("hours 不能是负数"), nil
		}
		end := time.Now()
		start := end.Add(-time.Duration(hours * float64(time.Hour)))
		sessionID, err := resolvePluginChatSessionID(ctx, mem, chatID)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		rows, err := mem.ListMessagesBySessionIDTimeRange(ctx, sessionID, start, end, 0)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		limit := pluginLimit(args, 100)
		rows = limitPluginMessages(filterPluginMessages(rows, args), limit, pluginLimitMode(args))
		return pluginMessageResult(rows), nil
	}
}

func makeMessageCountNewHandler(mem *memory.Manager) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		if mem == nil {
			return capabilityFailure("消息存储未就绪"), nil
		}
		chatID := strings.TrimSpace(coalesceString(args, "chat_id", "stream_id", "session_id"))
		if chatID == "" {
			return capabilityFailure("缺少必要参数 chat_id"), nil
		}
		var start time.Time
		if since, ok := args["since"]; ok && since != nil {
			start = time.Unix(int64(coalesceFloat(map[string]any{"since": since}, "since", 0)), 0)
		} else {
			start = time.Unix(int64(coalesceFloat(args, "start_time", 0)), 0)
		}
		end, _ := pluginTimeRange(args)
		sessionID, err := resolvePluginChatSessionID(ctx, mem, chatID)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		db := mem.GetDB()
		query := db.WithContext(ctx).Model(&memory.MessageLog{}).Where("session_id = ? AND timestamp >= ?", sessionID, start).
			Where(memory.NoticeExternalMessageIDPredicate("external_message_id"))
		if !end.IsZero() {
			query = query.Where("timestamp <= ?", end)
		}
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		return map[string]any{"success": true, "count": count}, nil
	}
}

// pluginReadableMessages 对齐 message_service.build_readable_messages 的可读文本形态
func pluginReadableMessages(rows []memory.MessageLog, replaceBotName bool, timestampMode string, truncate bool, botName string) string {
	var builder strings.Builder
	now := time.Now()
	for _, row := range rows {
		nickname := row.UserNickname
		if nickname == "" {
			nickname = strconv.FormatInt(row.UserID, 10)
		}
		if replaceBotName && row.UserID == 0 && botName != "" {
			nickname = botName
		}
		content := row.ProcessedPlainText
		if content == "" {
			content = row.ProcessedPlainText
		}
		if truncate {
			runes := []rune(content)
			if len(runes) > 100 {
				content = string(runes[:100]) + "…"
			}
		}
		var stamp string
		if timestampMode == "absolute" {
			stamp = row.Timestamp.Format("2006-01-02 15:04:05")
		} else {
			stamp = pluginRelativeTime(now.Sub(row.Timestamp), row.Timestamp)
		}
		builder.WriteString(stamp)
		builder.WriteString(" ")
		builder.WriteString(nickname)
		builder.WriteString(": ")
		builder.WriteString(content)
		builder.WriteString("\n")
	}
	return builder.String()
}

// pluginRelativeTime 对齐 math_utils.translate_timestamp_to_human_readable(RELATIVE)
func pluginRelativeTime(delta time.Duration, absolute time.Time) string {
	return commonutils.RelativeTimestampText(delta, absolute)
}

func makeMessageBuildReadableHandler(mem *memory.Manager, instance BotConfigSource) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		if mem == nil {
			return capabilityFailure("消息存储未就绪"), nil
		}
		var rows []memory.MessageLog
		if raw, ok := args["messages"]; ok && raw != nil {
			items, ok := raw.([]any)
			if !ok {
				return capabilityFailure("messages 必须是消息字典列表"), nil
			}
			for index, item := range items {
				message, ok := item.(map[string]any)
				if !ok {
					return capabilityFailure("messages[%d] 必须是消息字典", index), nil
				}
				rows = append(rows, pluginMessageFromDict(message))
			}
		} else {
			chatID := strings.TrimSpace(coalesceString(args, "chat_id", "stream_id", "session_id"))
			if chatID == "" {
				return capabilityFailure("缺少必要参数: messages 或 chat_id"), nil
			}
			sessionID, err := resolvePluginChatSessionID(ctx, mem, chatID)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			start, end := pluginTimeRange(args)
			rows, err = mem.ListMessagesBySessionIDTimeRange(ctx, sessionID, start, end, 0)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			rows = latestPluginMessages(rows, pluginLimit(args, 0))
		}
		botName := ""
		if instance != nil {
			if value, err := lookupConfigValue(instance, "bot.nickname", nil); err == nil {
				botName = stringValue(value)
			}
		}
		text := pluginReadableMessages(rows, !isFalsePayloadValue(args["replace_bot_name"]), coalesceString(args, "timestamp_mode"), truthyPayloadValue(args["truncate"]), botName)
		return map[string]any{"success": true, "text": text}, nil
	}
}

func isFalsePayloadValue(raw any) bool {
	if raw == nil {
		return false
	}
	if value, ok := raw.(bool); ok {
		return !value
	}
	return false
}

func pluginMessageFromDict(message map[string]any) memory.MessageLog {
	row := memory.MessageLog{
		SessionID:          coalesceString(message, "session_id", "chat_id", "stream_id"),
		Platform:           coalesceString(message, "platform"),
		AccountID:          coalesceString(message, "account_id"),
		Scope:              coalesceString(message, "scope"),
		UserNickname:       coalesceString(message, "user_nickname", "nickname"),
		UserCardname:       coalesceString(message, "user_cardname"),
		GroupID:            coalesceString(message, "group_id"),
		GroupName:          coalesceString(message, "group_name"),
		ConversationKind:   coalesceString(message, "chat_type", "conversation_kind"),
		ProcessedPlainText: coalesceString(message, "processed_plain_text", "content"),
		IsMentioned:        pluginMessageBool(message, "is_mentioned"),
		IsAt:               pluginMessageBool(message, "is_at"),
		IsEmoji:            pluginMessageBool(message, "is_emoji"),
		IsPicture:          pluginMessageBool(message, "is_picture"),
		IsCommand:          pluginMessageBool(message, "is_command"),
		IsNotify:           pluginMessageBool(message, "is_notify"),
		AdditionalConfig:   pluginMessageAdditionalConfig(message["additional_config"]),
	}
	row.UserID, _ = strconv.ParseInt(coalesceString(message, "user_id"), 10, 64)
	if timestamp := coalesceFloat(message, "timestamp", 0); timestamp > 0 {
		row.Timestamp = time.Unix(int64(timestamp), 0)
	}
	if messageID := coalesceString(message, "message_id"); messageID != "" {
		row.MessageID, _ = strconv.ParseInt(messageID, 10, 64)
	}
	return row
}

func pluginMessageBool(message map[string]any, key string) bool {
	value, ok := message[key]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	default:
		return false
	}
}

func pluginMessageAdditionalConfig(raw any) memory.MessageAdditionalConfig {
	if raw == nil {
		return nil
	}
	switch typed := raw.(type) {
	case map[string]any:
		if len(typed) == 0 {
			return nil
		}
		return memory.MessageAdditionalConfig(typed)
	case map[string]string:
		out := make(memory.MessageAdditionalConfig, len(typed))
		for key, value := range typed {
			out[key] = value
		}
		return out
	case string:
		text := strings.TrimSpace(typed)
		if text == "" || text == "null" {
			return nil
		}
		var out memory.MessageAdditionalConfig
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			return nil
		}
		return out
	default:
		return nil
	}
}
