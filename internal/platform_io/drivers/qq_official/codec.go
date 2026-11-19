package qq_official

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	groupAtEvent      = "GROUP_AT_MESSAGE_CREATE"
	groupMessageEvent = "GROUP_MESSAGE_CREATE"
	privateEvent      = "C2C_MESSAGE_CREATE"
)

var (
	qqAttachmentTagPattern = regexp.MustCompile(`(?i)<attachmentType=.*?>`)
	qqFaceTagPattern       = regexp.MustCompile(`(?i)<faceType=.*?>`)
	qqMentionTokenPattern  = regexp.MustCompile(`<@!?([0-9A-Za-z_-]+)>`)
)

func isSupportedEvent(event string) bool {
	switch event {
	case groupAtEvent, groupMessageEvent, privateEvent:
		return true
	default:
		return false
	}
}

func isGroupEvent(event string) bool {
	return event == groupAtEvent || event == groupMessageEvent
}

// DecodeMessage 对齐 codec.decode_message（P0：文本；附件以标签降级）
func DecodeMessage(event string, payload map[string]any, accountID, botName, unifiedAccountID, groupName string, attachmentSegments []map[string]any, attachmentLabels []string) (map[string]any, string, string, error) {
	if !isSupportedEvent(event) {
		return nil, "", "", fmt.Errorf("不支持的事件类型: %s", event)
	}
	messageID := strings.TrimSpace(asString(payload["id"]))
	author, ok := asMap(payload["author"])
	if messageID == "" || !ok {
		return nil, "", "", fmt.Errorf("消息缺少 id 或 author")
	}
	isGroup := isGroupEvent(event)
	groupID := ""
	if isGroup {
		groupID = strings.TrimSpace(asString(payload["group_openid"]))
	}
	userID := ""
	if isGroup {
		for _, key := range []string{"member_openid", "openid", "id", "user_openid"} {
			if v := strings.TrimSpace(asString(author[key])); v != "" {
				userID = v
				break
			}
		}
	} else {
		for _, key := range []string{"user_openid", "openid", "id"} {
			if v := strings.TrimSpace(asString(author[key])); v != "" {
				userID = v
				break
			}
		}
	}
	if userID == "" || (isGroup && groupID == "") {
		return nil, "", "", fmt.Errorf("消息缺少用户或群 OpenID")
	}

	additional := map[string]any{}
	if isGroup {
		additional["qq_official_group_openid"] = groupID
		additional["platform_io_target_group_id"] = groupID
	} else {
		additional["platform_io_target_user_id"] = userID
	}
	additional["qq_official_user_openid"] = userID
	msgIdx, refMsgIdx := extractMessageSceneFields(payload)
	if msgIdx != "" {
		additional["qq_official_msg_idx"] = msgIdx
	}
	if refMsgIdx != "" {
		additional["qq_official_ref_msg_idx"] = refMsgIdx
	}

	content := normalizeContent(extractContent(payload))
	segments := make([]map[string]any, 0)
	segments = append(segments, attachmentSegments...)
	labels := make([]string, 0, len(attachmentLabels))
	for _, label := range attachmentLabels {
		if strings.TrimSpace(label) != "" {
			labels = append(labels, label)
		}
	}
	isAt := isBotMentioned(event, payload, content, accountID)
	content, bodySegments, bodyPlain := rewriteMentionTokens(content, accountID, botName, payload, unifiedAccountID)
	if content == "" && len(segments) == 0 && len(bodySegments) == 0 {
		return nil, "", "", fmt.Errorf("消息不包含文本或可处理的附件")
	}
	receivedAt, err := parseTimestamp(payload["timestamp"])
	if err != nil {
		return nil, "", "", err
	}
	nickname := strings.TrimSpace(asString(author["nickname"]))
	if nickname == "" {
		nickname = strings.TrimSpace(asString(author["nick"]))
	}
	if nickname == "" {
		nickname = strings.TrimSpace(asString(author["username"]))
	}
	if nickname == "" {
		nickname = userID
	}
	messageInfo := map[string]any{
		"user_info": map[string]any{
			"user_id":       userID,
			"user_nickname": nickname,
		},
		"additional_config": additional,
	}
	targetID := userID
	scene := "private"
	if isGroup {
		scene = "group"
		targetID = groupID
		messageInfo["group_info"] = map[string]any{
			"group_id":   groupID,
			"group_name": strings.TrimSpace(groupName),
		}
	}
	rawMessage := append([]map[string]any{}, bodySegments...)
	if content != "" {
		rawMessage = append(rawMessage, map[string]any{"type": "text", "data": content})
	}
	rawMessage = append(rawMessage, segments...)
	plainParts := make([]string, 0, 1+len(labels))
	if bodyPlain != "" {
		plainParts = append(plainParts, bodyPlain)
	}
	if content != "" {
		plainParts = append(plainParts, content)
	}
	plainParts = append(plainParts, labels...)
	routeAccount := unifiedAccountID
	if routeAccount == "" {
		routeAccount = accountID
	}
	message := map[string]any{
		"message_id":           messageID,
		"timestamp":            fmt.Sprintf("%v", receivedAt),
		"platform":             PlatformQQ,
		"account_id":           routeAccount,
		"scope":                nil,
		"message_info":         messageInfo,
		"raw_message":          rawMessage,
		"processed_plain_text": strings.Join(plainParts, " "),
		"is_at":                isAt,
		"is_mentioned":         isAt,
		"is_emoji":             false,
		"is_picture":           false,
		"is_command":           strings.HasPrefix(content, "/"),
		"session_id":           "",
	}
	return message, scene, targetID, nil
}

func IsBotAuthor(payload map[string]any) bool {
	author, ok := asMap(payload["author"])
	if !ok {
		return false
	}
	switch typed := author["bot"].(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func extractMessageSceneFields(payload map[string]any) (string, string) {
	scene, ok := asMap(payload["message_scene"])
	if !ok {
		return "", ""
	}
	ext, _ := scene["ext"].([]any)
	msgIdx, refMsgIdx := "", ""
	for _, item := range ext {
		s, ok := item.(string)
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(s, "msg_idx="):
			msgIdx = strings.TrimSpace(strings.TrimPrefix(s, "msg_idx="))
		case strings.HasPrefix(s, "ref_msg_idx="):
			refMsgIdx = strings.TrimSpace(strings.TrimPrefix(s, "ref_msg_idx="))
		}
	}
	return msgIdx, refMsgIdx
}

func normalizeContent(content string) string {
	content = qqAttachmentTagPattern.ReplaceAllString(content, "")
	return strings.TrimSpace(qqFaceTagPattern.ReplaceAllString(content, "[表情]"))
}

func extractContent(payload map[string]any) string {
	if content := strings.TrimSpace(asString(payload["content"])); content != "" {
		return content
	}
	parts := make([]string, 0)
	elements, _ := payload["msg_elements"].([]any)
	for _, raw := range elements {
		element, ok := asMap(raw)
		if !ok {
			continue
		}
		if asString(element["message_type"]) == "103" || asString(element["type"]) == "103" {
			continue
		}
		if n, ok := asInt(element["message_type"]); ok && n == 103 {
			continue
		}
		if n, ok := asInt(element["type"]); ok && n == 103 {
			continue
		}
		for _, key := range []string{"content", "text"} {
			switch typed := element[key].(type) {
			case string:
				if strings.TrimSpace(typed) != "" {
					parts = append(parts, strings.TrimSpace(typed))
				}
			case map[string]any:
				if nested := strings.TrimSpace(asString(typed["content"])); nested != "" {
					parts = append(parts, nested)
				}
			}
		}
		if textElement, ok := asMap(element["text_element"]); ok {
			if nested := strings.TrimSpace(asString(textElement["content"])); nested != "" {
				parts = append(parts, nested)
			}
		}
	}
	return strings.Join(parts, "")
}

func isBotMentioned(event string, payload map[string]any, content, accountID string) bool {
	if event == groupAtEvent {
		return true
	}
	if strings.Contains(content, "<@"+accountID+">") || strings.Contains(content, "<@!"+accountID+">") {
		return true
	}
	return treeMentionsBot(payload, accountID)
}

func treeMentionsBot(value any, accountID string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for k, v := range typed {
			if (k == "id" || k == "openid" || strings.HasSuffix(k, "_openid")) && asString(v) == accountID {
				return true
			}
			if treeMentionsBot(v, accountID) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if treeMentionsBot(item, accountID) {
				return true
			}
		}
	}
	return false
}

func rewriteMentionTokens(content, accountID, botName string, payload map[string]any, botUserID string) (string, []map[string]any, string) {
	if botUserID == "" {
		botUserID = accountID
	}
	displayNames := map[string]string{}
	mentions, _ := payload["mentions"].([]any)
	for _, raw := range mentions {
		mention, ok := asMap(raw)
		if !ok {
			continue
		}
		mentionID := ""
		for _, key := range []string{"id", "openid", "user_openid", "member_openid"} {
			if v := strings.TrimSpace(asString(mention[key])); v != "" {
				mentionID = v
				break
			}
		}
		name := strings.TrimSpace(asString(mention["username"]))
		if name == "" {
			name = strings.TrimSpace(asString(mention["nickname"]))
		}
		if name == "" {
			name = strings.TrimSpace(asString(mention["nick"]))
		}
		if mentionID != "" && name != "" {
			displayNames[mentionID] = name
		}
	}
	type piece struct {
		text string
		seg  map[string]any
		label string
	}
	pieces := make([]piece, 0)
	cursor := 0
	for _, match := range qqMentionTokenPattern.FindAllStringSubmatchIndex(content, -1) {
		if match[0] > cursor {
			pieces = append(pieces, piece{text: content[cursor:match[0]]})
		}
		mentionID := content[match[2]:match[3]]
		if mentionID == accountID {
			label := ""
			if botName != "" {
				label = "@" + botName
			}
			pieces = append(pieces, piece{
				seg: map[string]any{
					"type": "at",
					"data": map[string]any{
						"target_user_id":       botUserID,
						"target_user_nickname": nilIfEmpty(botName),
						"target_user_cardname": nil,
					},
				},
				label: label,
			})
		} else {
			name := displayNames[mentionID]
			label := "@" + mentionID
			if name != "" {
				label = "@" + name
			}
			pieces = append(pieces, piece{
				seg: map[string]any{
					"type": "at",
					"data": map[string]any{
						"target_user_id":       mentionID,
						"target_user_nickname": nilIfEmpty(name),
						"target_user_cardname": nil,
					},
				},
				label: label,
			})
		}
		cursor = match[1]
	}
	if cursor < len(content) {
		pieces = append(pieces, piece{text: content[cursor:]})
	}
	segments := make([]map[string]any, 0)
	plainParts := make([]string, 0)
	remaining := strings.Builder{}
	for i, p := range pieces {
		if p.seg != nil {
			segments = append(segments, p.seg)
			if p.label != "" {
				plainParts = append(plainParts, p.label)
			}
			continue
		}
		text := p.text
		if i == 0 {
			text = strings.TrimLeft(text, " \t\r\n")
		}
		if i == len(pieces)-1 {
			text = strings.TrimRight(text, " \t\r\n")
		}
		remaining.WriteString(text)
	}
	return remaining.String(), segments, strings.Join(plainParts, " ")
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func parseTimestamp(v any) (float64, error) {
	raw := strings.TrimSpace(asString(v))
	if raw == "" {
		return 0, fmt.Errorf("消息缺少有效的 timestamp")
	}
	raw = strings.ReplaceAll(raw, "Z", "+00:00")
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return float64(t.UnixNano()) / 1e9, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return float64(t.UnixNano()) / 1e9, nil
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		return f, nil
	}
	return 0, fmt.Errorf("消息缺少有效的 timestamp")
}

// EncodeOutboundText P0：仅文本/@ 合并为 body；媒体降级占位
func EncodeOutboundText(outbound map[string]any) (scene, target, text string, err error) {
	messageInfo, ok := asMap(outbound["message_info"])
	if !ok {
		return "", "", "", fmt.Errorf("出站消息缺少 message_info")
	}
	config, _ := asMap(messageInfo["additional_config"])
	groupInfo, _ := asMap(messageInfo["group_info"])
	groupID := strings.TrimSpace(asString(groupInfo["group_id"]))
	if groupID == "" {
		groupID = strings.TrimSpace(asString(config["platform_io_target_group_id"]))
	}
	userID := strings.TrimSpace(asString(config["platform_io_target_user_id"]))
	if groupID != "" {
		scene = "group"
		target = groupID
	} else {
		scene = "private"
		target = userID
	}
	if target == "" {
		return "", "", "", fmt.Errorf("出站消息缺少目标 OpenID")
	}
	segments, _ := outbound["raw_message"].([]any)
	if len(segments) == 0 {
		return "", "", "", fmt.Errorf("出站消息缺少消息段")
	}
	parts := make([]string, 0)
	for _, raw := range segments {
		seg, ok := asMap(raw)
		if !ok {
			continue
		}
		switch asString(seg["type"]) {
		case "text":
			parts = append(parts, asString(seg["data"]))
		case "at":
			data, _ := asMap(seg["data"])
			nick := asString(data["target_user_cardname"])
			if nick == "" {
				nick = asString(data["target_user_nickname"])
			}
			if nick != "" {
				parts = append(parts, "@"+nick+" ")
			}
		case "image", "emoji":
			parts = append(parts, "[image]")
		case "voice":
			parts = append(parts, "[voice]")
		case "video", "file":
			parts = append(parts, "[file]")
		case "reply":
			continue
		}
	}
	text = strings.TrimSpace(strings.Join(parts, ""))
	if text == "" {
		return "", "", "", fmt.Errorf("出站消息为空")
	}
	return scene, target, text, nil
}
