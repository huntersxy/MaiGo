package internalchat

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"maibot-go/internal/common/database"
	"maibot-go/internal/platform_io/drivers/onebot"

	"go.uber.org/zap"
)

// Relay 把插件界面（桌宠、插件能力）的发言投递到内部统一聊天
type Relay struct {
	hub         *Hub
	instanceDir string

	mu      sync.RWMutex
	deliver func(*onebot.ConversationMessage) error
}

// NewRelay 创建插件中继，deliver 由 Agent 在启动后注入
func NewRelay(hub *Hub, instanceDir string) *Relay {
	return &Relay{hub: hub, instanceDir: instanceDir}
}

// SetDeliver 注入 Agent 入站投递函数；投递失败会回传给插件调用方。
func (r *Relay) SetDeliver(deliver func(*onebot.ConversationMessage) error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.deliver = deliver
	r.mu.Unlock()
}

// RelayPluginChatMessage 以主人身份投递插件消息，并返回内部聊天消息 ID
func (r *Relay) RelayPluginChatMessage(_ context.Context, pluginID, content string, images []map[string]any) (string, error) {
	if r == nil || r.hub == nil {
		return "", fmt.Errorf("内部聊天服务未就绪")
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return "", fmt.Errorf("插件 ID 不能为空")
	}
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("消息内容不能为空")
	}
	r.mu.RLock()
	deliver := r.deliver
	r.mu.RUnlock()
	if deliver == nil {
		return "", fmt.Errorf("内部聊天服务未就绪")
	}
	payloadImages := make([]any, 0, len(images))
	chatImages := make([]onebot.ImageInfo, 0, len(images))
	for _, image := range images {
		mimeType := strings.TrimSpace(stringValue(image["mime_type"]))
		encoded := strings.TrimSpace(stringValue(image["base64"]))
		name := strings.TrimSpace(stringValue(image["name"]))
		if mimeType == "" || encoded == "" {
			return "", fmt.Errorf("图片缺少 base64 数据")
		}
		payloadImages = append(payloadImages, map[string]any{
			"name":      name,
			"mime_type": mimeType,
			"base64":    encoded,
		})
		chatImages = append(chatImages, onebot.ImageInfo{
			URL: "data:" + mimeType + ";base64," + encoded,
		})
	}
	messageID := NextNumericMessageID()
	senderName := r.userName()
	payload := r.hub.BroadcastUserMessage(UserMessage{
		Content:      content,
		RawContent:   content,
		MessageID:    strconv.FormatInt(messageID, 10),
		Images:       payloadImages,
		SenderName:   senderName,
		SenderUserID: OwnerUserID,
		Origin:       "plugin:" + pluginID,
	})
	now := time.Now()
	chatMessage := &onebot.ConversationMessage{
		Platform:         Platform,
		ConversationKind: memory.ConversationKindPrivate,
		MessageID:        messageID,
		TargetID:         OwnerNumericID,
		UserID:           OwnerNumericID,
		Nickname:         senderName,
		Content:          content,
		Time:             now,
		ReceivedAt:       now,
		AdditionalConfig: map[string]any{PluginRelayConfigKey: pluginID},
	}
	if content != "" {
		chatMessage.MessageParts = append(chatMessage.MessageParts, onebot.MessagePart{Kind: "text", Text: content})
	}
	for index, image := range chatImages {
		chatMessage.Images = append(chatMessage.Images, image)
		chatMessage.MessageParts = append(chatMessage.MessageParts, onebot.MessagePart{Kind: "image", Index: index})
	}
	if err := deliver(chatMessage); err != nil {
		return "", fmt.Errorf("投递聊天消息失败: %w", err)
	}
	payloadID, _ := payload["message_id"].(string)
	if payloadID == "" {
		payloadID = strconv.FormatInt(messageID, 10)
	}
	return payloadID, nil
}

func (r *Relay) userName() string {
	name, err := LoadUserName(r.instanceDir, "")
	if err != nil {
		zap.L().Warn("读取内部聊天昵称失败，使用默认昵称", zap.Error(err))
		return DefaultUserName
	}
	if name == "" {
		return DefaultUserName
	}
	return name
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}
