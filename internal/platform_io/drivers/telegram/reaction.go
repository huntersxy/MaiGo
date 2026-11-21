package telegram

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

func DecodeReaction(update map[string]any, botID string) (map[string]any, error) {
	eventType := "message_reaction"
	raw, individual := update[eventType]
	if !individual {
		eventType = "message_reaction_count"
		raw = update[eventType]
	}
	payload, ok := asMap(raw)
	if !ok {
		return nil, fmt.Errorf("Telegram 表态事件格式无效")
	}
	chat, _ := asMap(payload["chat"])
	chatType := asString(chat["type"])
	if chatType != "private" && chatType != "group" && chatType != "supergroup" {
		return nil, nil
	}
	chatID, chatOK := asInt64(chat["id"])
	msgID, msgOK := asInt64(payload["message_id"])
	updateID, updateOK := asInt64(update["update_id"])
	if !chatOK || !msgOK || !updateOK {
		return nil, fmt.Errorf("Telegram 表态事件 ID 格式无效")
	}
	target := ExternalID(chatID, msgID)
	actor := chat
	name := "匿名表态统计"
	var text string
	if individual {
		actor, _ = asMap(payload["user"])
		if len(actor) == 0 {
			actor, _ = asMap(payload["actor_chat"])
		}
		if len(actor) == 0 {
			return nil, fmt.Errorf("Telegram 表态事件缺少发送者身份")
		}
		if asString(actor["id"]) == botID {
			return nil, nil
		}
		old, new := entityList(payload["old_reaction"]), entityList(payload["new_reaction"])
		added, err := changedReactions(new, old)
		if err != nil {
			return nil, err
		}
		removed, err := changedReactions(old, new)
		if err != nil {
			return nil, err
		}
		var changes []string
		if len(added) > 0 {
			changes = append(changes, "添加了 "+strings.Join(added, "、")+" 表态")
		}
		if len(removed) > 0 {
			changes = append(changes, "取消了 "+strings.Join(removed, "、")+" 表态")
		}
		if len(changes) == 0 {
			return nil, nil
		}
		name = displayName(actor)
		text = fmt.Sprintf("[事件-消息表态] %s 对消息 %s %s", name, target, strings.Join(changes, "；"))
	} else {
		stats := make([]string, 0)
		for _, entry := range entityList(payload["reactions"]) {
			kind, _ := asMap(entry["type"])
			label, err := reactionLabel(kind)
			if err != nil {
				return nil, err
			}
			stats = append(stats, fmt.Sprintf("%s × %v", label, entry["total_count"]))
		}
		if len(stats) == 0 {
			stats = append(stats, "无表态")
		}
		text = fmt.Sprintf("[事件-消息表态] 消息 %s 的匿名表态统计：%s", target, strings.Join(stats, "、"))
	}
	_, actorIsChat := payload["actor_chat"]
	info := map[string]any{
		"user_info": map[string]any{"user_id": asString(actor["id"]), "user_nickname": name},
		"additional_config": map[string]any{
			"telegram_event_type": eventType, "telegram_event_payload": payload,
			"telegram_chat_id": asString(chatID), "telegram_message_id": msgID,
			"telegram_reaction_target_message_id": target, "telegram_reaction_anonymous": !individual || actorIsChat,
		},
	}
	if chatType != "private" {
		info["group_info"] = map[string]any{"group_id": asString(chatID), "group_name": chat["title"]}
	}
	return map[string]any{
		"platform": PlatformTelegram, "account_id": botID, "scope": nil, "reply_to": target,
		"message_id": fmt.Sprintf("reaction:%d:%d", chatID, updateID), "timestamp": asString(payload["date"]),
		"message_info": info, "raw_message": []map[string]any{{"type": "text", "data": text}},
		"is_notify": true, "is_at": false, "is_mentioned": false,
	}, nil
}

func changedReactions(current, previous []map[string]any) ([]string, error) {
	var labels []string
	for _, item := range current {
		if slices.ContainsFunc(previous, func(other map[string]any) bool { return reflect.DeepEqual(item, other) }) {
			continue
		}
		label, err := reactionLabel(item)
		if err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	return labels, nil
}

func reactionLabel(reaction map[string]any) (string, error) {
	switch reaction["type"] {
	case "emoji":
		return asString(reaction["emoji"]), nil
	case "custom_emoji":
		return fmt.Sprintf("自定义表情(%s)", asString(reaction["custom_emoji_id"])), nil
	case "paid":
		return "付费⭐", nil
	default:
		return "", fmt.Errorf("不支持的 Telegram 表态类型: %v", reaction["type"])
	}
}
