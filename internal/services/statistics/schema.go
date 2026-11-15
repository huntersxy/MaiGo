package stats

import (
	"context"

	statschema "MaiGo/internal/services/statistics/schema"

	"gorm.io/gorm"
)

// EnsureSchema 创建统计汇总表并补齐 model_call_hourly 的 token 拆分列
func EnsureSchema(ctx context.Context, db *gorm.DB) error {
	return statschema.Ensure(ctx, db)
}
