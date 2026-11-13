package memory

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// UpdateMessageID 把旧消息编号替换为新编号，并同步引用它的 reply_to
// 对齐 Python utils_message.py:337-385 的 update_message_id：
// 先按 external_message_id 或 message_id 找到源消息，改其编号，
// 再把 reply_to 指向旧编号的消息改指新编号
func (m *Manager) UpdateMessageID(ctx context.Context, oldMessageID, newMessageID string) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("数据库未初始化")
	}
	oldID := strings.TrimSpace(oldMessageID)
	newID := strings.TrimSpace(newMessageID)
	if oldID == "" || newID == "" {
		return fmt.Errorf("消息编号不能为空")
	}
	if oldID == newID {
		return nil
	}
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 对齐 Python：新 ID 已存在则跳过，避免撞已有编号
		var existing int64
		if err := tx.Model(&MessageLog{}).Where("external_message_id = ?", newID).Limit(1).Count(&existing).Error; err != nil {
			return fmt.Errorf("检查消息编号失败: %w", err)
		}
		if existing > 0 {
			zap.L().Warn("消息 ID 回填时发现真实 ID 已存在，已跳过更新",
				zap.String("old", oldID), zap.String("new", newID))
			return nil
		}
		// 源消息：按 external_message_id 或 message_id 匹配
		sourceQuery := tx.Model(&MessageLog{}).Where("external_message_id = ?", oldID)
		if numeric, err := strconv.ParseInt(oldID, 10, 64); err == nil {
			sourceQuery = tx.Model(&MessageLog{}).
				Where("external_message_id = ? OR message_id = ?", oldID, numeric)
		}
		if err := sourceQuery.Update("external_message_id", newID).Error; err != nil {
			return fmt.Errorf("更新消息编号失败: %w", err)
		}
		// 引用消息：reply_to 指向旧编号的改指新编号
		if err := tx.Model(&MessageLog{}).
			Where("reply_to = ?", oldID).
			Update("reply_to", newID).Error; err != nil {
			return fmt.Errorf("更新引用编号失败: %w", err)
		}
		return nil
	})
}
