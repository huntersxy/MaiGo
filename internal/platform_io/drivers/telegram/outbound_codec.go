package telegram

import (
	"fmt"
	"strings"

	"MaiGo/internal/platform_io"
)

// EncodeOutbound 将宿主出站转为 Bot API 调用列表（P0：文本；媒体降级占位）
func EncodeOutbound(outbound platformio.OutboundMessageEnvelope) ([]map[string]any, error) {
	parts := make([]map[string]any, 0)
	appendText := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		for _, chunk := range splitTextUTF16(text, 4096) {
			parts = append(parts, map[string]any{
				"method": "sendMessage",
				"body": map[string]any{
					"text":     chunk,
					"entities": []any{},
				},
			})
		}
	}
	for _, segment := range outbound.Segments {
		switch segment.Kind {
		case platformio.SegmentText:
			appendText(segment.Text)
		case platformio.SegmentAt:
			label := segment.Text
			if label == "" {
				label = segment.AtUserID
			}
			if label != "" && !strings.HasPrefix(label, "@") {
				label = "@" + label
			}
			appendText(label + " ")
		case platformio.SegmentImage, platformio.SegmentEmoji, platformio.SegmentVoice, platformio.SegmentRecord:
			appendText(fmt.Sprintf("[%s]", segment.Kind))
		default:
			if segment.Text != "" {
				appendText(segment.Text)
			}
		}
	}
	if len(parts) == 0 {
		appendText(outbound.PlainText)
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("出站消息为空")
	}
	return parts, nil
}

func splitTextUTF16(text string, limit int) []string {
	if utf16Length(text) <= limit {
		return []string{text}
	}
	chunks := make([]string, 0)
	remaining := []rune(text)
	for len(remaining) > 0 {
		length := 0
		end := 0
		for end < len(remaining) {
			size := 1
			if remaining[end] >= 0x10000 {
				size = 2
			}
			if length+size > limit {
				break
			}
			length += size
			end++
		}
		if end == 0 {
			end = 1
		}
		chunks = append(chunks, string(remaining[:end]))
		remaining = remaining[end:]
	}
	return chunks
}

func OutboundTargetChatID(outbound platformio.OutboundMessageEnvelope) (int64, *int64, error) {
	target := strings.TrimSpace(outbound.TargetID)
	if target == "" {
		if outbound.MessageInfo.GroupInfo != nil {
			target = strings.TrimSpace(outbound.MessageInfo.GroupInfo.GroupID)
		}
	}
	if target == "" && outbound.MessageInfo.AdditionalConfig != nil {
		if v := asString(outbound.MessageInfo.AdditionalConfig["platform_io_target_group_id"]); v != "" {
			target = v
		} else if v := asString(outbound.MessageInfo.AdditionalConfig["platform_io_target_user_id"]); v != "" {
			target = v
		}
	}
	if target == "" {
		return 0, nil, fmt.Errorf("出站消息缺少目标聊天 ID")
	}
	return ParseGroupID(target)
}
