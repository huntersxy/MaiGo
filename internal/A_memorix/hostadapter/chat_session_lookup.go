package hostadapter

// 检索过滤使用：按会话 ID 回查群号与用户号

import (
	"context"
	"strings"

	"gorm.io/gorm"

	"MaiGo/internal/chat/session"
)

// NewChatSessionLookup 用 chatsession 表把 stream/session ID 解析为 group/user 身份
func NewChatSessionLookup(db *gorm.DB) func(streamID string) (groupID string, userID string) {
	if db == nil {
		return nil
	}
	manager := chatsession.NewManager(db)
	return func(streamID string) (string, string) {
		token := strings.TrimSpace(streamID)
		if token == "" {
			return "", ""
		}
		record, err := manager.GetExistingBySessionID(context.Background(), token)
		if err != nil || record == nil {
			return "", ""
		}
		groupID, userID := "", ""
		if record.GroupID != nil {
			groupID = strings.TrimSpace(*record.GroupID)
		}
		if record.UserID != nil {
			userID = strings.TrimSpace(*record.UserID)
		}
		return groupID, userID
	}
}
