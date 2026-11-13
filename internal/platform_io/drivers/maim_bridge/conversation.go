package maimbridge

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"maibot-go/internal/common/database"
	"maibot-go/internal/common/message_server"
	"maibot-go/internal/platform_io"
	"maibot-go/internal/platform_io/drivers/onebot"
)

const inboundMetaConversationMessage = "maimbridge.conversation_message"

// ConversationMessageFromInbound 从 maim_message 入站封装还原/构造 Agent 可用的 ConversationMessage
func ConversationMessageFromInbound(envelope platformio.InboundMessageEnvelope) (*onebot.ConversationMessage, bool) {
	if envelope.DriverID != DriverIDMaimMsg {
		return nil, false
	}
	if envelope.Metadata != nil {
		raw, ok := envelope.Metadata[inboundMetaConversationMessage]
		if ok && raw != nil {
			msg, ok := raw.(*onebot.ConversationMessage)
			return msg, ok && msg != nil
		}
	}
	if envelope.ChatMessage == nil {
		return nil, false
	}
	base, _ := MessageBaseFromInbound(envelope)
	msg := conversationFromChat(*envelope.ChatMessage, base, envelope.ReceivedAt)
	if msg == nil {
		return nil, false
	}
	return msg, true
}

// ExternalIdentity 返回非 QQ 入站保留的原始字符串身份
func ExternalIdentity(msg *onebot.ConversationMessage) (platform, targetID, userID, messageID string, ok bool) {
	if msg == nil {
		return "", "", "", "", false
	}
	platform = strings.TrimSpace(msg.Platform)
	targetID = strings.TrimSpace(msg.ExternalTargetID)
	userID = strings.TrimSpace(msg.ExternalUserID)
	messageID = strings.TrimSpace(msg.ExternalMessageID)
	if msg.AdditionalConfig != nil {
		if platform == "" {
			platform, _ = msg.AdditionalConfig["maimbridge.external_platform"].(string)
		}
		if targetID == "" {
			targetID, _ = msg.AdditionalConfig["maimbridge.external_target_id"].(string)
		}
		if userID == "" {
			userID, _ = msg.AdditionalConfig["maimbridge.external_user_id"].(string)
		}
		if messageID == "" {
			messageID, _ = msg.AdditionalConfig["maimbridge.external_message_id"].(string)
		}
	}
	return platform, targetID, userID, messageID, platform != "" || targetID != "" || userID != "" || messageID != ""
}

// UnsupportedAgentIdentityError 说明消息为何缺少可进入 Agent 的会话身份。
// 经过 ConversationMessageFromInbound 的非数值身份会生成代理键，此处返回 nil。
func UnsupportedAgentIdentityError(msg *onebot.ConversationMessage) error {
	if msg == nil {
		return errUnsupportedInbound
	}
	if msg.TargetID > 0 && msg.UserID > 0 && msg.MessageID > 0 {
		return nil
	}
	platform, targetID, userID, messageID, ok := ExternalIdentity(msg)
	if !ok {
		return fmt.Errorf("入站消息缺少可用的数值 ID，且未保留外部字符串身份")
	}
	return fmt.Errorf("外部身份未生成 Agent 代理键: platform=%s target=%s user=%s message=%s",
		platform, targetID, userID, messageID)
}

// ReadyForAgent 判断合成消息是否满足 Agent 链的最小要求（文本群/私聊）。
// 非数值外部 ID 已在 conversationFromChat 中生成稳定代理键，原始字符串保留在 External*。
func ReadyForAgent(msg *onebot.ConversationMessage) bool {
	if msg == nil {
		return false
	}
	if msg.TargetID <= 0 || msg.UserID <= 0 || msg.MessageID <= 0 {
		return false
	}
	if msg.ConversationKind != memory.ConversationKindGroup && msg.ConversationKind != memory.ConversationKindPrivate {
		return false
	}
	if strings.TrimSpace(msg.Content) == "" && len(msg.MessageParts) == 0 {
		return false
	}
	return true
}

// ApplyMentionSelf 根据 @ 段与 OneBot 机器账号补全 IsMentioned
func ApplyMentionSelf(msg *onebot.ConversationMessage, selfID int64) {
	if msg == nil || selfID <= 0 {
		return
	}
	if msg.IsMentioned {
		return
	}
	for _, uid := range msg.AtList {
		if uid == selfID || uid == onebot.AtAllUserID {
			msg.IsMentioned = true
			return
		}
	}
}

func conversationFromBase(msg maimmsg.MessageBase, chat platformio.ChatMessage, receivedAt time.Time) (*onebot.ConversationMessage, error) {
	conv := conversationFromChat(chat, msg, receivedAt)
	if conv == nil {
		return nil, errUnsupportedInbound
	}
	return conv, nil
}

func conversationFromChat(chat platformio.ChatMessage, base maimmsg.MessageBase, receivedAt time.Time) *onebot.ConversationMessage {
	kind, ok := conversationKindFromChatType(chat.ChatType)
	if !ok {
		return nil
	}
	// 非 QQ 平台 ID 可能是字符串；原始字符串保留在 External*，
	// Agent 仍需要的 int64 字段用稳定代理键，避免字符串身份被静默丢弃。
	targetID, targetNumeric := parseNumericID(chat.TargetID)
	userID, userNumeric := parseNumericID(chat.UserID)
	messageID, messageNumeric := parseNumericID(chat.MessageID)
	externalTarget := strings.TrimSpace(chat.TargetID)
	externalUser := strings.TrimSpace(chat.UserID)
	externalMessage := strings.TrimSpace(chat.MessageID)
	routeScope := strings.TrimSpace(chat.Scope)
	if !targetNumeric && externalTarget != "" {
		targetID = onebot.SyntheticIdentityID("target", chat.Platform, chat.AccountID, routeScope, externalTarget)
	}
	if !userNumeric && externalUser != "" {
		userID = onebot.SyntheticIdentityID("user", chat.Platform, chat.AccountID, routeScope, externalUser)
	}
	if !messageNumeric && externalMessage != "" {
		messageID = onebot.SyntheticIdentityID("message", chat.Platform, chat.AccountID, routeScope, externalMessage)
	}
	externalIdentity := map[string]any{
		"maimbridge.external_platform":   chat.Platform,
		"maimbridge.external_target_id":  chat.TargetID,
		"maimbridge.external_user_id":    chat.UserID,
		"maimbridge.external_message_id": chat.MessageID,
		"maimbridge.numeric_identity_ok": targetNumeric && userNumeric && messageNumeric,
	}
	if !targetNumeric || !userNumeric || !messageNumeric {
		externalIdentity["maimbridge.synthetic_identity_ok"] = true
	}
	if chat.AccountID != "" {
		externalIdentity["maimbridge.external_account_id"] = chat.AccountID
	}
	if chat.Scope != "" {
		externalIdentity["maimbridge.external_scope"] = chat.Scope
	}

	content := strings.TrimSpace(chat.PlainText)
	parts, atList, externalAtList, atNames, reply := messagePartsFromSegments(chat.Segments)
	if content == "" {
		content = plainTextFromParts(parts)
	}

	info := base.MessageInfo
	nickname := nicknameFromBase(info)
	groupCard := groupCardFromBase(info)
	groupName := ""
	if info.GroupInfo != nil {
		groupName = strings.TrimSpace(info.GroupInfo.GroupName)
	}

	msgTime := receivedAt
	if info.Time != nil && *info.Time > 0 {
		msgTime = time.UnixMilli(int64(*info.Time * 1000))
	}
	if receivedAt.IsZero() {
		receivedAt = time.Now()
	}
	if chat.ReceivedAt.After(time.Time{}) {
		receivedAt = chat.ReceivedAt
	}

	isMentioned := kind == memory.ConversationKindPrivate
	if meta := chat.Metadata; meta != nil {
		if v, ok := meta["is_mentioned"].(bool); ok {
			isMentioned = v
		}
	}
	if !isMentioned && strings.TrimSpace(chat.AccountID) != "" {
		for _, external := range externalAtList {
			if external == strings.TrimSpace(chat.AccountID) {
				isMentioned = true
				break
			}
		}
	}

	groupKey := ""
	if kind == memory.ConversationKindGroup {
		groupKey = externalTarget
	}
	return &onebot.ConversationMessage{
		Platform:          chat.Platform,
		AccountID:         strings.TrimSpace(chat.AccountID),
		Scope:             routeScope,
		MessageID:         messageID,
		ConversationKind:  kind,
		TargetID:          targetID,
		UserID:            userID,
		ExternalMessageID: externalMessage,
		ExternalTargetID:  externalTarget,
		ExternalUserID:    externalUser,
		Nickname:          nickname,
		GroupCard:         groupCard,
		GroupName:         groupName,
		Content:           content,
		MessageParts:      parts,
		IsMentioned:       isMentioned,
		Time:              msgTime,
		ReceivedAt:        receivedAt,
		AtList:            atList,
		ExternalAtList:    externalAtList,
		AtNames:           atNames,
		Reply:             reply,
		AdditionalConfig:  externalIdentity,
		GroupKey:          groupKey,
	}
}

func conversationKindFromChatType(chatType platformio.ChatType) (string, bool) {
	switch chatType {
	case platformio.ChatTypeGroup:
		return memory.ConversationKindGroup, true
	case platformio.ChatTypePrivate:
		return memory.ConversationKindPrivate, true
	default:
		return "", false
	}
}

func parseNumericID(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

func messagePartsFromSegments(segments []platformio.Segment) ([]onebot.MessagePart, []int64, []string, map[int64]string, *onebot.ReplyInfo) {
	if len(segments) == 0 {
		return nil, nil, nil, nil, nil
	}
	parts := make([]onebot.MessagePart, 0, len(segments))
	var atList []int64
	var externalAtList []string
	atNames := map[int64]string{}
	var reply *onebot.ReplyInfo
	for i, seg := range segments {
		part := onebot.MessagePart{Index: i}
		switch seg.Kind {
		case platformio.SegmentText:
			part.Kind = "text"
			part.Text = seg.Text
		case platformio.SegmentAt:
			part.Kind = "at"
			external := strings.TrimSpace(seg.AtUserID)
			if uid, ok := parseNumericID(external); ok {
				part.AtUserID = uid
			} else if external != "" {
				part.AtUserID = onebot.SyntheticIdentityID("at", external)
			}
			part.ExternalAtUserID = external
			if part.AtUserID != 0 {
				atList = append(atList, part.AtUserID)
				externalAtList = append(externalAtList, external)
				name := external
				if seg.Extra != nil && strings.TrimSpace(seg.Extra["name"]) != "" {
					name = strings.TrimSpace(seg.Extra["name"])
				}
				atNames[part.AtUserID] = name
			}
		case platformio.SegmentReply:
			part.Kind = "reply"
			externalID := strings.TrimSpace(seg.Text)
			if seg.Extra != nil {
				if id := strings.TrimSpace(seg.Extra["id"]); id != "" {
					externalID = id
				}
			}
			part.Text = externalID
			if externalID != "" && reply == nil {
				reply = &onebot.ReplyInfo{
					MessageID:         onebot.SyntheticIdentityID("reply", externalID),
					ExternalMessageID: externalID,
				}
				if seg.Extra != nil {
					if sender := strings.TrimSpace(seg.Extra["sender_id"]); sender != "" {
						reply.ExternalSenderID = sender
						if uid, ok := parseNumericID(sender); ok {
							reply.SenderID = uid
						} else {
							reply.SenderID = onebot.SyntheticIdentityID("reply-sender", sender)
						}
					}
				}
			}
		default:
			part.Kind = string(seg.Kind)
			part.Text = seg.Text
		}
		parts = append(parts, part)
	}
	return parts, atList, externalAtList, atNames, reply
}

func plainTextFromParts(parts []onebot.MessagePart) string {
	var b strings.Builder
	for _, part := range parts {
		if part.Kind == "text" && part.Text != "" {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

func nicknameFromBase(info maimmsg.BaseMessageInfo) string {
	if info.SenderInfo != nil && info.SenderInfo.UserInfo != nil {
		if n := strings.TrimSpace(info.SenderInfo.UserInfo.UserNickname); n != "" {
			return n
		}
	}
	if info.UserInfo != nil {
		return strings.TrimSpace(info.UserInfo.UserNickname)
	}
	return ""
}

func groupCardFromBase(info maimmsg.BaseMessageInfo) string {
	if info.SenderInfo != nil && info.SenderInfo.UserInfo != nil {
		return strings.TrimSpace(info.SenderInfo.UserInfo.UserCardname)
	}
	if info.UserInfo != nil {
		return strings.TrimSpace(info.UserInfo.UserCardname)
	}
	return ""
}
