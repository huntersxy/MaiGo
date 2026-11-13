package messagereceive

import (
	"strings"

	"maibot-go/internal/chat/session"
)

func sessionFromRecord(row chatsession.Record) *BotChatSession {
	return &BotChatSession{Record: row}
}

func updateSessionIdentity(session *BotChatSession, message *SessionMessage) bool {
	if session == nil || message == nil {
		return false
	}
	if message.MessageInfo.GroupInfo != nil {
		changed := false
		if name := message.GroupName(); name != "" {
			if pointerText(session.Record.GroupName) != name {
				value := name
				session.Record.GroupName = &value
				changed = true
			}
		}
		if session.Record.UserID != nil {
			session.Record.UserID = nil
			changed = true
		}
		if session.Record.UserNickname != nil {
			session.Record.UserNickname = nil
			changed = true
		}
		if session.Record.UserCardname != nil {
			session.Record.UserCardname = nil
			changed = true
		}
		return changed
	}
	changed := false
	if nickname := message.Nickname(); nickname != "" {
		if pointerText(session.Record.UserNickname) != nickname {
			value := nickname
			session.Record.UserNickname = &value
			changed = true
		}
	}
	cardname := strings.TrimSpace(message.GroupCard())
	if pointerText(session.Record.UserCardname) != cardname {
		var value *string
		if cardname != "" {
			copy := cardname
			value = &copy
		}
		session.Record.UserCardname = value
		changed = true
	}
	return changed
}

func applyRouteMetadata(record *chatsession.Record, accountID, scope string) bool {
	if record == nil {
		return false
	}
	changed := false
	if accountID = strings.TrimSpace(accountID); accountID != "" && pointerText(record.AccountID) == "" {
		value := accountID
		record.AccountID = &value
		changed = true
	}
	if scope = strings.TrimSpace(scope); scope != "" && pointerText(record.Scope) == "" {
		value := scope
		record.Scope = &value
		changed = true
	}
	return changed
}

func sessionMatchesTarget(session *BotChatSession, platform, targetID, chatType string) bool {
	if session == nil || strings.TrimSpace(session.Record.Platform) != platform {
		return false
	}
	return recordMatchesTarget(&session.Record, targetID, chatType)
}

func recordMatchesTarget(record *chatsession.Record, targetID, chatType string) bool {
	if record == nil {
		return false
	}
	switch chatType {
	case "group":
		return pointerText(record.GroupID) == targetID
	case "private":
		return pointerText(record.UserID) == targetID
	default:
		return false
	}
}

func sessionTargetID(session *BotChatSession) string {
	if session == nil {
		return ""
	}
	if session.IsGroup() {
		return pointerText(session.Record.GroupID)
	}
	return pointerText(session.Record.UserID)
}

func pointerText(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func groupIDOf(message *SessionMessage) string {
	if message == nil || message.MessageInfo.GroupInfo == nil {
		return ""
	}
	return strings.TrimSpace(message.MessageInfo.GroupInfo.GroupID)
}
