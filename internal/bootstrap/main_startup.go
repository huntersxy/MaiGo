package bootstrap

import (
	"context"
	"os"
	"sync"
	"time"

	"MaiGo/internal/common/database/migrations"
	"MaiGo/internal/runtime"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	startupCleanupTotalTimeout = 30 * time.Second
	startupCleanupStepTimeout  = 5 * time.Second
)

// startupCleanup 记录启动期已创建资源的清理动作，按逆序执行，避免依赖先被拆除
type startupCleanup struct {
	mu    sync.Mutex
	steps []runtime.ShutdownStep
}

func (c *startupCleanup) add(name string, run func(context.Context) error) {
	if c == nil || run == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.steps = append(c.steps, runtime.ShutdownStep{
		Name:    name,
		Phase:   runtime.PhaseFinal,
		Timeout: startupCleanupStepTimeout,
		Run:     run,
	})
}

func (c *startupCleanup) plan() runtime.ShutdownPlan {
	plan := runtime.ShutdownPlan{
		TotalTimeout:  startupCleanupTotalTimeout,
		StuckAfter:    3 * time.Second,
		StuckInterval: 5 * time.Second,
		OnStepStart: func(step runtime.ShutdownStep) {
			zap.L().Info("启动清理步骤开始", zap.String("step", step.Name))
		},
		OnStep: func(result runtime.ShutdownStepResult) {
			if result.Success {
				zap.L().Info("启动清理步骤完成",
					zap.String("step", result.Name),
					zap.Duration("duration", result.Duration))
				return
			}
			zap.L().Warn("启动清理步骤失败",
				zap.String("step", result.Name),
				zap.Bool("timed_out", result.TimedOut),
				zap.String("error", result.Err))
		},
		OnStuck: func(stuck runtime.ShutdownStuck) {
			zap.L().Warn("启动清理步骤等待现场",
				zap.String("step", stuck.Name),
				zap.Duration("elapsed", stuck.Elapsed),
				zap.Duration("timeout", stuck.Timeout),
				zap.String("stacks", stuck.Stacks))
		},
	}
	if c == nil {
		return plan
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	plan.Steps = make([]runtime.ShutdownStep, 0, len(c.steps))
	for i := len(c.steps) - 1; i >= 0; i-- {
		plan.Steps = append(plan.Steps, c.steps[i])
	}
	return plan
}

func (c *startupCleanup) run(signal *runtime.ShutdownSignal) bool {
	plan := c.plan()
	if len(plan.Steps) == 0 {
		return true
	}
	coordinator := runtime.NewShutdownCoordinator()
	report, completed := coordinator.RunWithSignal(context.Background(), plan, signal)
	if !completed {
		zap.L().Error("启动清理收到第二次中断信号，跳过剩余清理步骤并强制退出")
		return false
	}
	if report.Success {
		zap.L().Info("启动失败清理完成", zap.Duration("duration", report.Duration))
		return true
	}
	zap.L().Error("启动失败清理未全部完成",
		zap.Duration("duration", report.Duration),
		zap.Strings("failures", report.Failures))
	return true
}

// waitGroupContext 等待后台任务退出；超时只返回错误，不阻塞关停计划继续执行
func waitGroupContext(ctx context.Context, wg *sync.WaitGroup) error {
	if wg == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

const startupTaskWaitStepName = "等待启动期后台任务退出"

// insertStartupTaskWait 在数据库关闭前等待启动期长生命周期任务退出
func insertStartupTaskWait(plan runtime.ShutdownPlan, wg *sync.WaitGroup) runtime.ShutdownPlan {
	if wg == nil {
		return plan
	}
	index := len(plan.Steps)
	for i, step := range plan.Steps {
		if step.Name == "关闭数据库" {
			index = i
			break
		}
	}
	step := runtime.ShutdownStep{
		Name:    startupTaskWaitStepName,
		Phase:   runtime.PhaseFinal,
		Timeout: startupCleanupStepTimeout,
		Run: func(ctx context.Context) error {
			return waitGroupContext(ctx, wg)
		},
	}
	plan.Steps = append(plan.Steps, runtime.ShutdownStep{})
	copy(plan.Steps[index+1:], plan.Steps[index:])
	plan.Steps[index] = step
	return plan
}

// startupLifecycle 在长时间初始化前安装信号监听：首次中断取消初始化上下文并走清理，第二次直接强制退出
type startupLifecycle struct {
	signal          *runtime.ShutdownSignal
	ctx             context.Context
	cancel          context.CancelFunc
	cleanup         *startupCleanup
	done            chan struct{}
	once            sync.Once
	mu              sync.Mutex
	started         bool
	watchdog        *runtime.StartupWatchdog
	diagnosticsStop sync.Once
}

func newStartupLifecycle(signal *runtime.ShutdownSignal) *startupLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	l := &startupLifecycle{
		signal:  signal,
		ctx:     ctx,
		cancel:  cancel,
		cleanup: &startupCleanup{},
		done:    make(chan struct{}),
	}
	l.watchdog = runtime.StartStartupWatchdog(runtime.StartupWatchdogOptions{
		Name:           "main",
		WarnAfter:      runtime.DefaultStartupWarnAfter,
		Interval:       runtime.DefaultStartupWatchInterval,
		ReportInterval: runtime.DefaultStartupReportInterval,
		OnLag: func(report runtime.StartupLagReport) {
			if report.Stacks == "" {
				// 同一段卡顿的后续报告：只报时长，现场已在首次转储里
				zap.L().Warn("启动主循环仍在卡顿",
					zap.String("loop", report.Name),
					zap.Duration("lag", report.Lag))
				return
			}
			zap.L().Warn("启动主循环失去心跳",
				zap.String("loop", report.Name),
				zap.Duration("lag", report.Lag),
				zap.String("stacks", report.Stacks))
		},
	})
	go func() {
		select {
		case <-signal.Requested():
			cancel()
			select {
			case <-signal.Forced():
				l.forceExitIfStarting()
			case <-l.done:
			}
		case <-l.done:
		}
	}()
	return l
}

func (l *startupLifecycle) Context() context.Context {
	if l == nil {
		return context.Background()
	}
	return l.ctx
}

func (l *startupLifecycle) AddCleanup(name string, run func(context.Context) error) {
	if l == nil {
		return
	}
	l.cleanup.add(name, run)
}

// Check 在关键初始化阶段后检查是否已被信号取消
func (l *startupLifecycle) Check(stage string) bool {
	if l == nil {
		return true
	}
	if l.watchdog != nil {
		l.watchdog.Heartbeat()
	}
	if l.ctx.Err() == nil {
		return true
	}
	zap.L().Warn("启动期收到中断信号，停止后续初始化",
		zap.String("stage", stage),
		zap.Error(l.ctx.Err()))
	return false
}

// RunStage 在诊断边界内执行启动组件，失败、超时和取消都会带上阶段名返回
func (l *startupLifecycle) RunStage(stage string, timeout time.Duration, init func(context.Context) error) error {
	if init == nil {
		return nil
	}
	if l == nil {
		return runtime.RunStartupStage(context.Background(), runtime.StartupStageOptions{Name: stage, Timeout: timeout}, init)
	}
	if l.watchdog != nil {
		l.watchdog.Heartbeat()
	}
	err := runtime.RunStartupStage(l.Context(), runtime.StartupStageOptions{
		Name:    stage,
		Timeout: timeout,
		OnStart: func(name string) {
			zap.L().Debug("启动诊断阶段开始", zap.String("stage", name))
		},
		OnFinish: func(result runtime.StartupStageResult) {
			if result.Err != nil {
				zap.L().Warn("启动诊断阶段失败",
					zap.String("stage", result.Name),
					zap.Duration("duration", result.Duration),
					zap.Bool("timed_out", result.TimedOut),
					zap.Error(result.Err))
				return
			}
			zap.L().Debug("启动诊断阶段完成",
				zap.String("stage", result.Name),
				zap.Duration("duration", result.Duration))
		},
	}, init)
	if l.watchdog != nil {
		l.watchdog.Heartbeat()
	}
	return err
}

// RunInitialization 执行可注入的初始化步骤；步骤结束前若已收到中断则返回上下文错误
func (l *startupLifecycle) RunInitialization(stage string, init func(context.Context) error) error {
	if init == nil {
		return nil
	}
	if err := l.RunStage(stage, 0, init); err != nil {
		return err
	}
	if !l.Check(stage) {
		return l.Context().Err()
	}
	return nil
}

func (l *startupLifecycle) MarkStarted() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.started {
		l.mu.Unlock()
		return
	}
	l.started = true
	l.mu.Unlock()
	l.stopDiagnostics()
	l.once.Do(func() { close(l.done) })
	l.cancel()
}

func (l *startupLifecycle) stopDiagnostics() {
	if l == nil || l.watchdog == nil {
		return
	}
	l.diagnosticsStop.Do(func() { l.watchdog.Stop() })
}

// forceExitIfStarting 只在初始化仍未完成时响应第二次中断；成功上线的进程交给正常关停计划
func (l *startupLifecycle) forceExitIfStarting() {
	l.mu.Lock()
	if l.started {
		l.mu.Unlock()
		return
	}
	zap.L().Error("启动期收到第二次中断信号，强制退出")
	os.Exit(1)
}

// AbortIfNeeded 只在启动尚未完成时执行已注册清理；成功启动后交给正常关停计划
func (l *startupLifecycle) AbortIfNeeded(exitCode *int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	started := l.started
	l.mu.Unlock()
	if started {
		return
	}
	l.stopDiagnostics()
	l.once.Do(func() { close(l.done) })
	l.cancel()
	if !l.cleanup.run(l.signal) {
		os.Exit(1)
	}
	if exitCode != nil && *exitCode == 0 {
		*exitCode = 1
	}
}

// runStartupDatabaseMaintenance 执行 Python 启动期的一次性维护收尾，幂等
func runStartupDatabaseMaintenance(ctx context.Context, db *gorm.DB) error {
	done, err := migration.RunStartupMaintenance(ctx, db)
	if err != nil {
		return err
	}
	zap.L().Info("启动一次性维护检查完成", zap.Bool("done", done))
	return nil
}
