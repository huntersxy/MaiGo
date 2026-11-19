package snowluma

// inbound_codec.go：复用 drivers/onebot 模型层；P0 仅提供最小规范化。

import (
	"strconv"
	"strings"
	"time"
)

// NormalizeInboundMessage 将 OneBot 事件粗转 Host 消息字典骨架
func NormalizeInboundMessage(payload map[string]any, accountID, scope string) map[string]any {
	if payload == nil {
		return nil
	}
	messageType := asString(payload["message_type"])
	if messageType == "" {
		messageType = asString(payload["detail_type"])
	}
	userID := asString(payload["user_id"])
	groupID := asString(payload["group_id"])
	messageID := asString(payload["message_id"])
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
	rawMessage := payload["message"]
	if rawMessage == nil {
		rawMessage = payload["raw_message"]
	}
	timestamp := asFloat(payload["time"], float64(time.Now().Unix()))
	info := map[string]any{
		"user_info": map[string]any{
			"user_id":       userID,
			"user_nickname": nickname,
			"user_cardname": card,
		},
		"additional_config": map[string]any{},
	}
	if groupID != "" {
		info["group_info"] = map[string]any{
			"group_id":   groupID,
			"group_name": "",
		}
	}
	out := map[string]any{
		"platform":     PlatformQQ,
		"account_id":   accountID,
		"scope":        scope,
		"message_id":   messageID,
		"message_type": messageType,
		"user_id":      userID,
		"group_id":     groupID,
		"timestamp":    timestamp,
		"raw_message":  rawMessage,
		"message_info": info,
		"content":      plainTextFromSegments(rawMessage),
	}
	return out
}

func plainTextFromSegments(raw any) string {
	switch typed := raw.(type) {
	case string:
		return typed
	case []any:
		var b strings.Builder
		for _, item := range typed {
			seg, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if asString(seg["type"]) != "text" {
				continue
			}
			data := seg["data"]
			switch d := data.(type) {
			case string:
				b.WriteString(d)
			case map[string]any:
				b.WriteString(asString(d["text"]))
			}
		}
		return b.String()
	case []map[string]any:
		var b strings.Builder
		for _, seg := range typed {
			if asString(seg["type"]) != "text" {
				continue
			}
			data := seg["data"]
			switch d := data.(type) {
			case string:
				b.WriteString(d)
			case map[string]any:
				b.WriteString(asString(d["text"]))
			}
		}
		return b.String()
	default:
		return ""
	}
}

func int64String(value int64) string {
	return strconv.FormatInt(value, 10)
}
