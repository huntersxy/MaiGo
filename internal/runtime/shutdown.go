package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	goruntime "runtime"
	"sync"
	"syscall"
	"time"
)

const (
	// ShutdownTimeout 为 Worker 优雅关闭总预算，对齐 MaiBot SHUTDOWN_TIMEOUT
	ShutdownTimeout = 50 * time.Second
	// LogBroadcastTimeout 为停止 WebSocket 日志广播的预算，对齐 Python stop_ws_broadcasts 的 2 秒等待
	LogBroadcastTimeout = 2 * time.Second
	// ImageSyncTimeout 为等待图片描述同步的预算
	ImageSyncTimeout = 5 * time.Second
	// MemoryWriterTimeout 为停止记忆自动写入的预算
	MemoryWriterTimeout = 5 * time.Second
	// MemoryKernelTimeout 为关闭 A_Memorix 并持久化的预算
	MemoryKernelTimeout = 30 * time.Second
	// AsyncTaskTimeout 为停止异步任务管理器的预算
	AsyncTaskTimeout = 5 * time.Second
	// CancelRemainingTimeout 为取消并等待剩余任务的预算
	CancelRemainingTimeout = 5 * time.Second
	// FinalCleanupTimeout 为最终任务清理预算
	FinalCleanupTimeout = 5 * time.Second
	// defaultStuckAfter 为关停步骤开始采样现场前的等待时间，对齐 Python warn_after
	defaultStuckAfter = 3 * time.Second
	// defaultStuckInterval 为卡住步骤的采样间隔，对齐 Python 每五秒采样
	defaultStuckInterval = 5 * time.Second
	// maxShutdownStacks 限制单次现场转储大小，避免关停日志吞掉磁盘
	maxShutdownStacks = 32 << 10
)

// ShutdownPhase 表示关停阶段，决定共享的截止时间
type ShutdownPhase string

const (
	// PhasePre 为前置关停（日志广播、消息入口、聊天循环、插件、图片同步等），共享 10 秒
	PhasePre ShutdownPhase = "pre"
	// PhaseMemoryWriter 为停止记忆自动写入
	PhaseMemoryWriter ShutdownPhase = "memory_writer"
	// PhaseMemoryKernel 为关闭 A_Memorix 并持久化
	PhaseMemoryKernel ShutdownPhase = "memory_kernel"
	// PhaseFinal 为最终清理（Emoji、MCP、异步任务、剩余协程）
	PhaseFinal ShutdownPhase = "final"
)

// ShutdownStep 是一个可观测、带独立超时的关停步骤
type ShutdownStep struct {
	Name    string
	Phase   ShutdownPhase
	Timeout time.Duration
	Run     func(context.Context) error
}

// ShutdownStepResult 记录单步关停结果
type ShutdownStepResult struct {
	Name     string
	Phase    ShutdownPhase
	Timeout  time.Duration
	Duration time.Duration
	Success  bool
	TimedOut bool
	Err      string
}

// ShutdownStuck 记录卡住步骤的现场，用于定位阻塞在同步调用的位置
type ShutdownStuck struct {
	Name    string
	Phase   ShutdownPhase
	Elapsed time.Duration
	Timeout time.Duration
	Stacks  string
}

// ShutdownReport 汇总一次关停过程
type ShutdownReport struct {
	StartedAt  time.Time
	Duration   time.Duration
	TotalLimit time.Duration
	Steps      []ShutdownStepResult
	Failures   []string
	Success    bool
	Aborted    bool
}

// ShutdownPlan 按阶段顺序执行关停步骤
type ShutdownPlan struct {
	Steps         []ShutdownStep
	TotalTimeout  time.Duration
	Now           func() time.Time
	OnStepStart   func(ShutdownStep)
	OnStep        func(ShutdownStepResult)
	OnStuck       func(ShutdownStuck)
	StuckAfter    time.Duration
	StuckInterval time.Duration
}

// Run 执行关停计划；单步超时、返回错误或 panic 都记录失败并继续后续步骤
func (p ShutdownPlan) Run(ctx context.Context) ShutdownReport {
	now := p.Now
	if now == nil {
		now = time.Now
	}
	total := p.TotalTimeout
	if total <= 0 {
		total = ShutdownTimeout
	}
	start := now()
	overallDeadline := start.Add(total)
	preDeadline := overallDeadline.Add(-(MemoryWriterTimeout + MemoryKernelTimeout + FinalCleanupTimeout))
	report := ShutdownReport{StartedAt: start, TotalLimit: total, Success: true}
	for _, step := range p.Steps {
		if ctx.Err() != nil {
			report.Aborted = true
			report.Success = false
			report.Failures = append(report.Failures, "关停上下文已取消")
			break
		}
		deadline := shutdownPhaseDeadline(step.Phase, preDeadline, overallDeadline)
		result := p.runStep(ctx, step, now, deadline)
		report.Steps = append(report.Steps, result)
		if !result.Success {
			report.Success = false
			report.Failures = append(report.Failures, shutdownFailureText(result))
		}
		p.notify(result)
	}
	report.Duration = now().Sub(start)
	if len(report.Steps) < len(p.Steps) && !report.Aborted {
		report.Success = false
	}
	return report
}

func (p ShutdownPlan) runStep(ctx context.Context, step ShutdownStep, now func() time.Time, deadline time.Time) ShutdownStepResult {
	stepStart := now()
	result := ShutdownStepResult{Name: step.Name, Phase: step.Phase}
	p.notifyStart(step)

	remaining := deadline.Sub(now())
	if remaining < 0 {
		remaining = 0
	}
	limit := step.Timeout
	if limit <= 0 || limit > remaining {
		limit = remaining
	}
	result.Timeout = limit
	if limit <= 0 {
		result.TimedOut = true
		result.Err = "关停预算已耗尽"
		result.Duration = 0
		return result
	}

	stopDiag := make(chan struct{})
	var diagWG sync.WaitGroup
	if p.OnStuck != nil {
		diagWG.Add(1)
		go func() {
			defer diagWG.Done()
			p.watchStuck(step, stepStart, stopDiag)
		}()
	}

	stepCtx, cancel := context.WithTimeout(ctx, limit)
	done := make(chan error, 1)
	go func(run func(context.Context) error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- fmt.Errorf("panic: %v", recovered)
			}
		}()
		done <- run(stepCtx)
	}(step.Run)

	var err error
	select {
	case err = <-done:
	case <-stepCtx.Done():
		err = stepCtx.Err()
		// 截止时间与完成同时就绪时优先采信真实返回值，避免把成功步骤记成超时
		select {
		case err = <-done:
		default:
		}
		if errors.Is(err, context.DeadlineExceeded) {
			result.TimedOut = true
		}
	}
	cancel()
	close(stopDiag)
	diagWG.Wait()

	result.Duration = now().Sub(stepStart)
	if err != nil {
		result.Err = err.Error()
		result.Success = false
	} else {
		result.Success = true
	}
	return result
}

func (p ShutdownPlan) watchStuck(step ShutdownStep, started time.Time, stop <-chan struct{}) {
	warnAfter := p.StuckAfter
	if warnAfter <= 0 {
		warnAfter = defaultStuckAfter
	}
	interval := p.StuckInterval
	if interval <= 0 {
		interval = defaultStuckInterval
	}

	timer := time.NewTimer(warnAfter)
	defer timer.Stop()
	select {
	case <-stop:
		return
	case <-timer.C:
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		stacks := captureGoroutineStacks()
		p.notifyStuck(ShutdownStuck{
			Name:    step.Name,
			Phase:   step.Phase,
			Elapsed: time.Since(started),
			Timeout: step.Timeout,
			Stacks:  stacks,
		})
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

func captureGoroutineStacks() string {
	buf := make([]byte, maxShutdownStacks)
	n := goruntime.Stack(buf, true)
	if n <= 0 {
		return ""
	}
	if n > len(buf) {
		n = len(buf)
	}
	text := string(buf[:n])
	if n == len(buf) {
		text += "\n... 现场转储已截断"
	}
	return text
}

func shutdownFailureText(result ShutdownStepResult) string {
	if result.Err == "" {
		if result.TimedOut {
			return result.Name + ": 超时"
		}
		return result.Name + ": 失败"
	}
	return result.Name + ": " + result.Err
}

func (p ShutdownPlan) notifyStart(step ShutdownStep) {
	if p.OnStepStart == nil {
		return
	}
	defer func() { _ = recover() }()
	p.OnStepStart(step)
}

func (p ShutdownPlan) notifyStuck(stuck ShutdownStuck) {
	if p.OnStuck == nil {
		return
	}
	defer func() { _ = recover() }()
	p.OnStuck(stuck)
}

func (p ShutdownPlan) notify(result ShutdownStepResult) {
	if p.OnStep == nil {
		return
	}
	defer func() { _ = recover() }()
	p.OnStep(result)
}

func shutdownPhaseDeadline(phase ShutdownPhase, preDeadline, overallDeadline time.Time) time.Time {
	switch phase {
	case PhaseMemoryWriter:
		return preDeadline.Add(MemoryWriterTimeout)
	case PhaseMemoryKernel:
		return preDeadline.Add(MemoryWriterTimeout + MemoryKernelTimeout)
	case PhaseFinal:
		return overallDeadline
	default:
		return preDeadline
	}
}

// ShutdownSignal 记录中断信号计数并暴露首次请求与第二次强制退出信号
type ShutdownSignal struct {
	mu        sync.Mutex
	count     int
	reason    string
	once      sync.Once
	request   chan struct{}
	forced    chan struct{}
	forceOnce sync.Once
}

// NewShutdownSignal 创建关停信号
func NewShutdownSignal() *ShutdownSignal {
	return &ShutdownSignal{
		request: make(chan struct{}),
		forced:  make(chan struct{}),
	}
}

// Trigger 记录一次关停请求；首次请求返回 true，第二次关闭强制退出通道
func (s *ShutdownSignal) Trigger(reason string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	switch s.count {
	case 1:
		s.reason = reason
		s.once.Do(func() { close(s.request) })
		return true
	case 2:
		s.forceOnce.Do(func() { close(s.forced) })
	}
	return false
}

// Count 返回收到的关停请求次数
func (s *ShutdownSignal) Count() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// Reason 返回首次关停请求来源
func (s *ShutdownSignal) Reason() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason
}

// Requested 返回首次关停请求的关闭通道
func (s *ShutdownSignal) Requested() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.request
}

// Forced 返回第二次中断信号的关闭通道，用于跳过剩余关停步骤强制退出
func (s *ShutdownSignal) Forced() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.forced
}

// InstallShutdownSignalHandler 监听 SIGINT/SIGTERM，首次信号触发 Trigger，第二次触发强制退出
func InstallShutdownSignalHandler(sig *ShutdownSignal, signals ...os.Signal) func() {
	if sig == nil {
		return func() {}
	}
	if len(signals) == 0 {
		// Windows 的 Ctrl+Break 由 Go runtime 映射为 os.Interrupt，无需单独注册 SIGBREAK
		signals = []os.Signal{os.Interrupt, syscall.SIGTERM}
	}
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, signals...)
	done := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		for {
			select {
			case <-done:
				signal.Stop(ch)
				return
			case s, ok := <-ch:
				if !ok {
					return
				}
				sig.Trigger("signal:" + s.String())
			}
		}
	}()
	return func() {
		stopOnce.Do(func() { close(done) })
	}
}
