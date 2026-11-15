package websocket

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/chat/session"
	"MaiGo/internal/common/data_models/messagecomponent"
	"MaiGo/internal/common/database"
	"MaiGo/internal/core/internal_chat"

	"go.uber.org/zap"
)

// internalChatHistory 合并内存实时历史与已落库的内部聊天记录
func (s *unifiedWSSession) internalChatHistory(state *chatSessionState) []map[string]any {
	var virtual *internalchat.VirtualIdentity
	if state != nil {
		virtual = state.virtual
	}
	live := []map[string]any{}
	if s.deps.ChatHub != nil {
		live = s.deps.ChatHub.HistoryForChannel(chatChannelKey(virtual))
	}
	if s.deps.Memory == nil {
		return live
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	loader := s.deps.chatDeps().HistoryBinaryLoader(ctx)
	platform, targetID, kind := chatHistoryTarget(virtual)
	sessionMgr := chatsession.NewManager(s.deps.Memory.GetDB())
	sessionIDs, err := sessionMgr.ResolveSessionIDsByTarget(ctx, platform, targetID, kind)
	if err != nil || len(sessionIDs) == 0 {
		return live
	}
	logs := make([]memory.MessageLog, 0, len(sessionIDs)*50)
	for _, sessionID := range sessionIDs {
		items, listErr := s.deps.Memory.ListRecentMessagesBySessionID(ctx, sessionID, 50, 0)
		if listErr != nil {
			zap.L().Warn("读取内部聊天历史失败", zap.Error(listErr))
			return live
		}
		logs = append(logs, items...)
	}
	sort.SliceStable(logs, func(i, j int) bool {
		if logs[i].Timestamp.Equal(logs[j].Timestamp) {
			return logs[i].MessageID < logs[j].MessageID
		}
		return logs[i].Timestamp.Before(logs[j].Timestamp)
	})
	if len(logs) > 50 {
		logs = logs[len(logs)-50:]
	}
	stored := make([]map[string]any, 0, len(logs))
	virtualMode := virtual != nil && virtual.Enabled
	var groupID any
	if virtualMode {
		groupID = virtual.GroupID
	}
	botID := int64(0)
	if s.deps.Agent != nil {
		botID = s.deps.Agent.SelfID()
	}
	index := make(map[string]memory.MessageLog, len(logs))
	for _, log := range logs {
		if log.MessageID != 0 {
			index[strconv.FormatInt(log.MessageID, 10)] = log
		}
	}
	for _, log := range logs {
		item := internalHistoryMessage(log, virtualMode, groupID, botID, loader)
		if segments, ok := item["segments"].([]map[string]any); ok && len(segments) > 0 {
			s.deps.fillInternalReplyTargets(ctx, segments, index)
		}
		stored = append(stored, item)
	}
	return mergeChatHistory(stored, live)
}

// fillInternalReplyTargets 用本页索引回填 reply 段，窗口外再按消息 ID 单查兜底
//
// 发送时 reply 段只有 target_message_id，不回填就会显示「未知发送者 /
// 原消息内容不可用」。
func (d Deps) fillInternalReplyTargets(ctx context.Context, segments []map[string]any, index map[string]memory.MessageLog) {
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
		target, found := index[targetID]
		if !found {
			if d.Memory == nil {
				continue
			}
			numeric, parseErr := strconv.ParseInt(targetID, 10, 64)
			if parseErr != nil || numeric == 0 {
				continue
			}
			log, lookupErr := d.Memory.GetMessageLogBySessionAndMessageID(ctx, "", numeric)
			if lookupErr != nil || log == nil {
				continue
			}
			target = *log
		}
		if value, _ := data["target_message_content"].(string); strings.TrimSpace(value) == "" {
			data["target_message_content"] = strings.TrimSpace(target.ProcessedPlainText)
		}
		if value, _ := data["target_message_sender_id"].(string); strings.TrimSpace(value) == "" {
			data["target_message_sender_id"] = strconv.FormatInt(target.UserID, 10)
		}
		if value, _ := data["target_message_sender_nickname"].(string); strings.TrimSpace(value) == "" {
			data["target_message_sender_nickname"] = target.UserNickname
		}
		if value, _ := data["target_message_sender_cardname"].(string); strings.TrimSpace(value) == "" {
			data["target_message_sender_cardname"] = target.UserCardname
		}
	}
}

// internalHistoryMessage 把 mai_messages 行还原成 WebUI 聊天消息
func internalHistoryMessage(log memory.MessageLog, virtualMode bool, groupID any, botID int64, loader internalchat.BinaryLoader) map[string]any {
	segments := []map[string]any{}
	files := []any{}
	if len(log.RawContent) > 0 {
		if sequence, err := messagecomponent.DecodeDBRecord(log.RawContent); err == nil {
			segments = internalchat.SerializeSequenceWith(sequence, loader)
			files = historyFiles(sequence)
		}
	}
	content := strings.TrimSpace(log.ProcessedPlainText)
	if content == "" {
		content = log.ProcessedPlainText
	}
	isBot := log.UserID != internalchat.OwnerNumericID
	if botID > 0 {
		isBot = log.UserID == botID
	}
	timestamp := float64(log.Timestamp.UnixNano()) / 1e9
	if log.Timestamp.IsZero() {
		timestamp = 0
	}
	messageID := strconv.FormatInt(log.MessageID, 10)
	sender := map[string]any{
		"name":    log.UserNickname,
		"user_id": strconv.FormatInt(log.UserID, 10),
		"is_bot":  isBot,
	}
	if log.UserCardname != "" {
		sender["cardname"] = log.UserCardname
	}
	messageGroupID := groupID
	if messageGroupID == nil && log.GroupID != "" {
		messageGroupID = log.GroupID
	}
	additional := map[string]any{}
	for key, value := range log.AdditionalConfig {
		additional[key] = value
	}
	// history 事件前端按 Python _message_to_dict 读顶层 sender_name/sender_id；
	// 实时事件消费方读 sender 子对象。两套都提供，避免任一方退化成兜底文案。
	senderName := log.UserNickname
	if strings.TrimSpace(senderName) == "" {
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
	payload := map[string]any{
		"type":              "user_message",
		"content":           content,
		"raw_content":       log.ProcessedPlainText,
		"is_bot":            isBot,
		"sender_name":       senderName,
		"sender_id":         senderID,
		"images":            []any{},
		"emojis":            []any{},
		"files":             files,
		"voices":            []any{},
		"group_id":          messageGroupID,
		"group_name":        log.GroupName,
		"user_cardname":     log.UserCardname,
		"message_id":        messageID,
		"timestamp":         timestamp,
		"sender":            sender,
		"is_mentioned":      log.IsMentioned,
		"is_at":             log.IsAt,
		"is_emoji":          log.IsEmoji,
		"is_picture":        log.IsPicture,
		"is_command":        log.IsCommand,
		"is_notify":         log.IsNotify,
		"additional_config": additional,
		"virtual_mode":      virtualMode,
		"origin":            "",
	}
	if isBot {
		messageType := "rich"
		if len(segments) == 1 && segments[0]["type"] == "text" {
			messageType = "text"
			segments = nil
		}
		payload["type"] = "bot_message"
		payload["message_type"] = messageType
		payload["segments"] = segments
		payload["sender"] = map[string]any{
			"name":   log.UserNickname,
			"avatar": nil,
			"is_bot": true,
		}
	} else if len(segments) > 0 {
		payload["segments"] = segments
	}
	return payload
}

// historyFiles 从文件组件还原历史消息的文件元数据
func historyFiles(sequence *messagecomponent.MessageSequence) []any {
	files := []any{}
	if sequence == nil {
		return files
	}
	for _, component := range sequence.Components {
		file, ok := component.(messagecomponent.FileComponent)
		if !ok {
			continue
		}
		size := int64(0)
		if file.Size != "" {
			size, _ = strconv.ParseInt(file.Size, 10, 64)
		}
		files = append(files, map[string]any{
			"file_ref":  file.FileRef,
			"name":      file.Name,
			"mime_type": file.MimeType,
			"size":      size,
		})
	}
	return files
}

// mergeChatHistory 按消息 ID 去重，实时消息优先，再按时间排序
func mergeChatHistory(stored, live []map[string]any) []map[string]any {
	merged := make(map[string]map[string]any, len(stored)+len(live))
	for _, message := range stored {
		if id := chatMessageID(message); id != "" {
			merged[id] = message
		}
	}
	for _, message := range live {
		if id := chatMessageID(message); id != "" {
			merged[id] = message
		}
	}
	out := make([]map[string]any, 0, len(merged))
	for _, message := range merged {
		out = append(out, message)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return chatMessageTimestamp(out[i]) < chatMessageTimestamp(out[j])
	})
	return out
}

func chatMessageID(message map[string]any) string {
	value, _ := message["message_id"].(string)
	return value
}

func chatMessageTimestamp(message map[string]any) float64 {
	value, _ := message["timestamp"].(float64)
	return value
}
