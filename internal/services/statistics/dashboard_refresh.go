package stats

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// defaultDashboardCacheHours 对齐 Python DEFAULT_DASHBOARD_CACHE_HOURS
var defaultDashboardCacheHours = []int{24, 168, 720}

// writeDashboardStatisticsCache 计算并写入 24/168/720 三档 WebUI 仪表盘缓存
//
// 对齐 Python refresh_dashboard_statistics_cache；调用方需先刷新统计聚合与小时用量汇总
func writeDashboardStatisticsCache(ctx context.Context, db *gorm.DB, instanceDir string, now time.Time, usageHourly UsageHourlyState) error {
	for _, hours := range defaultDashboardCacheHours {
		data, err := computeDashboardStatisticsAt(ctx, db, hours, now, usageHourly)
		if err != nil {
			return fmt.Errorf("计算 %d 小时仪表盘统计失败: %w", hours, err)
		}
		if err := updateDashboardStatisticsCacheEntry(instanceDir, hours, data, now); err != nil {
			return fmt.Errorf("写入 %d 小时仪表盘缓存失败: %w", hours, err)
		}
	}
	return nil
}
