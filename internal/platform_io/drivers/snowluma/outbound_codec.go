package snowluma

import (
	"MaiGo/internal/platform_io"
)

// EncodeOutboundSegments 将出站封装转为 OneBot message 段列表（P0 最小实现）
func EncodeOutboundSegments(outbound platformio.OutboundMessageEnvelope, profile ClientProfile) []map[string]any {
	_ = profile
	segments := make([]map[string]any, 0, len(outbound.Segments)+1)
	for _, segment := range outbound.Segments {
		switch segment.Kind {
		case platformio.SegmentText:
			if segment.Text == "" {
				continue
			}
			segments = append(segments, map[string]any{
				"type": "text",
				"data": map[string]any{"text": segment.Text},
			})
		case platformio.SegmentAt:
			qq := segment.AtUserID
			if qq == "" {
				qq = "all"
			}
			segments = append(segments, map[string]any{
				"type": "at",
				"data": map[string]any{"qq": qq},
			})
		case platformio.SegmentImage:
			file := firstNonEmpty(segment.URL, segmentExtra(segment, "file"))
			if file == "" {
				continue
			}
			segments = append(segments, map[string]any{
				"type": "image",
				"data": map[string]any{"file": file},
			})
		case platformio.SegmentVoice, platformio.SegmentRecord:
			file := firstNonEmpty(segment.URL, segmentExtra(segment, "file"))
			if file == "" {
				segments = append(segments, map[string]any{
					"type": "text",
					"data": map[string]any{"text": VoicePlaceholder()},
				})
				continue
			}
			segments = append(segments, map[string]any{
				"type": "record",
				"data": map[string]any{"file": file},
			})
		default:
			if segment.Text != "" {
				segments = append(segments, map[string]any{
					"type": "text",
					"data": map[string]any{"text": segment.Text},
				})
			}
		}
	}
	if len(segments) == 0 && outbound.PlainText != "" {
		segments = append(segments, map[string]any{
			"type": "text",
			"data": map[string]any{"text": outbound.PlainText},
		})
	}
	return segments
}

func segmentExtra(segment platformio.Segment, key string) string {
	if segment.Extra == nil {
		return ""
	}
	return segment.Extra[key]
}

// ResolveSendAction 按画像选择发送动作名与参数键
func ResolveSendAction(profile ClientProfile, chatType platformio.ChatType, targetID string, message []map[string]any) (string, map[string]any) {
	params := map[string]any{"message": message}
	if profile.MsgActionStyle == MsgActionStyleGeneric {
		if chatType == platformio.ChatTypeGroup {
			params["message_type"] = "group"
			params["group_id"] = targetID
		} else {
			params["message_type"] = "private"
			params["user_id"] = targetID
		}
		return "send_msg", params
	}
	if chatType == platformio.ChatTypeGroup {
		params["group_id"] = targetID
		return "send_group_msg", params
	}
	params["user_id"] = targetID
	return "send_private_msg", params
}
