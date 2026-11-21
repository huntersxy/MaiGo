package bootstrap

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/chat/heart_flow"
	"MaiGo/internal/chat/image_system"
	"MaiGo/internal/chat/message_receive"
	"MaiGo/internal/chat/wiring"
	"MaiGo/internal/common/data_models/messagecomponent"
	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/platform_io/drivers/onebot"
)

// chatFrequencyFallback 动态读取实例配置，热重载后不会沿用旧频率表
func chatFrequencyFallback(lookup func() *maibotcfg.ChatReplyTimingConfig) func(*messagereceive.SessionMessage) (float64, bool) {
	if lookup == nil {
		return nil
	}
	return func(message *messagereceive.SessionMessage) (float64, bool) {
		return chatwiring.ConfigFrequencyFallback(lookup())(message)
	}
}

// chatImageFileResolver 把图片 hash 解析为图片系统缓存的真实文件
type chatImageFileResolver struct {
	manager *imagesystem.Manager
}

// ResolveImageFile 实现 chatwiring.ImageFileResolver
func (r *chatImageFileResolver) ResolveImageFile(ctx context.Context, hash string) (string, error) {
	if r == nil || r.manager == nil {
		return "", fmt.Errorf("图片系统未接线")
	}
	image, err := r.manager.GetImageFromDB(ctx, hash)
	if err != nil {
		return "", fmt.Errorf("读取图片文件失败: %w", err)
	}
	if image == nil || strings.TrimSpace(image.FullPath) == "" {
		return "", fmt.Errorf("图片文件不存在: %s", strings.TrimSpace(hash))
	}
	return image.FullPath, nil
}

// chatInboundSessionMessage 把 OneBot 入站消息转换为统一接收链的 SessionMessage
func chatInboundSessionMessage(msg *onebot.ConversationMessage, session heartflow.ChatSession) *messagereceive.SessionMessage {
	if msg == nil {
		return nil
	}
	timestamp := msg.Time
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	receivedAt := msg.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = timestamp
	}
	messageID := strings.TrimSpace(msg.ExternalMessageID)
	if messageID == "" && msg.MessageID != 0 {
		messageID = strconv.FormatInt(msg.MessageID, 10)
	}
	userID := strings.TrimSpace(msg.ExternalUserID)
	if userID == "" && msg.UserID > 0 {
		userID = strconv.FormatInt(msg.UserID, 10)
	}
	nickname := strings.TrimSpace(msg.Nickname)
	if nickname == "" {
		nickname = strings.TrimSpace(msg.GroupCard)
	}
	if nickname == "" {
		nickname = "用户" + userID
	}
	info := messagereceive.MessageInfo{
		UserInfo: messagereceive.UserInfo{
			UserID:       userID,
			UserNickname: nickname,
		},
		AdditionalConfig: msg.AdditionalConfig,
	}
	if strings.TrimSpace(msg.GroupCard) != "" {
		card := strings.TrimSpace(msg.GroupCard)
		info.UserInfo.UserCardname = &card
	}
	if session.IsGroup {
		info.GroupInfo = &messagereceive.GroupInfo{
			GroupID:   session.GroupID,
			GroupName: chatGroupName(msg, session),
		}
	}
	host := &messagereceive.SessionMessage{
		MessageID:   messageID,
		Timestamp:   timestamp,
		ReceivedAt:  receivedAt,
		Platform:    session.Platform,
		MessageInfo: info,
		// IsAt/IsMentioned 只保留「指向机器人」的上游标记；最终语义由 is_mentioned_bot_in_message 统一判定
		IsMentioned: msg.IsMentioned,
		IsNotify:    msg.IsNotify,
		IsAt:        msg.IsMentioned && (len(msg.AtList) > 0 || len(msg.ExternalAtList) > 0),
		IsEmoji:     chatHasEmoji(msg),
		IsPicture:   chatHasPicture(msg),
		SessionID:   session.SessionID,
		RawMessage:  chatInboundRawMessage(msg),
	}
	if accountID := strings.TrimSpace(session.AccountID); accountID != "" {
		host.AccountID = &accountID
	}
	if scope := strings.TrimSpace(session.Scope); scope != "" {
		host.Scope = &scope
	}
	if replyTo := chatReplyMessageID(msg); replyTo != "" {
		host.ReplyTo = &replyTo
	}
	if messageID != "" {
		platformMessageID := messageID
		host.PlatformMessageID = &platformMessageID
	}
	host.SetPlainText(msg.Content)
	return host
}

func chatGroupName(msg *onebot.ConversationMessage, session heartflow.ChatSession) string {
	name := strings.TrimSpace(msg.GroupName)
	if name == "" {
		name = strings.TrimSpace(session.GroupID)
	}
	return name
}

func chatHasPicture(msg *onebot.ConversationMessage) bool {
	for _, image := range msg.Images {
		if image.SubType != 1 {
			return true
		}
	}
	return false
}

func chatHasEmoji(msg *onebot.ConversationMessage) bool {
	if len(msg.Faces) > 0 {
		return true
	}
	for _, image := range msg.Images {
		if image.SubType == 1 {
			return true
		}
	}
	return false
}

func chatReplyMessageID(msg *onebot.ConversationMessage) string {
	if msg == nil || msg.Reply == nil {
		return ""
	}
	if value := strings.TrimSpace(msg.Reply.ExternalMessageID); value != "" {
		return value
	}
	if msg.Reply.MessageID != 0 {
		return strconv.FormatInt(msg.Reply.MessageID, 10)
	}
	return ""
}

// chatInboundRawMessage 重建消息组件；图片描述已在预处理阶段完成，这里只保留文本标记
func chatInboundRawMessage(msg *onebot.ConversationMessage) *messagecomponent.MessageSequence {
	if msg == nil {
		return nil
	}
	sequence := messagecomponent.NewMessageSequence()
	usedFiles := map[int]bool{}
	for _, part := range msg.MessageParts {
		switch part.Kind {
		case "text":
			if part.Text != "" {
				sequence.Text(part.Text)
			}
		case "at":
			target := strings.TrimSpace(part.ExternalAtUserID)
			if target == "" && part.AtUserID != 0 {
				target = strconv.FormatInt(part.AtUserID, 10)
			}
			if target == "" {
				sequence.Text("@")
			} else {
				sequence.At(target)
			}
		case "image":
			sequence.Text(chatImageLabel(msg, part.Index))
		case "face":
			sequence.Text(chatFaceLabel(msg, part.Index))
		case "reply":
			sequence.Components = append(sequence.Components, chatReplyComponent(msg))
		case "record":
			voice := messagecomponent.NewVoiceComponent("", "", nil)
			if part.Index >= 0 && part.Index < len(msg.Voices) {
				voice.Source = msg.Voices[part.Index].VoiceSource()
			}
			// 非 OneBot 适配器可直接把媒体引用放在 record 段文本里
			if strings.TrimSpace(voice.Source) == "" {
				voice.Source = strings.TrimSpace(part.Text)
			}
			sequence.Components = append(sequence.Components, voice)
		case "video":
			sequence.Text("[视频]")
		case "file":
			file, index, ok := chatFileComponent(msg, part.Index)
			if ok {
				usedFiles[index] = true
				sequence.File(file)
			} else {
				sequence.Text("[文件]")
			}
		case "card":
			sequence.Text(chatCardText(msg, part.Index))
		case "forward":
			if part.Forward != nil {
				sequence.Components = append(sequence.Components, messagecomponent.CloneComponent(*part.Forward))
			} else {
				sequence.Text("[合并转发消息]")
			}
		}
	}
	for index, file := range msg.Files {
		if usedFiles[index] {
			continue
		}
		sequence.File(messagecomponent.FileComponent{
			Name:     strings.TrimSpace(file.Name),
			MimeType: strings.TrimSpace(file.MimeType),
			Size:     chatFileSize(file.Size),
			FileRef:  strings.TrimSpace(file.FileRef),
		})
	}
	if len(sequence.Components) == 0 && strings.TrimSpace(msg.Content) != "" {
		sequence.Text(msg.Content)
	}
	return sequence
}

func chatImageLabel(msg *onebot.ConversationMessage, index int) string {
	if msg == nil || index < 0 || index >= len(msg.Images) {
		return "[图片]"
	}
	image := msg.Images[index]
	if image.SubType == 1 {
		if desc := strings.TrimSpace(image.Desc); desc != "" {
			return "[表情包：" + desc + "]"
		}
		return "[表情包]"
	}
	if desc := strings.TrimSpace(image.Desc); desc != "" {
		return "[图片：" + desc + "]"
	}
	return "[图片]"
}

func chatFaceLabel(msg *onebot.ConversationMessage, index int) string {
	if msg == nil || index < 0 || index >= len(msg.Faces) {
		return "[表情]"
	}
	if name := strings.TrimSpace(msg.Faces[index].Name); name != "" {
		return "[" + name + "]"
	}
	return "[表情]"
}

func chatReplyComponent(msg *onebot.ConversationMessage) messagecomponent.ReplyComponent {
	if msg == nil || msg.Reply == nil {
		return messagecomponent.ReplyComponent{}
	}
	component := messagecomponent.ReplyComponent{TargetMessageID: chatReplyMessageID(msg)}
	if content := strings.TrimSpace(msg.Reply.Content); content != "" {
		component.TargetContent = &content
	}
	senderID := strings.TrimSpace(msg.Reply.ExternalSenderID)
	if senderID == "" && msg.Reply.SenderID != 0 {
		senderID = strconv.FormatInt(msg.Reply.SenderID, 10)
	}
	if senderID != "" {
		component.SenderID = &senderID
	}
	if nickname := strings.TrimSpace(msg.Reply.Nickname); nickname != "" {
		component.SenderNickname = &nickname
	}
	if card := strings.TrimSpace(msg.Reply.GroupCard); card != "" {
		component.SenderCardname = &card
	}
	return component
}

func chatFileComponent(msg *onebot.ConversationMessage, index int) (messagecomponent.FileComponent, int, bool) {
	if msg == nil || index < 0 {
		return messagecomponent.FileComponent{}, -1, false
	}
	if index < len(msg.Files) {
		file := msg.Files[index]
		return messagecomponent.FileComponent{
			Name:     strings.TrimSpace(file.Name),
			MimeType: strings.TrimSpace(file.MimeType),
			Size:     chatFileSize(file.Size),
			FileRef:  strings.TrimSpace(file.FileRef),
		}, index, true
	}
	if index < len(msg.FileNames) {
		return messagecomponent.FileComponent{Name: strings.TrimSpace(msg.FileNames[index])}, -1, true
	}
	return messagecomponent.FileComponent{}, -1, false
}

func chatFileSize(size int64) string {
	if size <= 0 {
		return ""
	}
	return strconv.FormatInt(size, 10)
}

func chatCardText(msg *onebot.ConversationMessage, index int) string {
	if msg == nil || index < 0 || index >= len(msg.Cards) {
		return "[卡片]"
	}
	card := msg.Cards[index]
	return card.Format()
}
