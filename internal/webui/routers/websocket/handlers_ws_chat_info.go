package websocket

import (
	"strconv"

	"MaiGo/internal/core/internal_chat"
)

// chatSessionInfo 构造 session_info 消息
func (s *unifiedWSSession) chatSessionInfo(internalSessionID, userName string, clientInfo map[string]any, virtual *internalchat.VirtualIdentity) map[string]any {
	botName := "MaiBot"
	if s.deps.ChatHub != nil {
		botName = s.deps.ChatHub.BotName()
	}
	platform := internalchat.Platform
	if virtual != nil && virtual.Enabled {
		platform = virtual.PlatformName()
	}
	info := map[string]any{
		"type":         "session_info",
		"session_id":   internalSessionID,
		"user_id":      internalchat.OwnerUserID,
		"user_name":    userName,
		"bot_name":     botName,
		"platform":     platform,
		"client_mode":  chatClientMode(clientInfo),
		"client":       clientInfo,
		"capabilities": map[string]any{"history": true, "rich_segments": true, "typing": true, "attachments": true, "nickname_update": true},
	}
	if s.deps.Agent != nil {
		if selfID := s.deps.Agent.SelfID(); selfID > 0 {
			info["bot_qq"] = strconv.FormatInt(selfID, 10)
		}
	}
	if virtual != nil && virtual.Enabled {
		info["virtual_mode"] = true
		info["group_id"] = virtual.GroupID
		info["virtual_identity"] = map[string]any{
			"platform":      virtual.Platform,
			"user_id":       virtual.UserID,
			"user_nickname": virtual.UserNickname,
			"group_name":    virtual.GroupName,
		}
	}
	return info
}

// chatWelcomeMessage 对齐 build_welcome_message
func (s *unifiedWSSession) chatWelcomeMessage(virtual *internalchat.VirtualIdentity) string {
	botName := "MaiBot"
	if s.deps.ChatHub != nil {
		botName = s.deps.ChatHub.BotName()
	}
	if virtual != nil && virtual.Enabled {
		return "已以 " + virtual.UserNickname + " 的身份连接到「" + virtual.GroupName + "」，开始与 " + botName + " 对话吧！"
	}
	return "已连接到本地聊天室，可以开始与 " + botName + " 对话了！"
}
