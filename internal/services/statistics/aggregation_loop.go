package stats

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// DefaultAggregationInterval 对齐 MaiBot StatisticOutputTask.RUN_INTERVAL_SECONDS（15 分钟）
const DefaultAggregationInterval = 15 * time.Minute

// DefaultAggregationTimeout 单次增量汇总预算，避免慢查询无限占用后台协程
const DefaultAggregationTimeout = time.Minute

// AggregationRefresherOptions 控制统计汇总后台刷新
type AggregationRefresherOptions struct {
	// Interval 为两次汇总之间的间隔，<=0 使用 15 分钟
	Interval time.Duration
	// Timeout 为单次汇总预算，<=0 使用 1 分钟
	Timeout time.Duration
	// Logger 为空时使用 zap.L()
	Logger *zap.Logger
	// RunImmediately 关闭后只在首个 tick 后执行；默认启动即刷新，对齐 Python wait_before_start=0
	SkipImmediate bool
	// InstanceDir 为 WebUI 仪表盘缓存所在实例目录；为空时沿用遥测存储默认路径
	InstanceDir string
}

// StartAggregationRefresher 启动 statistics_message_hourly/tool/model 的后台增量汇总
// 返回幂等的停止函数；停止时取消当前汇总并等待协程退出
func StartAggregationRefresher(ctx context.Context, db *gorm.DB, opts AggregationRefresherOptions) func() {
	if db == nil {
		return func() {}
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultAggregationInterval
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultAggregationTimeout
	}
	log := opts.Logger
	if log == nil {
		log = zap.L()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	refresh := func() {
		refreshCtx, refreshCancel := context.WithTimeout(runCtx, timeout)
		defer refreshCancel()
		startedAt := time.Now()
		if err := RefreshStatisticsAggregates(refreshCtx, db); err != nil {
			log.Warn("统计汇总增量刷新失败，等待下一轮重试", zap.Error(err), zap.Duration("elapsed", time.Since(startedAt)))
			return
		}
		usageHourly := RefreshUsageHourly(refreshCtx, db, usageHourlyTimeBudget)
		if err := writeDashboardStatisticsCache(refreshCtx, db, opts.InstanceDir, time.Now(), usageHourly); err != nil {
			log.Warn("刷新 WebUI 仪表盘缓存失败，等待下一轮重试", zap.Error(err), zap.Duration("elapsed", time.Since(startedAt)))
			return
		}
		log.Debug("统计汇总增量刷新与仪表盘缓存完成", zap.Duration("elapsed", time.Since(startedAt)))
	}

	go func() {
		defer close(done)
		if !opts.SkipImmediate {
			refresh()
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}
