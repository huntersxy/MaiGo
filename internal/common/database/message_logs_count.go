package memory

// 会话消息计数与按消息 ID 回查，对齐 Python common/message_repository.py 的 count_messages / find_messages

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// CountMessagesBySessionID 统计会话内消息数，对齐 count_messages(session_id=..., end_time=...)
// Python 在异常时返回 0；这里把错误交给调用方，由写回 worker 记录 warning 后跳过，用户可见行为一致
func (m *Manager) CountMessagesBySessionID(ctx context.Context, sessionID string, endTime *time.Time) (int64, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, nil
	}
	var count int64
	query := m.db.WithContext(ctx).Model(&MessageLog{}).
		Where("session_id=?", sessionID).
		Where(NoticeExternalMessageIDPredicate("external_message_id"))
	if endTime != nil && !endTime.IsZero() {
		query = query.Where("timestamp <= ?", *endTime)
	}
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// FindMessageBySessionIDAndMessageID 按会话与消息 ID 回查单条消息，对齐 find_messages(limit=1)
// limit_mode 默认 latest，因此取时间最新的一条；外部 ID 与 OneBot 数字 ID 都参与匹配
func (m *Manager) FindMessageBySessionIDAndMessageID(ctx context.Context, sessionID string, messageID string) (*MessageLog, error) {
	sessionID = strings.TrimSpace(sessionID)
	messageID = strings.TrimSpace(messageID)
	if sessionID == "" || messageID == "" {
		return nil, nil
	}
	var rows []MessageLog
	query := m.db.WithContext(ctx).
		Where("session_id=?", sessionID).
		Where(NoticeExternalMessageIDPredicate("external_message_id")).
		Where("(external_message_id = ? OR CAST(message_id AS TEXT) = ?)", messageID, messageID)
	if err := query.Order("timestamp DESC, id DESC").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// MessageLogMessageID 返回消息的对外 ID，优先外部字符串 ID，其次 OneBot 数字 ID
func MessageLogMessageID(record *MessageLog) string {
	if record == nil {
		return ""
	}
	if external := strings.TrimSpace(record.ExternalMessageID); external != "" {
		return external
	}
	if record.MessageID != 0 {
		return strconv.FormatInt(record.MessageID, 10)
	}
	return ""
}
