package runtime

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/common/database"
	"MaiGo/internal/platform_io/drivers/onebot"
)

// DecodeExternalPluginMessage 将插件网关消息转换为统一入站消息
// 对齐 src/plugin_runtime/host/message_gateway.py receive_external_message
func DecodeExternalPluginMessage(message map[string]any) (*onebot.ConversationMessage, error) {
	if len(message) == 0 {
		return nil, fmt.Errorf("外部消息为空")
	}
	platform := strings.TrimSpace(stringValueOf(message["platform"]))
	if platform == "" {
		return nil, fmt.Errorf("外部消息缺少 platform")
	}
	accountID := strings.TrimSpace(firstNonEmptyMapString(message, "account_id", "self_id"))
	scope := strings.TrimSpace(stringValueOf(message["scope"]))
	groupID := strings.TrimSpace(firstNonEmptyMapString(message, "group_id"))
	externalTarget := strings.TrimSpace(firstNonEmptyMapString(message, "target_id"))
	externalUser := strings.TrimSpace(firstNonEmptyMapString(message, "user_id"))
	// 标准 MessageDict 把归属放在 message_info 内，与顶层兼容字段并存
	if info, ok := message["message_info"].(map[string]any); ok {
		if groupID == "" {
			if group, ok := info["group_info"].(map[string]any); ok {
				groupID = strings.TrimSpace(firstNonEmptyMapString(group, "group_id"))
			}
		}
		if externalUser == "" {
			if user, ok := info["user_info"].(map[string]any); ok {
				externalUser = strings.TrimSpace(firstNonEmptyMapString(user, "user_id"))
			}
		}
	}
	if externalTarget == "" {
		externalTarget = groupID
	}
	if externalUser == "" {
		if info, ok := message["user_info"].(map[string]any); ok {
			externalUser = strings.TrimSpace(firstNonEmptyMapString(info, "user_id"))
		}
	}
	if externalTarget == "" {
		// 私聊消息没有显式 target_id 时，接收者就是发送者
		externalTarget = externalUser
	}
	if externalTarget == "" {
		return nil, fmt.Errorf("外部消息缺少 target_id 或 group_id")
	}
	if externalUser == "" {
		externalUser = externalTarget
	}
	kind := memory.ConversationKindPrivate
	chatType := strings.ToLower(strings.TrimSpace(firstNonEmptyMapString(message, "chat_type", "message_type")))
	if groupID != "" || chatType == "group" {
		kind = memory.ConversationKindGroup
	}
	targetID, err := strconv.ParseInt(externalTarget, 10, 64)
	if err != nil || targetID <= 0 {
		targetID = onebot.SyntheticIdentityID("target", platform, accountID, scope, externalTarget)
	}
	userID, err := strconv.ParseInt(externalUser, 10, 64)
	if err != nil || userID <= 0 {
		if externalUser == "" {
			externalUser = externalTarget
		}
		userID = onebot.SyntheticIdentityID("user", platform, accountID, scope, externalUser)
	}
	messageIDText := strings.TrimSpace(firstNonEmptyMapString(message, "message_id", "msg_id"))
	messageID, err := strconv.ParseInt(messageIDText, 10, 64)
	if err != nil || messageID <= 0 {
		messageID = onebot.SyntheticIdentityID("message", platform, accountID, scope, externalTarget, messageIDText)
	}
	content := pluginGatewayContent(message)
	parts, images, faces, voices, fileNames, atList, externalAtList, atNames, reply := pluginGatewayParts(message)
	if content == "" {
		content = pluginGatewayText(parts)
	}
	if content == "" {
		content = "[外部消息]"
	}
	nickname := strings.TrimSpace(firstNonEmptyMapString(message, "nickname", "user_nickname"))
	if nickname == "" {
		if info, ok := message["user_info"].(map[string]any); ok {
			nickname = strings.TrimSpace(firstNonEmptyMapString(info, "user_nickname", "nickname"))
		}
	}
	if nickname == "" {
		if info, ok := message["message_info"].(map[string]any); ok {
			if user, ok := info["user_info"].(map[string]any); ok {
				nickname = strings.TrimSpace(firstNonEmptyMapString(user, "user_nickname", "nickname"))
			}
		}
	}
	now := time.Now()
	isMentioned := pluginGatewayBool(message["is_mentioned"]) || pluginGatewayBool(message["is_at"])
	if !isMentioned && accountID != "" {
		for _, external := range externalAtList {
			if external == accountID {
				isMentioned = true
				break
			}
		}
	}
	additional := map[string]any{}
	if info, ok := message["message_info"].(map[string]any); ok {
		if cfg, ok := info["additional_config"].(map[string]any); ok {
			for key, value := range cfg {
				additional[key] = value
			}
		}
	}
	if pluginGatewayBool(message["is_command"]) || strings.HasPrefix(content, "/") {
		additional["is_command"] = true
	}
	msg := &onebot.ConversationMessage{
		Platform:          platform,
		AccountID:         accountID,
		Scope:             scope,
		MessageID:         messageID,
		ConversationKind:  kind,
		TargetID:          targetID,
		UserID:            userID,
		ExternalMessageID: messageIDText,
		ExternalTargetID:  externalTarget,
		ExternalUserID:    externalUser,
		Nickname:          nickname,
		Content:           content,
		FinalContent:      content,
		MessageParts:      parts,
		Images:            images,
		Faces:             faces,
		Voices:            voices,
		FileNames:         fileNames,
		AtList:            atList,
		ExternalAtList:    externalAtList,
		AtNames:           atNames,
		Reply:             reply,
		IsMentioned:       isMentioned,
		IsNotify:          pluginGatewayBool(message["is_notify"]),
		AdditionalConfig:  additional,
		Time:              now,
		ReceivedAt:        now,
	}
	return msg, nil
}

func pluginGatewayContent(message map[string]any) string {
	if text := strings.TrimSpace(stringValueOf(message["processed_plain_text"])); text != "" {
		return text
	}
	raw, ok := message["message"]
	if !ok || raw == nil {
		raw = message["content"]
	}
	if text, ok := raw.(string); ok {
		return strings.TrimSpace(text)
	}
	items, ok := raw.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		node, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(stringValueOf(node["type"]))) {
		case "text":
			if text := strings.TrimSpace(pluginGatewayTextData(node["data"])); text != "" {
				parts = append(parts, text)
			}
		case "image", "emoji":
			parts = append(parts, "[图片]")
		case "at":
			parts = append(parts, "@"+pluginGatewayAtTarget(node["data"]))
		case "face":
			parts = append(parts, "[表情]")
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// pluginGatewayParts 把插件消息字典的 raw_message 还原为消息段与媒体列表
// 对齐 Python PluginMessageUtils._component_from_dict：图片/表情带二进制、@ 与回复保留目标
func pluginGatewayParts(message map[string]any) (
	parts []onebot.MessagePart,
	images []onebot.ImageInfo,
	faces []onebot.FaceInfo,
	voices []onebot.VoiceInfo,
	fileNames []string,
	atList []int64,
	externalAtList []string,
	atNames map[int64]string,
	reply *onebot.ReplyInfo,
) {
	raw, ok := message["raw_message"]
	if !ok || raw == nil {
		raw = message["segments"]
	}
	items, _ := raw.([]any)
	if len(items) == 0 {
		return nil, nil, nil, nil, nil, nil, nil, nil, nil
	}
	atNames = map[int64]string{}
	for index, item := range items {
		node, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(stringValueOf(node["type"])))
		data := node["data"]
		part := onebot.MessagePart{Index: index}
		switch kind {
case "text":
				part.Kind = "text"
				part.Text = pluginGatewayTextData(data)
		case "at":
			part.Kind = "at"
			external := pluginGatewayAtTarget(data)
			part.ExternalAtUserID = external
			if numeric, err := strconv.ParseInt(external, 10, 64); err == nil && numeric > 0 {
				part.AtUserID = numeric
			} else if external != "" {
				part.AtUserID = onebot.SyntheticIdentityID("at", external)
			}
			if part.AtUserID != 0 {
				atList = append(atList, part.AtUserID)
				externalAtList = append(externalAtList, external)
				name := pluginGatewayDataString(data, "target_user_nickname", "nickname", "name")
				if name == "" {
					name = external
				}
				atNames[part.AtUserID] = name
			}
		case "reply":
			part.Kind = "reply"
			externalID := pluginGatewayDataString(data, "target_message_id", "message_id", "id")
			if externalID == "" {
				externalID = stringValueOf(data)
			}
			part.Text = externalID
			if externalID != "" && reply == nil {
				reply = &onebot.ReplyInfo{
					MessageID:         onebot.SyntheticIdentityID("reply", externalID),
					ExternalMessageID: externalID,
					Content:           pluginGatewayDataString(data, "target_message_content", "content"),
					Nickname:          pluginGatewayDataString(data, "target_message_sender_nickname", "nickname"),
					GroupCard:         pluginGatewayDataString(data, "target_message_sender_cardname", "cardname"),
				}
				if sender := pluginGatewayDataString(data, "target_message_sender_id", "sender_id"); sender != "" {
					reply.ExternalSenderID = sender
					if numeric, err := strconv.ParseInt(sender, 10, 64); err == nil && numeric > 0 {
						reply.SenderID = numeric
					} else {
						reply.SenderID = onebot.SyntheticIdentityID("reply-sender", sender)
					}
				}
			}
		case "image", "emoji":
			part.Kind = "image"
			image := onebot.ImageInfo{}
			if kind == "emoji" {
				image.SubType = 1
			}
			image.URL, image.File = pluginGatewayMediaRef(node, data)
			if image.URL == "" && image.File == "" {
				continue
			}
			image.Desc = pluginGatewayDataString(data, "summary", "desc")
			part.Index = len(images)
			images = append(images, image)
		case "face":
			part.Kind = "face"
			face := onebot.FaceInfo{
				ID:   int(pluginGatewayDataNumber(data, "id")),
				Name: pluginGatewayDataString(data, "name"),
			}
			if face.ID == 0 && face.Name == "" {
				face.Name = stringValueOf(data)
			}
			part.Index = len(faces)
			faces = append(faces, face)
		case "record", "voice", "audio":
			part.Kind = "record"
			url, file := pluginGatewayMediaRef(node, data)
			part.Text = url
			if part.Text == "" {
				part.Text = file
			}
			part.Index = len(voices)
			voices = append(voices, onebot.VoiceInfo{URL: url, File: file})
		case "video":
			part.Kind = "video"
			url, file := pluginGatewayMediaRef(node, data)
			part.Text = url
			if part.Text == "" {
				part.Text = file
			}
		case "file":
			part.Kind = "file"
			name := pluginGatewayDataString(data, "name", "file_name", "filename")
			if name == "" {
				name = stringValueOf(data)
			}
			part.Index = len(fileNames)
			fileNames = append(fileNames, name)
		case "forward":
			part.Kind = "forward"
		case "json", "card", "dict":
			part.Kind = "text"
			part.Text = "[卡片消息]"
		default:
			part.Kind = "text"
			part.Text = stringValueOf(data)
		}
		parts = append(parts, part)
	}
	return parts, images, faces, voices, fileNames, atList, externalAtList, atNames, reply
}

// pluginGatewayText 从消息段提取纯文本
func pluginGatewayText(parts []onebot.MessagePart) string {
	var builder strings.Builder
	for _, part := range parts {
		if part.Kind == "text" && part.Text != "" {
			builder.WriteString(part.Text)
		}
	}
	return strings.TrimSpace(builder.String())
}

// pluginGatewayTextData 解析 text 段 data：Host 规范为字符串，OneBot 原始为 {"text": "..."}
func pluginGatewayTextData(data any) string {
	switch typed := data.(type) {
	case string:
		return typed
	case map[string]any:
		if text := strings.TrimSpace(stringValueOf(typed["text"])); text != "" {
			return text
		}
		if text := strings.TrimSpace(stringValueOf(typed["content"])); text != "" {
			return text
		}
		return ""
	default:
		return ""
	}
}

// pluginGatewayAtTarget 解析 @ 段目标，兼容字符串与对象两种 data
func pluginGatewayAtTarget(data any) string {
	if text, ok := data.(string); ok {
		return strings.TrimSpace(text)
	}
	return pluginGatewayDataString(data, "target_user_id", "user_id", "qq", "id")
}

// pluginGatewayMediaRef 返回媒体段的可下载地址与文件标识
// 优先使用 binary_data_base64（插件序列化产物），其次 url/file，最后把 base64 正文转成 data URL
func pluginGatewayMediaRef(node map[string]any, data any) (string, string) {
	encoded := strings.TrimSpace(stringValueOf(node["binary_data_base64"]))
	payload := map[string]any(nil)
	if typed, ok := data.(map[string]any); ok {
		payload = typed
		if encoded == "" {
			encoded = strings.TrimSpace(stringValueOf(typed["binary_data_base64"]))
		}
		if encoded == "" {
			encoded = strings.TrimSpace(stringValueOf(typed["base64"]))
		}
	}
	url := pluginGatewayDataString(data, "url", "src")
	file := pluginGatewayDataString(data, "file", "path")
	if url == "" && payload == nil {
		if text, ok := data.(string); ok {
			text = strings.TrimSpace(text)
			switch {
			case strings.HasPrefix(text, "data:image/"):
				url = text
			case strings.HasPrefix(strings.ToLower(text), "http://"), strings.HasPrefix(strings.ToLower(text), "https://"):
				url = text
			case strings.HasPrefix(text, "base64://"):
				encoded = strings.TrimPrefix(text, "base64://")
			case len(text) > 100:
				encoded = text
			}
		}
	}
	if encoded != "" {
		encoded = strings.TrimPrefix(encoded, "base64://")
		url = onebot.ImageDataURL(encoded)
	}
	return url, file
}

func pluginGatewayDataString(data any, keys ...string) string {
	payload, ok := data.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range keys {
		if value := strings.TrimSpace(stringValueOf(payload[key])); value != "" {
			return value
		}
	}
	return ""
}

func pluginGatewayDataNumber(data any, key string) float64 {
	payload, ok := data.(map[string]any)
	if !ok {
		return 0
	}
	switch value := payload[key].(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
			return parsed
		}
	}
	return 0
}

func pluginGatewayBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		normalized := strings.ToLower(strings.TrimSpace(typed))
		return normalized == "1" || normalized == "true" || normalized == "yes"
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	default:
		return false
	}
}
