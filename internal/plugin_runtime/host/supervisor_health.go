package host

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/plugin_runtime/protocol"
)

// DefaultRunnerHealthInterval 健康检查间隔，对齐 PluginRuntimeConfig.health_check_interval_sec
const DefaultRunnerHealthInterval = 30 * time.Second

// RunnerRestartFunc 重启 Runner 子进程的回调；返回 nil 表示重启成功
type RunnerRestartFunc func(ctx context.Context) error

// RunnerHealthProbe 健康检查探针，测试可注入替身
type RunnerHealthProbe func(ctx context.Context, timeoutMS int64) (RunnerHealth, error)

// StartHealthMonitor 启动 Runner 健康巡检与使用时长结算，返回停止函数
//
// 对齐 supervisor._health_check_loop：每轮先探测健康，不健康或探测失败立即重启；
// 重启失败且达到上限时结束巡检；每轮按真实运行插件结算使用时长，持久化由 60 秒 checkpoint 负责
func (h *Host) StartHealthMonitor(ctx context.Context, interval time.Duration, restart RunnerRestartFunc) func() {
	if h == nil {
		return func() {}
	}
	group, ok := h.activeRunnerGroup()
	if !ok {
		return func() {}
	}
	return h.startHealthMonitor(ctx, group, interval, restart, nil)
}

func (h *Host) startHealthMonitor(ctx context.Context, group RunnerGroup, interval time.Duration, restart RunnerRestartFunc, probe RunnerHealthProbe) func() {
	if interval <= 0 {
		interval = h.RunnerHealthInterval()
	}
	timeoutMS := interval.Milliseconds()
	if timeoutMS < 1000 {
		timeoutMS = 1000
	}
	if probe == nil {
		probe = h.RequestRunnerHealth
	}
	monitorCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
				restartReason := h.runnerProcessRestartReason(group)
				var health RunnerHealth
				var err error
				if restartReason == "" {
					health, err = h.probeRunnerHealth(monitorCtx, timeoutMS, probe)
					if err != nil {
						restartReason = "health_check_failed"
						zap.L().Warn("Runner 健康检查失败", zap.Error(err))
					} else if !health.Healthy {
						restartReason = "health_check_unhealthy"
						zap.L().Warn("Runner 健康检查不健康")
					}
				} else {
					zap.L().Warn("Runner 进程不可用", zap.String("reason", restartReason))
				}
				if restartReason != "" {
					if restart == nil {
						zap.L().Error("Runner 健康异常但未配置重启回调，停止健康巡检")
						return
					}
					if restartErr := restart(monitorCtx); restartErr != nil {
						zap.L().Error("Runner 重启失败", zap.Error(restartErr))
						if !h.shouldKeepHealthLoop(group) {
							return
						}
						continue
					}
					continue
				}
				h.RecoverMissingPlugins(monitorCtx, health.LoadedPlugins)
				h.ReconcileUsage()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			wg.Wait()
		})
	}
}

// probeRunnerHealth 执行一次健康探测；超时后按 Python 语义快速复核一次
func (h *Host) probeRunnerHealth(ctx context.Context, timeoutMS int64, probe RunnerHealthProbe) (RunnerHealth, error) {
	health, err := probe(ctx, timeoutMS)
	if err == nil || !isRunnerHealthTimeout(err) {
		return health, err
	}
	retryMS := timeoutMS
	if retryMS > 5000 {
		retryMS = 5000
	}
	zap.L().Warn("Runner 健康检查超时，快速复核以排除 Host 事件循环失速",
		zap.Int64("retry_timeout_ms", retryMS))
	return probe(ctx, retryMS)
}

func isRunnerHealthTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "超时") || strings.Contains(text, "timeout")
}

// activeRunnerGroup 返回当前已启动的 Runner 分组
func (h *Host) activeRunnerGroup() (RunnerGroup, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, group := range []RunnerGroup{RunnerGroupThirdParty, RunnerGroupBuiltin} {
		if state := h.runners[group]; state != nil && state.Started {
			return group, true
		}
	}
	return "", false
}

// runnerProcessRestartReason 优先检测子进程是否已退出或缺失，对齐 supervisor._health_check_loop
func (h *Host) runnerProcessRestartReason(group RunnerGroup) string {
	h.mu.RLock()
	state := h.runners[group]
	h.mu.RUnlock()
	if state == nil {
		return "runner_process_missing"
	}
	if !state.SpawnPython {
		return ""
	}
	if state.Child() == nil {
		return "runner_process_missing"
	}
	if state.Child().HasExited() {
		return "runner_process_exited"
	}
	return ""
}

// shouldKeepHealthLoop 重启失败后是否还有剩余重试次数
func (h *Host) shouldKeepHealthLoop(group RunnerGroup) bool {
	allowed, _, _ := h.shouldRestart(group)
	return allowed
}

// RestartRunner 停止并重新启动指定分组的 Runner，对齐 supervisor._restart_runner 的进程层语义
func (h *Host) RestartRunner(ctx context.Context, opts StartRunnerOptions) error {
	if h == nil {
		return nil
	}
	h.restartMu.Lock()
	defer h.restartMu.Unlock()
	if opts.Group == "" {
		opts.Group = RunnerGroupThirdParty
	}
	allowed, count, maxAttempts := h.shouldRestart(opts.Group)
	if !allowed {
		return h.restartExceededError(count, maxAttempts)
	}
	h.markRunnerRestarting(opts.Group)
	h.TrackPluginRecoveryTargets(h.runnerReadyLoadedPlugins())
	h.StopRunner(opts.Group)
	state, err := h.StartRunner(ctx, opts)
	if err != nil {
		h.failRunnerStart(opts.Group, err)
		return err
	}
	if opts.SpawnPython {
		payload, waitErr := h.WaitForRunnerReady(ctx, opts.Group, h.RunnerSpawnTimeout())
		if waitErr != nil {
			h.failRunnerStart(opts.Group, waitErr)
			h.StopRunner(opts.Group)
			return waitErr
		}
		if missing := h.ValidatePluginRecovery(payload); len(missing) > 0 {
			zap.L().Warn("Runner 进程已恢复，原有插件仍待恢复",
				zap.Strings("plugins", missing),
			)
			h.RecoverMissingPlugins(ctx, payload.LoadedPlugins)
		}
	}
	if len(h.ValidatePluginRecovery(h.currentRunnerReady(opts.Group))) == 0 {
		h.resetRestartCount(opts.Group)
	}
	_ = state
	return nil
}

// currentRunnerReady 返回指定分组最近一次 runner.ready，缺失时回退全局快照
func (h *Host) currentRunnerReady(group RunnerGroup) protocol.RunnerReadyPayload {
	if h == nil {
		return protocol.RunnerReadyPayload{}
	}
	group = runnerGroupOrDefault(group)
	h.mu.RLock()
	defer h.mu.RUnlock()
	if ready, ok := h.runnerReadyByGroup[group]; ok {
		return ready
	}
	return h.runnerReady
}
