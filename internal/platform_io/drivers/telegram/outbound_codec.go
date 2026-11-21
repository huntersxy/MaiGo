package telegram

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"MaiGo/internal/platform_io"
)

type outboundPart struct {
	method string
	body   map[string]any
	kind   platformio.SegmentKind
	binary []byte
	album  int
}

func encodeOutbound(ctx context.Context, segments []platformio.Segment, depth int) ([]outboundPart, error) {
	if depth > 8 {
		return nil, fmt.Errorf("合并转发嵌套层数超过 8")
	}
	parts := make([]outboundPart, 0)
	var text strings.Builder
	var entities []map[string]any
	offset := 0
	album := 0
	flush := func() error {
		chunks, err := splitText(text.String(), entities, 4096)
		if err != nil {
			return err
		}
		parts = append(parts, chunks...)
		text.Reset()
		entities = nil
		offset = 0
		return nil
	}
	for _, segment := range segments {
		switch segment.Kind {
		case platformio.SegmentText:
			text.WriteString(segment.Text)
			offset += utf16Length(segment.Text)
		case platformio.SegmentAt:
			target := pickText(segment.AtUserID, segment.Extra["target_user_id"], segment.Extra["user_id"])
			label := pickText(segment.Extra["target_user_cardname"], segment.Extra["cardname"], segment.Extra["card"],
				segment.Extra["target_user_nickname"], segment.Extra["nickname"], segment.Extra["name"], segment.Text, target)
			entity := map[string]any{"type": "mention"}
			if strings.HasPrefix(target, "@") {
				label = target
			} else {
				user, err := ParseGroupIDForUser(target)
				if err != nil {
					return nil, err
				}
				if !strings.HasPrefix(label, "@") {
					label = "@" + label
				}
				entity["type"] = "text_mention"
				entity["user"] = map[string]any{"id": user, "is_bot": false, "first_name": label}
			}
			n := utf16Length(label)
			entity["offset"], entity["length"] = offset, n
			entities = append(entities, entity)
			text.WriteString(label + " ")
			offset += n + 1
		case platformio.SegmentReply:
		case platformio.SegmentImage, platformio.SegmentEmoji, platformio.SegmentVoice, platformio.SegmentRecord:
			if err := flush(); err != nil {
				return nil, err
			}
			kind := segment.Kind
			if kind == platformio.SegmentRecord {
				kind = platformio.SegmentVoice
			}
			encoded := segment.Extra["binary_data_base64"]
			if encoded == "" {
				var err error
				encoded, err = platformio.SegmentMediaBase64(ctx, segment)
				if err != nil {
					return nil, err
				}
			}
			if len(encoded) > base64.StdEncoding.EncodedLen(maxConvertedBytes) || strings.ContainsAny(encoded, "\r\n") {
				return nil, fmt.Errorf("Telegram 媒体 Base64 无效或超过 50 MB")
			}
			binary, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || len(binary) == 0 || len(binary) > maxConvertedBytes {
				return nil, fmt.Errorf("Telegram 媒体 Base64 无效或超过 50 MB")
			}
			parts = append(parts, outboundPart{kind: kind, binary: binary, body: map[string]any{}})
		case platformio.SegmentForward:
			if err := flush(); err != nil {
				return nil, err
			}
			if len(segment.ForwardNodes) == 0 {
				return nil, fmt.Errorf("转发段必须包含节点")
			}
			var forwarded []outboundPart
			for _, node := range segment.ForwardNodes {
				if messageIDPattern.MatchString(node.MessageID) {
					chatID, msgID, err := ParseMessageID(node.MessageID)
					if err != nil {
						return nil, err
					}
					forwarded = append(forwarded, outboundPart{method: "forwardMessage", body: map[string]any{"from_chat_id": chatID, "message_id": msgID}})
					continue
				}
				if len(node.Segments) == 0 {
					return nil, fmt.Errorf("转发节点缺少内容")
				}
				children, err := encodeOutbound(ctx, node.Segments, depth+1)
				if err != nil {
					return nil, err
				}
				forwarded = append(forwarded, children...)
			}
			var rendered []outboundPart
			for i := 0; i < len(forwarded); {
				if forwarded[i].kind != platformio.SegmentImage {
					rendered = append(rendered, forwarded[i])
					i++
					continue
				}
				j := i + 1
				for j < len(forwarded) && forwarded[j].kind == platformio.SegmentImage {
					j++
				}
				if len(rendered) > 0 {
					prev := rendered[len(rendered)-1]
					if prev.method == "sendMessage" && utf16Length(asString(prev.body["text"])) <= 1024 {
						forwarded[i].body["caption"] = prev.body["text"]
						forwarded[i].body["caption_entities"] = prev.body["entities"]
						rendered = rendered[:len(rendered)-1]
					}
				}
				album++
				for _, part := range forwarded[i:j] {
					part.album = album
					rendered = append(rendered, part)
				}
				i = j
			}
			parts = append(parts, rendered...)
		default:
			return nil, fmt.Errorf("不支持的出站消息段: %s", segment.Kind)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return parts, nil
}

func EncodeOutbound(outbound platformio.OutboundMessageEnvelope) ([]map[string]any, error) {
	parts, err := encodeOutbound(context.Background(), outbound.Segments, 0)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("出站消息为空")
	}
	result := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		if part.method != "" {
			result = append(result, map[string]any{"method": part.method, "body": part.body})
		} else {
			result = append(result, map[string]any{"kind": string(part.kind), "binary": part.binary, "body": part.body})
		}
	}
	return result, nil
}

func splitText(text string, entities []map[string]any, limit int) ([]outboundPart, error) {
	var parts []outboundPart
	start := 0
	for len(text) > 0 {
		units := 0
		for _, r := range text {
			n := 1
			if r >= 0x10000 {
				n = 2
			}
			if units+n > limit {
				break
			}
			units += n
		}
		end := start + units
		for _, entity := range entities {
			offset, length := entity["offset"].(int), entity["length"].(int)
			if offset < end && end < offset+length {
				end = offset
			}
		}
		if end == start {
			return nil, fmt.Errorf("单个 @ entity 超过 Telegram 文本上限")
		}
		local := make([]map[string]any, 0)
		for _, entity := range entities {
			offset := entity["offset"].(int)
			if offset >= start && offset < end {
				copyEntity := make(map[string]any, len(entity))
				for key, value := range entity {
					copyEntity[key] = value
				}
				copyEntity["offset"] = offset - start
				local = append(local, copyEntity)
			}
		}
		idx, err := utf16Index(text, end-start)
		if err != nil {
			return nil, err
		}
		runes := []rune(text)
		parts = append(parts, outboundPart{method: "sendMessage", body: map[string]any{"text": string(runes[:idx]), "entities": local}})
		text = string(runes[idx:])
		start = end
	}
	return parts, nil
}

type telegramRequest struct {
	method  string
	body    map[string]any
	uploads []UploadField
	album   int
}

func prepareRequests(ctx context.Context, parts []outboundPart, store *Store, account string, timeout time.Duration) ([]telegramRequest, error) {
	var prepared []telegramRequest
	for _, part := range parts {
		request := telegramRequest{method: part.method, body: part.body, album: part.album}
		if request.method != "" {
			prepared = append(prepared, request)
			continue
		}
		if part.kind == platformio.SegmentVoice {
			opus, err := convertMedia(ctx, timeout, part.binary, ".ogg", "-vn", "-ac", "1", "-ar", "48000", "-c:a", "libopus", "-b:a", "48k")
			if err != nil {
				return nil, err
			}
			request.method = "sendVoice"
			request.uploads = []UploadField{{"voice", "voice.ogg", "audio/ogg", opus}}
		} else {
			sticker := part.kind == platformio.SegmentEmoji
			if sticker && store != nil {
				id, err := store.Sticker(account, sha256Hex(part.binary))
				if err != nil {
					return nil, err
				}
				if id != "" {
					request.method = "sendSticker"
					request.body["sticker"] = id
				}
			}
			if request.method == "" {
				method, upload, err := prepareImage(part.binary, sticker)
				if err != nil {
					return nil, err
				}
				request.method = method
				request.uploads = []UploadField{upload}
			}
		}
		prepared = append(prepared, request)
	}
	var grouped []telegramRequest
	for i := 0; i < len(prepared); {
		j := i + 1
		first := prepared[i]
		if first.album > 0 && first.method == "sendPhoto" {
			for j < len(prepared) && j-i < 10 && prepared[j].album == first.album && prepared[j].method == "sendPhoto" {
				j++
			}
		}
		if j == i+1 {
			grouped = append(grouped, first)
		} else {
			var media []map[string]any
			var uploads []UploadField
			for index, part := range prepared[i:j] {
				upload := part.uploads[0]
				upload.Field = fmt.Sprintf("photo%d", index)
				uploads = append(uploads, upload)
				item := map[string]any{"type": "photo", "media": "attach://" + upload.Field}
				for key, value := range part.body {
					item[key] = value
				}
				media = append(media, item)
			}
			grouped = append(grouped, telegramRequest{method: "sendMediaGroup", body: map[string]any{"media": media}, uploads: uploads})
		}
		i = j
	}
	return grouped, nil
}

func OutboundTargetChatID(outbound platformio.OutboundMessageEnvelope) (int64, *int64, error) {
	target := ""
	if outbound.MessageInfo.GroupInfo != nil {
		target = outbound.MessageInfo.GroupInfo.GroupID
	}
	config := outbound.MessageInfo.AdditionalConfig
	target = pickText(target, asString(config["platform_io_target_group_id"]), asString(config["platform_io_target_user_id"]), outbound.TargetID)
	if target == "" {
		return 0, nil, fmt.Errorf("出站消息缺少目标聊天 ID")
	}
	return ParseGroupID(target)
}

func ParseGroupIDForUser(target string) (int64, error) {
	id, thread, err := ParseGroupID(target)
	if err != nil || thread != nil || id <= 0 {
		return 0, fmt.Errorf("Telegram @ 用户 ID 无效")
	}
	return id, nil
}

func pickText(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func replyParameters(outbound platformio.OutboundMessageEnvelope, chatID int64) (map[string]any, error) {
	for _, segment := range outbound.Segments {
		if segment.Kind != platformio.SegmentReply {
			continue
		}
		data, _ := asMap(segment.Data)
		text, _ := segment.Data.(string)
		target := pickText(asString(data["target_message_id"]), segment.Extra["target_message_id"], segment.Extra["id"], segment.Extra["message_id"], segment.Text, text)
		if target == "" {
			return nil, nil
		}
		sourceID, msgID, err := ParseMessageID(target)
		if err != nil {
			return nil, err
		}
		if sourceID != chatID {
			return nil, fmt.Errorf("回复引用不属于目标聊天")
		}
		return map[string]any{"message_id": msgID}, nil
	}
	return nil, nil
}
