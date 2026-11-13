package chatsession

import (
	"context"
	"strings"

	"maibot-go/internal/common/database"
)

// ListRecentMessages 按已入库 MaiBot session_id 列出 mai_messages 最近消息
func ListRecentMessages(ctx context.Context, mem *memory.Manager, sessionID string, limit, offset int) ([]memory.MessageLog, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || mem == nil {
		return nil, nil
	}
	return mem.ListRecentMessagesBySessionID(ctx, sessionID, limit, offset)
}
