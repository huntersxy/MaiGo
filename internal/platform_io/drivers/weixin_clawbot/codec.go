package weixin_clawbot

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/platform_io"
)

const Platform = PlatformWeixin

func MessageID(account string, msg map[string]any) string {
	identity := firstNonEmpty(
		asString(msg["message_id"]),
		asString(msg["msg_id"]),
	)
	if identity == "" {
		raw, _ := json.Marshal(msg)
		sum := sha256.Sum256(raw)
		identity = hex.EncodeToString(sum[:])
	}
	return fmt.Sprintf("%s:%s:%s", account, asString(msg["from_user_id"]), identity)
}

func Inbound(account string, msg map[string]any, segments []map[string]any, nickname string) map[string]any {
	stamp := asFloat64(msg["create_time_ms"])
	if stamp == 0 {
		stamp = asFloat64(msg["create_time"])
	}
	if stamp == 0 {
		stamp = float64(time.Now().Unix())
	}
	if stamp > 1_000_000_000_000 {
		stamp /= 1000
	}
	if nickname == "" {
		nickname = asString(msg["from_user_id"])
	}
	isPicture := false
	for _, seg := range segments {
		if asString(seg["type"]) == "image" {
			isPicture = true
			break
		}
	}
	return map[string]any{
		"platform":   Platform,
		"account_id": account,
		"message_id": MessageID(account, msg),
		"timestamp":  fmt.Sprint(stamp),
		"message_info": map[string]any{
			"user_info": map[string]any{
				"user_id":       asString(msg["from_user_id"]),
				"user_nickname": nickname,
			},
			"additional_config": map[string]any{},
		},
		"raw_message": segments,
		"is_picture":  isPicture,
	}
}

// OutboundPart 出站部件
type OutboundPart struct {
	Kind string
	Text string
	Raw  []byte
}

func EncodeOutbound(outbound platformio.OutboundMessageEnvelope, maxBytes int) (target string, parts []OutboundPart, err error) {
	if outbound.MessageInfo.GroupInfo != nil && strings.TrimSpace(outbound.MessageInfo.GroupInfo.GroupID) != "" {
		return "", nil, fmt.Errorf("ClawBot 适配器不支持群聊")
	}
	if outbound.MessageInfo.AdditionalConfig != nil {
		if asString(outbound.MessageInfo.AdditionalConfig["platform_io_target_group_id"]) != "" {
			return "", nil, fmt.Errorf("ClawBot 适配器不支持群聊")
		}
		target = asString(outbound.MessageInfo.AdditionalConfig["platform_io_target_user_id"])
	}
	if target == "" {
		target = strings.TrimSpace(outbound.TargetID)
	}
	if target == "" {
		return "", nil, fmt.Errorf("缺少出站目标用户 ID")
	}
	for _, segment := range outbound.Segments {
		switch segment.Kind {
		case platformio.SegmentReply:
			continue
		case platformio.SegmentText:
			if segment.Text != "" {
				parts = append(parts, OutboundPart{Kind: "text", Text: segment.Text})
			}
		case platformio.SegmentImage, platformio.SegmentEmoji:
			raw, decodeErr := decodeSegmentBinary(segment)
			if decodeErr != nil {
				return "", nil, decodeErr
			}
			if len(raw) == 0 || len(raw) > maxBytes {
				return "", nil, fmt.Errorf("图片为空或超过大小限制")
			}
			parts = append(parts, OutboundPart{Kind: "image", Raw: raw})
		default:
			if segment.Text != "" {
				parts = append(parts, OutboundPart{Kind: "text", Text: segment.Text})
			}
		}
	}
	if len(parts) == 0 && strings.TrimSpace(outbound.PlainText) != "" {
		parts = append(parts, OutboundPart{Kind: "text", Text: outbound.PlainText})
	}
	if len(parts) == 0 {
		return "", nil, fmt.Errorf("没有可发送的消息内容")
	}
	return target, parts, nil
}

func decodeSegmentBinary(segment platformio.Segment) ([]byte, error) {
	if segment.Extra != nil {
		if b64 := segment.Extra["binary_data_base64"]; b64 != "" {
			return base64.StdEncoding.DecodeString(b64)
		}
	}
	if data, ok := segment.Data.(string); ok && data != "" {
		return base64.StdEncoding.DecodeString(data)
	}
	if segment.URL != "" && strings.HasPrefix(segment.URL, "data:") {
		idx := strings.Index(segment.URL, ",")
		if idx > 0 {
			return base64.StdEncoding.DecodeString(segment.URL[idx+1:])
		}
	}
	return nil, fmt.Errorf("图片超过大小限制")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func asFloat64(v any) float64 {
	switch typed := v.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err == nil {
			return f
		}
	default:
	}
	return 0
}
