package internalchat

import (
	"strconv"
	"strings"

	memory "maibot-go/internal/common/database"
	"maibot-go/internal/platform_io/drivers/onebot"
)

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

// ConversationChannelKey 返回 Agent 消息对应的内部聊天频道键。
//
// WebUI 聊天与插件中继共用同一个键，原生会话调度器才能把同一会话的消息排进同一条 FIFO。
func ConversationChannelKey(msg *onebot.ConversationMessage) string {
	if msg == nil {
		return ""
	}
	platform := strings.TrimSpace(msg.Platform)
	kind := strings.TrimSpace(msg.ConversationKind)
	if platform == Platform {
		if kind == memory.ConversationKindGroup {
			groupID := strings.TrimSpace(msg.GroupKey)
			if groupID == "" {
				groupID = strconv.FormatInt(msg.TargetID, 10)
			}
			return GroupChannel(groupID)
		}
		return OwnerChannelKey()
	}
	return strings.Join([]string{
		platform,
		strings.TrimSpace(msg.AccountID),
		strings.TrimSpace(msg.Scope),
		kind,
		strconv.FormatInt(msg.TargetID, 10),
		strconv.FormatInt(msg.UserID, 10),
	}, "|")
}
