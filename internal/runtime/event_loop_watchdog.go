package runtime

import (
	"context"
	"fmt"
	"runtime/metrics"
	"sync"
	"time"

	"go.uber.org/zap"
)

// 事件循环看门狗默认参数，对齐 Python src/common/event_loop_watchdog.py:20-30
const (
	defaultWatchdogInterval      = time.Second
	defaultWatchdogWarnThreshold = 500 * time.Millisecond
)

// EventLoopWatchdogOptions 事件循环看门狗选项
// Go 无 asyncio 事件循环，用调度延迟（sched/latencies）作为等价指标
type EventLoopWatchdogOptions struct {
	// Name 看门狗名称，用于日志区分
	Name string
	// Interval 采样间隔
	Interval time.Duration
	// WarnThreshold 超过该延迟即告警
	WarnThreshold time.Duration
}

// 下限钳制，对齐 Python `max(0.1, interval)` / `max(0.01, warn)`
const (
	minWatchdogInterval      = 100 * time.Millisecond
	minWatchdogWarnThreshold = 10 * time.Millisecond
)

func (o *EventLoopWatchdogOptions) applyDefaults() {
	if o.Interval <= 0 {
		o.Interval = defaultWatchdogInterval
	}
	if o.Interval < minWatchdogInterval {
		o.Interval = minWatchdogInterval
	}
	if o.WarnThreshold <= 0 {
		o.WarnThreshold = defaultWatchdogWarnThreshold
	}
	if o.WarnThreshold < minWatchdogWarnThreshold {
		o.WarnThreshold = minWatchdogWarnThreshold
	}
	if o.Name == "" {
		o.Name = "main"
	}
}

// EventLoopWatchdog 周期性采样 goroutine 调度延迟
// 对齐 Python event_loop_watchdog.py:33-48 的 EventLoopLagWatchdog：
// Python 测的是事件循环被阻塞的时长，Go 测的是 goroutine 等待被调度的时长
type EventLoopWatchdog struct {
	opts     EventLoopWatchdogOptions
	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// StartEventLoopWatchdog 启动看门狗；ctx 取消或 Stop 后退出
func StartEventLoopWatchdog(ctx context.Context, opts EventLoopWatchdogOptions) *EventLoopWatchdog {
	opts.applyDefaults()
	watchdog := &EventLoopWatchdog{
		opts:   opts,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	go watchdog.loop(ctx)
	return watchdog
}

// Stop 停止看门狗并等待退出
func (w *EventLoopWatchdog) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stopCh) })
	<-w.doneCh
}

func (w *EventLoopWatchdog) loop(ctx context.Context) {
	defer close(w.doneCh)
	ticker := time.NewTicker(w.opts.Interval)
	defer ticker.Stop()
	samples := []metrics.Sample{{Name: "/sched/latencies:seconds"}}
	// /sched/latencies 是进程生命周期累计直方图：只比较本 tick 新增的桶，
	// 否则历史尖峰会让后续每个 tick 都重复告警
	var previous []uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			metrics.Read(samples)
			if samples[0].Value.Kind() != metrics.KindFloat64Histogram {
				continue
			}
			histogram := samples[0].Value.Float64Histogram()
			lag := histogramDeltaMaxSeconds(histogram, previous)
			previous = append(previous[:0], histogram.Counts...)
			if lag >= w.opts.WarnThreshold.Seconds() {
				zap.L().Warn(fmt.Sprintf("事件循环卡顿: loop=%s 迟到=%.2fs (告警阈值 %.2fs)",
					w.opts.Name, lag, w.opts.WarnThreshold.Seconds()))
			}
		}
	}
}

// histogramDeltaMaxSeconds 返回本 tick 新增样本的最大非空桶上界
func histogramDeltaMaxSeconds(histogram *metrics.Float64Histogram, previous []uint64) float64 {
	if histogram == nil {
		return 0
	}
	for index := len(histogram.Counts) - 1; index >= 0; index-- {
		current := histogram.Counts[index]
		before := uint64(0)
		if index < len(previous) {
			before = previous[index]
		}
		if current <= before {
			continue
		}
		if index+1 < len(histogram.Buckets) {
			return histogram.Buckets[index+1]
		}
		return histogram.Buckets[len(histogram.Buckets)-1]
	}
	return 0
}
