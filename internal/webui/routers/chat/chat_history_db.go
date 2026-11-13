package chat

import (
	"context"
	"maibot-go/internal/webui/httputil"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"maibot-go/internal/chat/session"
	"maibot-go/internal/common/data_models/messagecomponent"
	"maibot-go/internal/common/database"
	"maibot-go/internal/core/internal_chat"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// chatHistoryQuery 解析 /api/chat/history 的 limit 与 group_id
// 对齐 Python src/webui/routers/chat/routes.py:1306-1319：limit 默认 50、范围 1..200，
// 越界按 FastAPI 语义返回 422（不是钳制），因此不复用 httputil.QueryInt。
func (d Deps) chatHistoryQuery(w http.ResponseWriter, r *http.Request) (limit int, groupID string, ok bool) {
	limit = 50
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []any{map[string]any{
				"type":  "int_parsing",
				"loc":   []any{"query", "limit"},
				"msg":   "Input should be a valid integer, unable to parse string as an integer",
				"input": raw,
			}}})
			return 0, "", false
		}
		if parsed < 1 || parsed > 200 {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []any{map[string]any{
				"type":  "value_error",
				"loc":   []any{"query", "limit"},
				"msg":   "Input should be between 1 and 200",
				"input": raw,
			}}})
			return 0, "", false
		}
		limit = parsed
	}
	return limit, strings.TrimSpace(r.URL.Query().Get("group_id")), true
}

// chatHistoryQueryTarget 返回历史查询使用的平台、目标与聊天类型
// 对齐 Python service.py:210-216：有 group_id 走 webui 群聊，否则走 internal 主人私聊
//
// chat_sessions.user_id 存的是 Python 的 INTERNAL_CHAT_USER_ID="owner"，
// 数值身份只出现在 mai_messages 的 BIGINT 列。
func chatHistoryQueryTarget(groupID string) (platform, targetID, chatType string) {
	if groupID != "" {
		return internalchat.WebUIPlatform, groupID, "group"
	}
	return internalchat.Platform, internalchat.OwnerUserID, "private"
}

// loadChatHistory 从数据库读取历史消息，按 Python get_history 的形状输出
// 对齐 src/webui/routers/chat/service.py:219-283
// resolveChatSessionIDs 解析目标对应的会话
func resolveChatSessionIDs(ctx context.Context, db *gorm.DB, platform, targetID, chatType string) ([]string, error) {
	manager := chatsession.NewManager(db)
	return manager.ResolveSessionIDsByTarget(ctx, platform, targetID, chatType)
}

func (d Deps) loadChatHistory(ctx context.Context, limit int, groupID string) []map[string]any {
	if d.Memory == nil {
		return []map[string]any{}
	}
	platform, targetID, chatType := chatHistoryQueryTarget(groupID)
	sessionIDs, err := resolveChatSessionIDs(ctx, d.Memory.GetDB(), platform, targetID, chatType)
	if err != nil || len(sessionIDs) == 0 {
		zap.L().Debug("获取聊天历史时未找到已存在的聊天流，返回空列表")
		return []map[string]any{}
	}
	logs := make([]memory.MessageLog, 0, limit*len(sessionIDs))
	for _, sessionID := range sessionIDs {
		items, listErr := d.Memory.ListRecentMessagesBySessionID(ctx, sessionID, limit, 0)
		if listErr != nil {
			zap.L().Error("从数据库加载聊天记录失败", zap.Error(listErr))
			return []map[string]any{}
		}
		logs = append(logs, items...)
	}
	sort.SliceStable(logs, func(i, j int) bool {
		if logs[i].Timestamp.Equal(logs[j].Timestamp) {
			return logs[i].MessageID < logs[j].MessageID
		}
		return logs[i].Timestamp.Before(logs[j].Timestamp)
	})
	if limit > 0 && len(logs) > limit {
		logs = logs[len(logs)-limit:]
	}

	loader := d.HistoryBinaryLoader(ctx)
	botID := int64(0)
	if d.Agent != nil {
		botID = d.Agent.SelfID()
	}
	index := make(map[string]memory.MessageLog, len(logs))
	for _, log := range logs {
		if id := strconv.FormatInt(log.MessageID, 10); id != "" && log.MessageID != 0 {
			index[id] = log
		}
	}
	result := make([]map[string]any, 0, len(logs))
	for _, log := range logs {
		item := chatHistoryMessage(log, botID, loader)
		if segments, ok := item["segments"].([]map[string]any); ok && len(segments) > 0 {
			d.enrichChatHistoryReplySegments(ctx, segments, index, log.SessionID)
		}
		result = append(result, item)
	}
	return result
}

// chatHistoryMessage 把 mai_messages 行转成 Python _message_to_dict 的形状
// 对齐 service.py:88-137：id/type/content/timestamp/sender_name/sender_id/is_bot/message_type/segments
func chatHistoryMessage(log memory.MessageLog, botID int64, loader internalchat.BinaryLoader) map[string]any {
	segments := []map[string]any{}
	if len(log.RawContent) > 0 {
		if sequence, err := messagecomponent.DecodeDBRecord(log.RawContent); err == nil {
			segments = internalchat.SerializeSequenceWith(sequence, loader)
		}
	}
	isBot := chatHistoryIsBot(log, botID)
	content := strings.TrimSpace(log.ProcessedPlainText)
	if content == "" {
		content = log.ProcessedPlainText
	}
	timestamp := float64(log.Timestamp.UnixNano()) / 1e9
	if log.Timestamp.IsZero() {
		timestamp = 0
	}
	senderName := strings.TrimSpace(log.UserNickname)
	if senderName == "" {
		if isBot {
			senderName = "麦麦"
		} else {
			senderName = "未知用户"
		}
	}
	senderID := strconv.FormatInt(log.UserID, 10)
	if isBot {
		senderID = "bot"
	}
	// 只有单段纯文本时不算富文本，segments 置空，对齐 Python is_rich 判定
	isRich := len(segments) > 0
	if len(segments) == 1 {
		if kind, _ := segments[0]["type"].(string); kind == "text" {
			isRich = false
		}
	}
	messageType := "text"
	var payloadSegments any
	if isRich {
		messageType = "rich"
		payloadSegments = segments
	}
	messageTypeValue := "user"
	if isBot {
		messageTypeValue = "bot"
	}
	return map[string]any{
		"id":           strconv.FormatInt(log.MessageID, 10),
		"type":         messageTypeValue,
		"content":      content,
		"timestamp":    timestamp,
		"sender_name":  senderName,
		"sender_id":    senderID,
		"is_bot":       isBot,
		"message_type": messageType,
		"segments":     payloadSegments,
	}
}

// chatHistoryIsBot 判定消息是否由机器人自己发出
// 对齐 Python is_bot_self：优先用 Agent.SelfID() 数值比对，SelfID 为 0 时退化为 UserID != OwnerNumericID
func chatHistoryIsBot(log memory.MessageLog, botID int64) bool {
	if botID > 0 {
		return log.UserID == botID
	}
	return log.UserID != internalchat.OwnerNumericID
}

// enrichChatHistoryReplySegments 回填 reply 段缺失的发送者与原内容
// 对齐 service.py:139-192：先查本页 message_id 索引，再按 ID 单查兜底，只补缺失字段
func (d Deps) enrichChatHistoryReplySegments(ctx context.Context, segments []map[string]any, index map[string]memory.MessageLog, sessionID string) {
	for _, segment := range segments {
		if segment == nil {
			continue
		}
		if kind, _ := segment["type"].(string); kind != "reply" {
			continue
		}
		data, ok := segment["data"].(map[string]any)
		if !ok {
			continue
		}
		targetID, _ := data["target_message_id"].(string)
		targetID = strings.TrimSpace(targetID)
		if targetID == "" {
			continue
		}
		hasContent := strings.TrimSpace(chatHistoryString(data["target_message_content"])) != ""
		hasSender := false
		for _, key := range []string{"target_message_sender_id", "target_message_sender_nickname", "target_message_sender_cardname"} {
			if strings.TrimSpace(chatHistoryString(data[key])) != "" {
				hasSender = true
				break
			}
		}
		if hasContent && hasSender {
			continue
		}
		target, found := index[targetID]
		if !found {
			// 不在当前窗口内时才付出 DB 代价
			target, found = d.lookupChatHistoryMessage(ctx, targetID, sessionID)
		}
		if !found {
			continue
		}
		if !hasContent {
			content := strings.TrimSpace(target.ProcessedPlainText)
			if content == "" {
				content = target.ProcessedPlainText
			}
			data["target_message_content"] = content
		}
		if !hasSender {
			data["target_message_sender_id"] = strconv.FormatInt(target.UserID, 10)
			data["target_message_sender_nickname"] = target.UserNickname
			data["target_message_sender_cardname"] = target.UserCardname
		}
	}
}

// lookupChatHistoryMessage 按消息 ID 单查，限定在给定会话内
func (d Deps) lookupChatHistoryMessage(ctx context.Context, messageID, sessionID string) (memory.MessageLog, bool) {
	if d.Memory == nil {
		return memory.MessageLog{}, false
	}
	numeric, err := strconv.ParseInt(strings.TrimSpace(messageID), 10, 64)
	if err != nil || numeric == 0 {
		return memory.MessageLog{}, false
	}
	log, err := d.Memory.GetMessageLogBySessionAndMessageID(ctx, sessionID, numeric)
	if err != nil || log == nil {
		return memory.MessageLog{}, false
	}
	return *log, true
}

func chatHistoryString(raw any) string {
	value, _ := raw.(string)
	return value
}
