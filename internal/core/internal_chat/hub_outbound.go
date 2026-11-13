package internalchat

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// SendBotText 广播纯文本机器人消息
func (h *Hub) SendBotText(_ context.Context, conversationKind string, targetID int64, text string) (int64, error) {
	channelKey, err := h.conversationChannel(conversationKind, targetID)
	if err != nil {
		return 0, err
	}
	messageID := NextNumericMessageID()
	h.BroadcastToChannel(channelKey, "bot_message", h.botMessagePayload(channelKey, strconv.FormatInt(messageID, 10), text, "text", nil))
	return messageID, nil
}

// SendBotRich 广播带引用与 @ 的机器人消息，对齐 internal 出站的富文本段
func (h *Hub) SendBotRich(ctx context.Context, conversationKind string, targetID int64, text string, replyTo int64, mentions []int64) (int64, error) {
	if replyTo == 0 && len(mentions) == 0 {
		return h.SendBotText(ctx, conversationKind, targetID, text)
	}
	channelKey, err := h.conversationChannel(conversationKind, targetID)
	if err != nil {
		return 0, err
	}
	segments := make([]map[string]any, 0, len(mentions)+2)
	if replyTo != 0 {
		segments = append(segments, map[string]any{
			"type": "reply",
			"data": map[string]any{"target_message_id": strconv.FormatInt(replyTo, 10)},
		})
	}
	for _, mention := range mentions {
		if mention <= 0 {
			continue
		}
		segments = append(segments, map[string]any{
			"type": "at",
			"data": map[string]any{
				"target_user_id":       strconv.FormatInt(mention, 10),
				"target_user_nickname": nil,
				"target_user_cardname": nil,
			},
		})
	}
	if text != "" {
		segments = append(segments, map[string]any{"type": "text", "data": text})
	}
	h.fillReplyTargets(ctx, segments)
	messageID := NextNumericMessageID()
	h.BroadcastToChannel(channelKey, "bot_message", h.botMessagePayload(channelKey, strconv.FormatInt(messageID, 10), text, "rich", segments))
	return messageID, nil
}

// SendBotImage 广播机器人图片消息
func (h *Hub) SendBotImage(_ context.Context, conversationKind string, targetID int64, filePath string, isSticker bool) (int64, error) {
	segment, err := imageSegment(filePath, isSticker)
	if err != nil {
		return 0, err
	}
	channelKey, err := h.conversationChannel(conversationKind, targetID)
	if err != nil {
		return 0, err
	}
	messageID := NextNumericMessageID()
	segments := []map[string]any{segment}
	h.BroadcastToChannel(channelKey, "bot_message", h.botMessagePayload(channelKey, strconv.FormatInt(messageID, 10), "", "rich", segments))
	return messageID, nil
}

// SendBotSegments 广播富文本机器人消息
func (h *Hub) SendBotSegments(plainText string, segments []map[string]any) {
	if h == nil {
		return
	}
	messageType := "rich"
	if len(segments) == 1 && segments[0]["type"] == "text" {
		messageType = "text"
		segments = nil
	}
	h.fillReplyTargets(context.Background(), segments)
	messageID := NextNumericMessageID()
	channelKey := OwnerChannelKey()
	h.BroadcastToChannel(channelKey, "bot_message", h.botMessagePayload(channelKey, strconv.FormatInt(messageID, 10), plainText, messageType, segments))
}

// SendBotFile 广播普通内部聊天文件消息并返回数值消息 ID
func (h *Hub) SendBotFile(file map[string]any) (int64, error) {
	return h.SendBotFileToChannel(OwnerChannelKey(), file)
}

// SendBotFileToChannel 向指定频道广播文件消息
func (h *Hub) SendBotFileToChannel(channelKey string, file map[string]any) (int64, error) {
	if h == nil {
		return 0, fmt.Errorf("内部聊天未初始化")
	}
	if len(file) == 0 {
		return 0, fmt.Errorf("文件附件不能为空")
	}
	channelKey = strings.TrimSpace(channelKey)
	if channelKey == "" {
		return 0, fmt.Errorf("内部聊天频道不能为空")
	}
	messageID := NextNumericMessageID()
	segments := []map[string]any{{"type": "file", "data": file}}
	h.BroadcastToChannel(channelKey, "bot_message", h.botMessagePayload(channelKey, strconv.FormatInt(messageID, 10), "", "rich", segments))
	return messageID, nil
}

// BroadcastTyping 广播普通内部聊天机器人输入状态
func (h *Hub) BroadcastTyping(isTyping bool) {
	h.BroadcastTypingTo(OwnerChannelKey(), isTyping)
}

// BroadcastTypingTo 广播指定频道的机器人输入状态
func (h *Hub) BroadcastTypingTo(channelKey string, isTyping bool) {
	h.BroadcastToChannel(channelKey, "typing", map[string]any{"type": "typing", "is_typing": isTyping})
}
