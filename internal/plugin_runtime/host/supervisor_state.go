package host

import (
	"context"
	"fmt"
	"time"

	"maibot-go/internal/plugin_runtime/protocol"
)

// RunnerPhase Runner 生命周期阶段，对齐 supervisor 的启动/就绪/停止/重启状态
type RunnerPhase string

const (
	RunnerPhaseStopped      RunnerPhase = "stopped"
	RunnerPhaseStarting     RunnerPhase = "starting"
	RunnerPhaseWaitingReady RunnerPhase = "waiting_ready"
	RunnerPhaseRunning      RunnerPhase = "running"
	RunnerPhaseStopping     RunnerPhase = "stopping"
	RunnerPhaseRestarting   RunnerPhase = "restarting"
	RunnerPhaseFailed       RunnerPhase = "failed"

	DefaultRunnerSpawnWait   = 30 * time.Second
	DefaultMaxRestartAttempt = 3
)

// RunnerStatus Runner 运行时快照
type RunnerStatus struct {
	Group         RunnerGroup
	Phase         RunnerPhase
	IPCAddress    string
	Started       bool
	PythonSpawn   bool
	RestartCount  int
	LastError     string
	StartedAt     time.Time
	ReadyAt       time.Time
	LoadedPlugins []string
}

type runnerLifecycle struct {
	Phase        RunnerPhase
	IPCAddress   string
	Started      bool
	PythonSpawn  bool
	LastError    string
	StartedAt    time.Time
	ReadyAt      time.Time
	RestartCount int
}

// runnerLifecycleLocked 要求调用方已持有 h.mu
func (h *Host) runnerLifecycleLocked(group RunnerGroup) *runnerLifecycle {
	if h.runnerLifecycles == nil {
		h.runnerLifecycles = make(map[RunnerGroup]*runnerLifecycle)
	}
	lifecycle := h.runnerLifecycles[group]
	if lifecycle == nil {
		lifecycle = &runnerLifecycle{Phase: RunnerPhaseStopped}
		h.runnerLifecycles[group] = lifecycle
	}
	return lifecycle
}

func (h *Host) beginRunnerStart(group RunnerGroup) {
	h.mu.Lock()
	lifecycle := h.runnerLifecycleLocked(group)
	lifecycle.Phase = RunnerPhaseStarting
	lifecycle.LastError = ""
	lifecycle.StartedAt = time.Now()
	lifecycle.ReadyAt = time.Time{}
	h.mu.Unlock()
	h.notifyStatusChanges()
}

func (h *Host) finishRunnerStart(group RunnerGroup, state *RunnerState, spawn bool) {
	h.mu.Lock()
	lifecycle := h.runnerLifecycleLocked(group)
	lifecycle.Started = state != nil && state.Started
	lifecycle.IPCAddress = ""
	if state != nil {
		lifecycle.IPCAddress = state.IPCAddress
	}
	lifecycle.PythonSpawn = spawn
	if spawn {
		lifecycle.Phase = RunnerPhaseWaitingReady
	} else {
		lifecycle.Phase = RunnerPhaseRunning
		lifecycle.ReadyAt = time.Now()
	}
	h.mu.Unlock()
	h.notifyStatusChanges()
}

func (h *Host) failRunnerStart(group RunnerGroup, err error) {
	h.mu.Lock()
	lifecycle := h.runnerLifecycleLocked(group)
	lifecycle.Phase = RunnerPhaseFailed
	lifecycle.Started = false
	if err != nil {
		lifecycle.LastError = err.Error()
	}
	h.mu.Unlock()
	h.notifyStatusChanges()
}

func (h *Host) markRunnerStopping(group RunnerGroup) {
	h.mu.Lock()
	lifecycle := h.runnerLifecycleLocked(group)
	if lifecycle.Phase != RunnerPhaseRestarting {
		lifecycle.Phase = RunnerPhaseStopping
	}
	h.mu.Unlock()
	h.notifyStatusChanges()
}

func (h *Host) markRunnerStopped(group RunnerGroup) {
	h.mu.Lock()
	lifecycle := h.runnerLifecycleLocked(group)
	lifecycle.Phase = RunnerPhaseStopped
	lifecycle.Started = false
	lifecycle.PythonSpawn = false
	lifecycle.IPCAddress = ""
	lifecycle.ReadyAt = time.Time{}
	h.mu.Unlock()
	h.notifyStatusChanges()
}

func (h *Host) markRunnerRestarting(group RunnerGroup) {
	h.mu.Lock()
	lifecycle := h.runnerLifecycleLocked(group)
	lifecycle.Phase = RunnerPhaseRestarting
	lifecycle.RestartCount++
	h.mu.Unlock()
	h.notifyStatusChanges()
}

// RunnerStatus 返回指定分组的生命周期快照
func (h *Host) RunnerStatus(group RunnerGroup) RunnerStatus {
	if h == nil {
		return RunnerStatus{}
	}
	if group == "" {
		group = RunnerGroupThirdParty
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	lifecycle := h.runnerLifecycles[group]
	if lifecycle == nil {
		lifecycle = &runnerLifecycle{Phase: RunnerPhaseStopped}
	}
	status := RunnerStatus{
		Group:        group,
		Phase:        lifecycle.Phase,
		IPCAddress:   lifecycle.IPCAddress,
		Started:      lifecycle.Started,
		PythonSpawn:  lifecycle.PythonSpawn,
		RestartCount: lifecycle.RestartCount,
		LastError:    lifecycle.LastError,
		StartedAt:    lifecycle.StartedAt,
		ReadyAt:      lifecycle.ReadyAt,
	}
	if ready, ok := h.runnerReadyByGroup[group]; ok {
		status.LoadedPlugins = append([]string(nil), ready.LoadedPlugins...)
	}
	return status
}

// AllRunnerStatuses 返回全部 Runner 分组状态
func (h *Host) AllRunnerStatuses() []RunnerStatus {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	groups := make([]RunnerGroup, 0, len(h.runnerLifecycles))
	for group := range h.runnerLifecycles {
		groups = append(groups, group)
	}
	h.mu.RUnlock()
	out := make([]RunnerStatus, 0, len(groups))
	for _, group := range groups {
		out = append(out, h.RunnerStatus(group))
	}
	return out
}

// WaitForRunnerReady 等待 Runner 上报 runner.ready，超时返回明确错误
func (h *Host) WaitForRunnerReady(ctx context.Context, group RunnerGroup, timeout time.Duration) (protocol.RunnerReadyPayload, error) {
	if h == nil {
		return protocol.RunnerReadyPayload{}, fmt.Errorf("Host 未初始化")
	}
	if group == "" {
		group = RunnerGroupThirdParty
	}
	if timeout <= 0 {
		timeout = h.RunnerSpawnTimeout()
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		h.mu.RLock()
		lifecycle := h.runnerLifecycles[group]
		phase := RunnerPhaseStopped
		lastError := ""
		if lifecycle != nil {
			phase = lifecycle.Phase
			lastError = lifecycle.LastError
		}
		ready, readyOK := h.runnerReadyByGroup[group]
		state := h.runners[group]
		connected := state != nil && state.Started
		h.mu.RUnlock()
		if state != nil && state.rpc != nil {
			if reason := state.rpc.LastHandshakeRejectionReason(); reason != "" {
				return protocol.RunnerReadyPayload{}, fmt.Errorf("等待 Runner 就绪失败: 握手被拒绝: %s", reason)
			}
		}
		if phase == RunnerPhaseRunning && readyOK {
			return ready, nil
		}
		if phase == RunnerPhaseFailed || (phase == RunnerPhaseStopped && !connected) {
			if lastError == "" {
				lastError = "Runner 未启动或已停止"
			}
			return protocol.RunnerReadyPayload{}, fmt.Errorf("等待 Runner 就绪失败: %s", lastError)
		}
		select {
		case <-ctx.Done():
			return protocol.RunnerReadyPayload{}, ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return protocol.RunnerReadyPayload{}, fmt.Errorf("等待 Runner 就绪超时（%s）", timeout)
			}
		}
	}
}

func (h *Host) markRunnerReadyForGroup(group RunnerGroup) {
	h.mu.Lock()
	lifecycle := h.runnerLifecycleLocked(group)
	lifecycle.Phase = RunnerPhaseRunning
	lifecycle.ReadyAt = time.Now()
	lifecycle.LastError = ""
	lifecycle.Started = true
	h.mu.Unlock()
	h.notifyStatusChanges()
}

// SetMaxRestartAttempts 设置自动重启上限
func (h *Host) SetMaxRestartAttempts(attempts int) {
	if attempts < 0 {
		attempts = 0
	}
	h.mu.Lock()
	h.maxRestartAttempts = attempts
	h.mu.Unlock()
}

func (h *Host) maxRestartAttemptsValue() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.maxRestartAttempts
}

func (h *Host) shouldRestart(group RunnerGroup) (bool, int, int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	lifecycle := h.runnerLifecycles[group]
	count := 0
	if lifecycle != nil {
		count = lifecycle.RestartCount
	}
	maxAttempts := h.maxRestartAttempts
	if maxAttempts < 0 {
		maxAttempts = 0
	}
	return count < maxAttempts, count, maxAttempts
}

func (h *Host) restartExceededError(count, max int) error {
	return fmt.Errorf("Runner 自动重启次数已达上限（%d/%d）", count, max)
}

func (h *Host) resetRestartCount(group RunnerGroup) {
	h.mu.Lock()
	lifecycle := h.runnerLifecycleLocked(group)
	lifecycle.RestartCount = 0
	h.mu.Unlock()
}
