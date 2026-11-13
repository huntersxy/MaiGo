package memory

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ListRecentMessagesBySessionID 按 MaiBot session_id 倒序取最近消息，再反转为时间正序
func (m *Manager) ListRecentMessagesBySessionID(ctx context.Context, sessionID string, limit, offset int) ([]MessageLog, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	var items []MessageLog
	q := m.db.WithContext(ctx).Where("session_id=?", sessionID).
		Where(NoticeExternalMessageIDPredicate("external_message_id")).Order("timestamp DESC, id DESC").Limit(limit)
	if offset > 0 {
		q = q.Offset(offset)
	}
	if err := q.Find(&items).Error; err != nil {
		return nil, err
	}
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items, nil
}

// ListMessagesBySessionIDTimeRange 按 session_id 与时间窗查询消息，结果按时间正序
func (m *Manager) ListMessagesBySessionIDTimeRange(ctx context.Context, sessionID string, start, end time.Time, limit int) ([]MessageLog, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	var items []MessageLog
	q := m.db.WithContext(ctx).Where("session_id=?", sessionID).
		Where(NoticeExternalMessageIDPredicate("external_message_id"))
	if !start.IsZero() {
		q = q.Where("timestamp >= ?", start)
	}
	if !end.IsZero() {
		q = q.Where("timestamp <= ?", end)
	}
	q = q.Order("timestamp DESC, id DESC").Limit(limit)
	if err := q.Find(&items).Error; err != nil {
		return nil, err
	}
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items, nil
}

// AttachMessageLogSessionID 为已落库行补写 session_id（冲突行或历史空值）
func (m *Manager) AttachMessageLogSessionID(ctx context.Context, logID uint, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if logID == 0 || sessionID == "" {
		return nil
	}
	return m.db.WithContext(ctx).Model(&MessageLog{}).
		Where("id=? AND (session_id IS NULL OR session_id='')", logID).
		Update("session_id", sessionID).Error
}

// AttachMessageLogSessionIDTx 事务内补写 session_id
func AttachMessageLogSessionIDTx(tx *gorm.DB, logID uint, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if logID == 0 || sessionID == "" {
		return nil
	}
	return tx.Model(&MessageLog{}).
		Where("id=? AND (session_id IS NULL OR session_id='')", logID).
		Update("session_id", sessionID).Error
}
