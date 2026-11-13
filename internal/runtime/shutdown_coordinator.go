package runtime

import (
	"context"
	"fmt"
	"sync"
)

// ShutdownCoordinator 保证关停计划只执行一次，重复调用返回同一份报告
type ShutdownCoordinator struct {
	mu      sync.Mutex
	started bool
	done    chan struct{}
	report  ShutdownReport
}

// NewShutdownCoordinator 创建幂等的关停协调器
func NewShutdownCoordinator() *ShutdownCoordinator {
	return &ShutdownCoordinator{}
}

// Run 执行关停计划；并发或重复调用会等待并复用第一次执行结果
func (c *ShutdownCoordinator) Run(ctx context.Context, plan ShutdownPlan) (report ShutdownReport) {
	if c == nil {
		return plan.Run(ctx)
	}
	c.mu.Lock()
	if c.started {
		done := c.done
		c.mu.Unlock()
		<-done
		c.mu.Lock()
		report = c.report
		c.mu.Unlock()
		return report
	}
	c.started = true
	c.done = make(chan struct{})
	c.mu.Unlock()

	defer func() {
		if recovered := recover(); recovered != nil {
			report = ShutdownReport{
				Success:  false,
				Failures: []string{fmt.Sprintf("关停计划 panic: %v", recovered)},
			}
		}
		c.mu.Lock()
		c.report = report
		close(c.done)
		c.mu.Unlock()
	}()
	report = plan.Run(ctx)
	return report
}

// RunWithSignal 执行关停计划；第二次中断信号到达时取消计划并返回 completed=false
func (c *ShutdownCoordinator) RunWithSignal(ctx context.Context, plan ShutdownPlan, sig *ShutdownSignal) (report ShutdownReport, completed bool) {
	if sig == nil {
		return c.Run(ctx, plan), true
	}
	select {
	case <-sig.Forced():
		return ShutdownReport{}, false
	default:
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan ShutdownReport, 1)
	go func() {
		done <- c.Run(runCtx, plan)
	}()

	select {
	case report = <-done:
		return report, true
	case <-sig.Forced():
		select {
		case report = <-done:
			return report, true
		default:
		}
		cancel()
		return ShutdownReport{}, false
	}
}
