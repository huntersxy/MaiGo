package telegram

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

var messageIDPattern = regexp.MustCompile(`^(-?\d+):(\d+)$`)

func ExternalID(chatID, messageID int64) string {
	return fmt.Sprintf("%d:%d", chatID, messageID)
}

func ParseMessageID(value string) (chatID, messageID int64, err error) {
	match := messageIDPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return 0, 0, fmt.Errorf("Telegram 消息 ID 必须为 chat_id:message_id")
	}
	chatID, err = strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	messageID, err = strconv.ParseInt(match[2], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	return chatID, messageID, nil
}

func ParseGroupID(value string) (chatID int64, threadID *int64, err error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, ":") {
		n, err := strconv.ParseInt(value, 10, 64)
		return n, nil, err
	}
	chatID, thread, err := ParseMessageID(value)
	if err != nil {
		return 0, nil, err
	}
	if chatID >= 0 || thread <= 0 {
		return 0, nil, fmt.Errorf("Telegram 话题目标必须为负群 ID 和正话题 ID")
	}
	return chatID, &thread, nil
}

func displayName(user map[string]any) string {
	if user == nil {
		return ""
	}
	if title := asString(user["title"]); title != "" {
		return title
	}
	parts := make([]string, 0, 2)
	if v := asString(user["first_name"]); v != "" {
		parts = append(parts, v)
	}
	if v := asString(user["last_name"]); v != "" {
		parts = append(parts, v)
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	return asString(user["id"])
}

func utf16Length(text string) int {
	return len(utf16.Encode([]rune(text)))
}

func utf16Index(text string, offset int) (int, error) {
	current := 0
	runes := []rune(text)
	for index, r := range runes {
		if current == offset {
			return index, nil
		}
		size := 1
		if r >= 0x10000 {
			size = 2
		}
		current += size
		if current > offset {
			return 0, fmt.Errorf("Telegram entity 位于代理对内部")
		}
	}
	if current == offset {
		return len(runes), nil
	}
	return 0, fmt.Errorf("Telegram entity 超出文本范围")
}

func decodeText(text string, entities []map[string]any, botID, botUsername string) ([]map[string]any, bool, error) {
	segments := make([]map[string]any, 0)
	cursor := 0
	mentioned := false
	runes := []rune(text)
	for _, entity := range entities {
		kind := asString(entity["type"])
		if kind != "mention" && kind != "text_mention" {
			continue
		}
		offset, _ := asInt(entity["offset"])
		length, _ := asInt(entity["length"])
		start, err := utf16Index(text, offset)
		if err != nil {
			return nil, false, err
		}
		end, err := utf16Index(text, offset+length)
		if err != nil {
			return nil, false, err
		}
		if start < cursor {
			return nil, false, fmt.Errorf("Telegram @ entities 相互重叠")
		}
		if start > cursor {
			segments = append(segments, map[string]any{"type": "text", "data": string(runes[cursor:start])})
		}
		label := string(runes[start:end])
		var target, name string
		if kind == "text_mention" {
			user, _ := asMap(entity["user"])
			target = asString(user["id"])
			name = displayName(user)
		} else {
			target = label
			if strings.EqualFold(label, "@"+botUsername) {
				target = botID
			}
			name = label
		}
		if target == botID {
			mentioned = true
		}
		segments = append(segments, map[string]any{
			"type": "at",
			"data": map[string]any{
				"target_user_id":       target,
				"target_user_nickname": name,
			},
		})
		cursor = end
	}
	if cursor < len(runes) {
		segments = append(segments, map[string]any{"type": "text", "data": string(runes[cursor:])})
	}
	return segments, mentioned, nil
}

func BinarySegment(kind string, binary []byte, label string) (map[string]any, error) {
	if len(binary) == 0 {
		return nil, fmt.Errorf("媒体内容为空")
	}
	sum := sha256Hex(binary)
	return map[string]any{
		"type":               kind,
		"data":               label,
		"hash":               sum,
		"binary_data_base64": base64.StdEncoding.EncodeToString(binary),
	}, nil
}

func topicGroup(payload map[string]any, topicName string) map[string]string {
	chat, _ := asMap(payload["chat"])
	groupID := asString(chat["id"])
	name := asString(chat["title"])
	if asBool(payload["is_topic_message"]) {
		threadID, _ := asInt64(payload["message_thread_id"])
		groupID = fmt.Sprintf("%s:%d", groupID, threadID)
		if topicName == "" {
			topicName = fmt.Sprintf("话题 %d", threadID)
		}
		name = name + " / " + topicName
	} else if asBool(chat["is_forum"]) {
		name = name + " / General"
	}
	return map[string]string{"group_id": groupID, "group_name": name}
}

// DecodeMessage 对齐 codec.decode_message；media 已准备好的段
func DecodeMessage(payload map[string]any, botID, botUsername string, media []map[string]any, topicName string) (map[string]any, error) {
	chat, ok := asMap(payload["chat"])
	if !ok {
		return nil, fmt.Errorf("缺少 chat")
	}
	chatType := asString(chat["type"])
	if chatType != "private" && chatType != "group" && chatType != "supergroup" {
		return nil, nil
	}
	var sender map[string]any
	if sc, ok := asMap(payload["sender_chat"]); ok {
		sender = sc
	} else if from, ok := asMap(payload["from"]); ok {
		sender = from
	} else {
		return nil, fmt.Errorf("Telegram 消息缺少发送者身份")
	}
	if asString(sender["id"]) == botID {
		if _, hasSenderChat := asMap(payload["sender_chat"]); !hasSenderChat {
			return nil, nil
		}
	}
	text := asString(payload["text"])
	if text == "" {
		text = asString(payload["caption"])
	}
	entities := entityList(payload["entities"])
	if len(entities) == 0 {
		entities = entityList(payload["caption_entities"])
	}
	body, mentioned, err := decodeText(text, entities, botID, botUsername)
	if err != nil {
		return nil, err
	}
	body = append(body, media...)
	if len(body) == 0 {
		return nil, nil
	}
	chatID := asString(chat["id"])
	msgIDNum, _ := asInt64(payload["message_id"])
	messageID := ExternalID(mustParseInt64(chatID), msgIDNum)
	additional := map[string]any{
		"telegram_chat_id":    chatID,
		"telegram_message_id": msgIDNum,
	}
	if asBool(payload["is_topic_message"]) {
		additional["telegram_message_thread_id"] = payload["message_thread_id"]
	}
	if origin, ok := asMap(payload["forward_origin"]); ok {
		additional["telegram_forward_origin"] = origin
		var originUser map[string]any
		if u, ok := asMap(origin["sender_user"]); ok {
			originUser = u
		} else if u, ok := asMap(origin["sender_chat"]); ok {
			originUser = u
		} else if u, ok := asMap(origin["chat"]); ok {
			originUser = u
		}
		nickname := asString(origin["sender_user_name"])
		var sourceID any
		if originUser != nil {
			nickname = displayName(originUser)
			sourceID = asString(originUser["id"])
		}
		body = []map[string]any{{
			"type": "forward",
			"data": []map[string]any{{
				"user_id":       sourceID,
				"user_nickname": nickname,
				"message_id":    messageID,
				"content":       body,
			}},
		}}
	}
	var replyTo any
	if reply, ok := asMap(payload["reply_to_message"]); ok {
		replyMsgID, _ := asInt64(reply["message_id"])
		replyTo = ExternalID(mustParseInt64(chatID), replyMsgID)
		var author map[string]any
		if a, ok := asMap(reply["sender_chat"]); ok {
			author = a
		} else if a, ok := asMap(reply["from"]); ok {
			author = a
		}
		replyData := map[string]any{
			"target_message_id":              replyTo,
			"target_message_content":         firstNonEmpty(asString(reply["text"]), asString(reply["caption"])),
			"target_message_sender_id":       nil,
			"target_message_sender_nickname": nil,
		}
		if author != nil {
			replyData["target_message_sender_id"] = asString(author["id"])
			replyData["target_message_sender_nickname"] = displayName(author)
			if asString(author["id"]) == botID {
				mentioned = true
			}
		}
		body = append([]map[string]any{{"type": "reply", "data": replyData}}, body...)
	}
	info := map[string]any{
		"user_info": map[string]any{
			"user_id":       asString(sender["id"]),
			"user_nickname": displayName(sender),
		},
		"additional_config": additional,
	}
	if chatType != "private" {
		info["group_info"] = topicGroup(payload, topicName)
	}
	isPicture := false
	isEmoji := false
	for _, item := range media {
		switch asString(item["type"]) {
		case "image":
			isPicture = true
		case "emoji":
			isEmoji = true
		}
	}
	isCommand := false
	for _, entity := range entities {
		if asString(entity["type"]) == "bot_command" {
			offset, _ := asInt(entity["offset"])
			if offset == 0 {
				isCommand = true
				break
			}
		}
	}
	result := map[string]any{
		"platform":     PlatformTelegram,
		"account_id":   botID,
		"scope":        nil,
		"message_id":   messageID,
		"timestamp":    fmt.Sprint(payload["date"]),
		"message_info": info,
		"raw_message":  body,
		"is_at":        mentioned,
		"is_mentioned": mentioned,
		"is_picture":   isPicture,
		"is_emoji":     isEmoji,
		"is_command":   isCommand,
	}
	if replyTo != nil {
		result["reply_to"] = replyTo
	}
	return result, nil
}

func entityList(v any) []map[string]any {
	switch typed := v.(type) {
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if m, ok := asMap(item); ok {
				out = append(out, m)
			}
		}
		return out
	case []map[string]any:
		return typed
	default:
		return nil
	}
}

func mustParseInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func sha256Hex(binary []byte) string {
	sum := sha256.Sum256(binary)
	return hex.EncodeToString(sum[:])
}
