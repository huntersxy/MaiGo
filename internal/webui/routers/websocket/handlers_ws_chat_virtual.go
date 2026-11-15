package websocket

import (
	"context"
	"strings"
	"time"

	"MaiGo/internal/core/internal_chat"
	"MaiGo/internal/person_info"
)

// resolveVirtualIdentity 按 session.open 参数解析虚拟身份，失败时返回 nil
func (s *unifiedWSSession) resolveVirtualIdentity(data map[string]any) *internalchat.VirtualIdentity {
	if data == nil {
		return nil
	}
	platform := strings.TrimSpace(wsAnyString(data["platform"]))
	personID := strings.TrimSpace(wsAnyString(data["person_id"]))
	if platform == "" || personID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	profile, err := personinfo.DefaultRegistry().GetProfile(ctx, personID)
	if err != nil || profile == nil {
		return nil
	}
	groupID := strings.TrimSpace(wsAnyString(data["group_id"]))
	if groupID == "" {
		groupID = internalchat.VirtualGroupPrefix + platform + "_" + strings.TrimSpace(profile.UserID)
	}
	groupName := strings.TrimSpace(wsAnyString(data["group_name"]))
	if groupName == "" {
		groupName = internalchat.DefaultVirtualGroupName
	}
	nickname := strings.TrimSpace(profile.PersonName)
	if nickname == "" {
		nickname = strings.TrimSpace(profile.Nickname)
	}
	if nickname == "" {
		nickname = strings.TrimSpace(profile.UserID)
	}
	return &internalchat.VirtualIdentity{
		Enabled:      true,
		Platform:     firstNonEmpty(strings.TrimSpace(profile.Platform), platform),
		PersonID:     personID,
		UserID:       strings.TrimSpace(profile.UserID),
		UserNickname: nickname,
		GroupID:      groupID,
		GroupName:    groupName,
	}
}

// chatChannelKey 返回当前会话绑定的频道键
func chatChannelKey(virtual *internalchat.VirtualIdentity) string {
	if virtual == nil || !virtual.Enabled {
		return internalchat.OwnerChannelKey()
	}
	return virtual.ChannelKey()
}

// chatHistoryTarget 返回历史查询使用的平台、目标与会话类型
//
// chat_sessions.user_id 存 Python 的 INTERNAL_CHAT_USER_ID="owner"，
// 与 handleChatMessage 落库时写入的 ExternalUserID 保持一致。
func chatHistoryTarget(virtual *internalchat.VirtualIdentity) (platform, targetID, kind string) {
	if virtual != nil && virtual.Enabled {
		return virtual.PlatformName(), virtual.GroupID, "group"
	}
	return internalchat.Platform, internalchat.OwnerUserID, "private"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
