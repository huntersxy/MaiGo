package maimbridge

import (
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"maibot-go/internal/common/message_server"
	"maibot-go/internal/platform_io"
)

// maimSegPayload maim_message 段数据负载的常见字段
type maimSegPayload struct {
	Text    string `json:"text"`
	Content string `json:"content"`
	URL     string `json:"url"`
	File    string `json:"file"`
	Path    string `json:"path"`
	Name    string `json:"name"`
	ID      string `json:"id"`
	QQ      string `json:"qq"`
	UserID  string `json:"user_id"`
	Summary string `json:"summary"`
	Base64  string `json:"base64"`
}

func decodeSegPayload(data json.RawMessage) map[string]any {
	payload := map[string]any{}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return payload
	}
	// data 既可能是对象，也可能是被字符串化的 JSON
	if err := json.Unmarshal(data, &payload); err == nil {
		return payload
	}
	var encoded string
	if err := json.Unmarshal(data, &encoded); err == nil && strings.TrimSpace(encoded) != "" {
		if unmarshalErr := json.Unmarshal([]byte(encoded), &payload); unmarshalErr != nil {
			zap.L().Warn("maim_message 媒体段 JSON 解析失败，payload 置空", zap.Error(unmarshalErr))
		}
	}
	return payload
}

func payloadString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok || value == nil {
			continue
		}
		if text := mediaStringValue(value); text != "" {
			return text
		}
	}
	return ""
}

// mediaSegmentFromMaimSeg 将图片/文件/表情/语音/视频等媒体段转换为规范化段
func mediaSegmentFromMaimSeg(kind platformio.SegmentKind, data json.RawMessage) platformio.Segment {
	payload := decodeSegPayload(data)
	seg := platformio.Segment{Kind: kind, Extra: map[string]string{}}
	seg.URL = payloadString(payload, "url", "src")
	file := payloadString(payload, "file", "path")
	if file != "" {
		seg.Extra["file"] = file
	}
	if name := payloadString(payload, "name", "file_name"); name != "" {
		seg.Extra["name"] = name
	}
	if id := payloadString(payload, "id", "file_id"); id != "" {
		seg.Extra["id"] = id
	}
	if summary := payloadString(payload, "summary", "content", "text"); summary != "" {
		seg.Text = summary
	}
	if base64 := maimMediaBase64(kind, data, payload); base64 != "" {
		seg.Extra["base64"] = base64
	}
	if kind == platformio.SegmentReply {
		if sender := payloadString(payload, "sender_id", "user_id", "sender"); sender != "" {
			seg.Extra["sender_id"] = sender
		}
	}
	if kind == platformio.SegmentJSON {
		if raw := strings.TrimSpace(string(data)); raw != "" && raw != "null" {
			seg.Extra["data"] = raw
		}
	}
	if len(seg.Extra) == 0 {
		seg.Extra = nil
	}
	return seg
}

// segmentToMaimSeg 将规范化出站段转换为 maim_message 段
func segmentToMaimSeg(seg platformio.Segment) maimmsg.Seg {
	switch seg.Kind {
	case platformio.SegmentText:
		return maimmsg.SegText(seg.Text)
	case platformio.SegmentAt:
		payload := map[string]any{}
		if seg.AtUserID != "" {
			payload["user_id"] = seg.AtUserID
		}
		if seg.Extra != nil {
			if name := strings.TrimSpace(seg.Extra["name"]); name != "" {
				payload["name"] = name
			}
		}
		return maimmsg.Seg{Type: "at", Data: mustJSON(payload)}
	case platformio.SegmentForward:
		return maimmsg.Seg{Type: "forward", Data: mustJSON(forwardNodesToMaimData(seg.ForwardNodes))}
	case platformio.SegmentImage, platformio.SegmentEmoji, platformio.SegmentVoice,
		platformio.SegmentVideo, platformio.SegmentRecord, platformio.SegmentFile:
		base64Value := ""
		if seg.Extra != nil {
			base64Value = strings.TrimSpace(seg.Extra["base64"])
		}
		if base64Value != "" && seg.URL == "" && (seg.Extra == nil || strings.TrimSpace(seg.Extra["file"]) == "") {
			return maimmsg.Seg{Type: string(seg.Kind), Data: mustJSON(base64Value)}
		}
		payload := map[string]any{}
		if seg.URL != "" {
			payload["url"] = seg.URL
		}
		if seg.Extra != nil {
			if file := strings.TrimSpace(seg.Extra["file"]); file != "" {
				payload["file"] = file
			}
			if name := strings.TrimSpace(seg.Extra["name"]); name != "" {
				payload["name"] = name
			}
			if id := strings.TrimSpace(seg.Extra["id"]); id != "" {
				payload["id"] = id
			}
		}
		if base64Value != "" {
			payload["base64"] = base64Value
		}
		if seg.Text != "" {
			payload["text"] = seg.Text
		}
		if len(payload) == 0 {
			payload["text"] = ""
		}
		return maimmsg.Seg{Type: string(seg.Kind), Data: mustJSON(payload)}
	case platformio.SegmentFace:
		payload := map[string]any{}
		if seg.Extra != nil {
			if id := strings.TrimSpace(seg.Extra["id"]); id != "" {
				payload["id"] = id
			}
		}
		if seg.Text != "" {
			payload["text"] = seg.Text
		}
		return maimmsg.Seg{Type: "face", Data: mustJSON(payload)}
	case platformio.SegmentReply:
		payload := map[string]any{}
		if seg.Text != "" {
			payload["id"] = seg.Text
		}
		if seg.Extra != nil {
			if id := strings.TrimSpace(seg.Extra["id"]); id != "" {
				payload["id"] = id
			}
		}
		return maimmsg.Seg{Type: "reply", Data: mustJSON(payload)}
	case platformio.SegmentDict:
		return maimmsg.Seg{Type: "dict", Data: mustJSON(seg.Data)}
	case platformio.SegmentJSON:
		payload := map[string]any{}
		if seg.Extra != nil {
			if raw := strings.TrimSpace(seg.Extra["data"]); raw != "" {
				if err := json.Unmarshal([]byte(raw), &payload); err != nil {
					payload["data"] = raw
				}
			}
			if text := strings.TrimSpace(seg.Text); text != "" {
				if err := json.Unmarshal([]byte(text), &payload); err != nil {
					payload["data"] = text
				}
			}
		} else if text := strings.TrimSpace(seg.Text); text != "" {
			if err := json.Unmarshal([]byte(text), &payload); err != nil {
				payload["data"] = text
			}
		}
		return maimmsg.Seg{Type: "json", Data: mustJSON(payload)}
	default:
		return maimmsg.Seg{Type: string(seg.Kind), Data: mustJSON(map[string]any{"text": seg.Text})}
	}
}

// messageSegmentFromOutbound 组装出站消息段，多段时使用 seglist
func messageSegmentFromOutbound(outbound platformio.OutboundMessageEnvelope) maimmsg.Seg {
	segments := outbound.Segments
	replyID := strings.TrimSpace(outbound.ExternalReplyToMessageID)
	if replyID != "" {
		segments = append([]platformio.Segment{{Kind: platformio.SegmentReply, Extra: map[string]string{"id": replyID}}}, segments...)
	}
	if len(segments) == 0 {
		return maimmsg.SegText(outbound.PlainText)
	}
	if len(segments) == 1 {
		return segmentToMaimSeg(segments[0])
	}
	items := make([]map[string]any, 0, len(segments))
	for _, segment := range segments {
		maimSeg := segmentToMaimSeg(segment)
		var payload any
		if err := json.Unmarshal(maimSeg.Data, &payload); err != nil {
			payload = map[string]any{}
		}
		items = append(items, map[string]any{"type": maimSeg.Type, "data": payload})
	}
	return maimmsg.Seg{Type: "seglist", Data: mustJSON(items)}
}

// maimMediaBase64 读取 maim_message 媒体段中的 base64；图片等段允许 data 直接是 base64 字符串
func maimMediaBase64(kind platformio.SegmentKind, data json.RawMessage, payload map[string]any) string {
	switch kind {
	case platformio.SegmentImage, platformio.SegmentEmoji, platformio.SegmentVoice, platformio.SegmentVideo, platformio.SegmentRecord, platformio.SegmentFile:
	default:
		return ""
	}
	if value := payloadString(payload, "base64"); value != "" {
		return value
	}
	trimmed := strings.TrimSpace(string(data))
	if !strings.HasPrefix(trimmed, "\"") {
		return ""
	}
	var encoded string
	if err := json.Unmarshal(data, &encoded); err != nil {
		return ""
	}
	return strings.TrimSpace(encoded)
}

func mediaStringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(fmt.Sprintf("%q", ""))
	}
	return raw
}
