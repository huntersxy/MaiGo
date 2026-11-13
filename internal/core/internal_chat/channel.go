package internalchat

import "strings"

// OwnerChannelKey 普通内部聊天的私聊频道键
func OwnerChannelKey() string {
	return PrivateChannel(OwnerUserID)
}

// PrivateChannel 构造私聊频道键
func PrivateChannel(userID string) string {
	return "private:" + strings.TrimSpace(userID)
}

// GroupChannel 构造群聊频道键
func GroupChannel(groupID string) string {
	return "group:" + strings.TrimSpace(groupID)
}

// ChannelGroupID 从群聊频道键还原 group_id
func ChannelGroupID(channelKey string) (string, bool) {
	trimmed := strings.TrimSpace(channelKey)
	if !strings.HasPrefix(trimmed, "group:") {
		return "", false
	}
	groupID := strings.TrimSpace(strings.TrimPrefix(trimmed, "group:"))
	return groupID, groupID != ""
}
