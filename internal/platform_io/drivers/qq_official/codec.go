package qq_official

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	memutils "MaiGo/internal/A_memorix/core/utils"
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

// DecodeMessage 将 QQ 官方事件转换为网关消息。
func DecodeMessage(event string, payload map[string]any, accountID, botName, unifiedAccountID, groupName string, attachmentSegments []map[string]any, attachmentLabels []string) (map[string]any, string, string, error) {
	return decodeMessage(event, payload, accountID, botName, unifiedAccountID, groupName, attachmentSegments, attachmentLabels, idMapping{}, false, false)
}

func decodeMessage(event string, payload map[string]any, accountID, botName, unifiedAccountID, groupName string, attachmentSegments []map[string]any, attachmentLabels []string, mapping idMapping, hasPicture, hasEmoji bool) (map[string]any, string, string, error) {
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
		additional["platform_io_target_group_id"] = firstNonEmpty(mapping.Group[groupID], groupID)
	} else {
		additional["platform_io_target_user_id"] = firstNonEmpty(mapping.User[userID], userID)
	}
	additional["qq_official_user_openid"] = userID
	for _, field := range []string{"id", "username", "avatar", "bot", "union_openid", "member_role", "member_nickname"} {
		if value, exists := author[field]; exists && (isGroup || (field != "member_role" && field != "member_nickname")) {
			additional["qq_official_author_"+field] = value
		}
	}
	if isGroup {
		for _, field := range []string{"union_group_openid", "mentions"} {
			if value, exists := payload[field]; exists {
				additional["qq_official_"+field] = value
			}
		}
	}
	userID = firstNonEmpty(mapping.User[userID], userID)
	if isGroup {
		groupID = firstNonEmpty(mapping.Group[groupID], groupID)
	}
	msgIdx, refMsgIdx := extractMessageSceneFields(payload)
	if msgIdx != "" {
		additional["qq_official_msg_idx"] = msgIdx
	}
	if refMsgIdx != "" {
		additional["qq_official_ref_msg_idx"] = refMsgIdx
	}

	content := normalizeContent(extractContent(payload))
	segments := append([]map[string]any(nil), attachmentSegments...)
	for _, segment := range segments {
		switch segment["type"] {
		case "emoji":
			hasEmoji = true
			hasPicture = true
		case "image":
			hasPicture = true
		}
	}
	if hasEmoji && (content == "[表情]" || content == "[动画表情]") {
		content = ""
	}
	labels := make([]string, 0, len(attachmentLabels))
	for _, label := range attachmentLabels {
		if strings.TrimSpace(label) != "" {
			labels = append(labels, label)
		}
	}
	isAt := isBotMentioned(event, payload, content, accountID)
	content, bodySegments, bodyPlain := rewriteMentionTokens(content, accountID, botName, payload, unifiedAccountID, mapping.User)
	if content == "" && len(segments) == 0 && len(bodySegments) == 0 {
		return nil, "", "", fmt.Errorf("消息不包含文本或可处理的附件")
	}
	receivedAt, err := parseTimestamp(payload["timestamp"])
	if err != nil {
		return nil, "", "", err
	}
	nickname := firstNonEmpty(asString(author["username"]), asString(author["nickname"]), asString(author["nick"]), userID)
	var card any
	if isGroup {
		card = nilIfEmpty(strings.TrimSpace(asString(author["member_nickname"])))
	}
	messageInfo := map[string]any{
		"user_info": map[string]any{
			"user_id":       userID,
			"user_nickname": nickname,
			"user_cardname": card,
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
			"group_name": firstNonEmpty(groupName, asString(payload["group_name"])),
		}
	}
	rawMessage := append([]map[string]any{}, bodySegments...)
	rawMessage = append(rawMessage, segments...)
	plainParts := make([]string, 0, 1+len(labels))
	if bodyPlain != "" {
		plainParts = append(plainParts, bodyPlain)
	}
	plainParts = append(plainParts, labels...)
	routeAccount := unifiedAccountID
	if routeAccount == "" {
		routeAccount = accountID
	}
	stamp, _ := memutils.PyJSONMarshal(receivedAt)
	message := map[string]any{
		"message_id":           messageID,
		"timestamp":            stamp,
		"platform":             PlatformQQ,
		"account_id":           routeAccount,
		"scope":                nil,
		"message_info":         messageInfo,
		"raw_message":          rawMessage,
		"processed_plain_text": strings.Join(plainParts, " "),
		"is_at":                isAt,
		"is_mentioned":         isAt,
		"is_emoji":             hasEmoji,
		"is_picture":           hasPicture,
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
		if isQuoteElement(element) {
			continue
		}
		for _, key := range []string{"content", "text"} {
			switch typed := element[key].(type) {
			case string:
				if strings.TrimSpace(typed) != "" {
					parts = append(parts, strings.TrimSpace(typed))
				}
			case map[string]any:
				if text, _ := typed["content"].(string); strings.TrimSpace(text) != "" {
					nested := strings.TrimSpace(text)
					parts = append(parts, nested)
				}
			}
		}
		if textElement, ok := asMap(element["text_element"]); ok {
			if text, _ := textElement["content"].(string); strings.TrimSpace(text) != "" {
				nested := strings.TrimSpace(text)
				parts = append(parts, nested)
			}
		}
	}
	return strings.Join(parts, "")
}

func isQuoteElement(element map[string]any) bool {
	for _, field := range []string{"type", "message_type"} {
		switch element[field] {
		case int(103), int64(103), float64(103), "103":
			return true
		}
	}
	return false
}

func isBotMentioned(event string, payload map[string]any, content, accountID string) bool {
	if event == groupAtEvent {
		return true
	}
	if event != groupMessageEvent || accountID == "" {
		return false
	}
	mentions, _ := payload["mentions"].([]any)
	for _, item := range mentions {
		mention, _ := asMap(item)
		for _, key := range []string{"id", "openid", "user_openid", "member_openid"} {
			if strings.TrimSpace(asString(mention[key])) == accountID {
				return true
			}
		}
	}
	if treeMentionsBot(payload["msg_elements"], accountID) ||
		strings.Contains(content, "<@"+accountID+">") || strings.Contains(content, "<@!"+accountID+">") {
		return true
	}
	return regexp.MustCompile(`@(?:机器人)?` + regexp.QuoteMeta(accountID) + `(?:[\s\p{Z}]|$)`).MatchString(content)
}

func treeMentionsBot(value any, accountID string) bool {
	matchKey := func(key string) bool {
		switch key {
		case "id", "openid", "user_id", "target_id", "member_openid":
			return true
		}
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		if isQuoteElement(typed) {
			return false
		}
		kind := strings.ToLower(firstNonEmpty(asString(typed["type"]), asString(typed["element_type"])))
		mentionType := strings.Contains(kind, "at") || strings.Contains(kind, "mention")
		for key, item := range typed {
			key = strings.ToLower(key)
			if nested, ok := asMap(item); ok && (strings.Contains(key, "at") || strings.Contains(key, "mention")) {
				for name, id := range nested {
					if matchKey(name) && strings.TrimSpace(asString(id)) == accountID {
						return true
					}
				}
			}
			if mentionType && matchKey(key) && strings.TrimSpace(asString(item)) == accountID {
				return true
			}
			if treeMentionsBot(item, accountID) {
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

func rewriteMentionTokens(content, accountID, botName string, payload map[string]any, botUserID string, userMap map[string]string) (string, []map[string]any, string) {
	if botUserID == "" {
		botUserID = accountID
	}
	displayNames := map[string]string{}
	cardNames := map[string]string{}
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
		if mentionID != "" {
			cardNames[mentionID] = strings.TrimSpace(asString(mention["member_nickname"]))
		}
	}
	type piece struct {
		text  string
		seg   map[string]any
		label string
	}
	pieces := make([]piece, 0)
	cursor := 0
	for _, match := range qqMentionTokenPattern.FindAllStringSubmatchIndex(content, -1) {
		pieces = append(pieces, piece{text: content[cursor:match[0]]})
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
			card := cardNames[mentionID]
			label := "@" + firstNonEmpty(card, name, mentionID)
			pieces = append(pieces, piece{
				seg: map[string]any{
					"type": "at",
					"data": map[string]any{
						"target_user_id":       firstNonEmpty(userMap[mentionID], mentionID),
						"target_user_nickname": nilIfEmpty(name),
						"target_user_cardname": nilIfEmpty(card),
					},
				},
				label: label,
			})
		}
		cursor = match[1]
	}
	pieces = append(pieces, piece{text: content[cursor:]})
	pieces[0].text = strings.TrimLeftFunc(pieces[0].text, unicode.IsSpace)
	last := &pieces[len(pieces)-1]
	last.text = strings.TrimRightFunc(last.text, unicode.IsSpace)

	segments := make([]map[string]any, 0, len(pieces))
	var plain, text strings.Builder
	for i, part := range pieces {
		if part.seg != nil {
			segments = append(segments, part.seg)
			if part.label != "" {
				plain.WriteString(part.label)
				if i+1 < len(pieces) {
					next := pieces[i+1].text
					if next != "" && strings.TrimLeftFunc(next, unicode.IsSpace) == next {
						plain.WriteByte(' ')
					}
				}
			}
		} else if part.text != "" {
			segments = append(segments, map[string]any{"type": "text", "data": part.text})
			text.WriteString(part.text)
			plain.WriteString(part.text)
		}
	}
	return strings.TrimSpace(text.String()), segments, strings.TrimSpace(plain.String())
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func parseTimestamp(v any) (float64, error) {
	raw, _ := v.(string)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return float64(t.UnixMicro()) / 1e6, nil
		}
	}
	return 0, fmt.Errorf("消息缺少有效的 timestamp")
}
