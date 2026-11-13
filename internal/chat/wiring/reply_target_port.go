package chatwiring

import (
	"context"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"maibot-go/internal/chat/message_receive"
	"maibot-go/internal/common/database"
)

// replyTargetResolver 按消息 ID 回查被引用消息，供 WebUI 实时推送补全引用卡片
type replyTargetResolver struct {
	db *gorm.DB
}

// NewReplyTargetResolver 构造基于 mai_messages 的引用目标查询器
func NewReplyTargetResolver(db *gorm.DB) messagereceive.ReplyTargetResolver {
	if db == nil {
		return nil
	}
	return &replyTargetResolver{db: db}
}

// LookupReplyTarget 按会话与消息 ID 查询被引用消息的展示信息
func (r *replyTargetResolver) LookupReplyTarget(ctx context.Context, sessionID, messageID string) (messagereceive.ReplyTargetInfo, bool) {
	numeric, err := strconv.ParseInt(strings.TrimSpace(messageID), 10, 64)
	if err != nil || numeric == 0 {
		return messagereceive.ReplyTargetInfo{}, false
	}
	var row memory.MessageLog
	query := r.db.WithContext(ctx).Where("message_id=?", numeric).
		Where(memory.NoticeExternalMessageIDPredicate("external_message_id"))
	if session := strings.TrimSpace(sessionID); session != "" {
		query = query.Where("session_id=?", session)
	}
	if err := query.First(&row).Error; err != nil {
		return messagereceive.ReplyTargetInfo{}, false
	}
	return messagereceive.ReplyTargetInfo{
		Content:  strings.TrimSpace(row.ProcessedPlainText),
		SenderID: strconv.FormatInt(row.UserID, 10),
		Nickname: row.UserNickname,
		Cardname: row.UserCardname,
	}, true
}
