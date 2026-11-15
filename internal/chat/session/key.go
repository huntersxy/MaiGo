package chatsession

import (
	"fmt"
	"strconv"
	"strings"

	"MaiGo/internal/common/database"
)

// ConversationKey 生成 agent / mai_messages 用的会话作用域键（非 MaiBot session_id）
func ConversationKey(kind string, targetID int64) string {
	return kind + ":" + strconv.FormatInt(targetID, 10)
}

// ParseConversationKey 解析 group:ID / private:ID
func ParseConversationKey(raw string) (kind string, targetID int64, err error) {
	kind, rawTarget, ok := strings.Cut(strings.TrimSpace(raw), ":")
	kind = strings.TrimSpace(kind)
	if !ok || (kind != memory.ConversationKindGroup && kind != memory.ConversationKindPrivate) {
		return "", 0, fmt.Errorf("conversation key 格式应为 group:ID 或 private:ID")
	}
	targetID, err = strconv.ParseInt(strings.TrimSpace(rawTarget), 10, 64)
	if err != nil || targetID <= 0 {
		return "", 0, fmt.Errorf("conversation key 中的目标 ID 无效")
	}
	return kind, targetID, nil
}

// IsConversationKey 判断字符串是否为 Go 侧 conversation key（非 MD5 session_id）
func IsConversationKey(raw string) bool {
	_, _, err := ParseConversationKey(raw)
	return err == nil
}

// ChatTypeFromKind 将 memory conversation kind 转为 MaiBot chat_type 字符串
func ChatTypeFromKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case memory.ConversationKindGroup:
		return memory.ConversationKindGroup
	case memory.ConversationKindPrivate:
		return memory.ConversationKindPrivate
	default:
		return ""
	}
}
