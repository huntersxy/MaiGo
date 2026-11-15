package chat

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"MaiGo/internal/core/internal_chat"
)

// ChatBroadcaster 把统一发送链的 WebUI 消息写入内部聊天 Hub
type ChatBroadcaster struct {
	Hub *internalchat.Hub
}

// NewChatBroadcaster 创建 WebUI 广播适配器
func NewChatBroadcaster(hub *internalchat.Hub) *ChatBroadcaster {
	return &ChatBroadcaster{Hub: hub}
}

// BroadcastToGroup 实现 messagereceive.WebUIBroadcaster
func (b *ChatBroadcaster) BroadcastToGroup(_ context.Context, groupID string, payload map[string]any, userID string) error {
	if b == nil || b.Hub == nil {
		return errors.New("WebUI 内部聊天 Hub 未接线")
	}
	if payload == nil {
		return errors.New("WebUI 广播负载不能为空")
	}
	copied := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		copied[key] = value
	}
	if _, ok := copied["message_id"].(string); !ok {
		copied["message_id"] = strconv.FormatInt(internalchat.NextNumericMessageID(), 10)
	}
	groupID = strings.TrimSpace(groupID)
	userID = strings.TrimSpace(userID)
	channelKey := internalchat.OwnerChannelKey()
	switch {
	case groupID != "":
		channelKey = internalchat.GroupChannel(groupID)
	case userID != "":
		channelKey = internalchat.PrivateChannel(userID)
	}
	if _, ok := copied["group_id"]; !ok {
		if groupID != "" {
			copied["group_id"] = groupID
		} else {
			copied["group_id"] = nil
		}
	}
	b.Hub.BroadcastToChannel(channelKey, "bot_message", copied)
	return nil
}
