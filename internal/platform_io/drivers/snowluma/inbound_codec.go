package snowluma

// inbound_codec.go：对齐 Python codecs/inbound/message_codec.py 的 Host MessageDict 骨架。
// OneBot text 段 data 是 {"text": "..."}；Host 要求 data 为字符串。

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// NormalizeInboundMessage 将 OneBot 事件转为 Host 可接受的消息字典
func NormalizeInboundMessage(payload map[string]any, accountID, scope string) map[string]any {
	return normalizeInboundMessage(payload, accountID, scope, true)
}

func normalizeInboundMessage(payload map[string]any, accountID, scope string, decodeCQ bool) map[string]any {
	if payload == nil {
		return nil
	}
	messageType := asString(payload["message_type"])
	if messageType == "" {
		messageType = asString(payload["detail_type"])
	}
	if messageType == "" {
		messageType = "private"
	}
	userID := asString(payload["user_id"])
	groupID := asString(payload["group_id"])
	messageID := asString(payload["message_id"])
	if messageID == "" {
		return nil
	}
	sender := asMap(payload["sender"])
	nickname := ""
	card := ""
	if sender != nil {
		nickname = asString(sender["nickname"])
		card = asString(sender["card"])
		if userID == "" {
			userID = asString(sender["user_id"])
		}
	}
	if nickname == "" {
		nickname = firstNonEmpty(card, userID)
	}
	message := payload["message"]
	if text, ok := message.(string); ok && decodeCQ {
		segments, err := parseCQMessage(text)
		if err != nil {
			return nil
		}
		message = segments
	}
	rawMessage, isAt, hasPicture, hasEmoji := convertInboundSegments(message, payload["raw_message"], accountID)
	if len(rawMessage) == 0 {
		rawMessage = []any{buildTextSegment("[unsupported]")}
	}
	plainText := buildPlainText(rawMessage)
	timestamp := asFloat(payload["time"], float64(time.Now().Unix()))
	isGroup := messageType == "group"

	additional := map[string]any{
		"napcat_message_type": messageType,
	}
	if isGroup && groupID != "" {
		additional["platform_io_target_group_id"] = groupID
	} else {
		additional["platform_io_target_user_id"] = userID
		if groupID != "" {
			additional["platform_io_temp_group_id"] = groupID
		}
	}

	info := map[string]any{
		"user_info": map[string]any{
			"user_id":       userID,
			"user_nickname": nickname,
			"user_cardname": nilIfEmpty(card),
		},
		"additional_config": additional,
	}
	if isGroup && groupID != "" {
		info["group_info"] = map[string]any{
			"group_id":   groupID,
			"group_name": asString(payload["group_name"]),
		}
	}

	out := map[string]any{
		"platform":             PlatformQQ,
		"account_id":           accountID,
		"scope":                nilIfEmpty(scope),
		"message_id":           messageID,
		"message_type":         messageType,
		"chat_type":            messageType,
		"user_id":              userID,
		"group_id":             groupID,
		"timestamp":            strconv.FormatFloat(timestamp, 'f', -1, 64),
		"raw_message":          rawMessage,
		"message_info":         info,
		"content":              plainText,
		"processed_plain_text": plainText,
		"display_message":      plainText,
		"is_mentioned":         isAt,
		"is_at":                isAt,
		"is_emoji":             hasEmoji,
		"is_picture":           hasPicture,
		"is_command":           strings.HasPrefix(plainText, "/"),
		"is_notify":            false,
		"session_id":           "",
	}
	if isGroup {
		out["target_id"] = groupID
	} else {
		out["target_id"] = userID
	}
	return out
}

func convertInboundSegments(message, fallback any, selfID string) (segments []any, isAt, hasPicture, hasEmoji bool) {
	items := segmentItems(message)
	if len(items) == 0 {
		items = segmentItems(fallback)
	}
	if text, ok := message.(string); ok {
		text = strings.TrimSpace(text)
		if text != "" {
			return []any{buildTextSegment(text)}, false, false, false
		}
	}
	out := make([]any, 0, len(items))
	for index, item := range items {
		seg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		segType := strings.TrimSpace(asString(seg["type"]))
		if segType == "" {
			continue
		}
		data := asMap(seg["data"])
		if data == nil {
			data = map[string]any{}
		}
		switch segType {
		case "text":
			textValue := rawString(data["text"])
			if textValue == "" {
				textValue = rawString(seg["data"])
			}
			if textValue == "" {
				continue
			}
			// SnowLuma 会在 markdown 后附相同正文的 text 兼容段，只消费一次
			if index > 0 {
				if prev, ok := items[index-1].(map[string]any); ok && asString(prev["type"]) == "markdown" {
					prevData := asMap(prev["data"])
					if prevData != nil && rawString(prevData["content"]) == textValue {
						continue
					}
				}
			}
			out = append(out, buildTextSegment(textValue))
		case "markdown":
			content := rawString(data["content"])
			if content == "" {
				continue
			}
			out = append(out, buildTextSegment(content))
		case "at":
			target := firstNonEmpty(asString(data["qq"]), asString(data["user_id"]), asString(data["target_user_id"]))
			if target == "" {
				continue
			}
			if selfID != "" && target == selfID {
				isAt = true
			}
			out = append(out, map[string]any{
				"type": "at",
				"data": map[string]any{
					"target_user_id":       target,
					"target_user_nickname": firstNonEmpty(asString(data["name"]), asString(data["nickname"]), target),
					"target_user_cardname": asString(data["card"]),
				},
			})
		case "reply":
			id := firstNonEmpty(asString(data["id"]), asString(data["message_id"]), asString(seg["data"]))
			if id == "" || id == "0" {
				continue
			}
			out = append(out, map[string]any{
				"type": "reply",
				"data": id,
			})
		case "image", "picture", "emoji":
			kind := "image"
			if segType == "emoji" || asInt(data["sub_type"], asInt(data["subType"], 0)) == 1 {
				kind = "emoji"
				hasEmoji = true
			} else {
				hasPicture = true
			}
			segment := map[string]any{"type": kind, "data": cloneSegmentData(data)}
			if ref, ok := seg["data"].(string); ok {
				segment["data"] = ref
			}
			if binary := asString(seg["binary_data_base64"]); binary != "" {
				segment["binary_data_base64"] = binary
			}
			out = append(out, segment)
		case "face":
			out = append(out, map[string]any{
				"type": "face",
				"data": cloneSegmentData(data),
			})
		case "record", "voice", "audio":
			out = append(out, map[string]any{
				"type": "voice",
				"data": cloneSegmentData(data),
			})
		case "file", "video", "forward", "json":
			out = append(out, map[string]any{
				"type": segType,
				"data": cloneSegmentData(data),
			})
		default:
			out = append(out, map[string]any{
				"type": segType,
				"data": cloneSegmentData(data),
			})
		}
	}
	return out, isAt, hasPicture, hasEmoji
}

func buildPlainText(rawMessage []any) string {
	var parts []string
	for _, item := range rawMessage {
		seg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch asString(seg["type"]) {
		case "text":
			parts = append(parts, segmentTextData(seg["data"]))
		case "at":
			data := asMap(seg["data"])
			if data == nil {
				continue
			}
			name := firstNonEmpty(
				asString(data["target_user_cardname"]),
				asString(data["target_user_nickname"]),
				asString(data["target_user_id"]),
			)
			if name != "" {
				parts = append(parts, "@"+name)
			}
		case "reply":
			parts = append(parts, "[reply]")
		case "forward":
			parts = append(parts, "[forward]")
		case "image", "emoji", "voice":
			parts = append(parts, "["+asString(seg["type"])+"]")
		}
	}
	plain := strings.TrimSpace(strings.Join(parts, ""))
	if plain == "" {
		return "[unsupported]"
	}
	return plain
}

func buildTextSegment(text string) map[string]any {
	return map[string]any{
		"type": "text",
		"data": text,
	}
}

func segmentTextData(data any) string {
	switch typed := data.(type) {
	case string:
		return typed
	case map[string]any:
		return rawString(typed["text"])
	default:
		return ""
	}
}

// rawString 保留正文原样，不做 Trim（命令正则自行吃前导空白）
func rawString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func segmentItems(raw any) []any {
	switch typed := raw.(type) {
	case []any:
		return typed
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func cloneSegmentData(data map[string]any) map[string]any {
	if data == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(data))
	for key, value := range data {
		out[key] = value
	}
	return out
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func int64String(value int64) string {
	return strconv.FormatInt(value, 10)
}
