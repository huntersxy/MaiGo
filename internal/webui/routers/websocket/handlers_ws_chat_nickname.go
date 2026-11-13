package websocket

import (
	"strings"

	"maibot-go/internal/core/internal_chat"
)

// updateChatNickname 更新当前会话昵称
func (s *unifiedWSSession) updateChatNickname(requestID any, message map[string]any) error {
	clientSessionID := strings.TrimSpace(wsAnyString(message["session"]))
	state := s.chatSessions[clientSessionID]
	if state == nil {
		return s.sendResponse(requestID, false, nil, map[string]any{
			"code":    "session_not_found",
			"message": "找不到聊天会话: " + clientSessionID,
		})
	}
	data, _ := message["data"].(map[string]any)
	nextName := strings.TrimSpace(wsAnyString(data["user_name"]))
	if nextName == "" {
		nextName = state.userName
	}
	if s.deps.ChatHub == nil {
		return s.sendResponse(requestID, false, nil, map[string]any{
			"code":    "chat_unavailable",
			"message": "内部聊天未初始化",
		})
	}
	if state.virtual != nil && state.virtual.Enabled {
		state.userName = nextName
		s.deps.ChatHub.UpdateUserName(state.client.SessionID, nextName)
		if err := s.sendChatEvent("nickname_updated", clientSessionID, map[string]any{
			"type":      "nickname_updated",
			"user_name": nextName,
			"timestamp": chatTimestamp(),
		}); err != nil {
			return err
		}
		return s.sendResponse(requestID, true, map[string]any{
			"session":   clientSessionID,
			"user_name": nextName,
		}, nil)
	}
	savedName, err := internalchat.SaveUserName(s.deps.InstanceDir, nextName)
	if err != nil {
		return s.sendResponse(requestID, false, nil, map[string]any{
			"code":    "nickname_invalid",
			"message": err.Error(),
		})
	}
	state.userName = savedName
	s.deps.ChatHub.UpdateAllUserNames(savedName)
	s.deps.ChatHub.BroadcastToChannel(internalchat.OwnerChannelKey(), "nickname_updated", map[string]any{
		"type":      "nickname_updated",
		"user_name": savedName,
		"timestamp": chatTimestamp(),
	})
	return s.sendResponse(requestID, true, map[string]any{
		"session":   clientSessionID,
		"user_name": savedName,
	}, nil)
}
