package snowluma

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/platform_io"
)

// EncodeOutboundSegments 将出站封装转为 OneBot message 段列表
func EncodeOutboundSegments(ctx context.Context, outbound platformio.OutboundMessageEnvelope) ([]map[string]any, error) {
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
		case platformio.SegmentReply:
			// 引用走独立段；缺 id 时整段丢弃，不能退化成正文文本
			id := firstNonEmpty(segmentExtra(segment, "id"), segment.Text)
			if id == "" {
				continue
			}
			segments = append(segments, map[string]any{
				"type": "reply",
				"data": map[string]any{"id": id},
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
		case platformio.SegmentImage, platformio.SegmentEmoji:
			encoded := strings.TrimSpace(segmentExtra(segment, "binary_data_base64"))
			if encoded == "" {
				binary, err := platformio.SegmentMediaBase64(ctx, segment)
				if err != nil {
					return nil, fmt.Errorf("编码 OneBot 图片段失败: %w", err)
				}
				encoded = binary
			}
			if encoded == "" {
				return nil, fmt.Errorf("OneBot 图片段内容为空")
			}
			data := map[string]any{"file": "base64://" + encoded, "sub_type": 0}
			if segment.Kind == platformio.SegmentEmoji || segmentExtra(segment, "sub_type") == "1" {
				data["sub_type"] = 1
				data["summary"] = "[动画表情]"
			}
			segments = append(segments, map[string]any{
				"type": "image",
				"data": data,
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
	return segments, nil
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
