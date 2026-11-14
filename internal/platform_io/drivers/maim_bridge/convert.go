package maimbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"maibot-go/internal/common/message_server"
	"maibot-go/internal/platform_io"
)

const inboundMetaMessageBase = "maimmsg.message_base"

// InboundFromMessageBase 将 maim_message MessageBase 转为 Platform IO 入站封装
func InboundFromMessageBase(msg maimmsg.MessageBase, connPlatform string) (platformio.InboundMessageEnvelope, error) {
	info := msg.MessageInfo
	platform := strings.TrimSpace(info.Platform)
	if platform == "" {
		platform = strings.TrimSpace(connPlatform)
	}
	if platform == "" {
		return platformio.InboundMessageEnvelope{}, fmt.Errorf("maimbridge: 缺少 platform")
	}
	info.Platform = platform

	chat, err := chatMessageFromBase(msg, platform)
	if err != nil {
		return platformio.InboundMessageEnvelope{}, err
	}
	routeKey, err := platformio.RouteKeyFactory{}.FromChatMessage(chat)
	if err != nil {
		return platformio.InboundMessageEnvelope{}, err
	}

	extID := strings.TrimSpace(info.MessageID)
	// received_at 是本机接收入站消息的时刻，不使用适配器提供的 message_info.time
	receivedAt := time.Now()

	payload := map[string]any{}
	if raw, err := msg.ToJSON(); err == nil {
		if unmarshalErr := json.Unmarshal(raw, &payload); unmarshalErr != nil {
			zap.L().Warn("maim_message 入站消息 JSON 解析失败，payload 置空",
				zap.String("message_id", extID), zap.Error(unmarshalErr))
		}
	}

	meta := map[string]any{
		"source":               "maim_message_ws",
		inboundMetaMessageBase: msg,
		"connection_platform":  connPlatform,
	}
	conv, convErr := conversationFromBase(msg, chat, receivedAt)
	if convErr == nil && conv != nil {
		meta[inboundMetaConversationMessage] = conv
	}

	return platformio.InboundMessageEnvelope{
		RouteKey:          routeKey,
		DriverID:          DriverIDMaimMsg,
		DriverKind:        platformio.DriverKindLegacy,
		ExternalMessageID: extID,
		DedupeKey:         extID,
		ChatMessage:       &chat,
		Payload:           payload,
		Metadata:          meta,
		ReceivedAt:        receivedAt,
	}, nil
}

// MessageBaseFromInbound 从入站封装取回 MessageBase
func MessageBaseFromInbound(envelope platformio.InboundMessageEnvelope) (maimmsg.MessageBase, bool) {
	if envelope.Metadata == nil {
		return maimmsg.MessageBase{}, false
	}
	raw, ok := envelope.Metadata[inboundMetaMessageBase]
	if !ok || raw == nil {
		return maimmsg.MessageBase{}, false
	}
	msg, ok := raw.(maimmsg.MessageBase)
	return msg, ok
}

// OutboundToMessageBase 出站封装转为 MessageBase
//
// 对齐 Python SessionMessage.to_maim_message：
//   - user_info / sender_info 是机器人自身，group_info 是接收群
//   - 私聊目标只出现在 receiver_info 与 additional_config
//   - message_segment 恒为 seglist
func OutboundToMessageBase(ctx context.Context, outbound platformio.OutboundMessageEnvelope, routeKey platformio.RouteKey) (maimmsg.MessageBase, error) {
	platform := strings.TrimSpace(routeKey.Platform)
	if platform == "" {
		platform = strings.TrimSpace(outbound.RouteKey.Platform)
	}
	timestamp := outbound.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	now := float64(timestamp.UnixMilli()) / 1000.0

	botUser := outboundBotUserInfo(outbound, routeKey, platform)
	groupInfo := outboundGroupInfo(outbound, platform)
	additionalConfig := outboundAdditionalConfig(outbound)

	info := maimmsg.BaseMessageInfo{
		Platform:         platform,
		MessageID:        strings.TrimSpace(outbound.InternalMessageID),
		Time:             &now,
		GroupInfo:        groupInfo,
		UserInfo:         &botUser,
		AdditionalConfig: additionalConfig,
	}
	info.SenderInfo = &maimmsg.SenderInfo{InfoBase: maimmsg.InfoBase{GroupInfo: groupInfo, UserInfo: &botUser}}
	info.ReceiverInfo = outboundReceiverInfo(outbound, platform, groupInfo)

	segment, err := outboundTopSegment(ctx, outbound)
	if err != nil {
		return maimmsg.MessageBase{}, err
	}
	return maimmsg.MessageBase{
		MessageInfo:    info,
		MessageSegment: segment,
	}, nil
}

// outboundBotUserInfo 出站 user_info 是机器人自身，对齐 Python send_service 的 bot 身份
func outboundBotUserInfo(outbound platformio.OutboundMessageEnvelope, routeKey platformio.RouteKey, platform string) maimmsg.UserInfo {
	info := outbound.MessageInfo.UserInfo
	userID := strings.TrimSpace(info.UserID)
	if userID == "" {
		userID = strings.TrimSpace(routeKey.AccountID)
	}
	return maimmsg.UserInfo{
		Platform:     platform,
		UserID:       userID,
		UserNickname: strings.TrimSpace(info.UserNickname),
		UserCardname: strings.TrimSpace(info.UserCardname),
	}
}

// outboundGroupInfo 群聊时 group_info 表示接收群；私聊保持空
func outboundGroupInfo(outbound platformio.OutboundMessageEnvelope, platform string) *maimmsg.GroupInfo {
	if outbound.ChatType != platformio.ChatTypeGroup {
		return nil
	}
	groupID := ""
	groupName := ""
	if outbound.MessageInfo.GroupInfo != nil {
		groupID = strings.TrimSpace(outbound.MessageInfo.GroupInfo.GroupID)
		groupName = strings.TrimSpace(outbound.MessageInfo.GroupInfo.GroupName)
	}
	if groupID == "" {
		groupID = strings.TrimSpace(outbound.TargetID)
	}
	if groupID == "" {
		return nil
	}
	return &maimmsg.GroupInfo{Platform: platform, GroupID: groupID, GroupName: groupName}
}

// outboundReceiverInfo 对齐 Python to_maim_message 的 receiver_info 构造
func outboundReceiverInfo(outbound platformio.OutboundMessageEnvelope, platform string, groupInfo *maimmsg.GroupInfo) *maimmsg.ReceiverInfo {
	receiver := &maimmsg.ReceiverInfo{InfoBase: maimmsg.InfoBase{GroupInfo: groupInfo}}
	if groupInfo == nil {
		if target := strings.TrimSpace(outbound.TargetID); target != "" {
			receiver.UserInfo = &maimmsg.UserInfo{Platform: platform, UserID: target}
		}
	}
	if receiver.GroupInfo == nil && receiver.UserInfo == nil {
		return nil
	}
	return receiver
}

// outboundAdditionalConfig 透传出站路由元数据，并保证私聊/群聊目标 ID 可被适配器读到
func outboundAdditionalConfig(outbound platformio.OutboundMessageEnvelope) map[string]any {
	source := outbound.MessageInfo.AdditionalConfig
	config := make(map[string]any, len(source)+2)
	for key, value := range source {
		config[key] = value
	}
	target := strings.TrimSpace(outbound.TargetID)
	if target != "" {
		if outbound.ChatType == platformio.ChatTypeGroup {
			if _, exists := config["platform_io_target_group_id"]; !exists {
				config["platform_io_target_group_id"] = target
			}
		} else if _, exists := config["platform_io_target_user_id"]; !exists {
			config["platform_io_target_user_id"] = target
		}
	}
	if len(config) == 0 {
		return nil
	}
	return config
}

func chatMessageFromBase(msg maimmsg.MessageBase, platform string) (platformio.ChatMessage, error) {
	info := msg.MessageInfo
	chatType, targetID, userID, groupID := resolveTarget(info)
	segments, plain := segmentsFromMaimSeg(msg.MessageSegment)
	return platformio.ChatMessage{
		Platform:   platform,
		ChatType:   chatType,
		AccountID:  additionalRouteString(info.AdditionalConfig, "platform_io_account_id", "account_id", "self_id", "bot_account"),
		Scope:      additionalRouteString(info.AdditionalConfig, "platform_io_scope", "route_scope", "adapter_scope", "connection_id"),
		UserID:     userID,
		GroupID:    groupID,
		TargetID:   targetID,
		MessageID:  strings.TrimSpace(info.MessageID),
		Segments:   segments,
		PlainText:  plain,
		ReceivedAt: time.Now(),
		Metadata:   info.AdditionalConfig,
	}, nil
}

func additionalRouteString(config map[string]any, keys ...string) string {
	if len(config) == 0 {
		return ""
	}
	for _, key := range keys {
		value, ok := config[key]
		if !ok || value == nil {
			continue
		}
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
			return text
		}
	}
	return ""
}

func resolveTarget(info maimmsg.BaseMessageInfo) (chatType platformio.ChatType, targetID, userID, groupID string) {
	if info.GroupInfo != nil && strings.TrimSpace(info.GroupInfo.GroupID) != "" {
		groupID = strings.TrimSpace(info.GroupInfo.GroupID)
		return platformio.ChatTypeGroup, groupID, pickUserID(info), groupID
	}
	if info.UserInfo != nil && strings.TrimSpace(info.UserInfo.UserID) != "" {
		userID = strings.TrimSpace(info.UserInfo.UserID)
		return platformio.ChatTypePrivate, userID, userID, ""
	}
	if info.SenderInfo != nil {
		if info.SenderInfo.GroupInfo != nil && strings.TrimSpace(info.SenderInfo.GroupInfo.GroupID) != "" {
			groupID = strings.TrimSpace(info.SenderInfo.GroupInfo.GroupID)
			return platformio.ChatTypeGroup, groupID, pickUserFromSender(info.SenderInfo), groupID
		}
		if info.SenderInfo.UserInfo != nil && strings.TrimSpace(info.SenderInfo.UserInfo.UserID) != "" {
			userID = strings.TrimSpace(info.SenderInfo.UserInfo.UserID)
			return platformio.ChatTypePrivate, userID, userID, ""
		}
	}
	return platformio.ChatTypePrivate, "", pickUserID(info), ""
}

func pickUserID(info maimmsg.BaseMessageInfo) string {
	if info.UserInfo != nil {
		return strings.TrimSpace(info.UserInfo.UserID)
	}
	return pickUserFromSender(info.SenderInfo)
}

func pickUserFromSender(sender *maimmsg.SenderInfo) string {
	if sender == nil || sender.UserInfo == nil {
		return ""
	}
	return strings.TrimSpace(sender.UserInfo.UserID)
}

func segmentsFromMaimSeg(root maimmsg.Seg) ([]platformio.Segment, string) {
	switch root.Type {
	case "text":
		text, err := root.Text()
		if err != nil {
			return nil, ""
		}
		return []platformio.Segment{{Kind: platformio.SegmentText, Text: text}}, text
	case "seglist":
		items, err := root.SegList()
		if err != nil {
			return nil, ""
		}
		var segments []platformio.Segment
		var parts []string
		for _, item := range items {
			sub, plain := segmentsFromMaimSeg(item)
			segments = append(segments, sub...)
			if plain != "" {
				parts = append(parts, plain)
			}
		}
		return segments, strings.Join(parts, "")
	case "at":
		payload := decodeSegPayload(root.Data)
		uid := payloadString(payload, "user_id", "qq", "id")
		if uid == "" {
			return []platformio.Segment{{Kind: platformio.SegmentAt}}, ""
		}
		seg := platformio.Segment{Kind: platformio.SegmentAt, AtUserID: uid}
		if name := payloadString(payload, "name", "nickname", "card"); name != "" {
			seg.Extra = map[string]string{"name": name}
		}
		return []platformio.Segment{seg}, ""
	case "forward":
		nodes, err := forwardNodesFromMaimData(root.Data)
		if err != nil {
			return nil, ""
		}
		return []platformio.Segment{{Kind: platformio.SegmentForward, ForwardNodes: nodes}}, ""
	case "image", "emoji", "voice", "video", "record", "file", "face", "reply", "json", "music":
		seg := mediaSegmentFromMaimSeg(platformio.SegmentKind(root.Type), root.Data)
		return []platformio.Segment{seg}, seg.Text
	default:
		seg := mediaSegmentFromMaimSeg(platformio.SegmentKind(root.Type), root.Data)
		return []platformio.Segment{seg}, seg.Text
	}
}

func parseAtUserIDFromSeg(root maimmsg.Seg) string {
	payload := decodeSegPayload(root.Data)
	if id := payloadString(payload, "user_id", "qq", "id"); id != "" {
		return id
	}
	return ""
}

func joinSegmentText(segments []platformio.Segment) string {
	var b strings.Builder
	for _, seg := range segments {
		if seg.Text != "" {
			b.WriteString(seg.Text)
		}
	}
	return b.String()
}
