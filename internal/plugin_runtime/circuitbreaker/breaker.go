// Package circuitbreaker 按插件 ID 隔离的运行时熔断器，对齐 src/plugin_runtime/host/circuit_breaker.py
package circuitbreaker

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// State 熔断状态名，对齐 CircuitStateName
type State string

const (
	StateClosed   State = "closed"
	StateOpen     State = "open"
	StateHalfOpen State = "half_open"
)

const (
	defaultFailureThreshold = 3
	defaultBaseCooldown     = 60 * time.Second
	defaultMaxCooldown      = 300 * time.Second
	defaultStableReset      = 600 * time.Second
	skipLogInterval         = 30 * time.Second
)

// Permit 一次插件调用的熔断许可
type Permit struct {
	PluginID      string
	ComponentName string
	Operation     string
	Allowed       bool
	HalfOpen      bool
	Reason        string
}

// Status 单个插件的熔断快照
type Status struct {
	State            State   `json:"state"`
	RemainingSec     float64 `json:"remaining_sec"`
	CooldownLevel    int     `json:"cooldown_level"`
	HalfOpenInflight bool    `json:"half_open_inflight"`
}

type circuitState struct {
	state               State
	consecutiveFailures int
	cooldownLevel       int
	openedUntil         time.Time
	halfOpenInflight    bool
	lastSuccessAt       time.Time
	lastRecoveredAt     time.Time
	lastSkipLogAt       time.Time
}

// Options 熔断器参数，零值字段取默认值
type Options struct {
	FailureThreshold int
	BaseCooldown     time.Duration
	MaxCooldown      time.Duration
	StableReset      time.Duration
	Now              func() time.Time
}

// Breaker 按插件 ID 隔离的运行时熔断器
type Breaker struct {
	mu               sync.Mutex
	failureThreshold int
	baseCooldown     time.Duration
	maxCooldown      time.Duration
	stableReset      time.Duration
	now              func() time.Time
	states           map[string]*circuitState
}

// New 构造熔断器
func New(opts Options) *Breaker {
	threshold := opts.FailureThreshold
	if threshold <= 0 {
		threshold = defaultFailureThreshold
	}
	base := opts.BaseCooldown
	if base < time.Second {
		base = defaultBaseCooldown
	}
	maxCooldown := opts.MaxCooldown
	if maxCooldown <= 0 {
		maxCooldown = defaultMaxCooldown
	}
	if maxCooldown < base {
		maxCooldown = base
	}
	stable := opts.StableReset
	if stable < time.Second {
		stable = defaultStableReset
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Breaker{
		failureThreshold: threshold,
		baseCooldown:     base,
		maxCooldown:      maxCooldown,
		stableReset:      stable,
		now:              now,
		states:           make(map[string]*circuitState),
	}
}

var (
	defaultMu      sync.Mutex
	defaultBreaker = New(Options{})
)

// Default 返回插件运行时全局熔断器
func Default() *Breaker {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	return defaultBreaker
}

// ResetDefault 重建全局熔断器，仅供测试使用
func ResetDefault() {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultBreaker = New(Options{})
}

// TryAcquire 尝试为一次插件调用取得执行许可
func (b *Breaker) TryAcquire(pluginID, componentName, operation string) (Permit, error) {
	normalized := strings.TrimSpace(pluginID)
	if normalized == "" {
		return Permit{}, fmt.Errorf("plugin_id 不能为空")
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	state := b.stateFor(normalized)
	if state.state == StateOpen && now.Before(state.openedUntil) {
		remaining := state.openedUntil.Sub(now)
		b.logSkipIfNeeded(normalized, componentName, operation, remaining, state, now)
		return Permit{
			PluginID:      normalized,
			ComponentName: componentName,
			Operation:     operation,
			Reason:        fmt.Sprintf("插件熔断中，剩余 %.1fs", remaining.Seconds()),
		}, nil
	}
	if state.state == StateOpen && !now.Before(state.openedUntil) {
		state.state = StateHalfOpen
		state.halfOpenInflight = false
		zap.L().Info("插件熔断冷却结束，进入半开测试", zap.String("plugin_id", normalized))
	}
	if state.state == StateHalfOpen {
		if state.halfOpenInflight {
			return Permit{
				PluginID:      normalized,
				ComponentName: componentName,
				Operation:     operation,
				Reason:        "插件半开测试已有进行中的调用",
			}, nil
		}
		state.halfOpenInflight = true
		return Permit{
			PluginID:      normalized,
			ComponentName: componentName,
			Operation:     operation,
			Allowed:       true,
			HalfOpen:      true,
		}, nil
	}
	return Permit{
		PluginID:      normalized,
		ComponentName: componentName,
		Operation:     operation,
		Allowed:       true,
	}, nil
}

// RecordSuccess 记录一次插件调用成功
func (b *Breaker) RecordSuccess(permit Permit) {
	if !permit.Allowed {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	state := b.stateFor(permit.PluginID)
	wasHalfOpen := state.state == StateHalfOpen || permit.HalfOpen
	b.resetCooldownIfStable(state, now)
	state.consecutiveFailures = 0
	state.halfOpenInflight = false
	state.lastSuccessAt = now
	if wasHalfOpen {
		state.state = StateClosed
		state.lastRecoveredAt = now
		zap.L().Info("插件半开测试成功，熔断已恢复", zap.String("plugin_id", permit.PluginID))
	}
}

// RecordFailure 记录一次可熔断失败
func (b *Breaker) RecordFailure(permit Permit, reason string) {
	if !permit.Allowed {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	state := b.stateFor(permit.PluginID)
	b.resetCooldownIfStable(state, now)
	state.halfOpenInflight = false
	if permit.HalfOpen || state.state == StateHalfOpen {
		b.openCircuit(permit, reason, state, now)
		return
	}
	state.consecutiveFailures++
	if state.consecutiveFailures < b.failureThreshold {
		zap.L().Warn("插件调用失败，熔断计数",
			zap.String("plugin_id", permit.PluginID),
			zap.Int("failures", state.consecutiveFailures),
			zap.Int("threshold", b.failureThreshold),
			zap.String("reason", reason))
		return
	}
	b.openCircuit(permit, reason, state, now)
}

// Statuses 返回当前存在熔断状态的插件快照
func (b *Breaker) Statuses() map[string]Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	out := make(map[string]Status, len(b.states))
	for pluginID, state := range b.states {
		switch state.state {
		case StateOpen:
			remaining := state.openedUntil.Sub(now)
			if remaining <= 0 {
				out[pluginID] = Status{
					State:            StateHalfOpen,
					RemainingSec:     0,
					CooldownLevel:    state.cooldownLevel,
					HalfOpenInflight: state.halfOpenInflight,
				}
				continue
			}
			out[pluginID] = Status{
				State:            StateOpen,
				RemainingSec:     remaining.Seconds(),
				CooldownLevel:    state.cooldownLevel,
				HalfOpenInflight: state.halfOpenInflight,
			}
		case StateHalfOpen:
			out[pluginID] = Status{
				State:            StateHalfOpen,
				RemainingSec:     0,
				CooldownLevel:    state.cooldownLevel,
				HalfOpenInflight: state.halfOpenInflight,
			}
		}
	}
	return out
}

func (b *Breaker) stateFor(pluginID string) *circuitState {
	state, ok := b.states[pluginID]
	if !ok {
		state = &circuitState{state: StateClosed}
		b.states[pluginID] = state
	}
	return state
}

func (b *Breaker) openCircuit(permit Permit, reason string, state *circuitState, now time.Time) {
	cooldown := b.baseCooldown
	for i := 0; i < state.cooldownLevel && cooldown < b.maxCooldown; i++ {
		cooldown *= 2
	}
	if cooldown > b.maxCooldown {
		cooldown = b.maxCooldown
	}
	state.state = StateOpen
	state.consecutiveFailures = 0
	state.cooldownLevel++
	state.openedUntil = now.Add(cooldown)
	state.halfOpenInflight = false
	state.lastSkipLogAt = time.Time{}
	zap.L().Warn("插件已熔断",
		zap.String("plugin_id", permit.PluginID),
		zap.Duration("cooldown", cooldown),
		zap.String("component", permit.ComponentName),
		zap.String("operation", permit.Operation),
		zap.String("reason", reason))
}

func (b *Breaker) resetCooldownIfStable(state *circuitState, now time.Time) {
	if state.lastRecoveredAt.IsZero() {
		return
	}
	if now.Sub(state.lastRecoveredAt) < b.stableReset {
		return
	}
	state.cooldownLevel = 0
	state.lastRecoveredAt = time.Time{}
}

func (b *Breaker) logSkipIfNeeded(pluginID, componentName, operation string, remaining time.Duration, state *circuitState, now time.Time) {
	if !state.lastSkipLogAt.IsZero() && now.Sub(state.lastSkipLogAt) < skipLogInterval {
		return
	}
	state.lastSkipLogAt = now
	zap.L().Warn("插件熔断中，跳过调用",
		zap.String("plugin_id", pluginID),
		zap.String("component", componentName),
		zap.String("operation", operation),
		zap.Float64("remaining_sec", remaining.Seconds()))
}
