package maimmsg

import (
	"strconv"
	"strings"
	"time"
)

// MessageConverter 在新 API 信封与旧版 MessageBase 之间转换，对齐 MaiBot MessageConverter
type MessageConverter struct{}

// ToAPIReceive 将平台入站 MessageBase 转为新 API 接收信封
func (MessageConverter) ToAPIReceive(msg MessageBase, apiKey, platform string) APIMessageBase {
	return convertToAPI(msg, apiKey, platform, false)
}

// ToAPISend 将出站 MessageBase 转为新 API 发送信封
func (MessageConverter) ToAPISend(msg MessageBase, apiKey, platform string) APIMessageBase {
	return convertToAPI(msg, apiKey, platform, true)
}

// FromAPIReceive 将新 API 接收信封还原为旧版 MessageBase
func (MessageConverter) FromAPIReceive(msg APIMessageBase) MessageBase {
	return convertFromAPI(msg, false)
}

// FromAPISend 将新 API 发送信封还原为旧版 MessageBase
func (MessageConverter) FromAPISend(msg APIMessageBase) MessageBase {
	return convertFromAPI(msg, true)
}

func convertToAPI(msg MessageBase, apiKey, platform string, outgoing bool) APIMessageBase {
	legacy := msg.MessageInfo
	targetPlatform := strings.TrimSpace(platform)
	if targetPlatform == "" {
		targetPlatform = strings.TrimSpace(legacy.Platform)
	}
	if targetPlatform == "" {
		targetPlatform = "unknown"
	}

	groupInfo, userInfo := extractGroupUserInfo(legacy)
	normalizeGroupUserPlatform(groupInfo, userInfo, targetPlatform)

	messageID := strings.TrimSpace(legacy.MessageID)
	now := time.Now()
	if messageID == "" {
		messageID = strconv.FormatInt(now.UnixMilli(), 10)
	}
	messageTime := float64(now.UnixMilli()) / 1000.0
	if legacy.Time != nil && *legacy.Time > 0 {
		messageTime = *legacy.Time
	}

	info := APIBaseMessageInfo{
		Platform:         targetPlatform,
		MessageID:        messageID,
		Time:             messageTime,
		FormatInfo:       legacy.FormatInfo,
		TemplateInfo:     legacy.TemplateInfo,
		AdditionalConfig: legacy.AdditionalConfig,
	}
	infoBase := InfoBase{GroupInfo: groupInfo, UserInfo: userInfo}
	if outgoing {
		info.ReceiverInfo = &ReceiverInfo{InfoBase: infoBase}
	} else {
		info.SenderInfo = &SenderInfo{InfoBase: infoBase}
	}

	return APIMessageBase{
		MessageInfo:    info,
		MessageSegment: msg.MessageSegment,
		MessageDim:     MessageDim{APIKey: strings.TrimSpace(apiKey), Platform: targetPlatform},
	}
}

func convertFromAPI(msg APIMessageBase, outgoing bool) MessageBase {
	info := msg.MessageInfo
	legacy := BaseMessageInfo{
		Platform:         strings.TrimSpace(info.Platform),
		MessageID:        strings.TrimSpace(info.MessageID),
		FormatInfo:       info.FormatInfo,
		TemplateInfo:     info.TemplateInfo,
		AdditionalConfig: info.AdditionalConfig,
	}
	if info.Time > 0 {
		value := info.Time
		legacy.Time = &value
	}
	var groupInfo *GroupInfo
	var userInfo *UserInfo
	if outgoing {
		legacy.ReceiverInfo = info.ReceiverInfo
		if info.ReceiverInfo != nil {
			groupInfo = info.ReceiverInfo.GroupInfo
			userInfo = info.ReceiverInfo.UserInfo
		}
	} else {
		legacy.SenderInfo = info.SenderInfo
		if info.SenderInfo != nil {
			groupInfo = info.SenderInfo.GroupInfo
			userInfo = info.SenderInfo.UserInfo
		}
	}
	// from_api_receive/send 会同时保留 sender_info/receiver_info 副本
	if info.SenderInfo != nil {
		legacy.SenderInfo = info.SenderInfo
	}
	if info.ReceiverInfo != nil {
		legacy.ReceiverInfo = info.ReceiverInfo
	}
	legacy.GroupInfo = groupInfo
	legacy.UserInfo = userInfo
	return MessageBase{
		MessageInfo:    legacy,
		MessageSegment: msg.MessageSegment,
	}
}

// extractGroupUserInfo 按直接字段 > sender_info > receiver_info 的优先级提取群/用户信息
func extractGroupUserInfo(info BaseMessageInfo) (*GroupInfo, *UserInfo) {
	if info.GroupInfo != nil || info.UserInfo != nil {
		return info.GroupInfo, info.UserInfo
	}
	if info.SenderInfo != nil && (info.SenderInfo.GroupInfo != nil || info.SenderInfo.UserInfo != nil) {
		return info.SenderInfo.GroupInfo, info.SenderInfo.UserInfo
	}
	if info.ReceiverInfo != nil {
		return info.ReceiverInfo.GroupInfo, info.ReceiverInfo.UserInfo
	}
	return nil, nil
}

func normalizeGroupUserPlatform(groupInfo *GroupInfo, userInfo *UserInfo, platform string) {
	if groupInfo != nil && strings.TrimSpace(groupInfo.Platform) == "" {
		groupInfo.Platform = platform
	}
	if userInfo != nil && strings.TrimSpace(userInfo.Platform) == "" {
		userInfo.Platform = platform
	}
}
