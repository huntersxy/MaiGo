package internalchat

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
	"strings"
)

// WebUIPlatform 网页聊天室虚拟身份使用的平台名，对齐 Python WEBUI_CHAT_PLATFORM
const WebUIPlatform = "webui"

// VirtualGroupPrefix WebUI 虚拟群 ID 前缀，对齐 Python VIRTUAL_GROUP_ID_PREFIX
const VirtualGroupPrefix = "webui_virtual_group_"

// DefaultVirtualGroupName 虚拟群默认名称
const DefaultVirtualGroupName = "WebUI虚拟群聊"

// 虚拟目标 ID 使用高位段，避免与真实 QQ 号/群号冲突
const (
	virtualGroupBase int64 = 1 << 62
	virtualUserBase  int64 = 1 << 61
)

// VirtualIdentity 一次 WebUI 会话的虚拟身份配置，对齐 Python VirtualIdentityConfig
type VirtualIdentity struct {
	Enabled      bool
	Platform     string
	PersonID     string
	UserID       string
	UserNickname string
	GroupID      string
	GroupName    string
}

// IsVirtualGroupID 判断群 ID 是否为 WebUI 虚拟群
func IsVirtualGroupID(groupID string) bool {
	return strings.HasPrefix(strings.TrimSpace(groupID), VirtualGroupPrefix)
}

// VirtualTargetID 把虚拟群 ID 稳定映射为 Agent 使用的 int64 目标
func VirtualTargetID(groupID string) int64 {
	trimmed := strings.TrimSpace(groupID)
	if trimmed == "" {
		return 0
	}
	sum := sha256.Sum256([]byte(trimmed))
	value := int64(binary.BigEndian.Uint64(sum[:8]) & uint64(virtualGroupBase-1))
	return virtualGroupBase | value
}

// VirtualUserNumericID 把虚拟身份的 user_id 转成 Agent 可用的 int64
func VirtualUserNumericID(userID string) int64 {
	trimmed := strings.TrimSpace(userID)
	if trimmed == "" {
		return 0
	}
	if value, err := strconv.ParseInt(trimmed, 10, 64); err == nil && value > 0 {
		return value
	}
	sum := sha256.Sum256([]byte(trimmed))
	value := int64(binary.BigEndian.Uint64(sum[:8]) & uint64(virtualUserBase-1))
	return virtualUserBase | value
}

// ChannelKey 返回虚拟身份绑定的逻辑频道键
func (v *VirtualIdentity) ChannelKey() string {
	if v == nil || !v.Enabled {
		return OwnerChannelKey()
	}
	return GroupChannel(v.GroupID)
}

// TargetID 返回虚拟群在 Agent 中的数值目标
func (v *VirtualIdentity) TargetID() int64 {
	if v == nil || !v.Enabled {
		return OwnerNumericID
	}
	return VirtualTargetID(v.GroupID)
}

// NumericUserID 返回虚拟用户 ID 的数值形式
func (v *VirtualIdentity) NumericUserID() int64 {
	if v == nil || !v.Enabled {
		return OwnerNumericID
	}
	if id := VirtualUserNumericID(v.UserID); id > 0 {
		return id
	}
	return OwnerNumericID
}

// ConversationKind 返回虚拟身份对应的会话类型
func (v *VirtualIdentity) ConversationKind() string {
	if v == nil || !v.Enabled {
		return "private"
	}
	return "group"
}

// SenderName 返回虚拟身份展示昵称
func (v *VirtualIdentity) SenderName(fallback string) string {
	if v != nil && v.Enabled && strings.TrimSpace(v.UserNickname) != "" {
		return strings.TrimSpace(v.UserNickname)
	}
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	return OwnerUserID
}

// SenderUserID 返回虚拟身份发送者 ID
func (v *VirtualIdentity) SenderUserID(fallback string) string {
	if v != nil && v.Enabled && strings.TrimSpace(v.UserID) != "" {
		return strings.TrimSpace(v.UserID)
	}
	return fallback
}

// PlatformName 返回消息平台，虚拟身份使用 PersonInfo 的平台
func (v *VirtualIdentity) PlatformName() string {
	if v != nil && v.Enabled && strings.TrimSpace(v.Platform) != "" {
		return strings.TrimSpace(v.Platform)
	}
	return Platform
}
