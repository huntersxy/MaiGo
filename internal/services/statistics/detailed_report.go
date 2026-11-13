package stats

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// DetailedReportOptions 控制 15 分钟统计报告任务
type DetailedReportOptions struct {
	DB          *gorm.DB
	Provider    DetailedProvider
	InstanceDir string
	ReportPath  string
	Interval    time.Duration
	Now         func() time.Time
}

// DetailedReportInterval 对齐 Python StatisticOutputTask.RUN_INTERVAL_SECONDS
const DetailedReportInterval = 15 * time.Minute

// ErrDetailedReporterStarted 表示报告任务已在运行
var ErrDetailedReporterStarted = fmt.Errorf("详细统计报告任务已启动")

// DetailedReporter 定时生成 HTML 报告并发布详细统计快照
type DetailedReporter struct {
	options DetailedReportOptions

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
	runMu   sync.Mutex
}

// NewDetailedReporter 创建统计报告任务
func NewDetailedReporter(options DetailedReportOptions) *DetailedReporter {
	if options.Interval <= 0 {
		options.Interval = DetailedReportInterval
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &DetailedReporter{options: options}
}

// Start 立即执行一次，之后按 Interval 重复；重复启动返回错误
func (r *DetailedReporter) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return ErrDetailedReporterStarted
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.done = make(chan struct{})
	r.running = true
	go r.runLoop(runCtx, r.done)
	return nil
}

// Stop 停止定时任务并等待当前一轮结束
func (r *DetailedReporter) Stop() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	cancel := r.cancel
	done := r.done
	r.running = false
	r.mu.Unlock()
	cancel()
	<-done
}

func (r *DetailedReporter) runLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	if _, err := r.RunOnce(ctx); err != nil {
		r.logger().Error("生成详细统计报告失败", zap.Error(err))
	}
	ticker := time.NewTicker(r.options.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := r.RunOnce(ctx); err != nil {
				r.logger().Error("生成详细统计报告失败", zap.Error(err))
			}
		}
	}
}

// RunOnce 构建快照、原子写 HTML 与快照文件
func (r *DetailedReporter) RunOnce(ctx context.Context) (DetailedStatisticsData, error) {
	r.runMu.Lock()
	defer r.runMu.Unlock()

	now := r.options.Now().In(time.Local)
	provider := r.options.Provider
	if provider == nil {
		if r.options.DB == nil {
			return emptyDetailedStatistics(now), fmt.Errorf("详细统计数据库未初始化")
		}
		if err := EnsureSchema(ctx, r.options.DB); err != nil {
			return emptyDetailedStatistics(now), fmt.Errorf("初始化统计表失败: %w", err)
		}
		provider = NewDetailedProvider(r.options.DB)
	}
	snapshot, err := BuildDetailedStatistics(ctx, provider, now)
	if err != nil {
		return emptyDetailedStatistics(now), err
	}
	htmlBytes, err := RenderDetailedHTML(snapshot)
	if err != nil {
		return emptyDetailedStatistics(now), err
	}
	reportPath := resolveDetailedReportPath(r.options.InstanceDir, r.options.ReportPath)
	if err := writeAtomicFile(reportPath, htmlBytes, 0o644); err != nil {
		return emptyDetailedStatistics(now), fmt.Errorf("写入详细统计 HTML 报告失败: %w", err)
	}
	if err := StoreDetailedStatisticsSnapshot(r.options.InstanceDir, snapshot); err != nil {
		return emptyDetailedStatistics(now), err
	}
	_ = cleanupStaleDetailedTemps(filepath.Dir(reportPath), now, 24*time.Hour)
	_ = cleanupStaleDetailedTemps(filepath.Dir(DetailedSnapshotPath(r.options.InstanceDir)), now, 24*time.Hour)
	r.logger().Info("详细统计报告已生成", zap.String("report_path", reportPath), zap.String("generated_at", snapshot.GeneratedAt))
	// 对齐 Python StatisticOutputTask：同一轮汇总后输出近1小时控制台统计
	logConsoleStatistics(snapshot)
	return snapshot, nil
}

func (r *DetailedReporter) logger() *zap.Logger {
	return zap.L()
}

func resolveDetailedReportPath(instanceDir, override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if configured := strings.TrimSpace(os.Getenv("MAIBOT_STATISTICS_REPORT_PATH")); configured != "" {
		return configured
	}
	if strings.TrimSpace(instanceDir) == "" {
		return "maibot_statistics.html"
	}
	return filepath.Join(instanceDir, "maibot_statistics.html")
}
