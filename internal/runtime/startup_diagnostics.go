package runtime

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultStartupWarnAfter 是启动阶段超过该时长没有心跳时的首次告警阈值
	DefaultStartupWarnAfter = 3 * time.Second
	// DefaultStartupWatchInterval 是启动看门狗检查心跳的间隔
	DefaultStartupWatchInterval = time.Second
	// DefaultStartupReportInterval 是连续卡顿时两次现场转储之间的最小间隔
	DefaultStartupReportInterval = 5 * time.Second
	// maxStartupStacks 限制单次启动现场转储大小，避免日志吞掉磁盘
	maxStartupStacks = 32 << 10
)

// StartupStageResult 记录一个启动诊断阶段的执行结果
type StartupStageResult struct {
	Name     string
	Duration time.Duration
	TimedOut bool
	Err      error
}

// StartupStageOptions 控制启动阶段超时与诊断回调
type StartupStageOptions struct {
	Name     string
	Timeout  time.Duration
	OnStart  func(name string)
	OnFinish func(result StartupStageResult)
}

// RunStartupStage 在诊断边界内执行启动初始化步骤
// 失败、超时和取消都会带上阶段名返回，panic 也会转成错误，避免启动现场丢失
func RunStartupStage(ctx context.Context, opts StartupStageOptions, run func(context.Context) error) (err error) {
	if run == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = "未命名启动阶段"
	}
	started := time.Now()
	if opts.OnStart != nil {
		opts.OnStart(name)
	}
	result := StartupStageResult{Name: name}
	defer func() {
		result.Duration = time.Since(started)
		result.Err = err
		if opts.OnFinish != nil {
			opts.OnFinish(result)
		}
	}()

	stageCtx := ctx
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		stageCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	if stageCtx.Err() != nil {
		return startupStageContextError(name, stageCtx.Err(), opts.Timeout, &result)
	}

	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("panic: %v", recovered)
			}
		}()
		err = run(stageCtx)
	}()

	timedOut := errors.Is(stageCtx.Err(), context.DeadlineExceeded)
	if timedOut {
		result.TimedOut = true
	}
	if err != nil {
		if timedOut {
			return fmt.Errorf("启动阶段 %s 超时(%s): %w", name, opts.Timeout, err)
		}
		if errors.Is(stageCtx.Err(), context.Canceled) {
			return fmt.Errorf("启动阶段 %s 被取消: %w", name, err)
		}
		return fmt.Errorf("启动阶段 %s 失败: %w", name, err)
	}
	if stageCtx.Err() != nil {
		return startupStageContextError(name, stageCtx.Err(), opts.Timeout, &result)
	}
	return nil
}

func startupStageContextError(name string, cause error, timeout time.Duration, result *StartupStageResult) error {
	if errors.Is(cause, context.DeadlineExceeded) {
		if result != nil {
			result.TimedOut = true
		}
		return fmt.Errorf("启动阶段 %s 超时(%s): %w", name, timeout, cause)
	}
	return fmt.Errorf("启动阶段 %s 被取消: %w", name, cause)
}

// StartupLagReport 是一次启动卡顿现场
type StartupLagReport struct {
	Name   string
	Lag    time.Duration
	Stacks string
}

// StartupWatchdogOptions 配置启动看门狗
type StartupWatchdogOptions struct {
	Name           string
	WarnAfter      time.Duration
	Interval       time.Duration
	ReportInterval time.Duration
	OnLag          func(report StartupLagReport)
}

// StartupWatchdog 在启动初始化期间检查心跳；主流程阻塞时转储 goroutine 现场
// 与 Python monitor_startup_loop 一样，它只观察，不取消或中断初始化
type StartupWatchdog struct {
	name           string
	warnAfter      time.Duration
	interval       time.Duration
	reportInterval time.Duration
	onLag          func(report StartupLagReport)

	mu            sync.Mutex
	lastHeartbeat time.Time
	lastReportAt  time.Time
	stop          chan struct{}
	done          chan struct{}
	stopOnce      sync.Once
}

// StartStartupWatchdog 启动看门狗；Stop 后 goroutine 退出
func StartStartupWatchdog(opts StartupWatchdogOptions) *StartupWatchdog {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = "main"
	}
	warnAfter := opts.WarnAfter
	if warnAfter <= 0 {
		warnAfter = DefaultStartupWarnAfter
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultStartupWatchInterval
	}
	reportInterval := opts.ReportInterval
	if reportInterval <= 0 {
		reportInterval = DefaultStartupReportInterval
	}
	watchdog := &StartupWatchdog{
		name:           name,
		warnAfter:      warnAfter,
		interval:       interval,
		reportInterval: reportInterval,
		onLag:          opts.OnLag,
		lastHeartbeat:  time.Now(),
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
	}
	go watchdog.loop()
	return watchdog
}

// Heartbeat 刷新启动主流程心跳
func (w *StartupWatchdog) Heartbeat() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.lastHeartbeat = time.Now()
	w.mu.Unlock()
}

// Stop 停止看门狗并等待后台 goroutine 退出；重复调用安全
func (w *StartupWatchdog) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() {
		close(w.stop)
	})
	<-w.done
}

func (w *StartupWatchdog) loop() {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case now := <-ticker.C:
			w.mu.Lock()
			lastHeartbeat := w.lastHeartbeat
			lastReportAt := w.lastReportAt
			lag := now.Sub(lastHeartbeat)
			if lag < w.warnAfter {
				w.lastReportAt = time.Time{}
				w.mu.Unlock()
				continue
			}
			if !lastReportAt.IsZero() && now.Sub(lastReportAt) < w.reportInterval {
				w.mu.Unlock()
				continue
			}
			w.lastReportAt = now
			w.mu.Unlock()
			if w.onLag != nil {
				w.onLag(StartupLagReport{Name: w.name, Lag: lag, Stacks: captureStartupStacks()})
			}
		}
	}
}

func captureStartupStacks() string {
	buf := make([]byte, maxStartupStacks)
	n := goruntime.Stack(buf, true)
	if n <= 0 {
		return ""
	}
	if n > len(buf) {
		n = len(buf)
	}
	text := string(buf[:n])
	if n == len(buf) {
		text += "\n... 启动现场转储已截断"
	}
	return text
}
