package runtime

import (
	"context"
	"strings"
	"time"

	"maibot-go/internal/common/database"
	"maibot-go/internal/core/internal_chat"
	"maibot-go/internal/platform_io/drivers/onebot"

	"go.uber.org/zap"
)

// WebChatSender 内部聊天出站能力，由 internal/internalchat.Hub 实现
type WebChatSender interface {
	SendBotText(ctx context.Context, conversationKind string, targetID int64, text string) (int64, error)
	SendBotRich(ctx context.Context, conversationKind string, targetID int64, text string, replyTo int64, mentions []int64) (int64, error)
	SendBotImage(ctx context.Context, conversationKind string, targetID int64, filePath string, isSticker bool) (int64, error)
}

// SetWebChatSender 注入内部聊天出站通道
func (a *Agent) SetWebChatSender(sender WebChatSender) {
	a.setCallbacks(func(c *AgentCallbacks) { c.WebChat = sender })
}

// typingBroadcaster 可选输入状态广播能力，由 internalchat.Hub 实现
type typingBroadcaster interface {
	BroadcastTypingTo(channelKey string, isTyping bool)
}

// broadcastInternalTyping 为内部聊天广播机器人输入状态；QQ 等平台没有对应能力时静默跳过
func (a *Agent) broadcastInternalTyping(kind string, targetID int64, typing bool, active bool) {
	if a == nil || !typing || !a.isInternalConversation(kind, targetID) {
		return
	}
	hub, ok := a.webChatSender().(typingBroadcaster)
	if !ok {
		return
	}
	hub.BroadcastTypingTo(internalchat.OwnerChannelKey(), active)
}

// SetThinkingNotifier 注入内部聊天思考状态通知，仅在主人私聊时触发
func (a *Agent) SetThinkingNotifier(notifier func(conversationKind string, targetID int64)) {
	a.setCallbacks(func(c *AgentCallbacks) { c.ThinkingNotifier = notifier })
}

// isInternalConversation 判断会话最近入站平台是否为内部聊天
func (a *Agent) isInternalConversation(conversationKind string, targetID int64) bool {
	if a == nil || a.webChatSender() == nil {
		return false
	}
	if a.conversationPlatform(conversationKind, targetID) == internalchat.Platform {
		return true
	}
	return a.isInternalTarget(conversationKind, targetID)
}

// persistInternalBotMessage 把 WebUI 内聊的机器人文本回复写入消息库
//
// WebUI 内聊没有协议端回传，Hub 只做广播；不落库的话历史回读只有用户消息，
// 前端会把整段对话画在同一侧。对齐 Python 的
// 「机器人消息由 MessageStorage.store_message 自动保存」。
func (a *Agent) persistInternalBotMessage(ctx context.Context, conversationKind string, targetID int64, text string, messageID, replyTo int64) {
	if a == nil || a.memory == nil || messageID == 0 || targetID <= 0 {
		return
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	if conversationKind == "" {
		conversationKind = memory.ConversationKindPrivate
	}
	now := time.Now()
	parts := make([]onebot.MessagePart, 0, 2)
	if replyTo != 0 {
		parts = append(parts, onebot.MessagePart{Kind: "reply", Index: 0})
	}
	parts = append(parts, onebot.MessagePart{Kind: "text", Text: text, Index: 0})
	msg := &onebot.ConversationMessage{
		Platform:         internalchat.Platform,
		AccountID:        internalchat.SelfUserID,
		MessageID:        messageID,
		ConversationKind: conversationKind,
		TargetID:         targetID,
		UserID:           a.SelfID(),
		// chat_sessions.user_id 对齐 Python INTERNAL_CHAT_USER_ID="owner"
		ExternalUserID: internalchat.OwnerUserID,
		ExternalTargetID: internalchat.OwnerUserID,
		Nickname:       a.BotName(),
		FinalContent:   text,
		Time:           now,
		ReceivedAt:     now,
		MessageParts:   parts,
	}
	if replyTo != 0 {
		msg.Reply = &onebot.ReplyInfo{MessageID: replyTo}
	}
	sessionID := a.ensureChatSession(ctx, msg)
	if _, _, err := a.persistConversationMessage(ctx, msg, nil, nil, internalchat.Platform, sessionID); err != nil {
		zap.L().Warn("保存 WebUI 机器人回复失败",
			zap.String("conversation_kind", conversationKind),
			zap.Int64("target_id", targetID),
			zap.Error(err))
	}
}

// PersistInternalFile 把 WebUI 文件页发出的普通内部聊天文件写入消息库
func (a *Agent) PersistInternalFile(messageID int64, file onebot.FileInfo) error {
	return a.PersistFileMessage(messageID, file, internalchat.Platform,
		memory.ConversationKindPrivate, internalchat.OwnerNumericID, "", "")
}

// PersistFileMessage 按目标会话写入文件消息
func (a *Agent) PersistFileMessage(messageID int64, file onebot.FileInfo, platform, conversationKind string, targetID int64, groupKey, groupName string) error {
	if a == nil || messageID == 0 || targetID <= 0 {
		return nil
	}
	if platform == "" {
		platform = internalchat.Platform
	}
	if conversationKind == "" {
		conversationKind = memory.ConversationKindPrivate
	}
	display := file.Name
	if display == "" {
		display = "文件"
	}
	now := time.Now()
	msg := &onebot.ConversationMessage{
		Platform:         platform,
		MessageID:        messageID,
		ConversationKind: conversationKind,
		TargetID:         targetID,
		UserID:           a.SelfID(),
		Nickname:         a.BotName(),
		FinalContent:     "[文件] " + display,
		Time:             now,
		ReceivedAt:       now,
		GroupKey:         groupKey,
		GroupName:        groupName,
		Files:            []onebot.FileInfo{file},
		MessageParts:     []onebot.MessagePart{{Kind: "file", Index: 0}},
	}
	ctx, cancel := a.persistenceContext()
	defer cancel()
	sessionID := a.ensureChatSession(ctx, msg)
	_, _, err := a.persistConversationMessage(ctx, msg, nil, nil, platform, sessionID)
	return err
}

// HandleInternalChatMessage 投递内部聊天消息，普通聊天使用 internal 平台
func (a *Agent) HandleInternalChatMessage(msg *onebot.ConversationMessage) {
	if a == nil || msg == nil || msg.UserID <= 0 || msg.TargetID <= 0 {
		return
	}
	if msg.Platform == "" {
		msg.Platform = internalchat.Platform
	}
	if msg.ConversationKind == "" {
		if msg.GroupKey != "" {
			msg.ConversationKind = memory.ConversationKindGroup
		} else {
			msg.ConversationKind = memory.ConversationKindPrivate
		}
	}
	a.markInternalConversation(msg.ConversationKind, msg.TargetID)
	ctx, cancel := a.persistenceContext()
	defer cancel()
	if msg.ConversationKind == memory.ConversationKindGroup && msg.GroupKey != "" {
		a.sessionIndex().setGroupKey(msg.ConversationKind, msg.TargetID, msg.GroupKey)
		_ = a.memory.EnsureConversationTargetName(ctx, msg.ConversationKind, msg.TargetID, msg.GroupName)
	}
	if msg.MessageID == 0 {
		msg.MessageID = internalchat.NextNumericMessageID()
	}
	if msg.ReceivedAt.IsZero() {
		msg.ReceivedAt = time.Now()
	}
	msg.IsMentioned = true
	if len(msg.MessageParts) == 0 && msg.Content != "" {
		msg.MessageParts = []onebot.MessagePart{{Kind: "text", Text: msg.Content}}
	}
	if err := a.memory.MarkConversationActive(ctx, msg.ConversationKind, msg.TargetID, true); err != nil {
		zap.L().Warn("启用内部聊天会话失败",
			zap.String("conversation_kind", msg.ConversationKind),
			zap.Int64("target_id", msg.TargetID),
			zap.Error(err))
		return
	}
	a.onMessage(msg)
}
