package weixin_clawbot

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	memutils "MaiGo/internal/A_memorix/core/utils"
	"MaiGo/internal/platform_io"
)

const Platform = PlatformWeixin

func MessageID(account string, msg map[string]any) string {
	identity := firstNonEmpty(
		messageIdentity(msg["message_id"]),
		messageIdentity(msg["msg_id"]),
	)
	if identity == "" {
		raw, _ := memutils.PyJSONMarshal(msg)
		sum := sha256.Sum256([]byte(raw))
		identity = hex.EncodeToString(sum[:])
	}
	return fmt.Sprintf("%s:%s:%s", account, messageIdentity(msg["from_user_id"]), identity)
}

func Inbound(account string, msg map[string]any, segments []map[string]any, nickname string) map[string]any {
	stamp, _ := messageStamp(msg)
	if nickname == "" {
		nickname = messageIdentity(msg["from_user_id"])
	}
	timestamp, _ := memutils.PyJSONMarshal(stamp)
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
		"timestamp":  timestamp,
		"message_info": map[string]any{
			"user_info": map[string]any{
				"user_id":       messageIdentity(msg["from_user_id"]),
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
	return encodeOutbound(context.Background(), outbound, maxBytes)
}

func encodeOutbound(ctx context.Context, outbound platformio.OutboundMessageEnvelope, maxBytes int) (target string, parts []OutboundPart, err error) {
	if outbound.MessageInfo.GroupInfo != nil {
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
			raw, decodeErr := decodeSegmentBinary(ctx, segment, maxBytes)
			if decodeErr != nil {
				return "", nil, decodeErr
			}
			if len(raw) == 0 || len(raw) > maxBytes {
				return "", nil, fmt.Errorf("图片为空或超过大小限制")
			}
			parts = append(parts, OutboundPart{Kind: "image", Raw: raw})
		default:
			return "", nil, fmt.Errorf("暂不支持发送 %s 消息", segment.Kind)
		}
	}
	if len(parts) == 0 {
		return "", nil, fmt.Errorf("没有可发送的消息内容")
	}
	return target, parts, nil
}

func decodeSegmentBinary(ctx context.Context, segment platformio.Segment, maxBytes int) ([]byte, error) {
	value := segment.Extra["binary_data_base64"]
	if value == "" {
		var err error
		value, err = platformio.SegmentMediaBase64(ctx, segment)
		if err != nil {
			return nil, err
		}
	}
	if len(value) > base64.StdEncoding.EncodedLen(maxBytes) {
		return nil, fmt.Errorf("图片超过大小限制")
	}
	if strings.ContainsAny(value, "\r\n") {
		return nil, fmt.Errorf("图片 Base64 无效")
	}
	return base64.StdEncoding.DecodeString(value)
}

func messageIdentity(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		if v != 0 {
			text, _ := memutils.PyJSONMarshal(v)
			return text
		}
	case int, int64, uint64, json.Number:
		if n := fmt.Sprint(v); n != "0" {
			return n
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func messageStamp(msg map[string]any) (float64, error) {
	value := msg["create_time_ms"]
	if messageIdentity(value) == "" {
		value = msg["create_time"]
	}
	if messageIdentity(value) == "" {
		return float64(time.Now().UnixNano()) / 1e9, nil
	}
	var stamp float64
	switch v := value.(type) {
	case float64:
		stamp = v
	case int64:
		stamp = float64(v)
	case int:
		stamp = float64(v)
	default:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(messageIdentity(value)), 64)
		if err != nil {
			return 0, fmt.Errorf("微信消息时间戳无效")
		}
		stamp = parsed
	}
	if stamp > 1_000_000_000_000 {
		stamp /= 1000
	}
	return stamp, nil
}
