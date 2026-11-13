package memory

import (
	"fmt"

	"gorm.io/gorm"
)

// EnsurePlatformIdentitySchema 为既有 v17 数据库补齐多平台身份字段。
// v17 建库脚本已包含这些列；该函数只做幂等 ADD COLUMN，兼容旧库原地升级。
func EnsurePlatformIdentitySchema(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}
	if !db.Migrator().HasTable("mai_messages") {
		return nil
	}
	columns := []struct {
		name string
		sql  string
	}{
		{"external_message_id", "ALTER TABLE mai_messages ADD COLUMN external_message_id TEXT NOT NULL DEFAULT ''"},
		{"external_target_id", "ALTER TABLE mai_messages ADD COLUMN external_target_id TEXT NOT NULL DEFAULT ''"},
		{"external_user_id", "ALTER TABLE mai_messages ADD COLUMN external_user_id TEXT NOT NULL DEFAULT ''"},
		{"account_id", "ALTER TABLE mai_messages ADD COLUMN account_id TEXT NOT NULL DEFAULT ''"},
		{"scope", "ALTER TABLE mai_messages ADD COLUMN scope TEXT NOT NULL DEFAULT ''"},
	}
	// SQLite 不支持 ADD COLUMN IF NOT EXISTS；先 HasColumn 再执行通用语句
	for _, column := range columns {
		if db.Migrator().HasColumn("mai_messages", column.name) {
			continue
		}
		if err := db.Exec(column.sql).Error; err != nil {
			return fmt.Errorf("补齐 mai_messages.%s 失败: %w", column.name, err)
		}
	}
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_message_logs_platform_identity ON mai_messages(platform,account_id,external_target_id)",
		"CREATE INDEX IF NOT EXISTS idx_message_logs_external_message ON mai_messages(platform,external_message_id)",
	}
	for _, statement := range indexes {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("补齐 mai_messages 多平台索引失败: %w", err)
		}
	}
	return nil
}
