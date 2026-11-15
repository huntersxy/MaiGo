package chatwiring

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"

	memory "MaiGo/internal/common/database"
)

// MessageIDPort 把消息编号回填接到 memory 层
// 对齐 Python utils_message.py:337-385 的 update_message_id
type MessageIDPort struct {
	DB *gorm.DB
}

// newMessageIDPort 创建消息编号回填端口；DB 为 nil 时返回 nil
func newMessageIDPort(db *gorm.DB) *MessageIDPort {
	if db == nil {
		return nil
	}
	return &MessageIDPort{DB: db}
}

// UpdateMessageID 把旧编号替换为新编号并同步引用
func (p *MessageIDPort) UpdateMessageID(ctx context.Context, oldMessageID, newMessageID string) error {
	oldID := strings.TrimSpace(oldMessageID)
	newID := strings.TrimSpace(newMessageID)
	if oldID == "" || newID == "" {
		return fmt.Errorf("消息编号不能为空")
	}
	if oldID == newID {
		return nil
	}
	if p == nil || p.DB == nil {
		return fmt.Errorf("消息编号回填未接线")
	}
	return p.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 对齐 Python：新 ID 已存在则跳过，避免撞已有 external_message_id
		var existing int64
		if err := tx.Model(&memory.MessageLog{}).Where("external_message_id = ?", newID).Limit(1).Count(&existing).Error; err != nil {
			return fmt.Errorf("检查消息编号失败: %w", err)
		}
		if existing > 0 {
			zap.L().Warn("消息 ID 回填时发现真实 ID 已存在，已跳过更新",
				zap.String("old", oldID), zap.String("new", newID))
			return nil
		}
		sourceQuery := tx.Model(&memory.MessageLog{}).Where("external_message_id = ?", oldID)
		if numeric, err := strconv.ParseInt(oldID, 10, 64); err == nil {
			sourceQuery = tx.Model(&memory.MessageLog{}).
				Where("external_message_id = ? OR message_id = ?", oldID, numeric)
		}
		if err := sourceQuery.Update("external_message_id", newID).Error; err != nil {
			return fmt.Errorf("更新消息编号失败: %w", err)
		}
		// 引用消息：reply_to 指向旧编号的改指新编号（仅当新旧都是数字）
		oldNumeric, oldErr := strconv.ParseInt(oldID, 10, 64)
		newNumeric, newErr := strconv.ParseInt(newID, 10, 64)
		if oldErr == nil && newErr == nil {
			if err := tx.Model(&memory.MessageLog{}).
				Where("reply_to = ?", oldNumeric).
				Update("reply_to", newNumeric).Error; err != nil {
				return fmt.Errorf("更新引用编号失败: %w", err)
			}
		}
		return nil
	})
}
