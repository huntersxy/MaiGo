package maimbridge

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/common/database"
	"MaiGo/internal/common/message_server"
	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/onebot"
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
	parsed := messagePartsFromSegments(chat.Segments)
	if content == "" {
		content = plainTextFromParts(parsed.Parts)
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
		for _, external := range parsed.ExternalAtList {
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
		MessageParts:      parsed.Parts,
		Images:            parsed.Images,
		Files:             parsed.Files,
		Dicts:             parsed.Dicts,
		Faces:             parsed.Faces,
		Voices:            parsed.Voices,
		FileNames:         parsed.FileNames,
		IsMentioned:       isMentioned,
		Time:              msgTime,
		ReceivedAt:        receivedAt,
		AtList:            parsed.AtList,
		ExternalAtList:    parsed.ExternalAtList,
		AtNames:           parsed.AtNames,
		Reply:             parsed.Reply,
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

type inboundMessageParts struct {
	Parts          []onebot.MessagePart
	Images         []onebot.ImageInfo
	Faces          []onebot.FaceInfo
	Voices         []onebot.VoiceInfo
	Files          []onebot.FileInfo
	Dicts          []any
	FileNames      []string
	AtList         []int64
	ExternalAtList []string
	AtNames        map[int64]string
	Reply          *onebot.ReplyInfo
}

// messagePartsFromSegments 还原入站消息段并收集图片/表情/语音/文件等媒体
func messagePartsFromSegments(segments []platformio.Segment) inboundMessageParts {
	out := inboundMessageParts{AtNames: map[int64]string{}}
	for i, seg := range segments {
		part := onebot.MessagePart{Index: i}
		switch seg.Kind {
		case platformio.SegmentText:
			part.Kind = "text"
			part.Text = seg.Text
		case platformio.SegmentAt:
			part.Kind = "at"
			external := strings.TrimSpace(seg.AtUserID)
			part.ExternalAtUserID = external
			if uid, ok := parseNumericID(external); ok {
				part.AtUserID = uid
			} else if external != "" {
				part.AtUserID = onebot.SyntheticIdentityID("at", external)
			}
			if part.AtUserID != 0 {
				out.AtList = append(out.AtList, part.AtUserID)
				out.ExternalAtList = append(out.ExternalAtList, external)
				if seg.Extra != nil {
					if name := strings.TrimSpace(seg.Extra["name"]); name != "" && name != external {
						out.AtNames[part.AtUserID] = name
					}
				}
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
			if externalID != "" && out.Reply == nil {
				reply := &onebot.ReplyInfo{
					ExternalMessageID: externalID,
				}
				if uid, ok := parseNumericID(externalID); ok {
					reply.MessageID = uid
				} else {
					reply.MessageID = onebot.SyntheticIdentityID("reply", externalID)
				}
				if seg.Extra != nil {
					reply.Content = strings.TrimSpace(seg.Extra["target_message_content"])
					reply.Nickname = strings.TrimSpace(seg.Extra["target_message_sender_nickname"])
					reply.GroupCard = strings.TrimSpace(seg.Extra["target_message_sender_cardname"])
					if sender := strings.TrimSpace(seg.Extra["sender_id"]); sender != "" {
						reply.ExternalSenderID = sender
						if uid, ok := parseNumericID(sender); ok {
							reply.SenderID = uid
						} else {
							reply.SenderID = onebot.SyntheticIdentityID("reply-sender", sender)
						}
					}
				}
				out.Reply = reply
			}
		case platformio.SegmentImage, platformio.SegmentEmoji:
			part.Kind = "image"
			image := onebot.ImageInfo{}
			if seg.Kind == platformio.SegmentEmoji {
				image.SubType = 1
			}
			if seg.Extra != nil {
				if encoded := strings.TrimSpace(seg.Extra["base64"]); encoded != "" {
					image.URL = onebot.ImageDataURL(encoded)
				}
				image.File = strings.TrimSpace(seg.Extra["file"])
			}
			if image.URL == "" && seg.URL != "" {
				image.URL = seg.URL
			}
			if image.URL == "" && image.File == "" {
				continue
			}
			part.Index = len(out.Images)
			out.Images = append(out.Images, image)
		case platformio.SegmentFace:
			part.Kind = "face"
			face := onebot.FaceInfo{Name: strings.TrimSpace(seg.Text)}
			if seg.Extra != nil {
				if id := strings.TrimSpace(seg.Extra["id"]); id != "" {
					if numeric, err := strconv.Atoi(id); err == nil {
						face.ID = numeric
					}
				}
				if name := strings.TrimSpace(seg.Extra["name"]); name != "" {
					face.Name = name
				}
			}
			part.Index = len(out.Faces)
			out.Faces = append(out.Faces, face)
		case platformio.SegmentVoice, platformio.SegmentRecord:
			part.Kind = "record"
			source := strings.TrimSpace(seg.URL)
			if source == "" {
				source = strings.TrimSpace(seg.Text)
			}
			if source == "" && seg.Extra != nil {
				source = strings.TrimSpace(seg.Extra["file"])
			}
			part.Text = source
			part.Index = len(out.Voices)
			out.Voices = append(out.Voices, onebot.VoiceInfo{URL: seg.URL, File: strings.TrimSpace(seg.Text)})
		case platformio.SegmentVideo:
			part.Kind = "video"
			part.Text = strings.TrimSpace(seg.URL)
		case platformio.SegmentFile:
			part.Kind = "file"
			file := onebot.FileInfo{}
			if seg.Extra != nil {
				file.Name = firstTextValue(seg.Extra["name"], seg.Extra["file_name"], seg.Extra["filename"], seg.Text)
				file.MimeType = firstTextValue(seg.Extra["mime_type"], seg.Extra["mimeType"])
				file.FileRef = firstTextValue(seg.Extra["file_ref"])
				file.Size = parseFileSize(firstTextValue(seg.Extra["size"], seg.Extra["file_size"]))
			}
			if file.Name == "" {
				file.Name = strings.TrimSpace(seg.Text)
			}
			if seg.URL != "" && file.FileRef == "" {
				file.FileRef = seg.URL
			}
			part.Index = len(out.Files)
			out.Files = append(out.Files, file)
			part.Text = file.Name
		case platformio.SegmentForward:
			part.Kind = "forward"
			part.Forward = forwardComponentFromSegments(seg.ForwardNodes)
		case platformio.SegmentDict, platformio.SegmentJSON:
			// 对齐 Python process_dict_component：保留原负载供上层生成可读摘要
			payload := seg.Data
			if payload == nil && seg.Extra != nil {
				if raw := strings.TrimSpace(seg.Extra["data"]); raw != "" {
					var decoded any
					if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
						payload = decoded
					} else {
						payload = raw
					}
				}
			}
			if payload == nil && seg.Text != "" {
				payload = seg.Text
			}
			if payload == nil {
				continue
			}
			part.Kind = "dict"
			part.Index = len(out.Dicts)
			out.Dicts = append(out.Dicts, payload)
			part.Text = seg.Text
		default:
			part.Kind = string(seg.Kind)
			part.Text = seg.Text
		}
		out.Parts = append(out.Parts, part)
	}
	return out
}

func firstTextValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
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

// parseFileSize 解析文件大小字符串；非法值按 0 处理
func parseFileSize(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}
