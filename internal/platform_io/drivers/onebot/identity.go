package onebot

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"
)

// SyntheticIdentityID 为没有数值 ID 的外部平台身份生成稳定正整数代理键。
// 原始字符串 ID 会同时保存在消息模型的 External* 字段，代理键只用于复用现有
// int64 会话索引与数据库列；相同 platform/account/scope/role/原始 ID 总是得到同一值。
func SyntheticIdentityID(parts ...string) int64 {
	hasher := sha256.New()
	for _, part := range parts {
		part = strings.TrimSpace(part)
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		_, _ = hasher.Write(size[:])
		_, _ = hasher.Write([]byte(part))
	}
	sum := hasher.Sum(nil)
	value := binary.BigEndian.Uint64(sum[:8]) & (1<<63 - 1)
	if value == 0 {
		value = 1
	}
	return int64(value)
}

// HasExternalIdentity 判断消息是否携带非数值平台身份
func (m *ConversationMessage) HasExternalIdentity() bool {
	if m == nil {
		return false
	}
	return strings.TrimSpace(m.ExternalTargetID) != "" ||
		strings.TrimSpace(m.ExternalUserID) != "" ||
		strings.TrimSpace(m.ExternalMessageID) != ""
}
