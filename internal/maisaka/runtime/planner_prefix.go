package runtime

import (
	"strconv"
	"strings"

	"MaiGo/internal/chat/session"
	maisakactx "MaiGo/internal/maisaka/context"
	"MaiGo/internal/platform_io/drivers/onebot"
)

// plannerMessageID 返回 Planner 前缀使用的消息编号，通知事件不输出 msg_id
func plannerMessageID(msg *onebot.ConversationMessage) string {
	if msg == nil || msg.IsNotify {
		return ""
	}
	if msg.MessageID != 0 {
		return strconv.FormatInt(msg.MessageID, 10)
	}
	return onebot.NoticeIDFromMessage(msg)
}

// plannerUserName 对齐 Python user_info.user_nickname or user_info.user_id
func plannerUserName(msg *onebot.ConversationMessage) string {
	if msg == nil {
		return ""
	}
	if nickname := strings.TrimSpace(msg.Nickname); nickname != "" {
		return nickname
	}
	if msg.UserID != 0 {
		return strconv.FormatInt(msg.UserID, 10)
	}
	return strings.TrimSpace(msg.ExternalUserID)
}

// plannerQuoteIDs 从回复信息提取被引用消息编号
func plannerQuoteIDs(msg *onebot.ConversationMessage) []string {
	if msg == nil || msg.Reply == nil {
		return nil
	}
	quoteID := strings.TrimSpace(msg.Reply.ExternalMessageID)
	if quoteID == "" && msg.Reply.MessageID != 0 {
		quoteID = strconv.FormatInt(msg.Reply.MessageID, 10)
	}
	if quoteID == "" {
		return nil
	}
	return []string{quoteID}
}

// plannerPrefixForMessage 构造 Planner 历史消息前缀
// 对齐 Python src/maisaka/context/planner_messages.py:17-84 的属性顺序与取值
func plannerPrefixForMessage(msg *onebot.ConversationMessage, sessionID, sessionName string, includeChatID, isSelfMessage bool) string {
	if msg == nil {
		return ""
	}
	messageID := plannerMessageID(msg)
	return maisakactx.BuildPlannerPrefix(maisakactx.PlannerPrefixInput{
		Timestamp:        msg.Time,
		UserName:         plannerUserName(msg),
		GroupCard:        strings.TrimSpace(msg.GroupCard),
		MessageID:        messageID,
		ChatID:           sessionID,
		ChatName:         sessionName,
		QuoteIDs:         plannerQuoteIDs(msg),
		IncludeMessageID: messageID != "",
		IncludeChatID:    includeChatID,
		IsSelfMessage:    isSelfMessage,
	})
}

// plannerSessionInfo 返回 Planner 前缀需要的会话编号、名称与 Focus 状态
// 对齐 Python _is_focus_mode_active_for_current_chat 决定是否输出 session_id/chat_name
func (a *Agent) plannerSessionInfo(session *Session) (string, string, bool) {
	if session == nil {
		return "", "", false
	}
	sessionID := session.MaisakaRuntimeSessionID()
	if sessionID == "" && a != nil {
		sessionID = a.resolveMaiBotSessionID(a.ctx, session.kind, session.targetID)
	}
	sessionName := a.plannerSessionDisplayName(sessionID)
	includeChatID := false
	if runtime := session.MaisakaRuntime(); runtime != nil {
		includeChatID = runtime.FocusModeActive()
	}
	return sessionID, sessionName, includeChatID
}

// plannerSessionDisplayName 对齐 Python chat_manager.get_session_name：
// 群聊优先群名，私聊给出「昵称的私聊」，都取不到时不输出 chat_name
func (a *Agent) plannerSessionDisplayName(sessionID string) string {
	if a == nil || a.chatSessions == nil || strings.TrimSpace(sessionID) == "" {
		return ""
	}
	ctx, cancel := a.persistenceContext()
	defer cancel()
	rec, err := a.chatSessions.GetExistingBySessionID(ctx, sessionID)
	if err != nil {
		return ""
	}
	return chatsession.DisplayName(rec)
}

// visibleSpeakerName 对齐 Python user_cardname or user_nickname or user_id
func visibleSpeakerName(msg *onebot.ConversationMessage) string {
	if msg == nil {
		return ""
	}
	if name := resolveMessageDisplayName(msg.GroupCard, msg.Nickname); name != "" {
		return name
	}
	if msg.UserID != 0 {
		return strconv.FormatInt(msg.UserID, 10)
	}
	return strings.TrimSpace(msg.ExternalUserID)
}
