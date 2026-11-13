package stats

import (
	"strings"

	"gorm.io/gorm"
)

// NewDetailedProvider 按数据库方言选择 detailed 统计查询器
func NewDetailedProvider(db *gorm.DB) DetailedProvider {
	if db != nil && db.Dialector != nil && strings.EqualFold(strings.TrimSpace(db.Dialector.Name()), "sqlite") {
		return NewSQLiteDetailedProvider(db)
	}
	return NewPostgresDetailedProvider(db)
}
