package turn_trigger

import (
	"fmt"
	"math"
	"time"

	"go.uber.org/zap"
)

// IdleCycleReasons 属于空闲退避的整轮结束原因，对齐 mode_policy.IDLE_CYCLE_REASONS
var IdleCycleReasons = map[string]struct{}{
	"planner_no_tool_end":       {},
	"tool_pause:wait":           {},
	"tool_stop_after_execution": {},
}

// IsIdleCycleReason 判断整轮结束原因是否属于空闲退避
//
// 定义在 turn_trigger 内是因为 idle_backoff 是唯一消费方，root 包再导出，避免 root↔turn_trigger 循环依赖
func IsIdleCycleReason(cycleEndReason string) bool {
	_, ok := IdleCycleReasons[cycleEndReason]
	return ok
}

// IdleBackoffConfig 对应 chat.reply_timing 的空闲退避参数
type IdleBackoffConfig struct {
	BaseSeconds        float64
	CapSeconds         float64
	StartCount         int
	BypassPendingCount int
}

// IdleBackoff 维护连续空闲结束后的消息触发退避状态，对应 runtime._idle_backoff
type IdleBackoff struct {
	cfg   IdleBackoffConfig
	count int
	until time.Time
}

// NewIdleBackoff 创建空闲退避控制器
func NewIdleBackoff(cfg IdleBackoffConfig) *IdleBackoff {
	return &IdleBackoff{cfg: cfg}
}

// SetConfig 热重载时更新退避参数
func (b *IdleBackoff) SetConfig(cfg IdleBackoffConfig) {
	if b == nil {
		return
	}
	b.cfg = cfg
}

// Reset 清理退避状态
func (b *IdleBackoff) Reset() {
	if b == nil {
		return
	}
	b.count = 0
	b.until = time.Time{}
}

// Count 返回连续空闲次数
func (b *IdleBackoff) Count() int {
	if b == nil {
		return 0
	}
	return b.count
}

// RecordIdleCycle 在整轮以空闲原因结束时递增退避
func (b *IdleBackoff) RecordIdleCycle(now time.Time, isGroupSession bool) {
	if b == nil || !isGroupSession {
		b.Reset()
		return
	}
	b.count++
	backoff := b.backoffSeconds()
	if backoff <= 0 {
		b.until = time.Time{}
		return
	}
	b.until = now.Add(time.Duration(backoff * float64(time.Second)))
}

// RecordActiveCycle 非空闲结束时清零
func (b *IdleBackoff) RecordActiveCycle() {
	b.Reset()
}

func (b *IdleBackoff) backoffSeconds() float64 {
	base := math.Max(0, b.cfg.BaseSeconds)
	capSeconds := math.Max(0, b.cfg.CapSeconds)
	if base <= 0 || capSeconds <= 0 {
		return 0
	}
	start := b.cfg.StartCount
	if start < 1 {
		start = 1
	}
	if b.count < start {
		return 0
	}
	exponent := b.count - start
	if exponent < 0 {
		exponent = 0
	}
	return math.Min(capSeconds, base*math.Pow(2, float64(exponent)))
}

// RemainingDelay 返回当前还需推迟触发的时长；focus 模式或私聊时不退避
func (b *IdleBackoff) RemainingDelay(now time.Time, pendingCount int, isGroupSession, focusActive bool) time.Duration {
	if b == nil || focusActive || !isGroupSession || b.until.IsZero() {
		return 0
	}
	remaining := b.until.Sub(now)
	if remaining <= 0 {
		b.until = time.Time{}
		return 0
	}
	if b.cfg.BypassPendingCount > 0 && pendingCount >= b.cfg.BypassPendingCount {
		return 0
	}
	return remaining
}

// IdleBackoffController 维护连续空闲结束后的消息触发退避，对齐 idle_backoff.IdleBackoffController
type IdleBackoffController struct {
	runtime RuntimeView
	backoff *IdleBackoff
	now     func() time.Time
}

// NewIdleBackoffController 从 runtime 快照参数创建退避控制器
func NewIdleBackoffController(runtime RuntimeView) *IdleBackoffController {
	return &IdleBackoffController{
		runtime: runtime,
		backoff: NewIdleBackoff(runtime.ReplyTiming()),
	}
}

// Backoff 暴露底层退避状态，供 agent 侧热重载与查询
func (c *IdleBackoffController) Backoff() *IdleBackoff { return c.backoff }

// Reset 清理连续空闲退避状态
func (c *IdleBackoffController) Reset() { c.backoff.Reset() }

// RecordCycleResult 按整轮结束原因维护空闲退避状态
func (c *IdleBackoffController) RecordCycleResult(cycleEndReason string) {
	if !IsIdleCycleReason(cycleEndReason) {
		c.backoff.Reset()
		return
	}
	if !c.runtime.IsGroupSession() {
		c.backoff.Reset()
		return
	}
	c.backoff.RecordIdleCycle(c.nowOrNow(), true)
	if c.backoff.Count() > 0 && c.backoff.backoffSeconds() > 0 {
		zap.L().Info(fmt.Sprintf("%s 空闲退避 来源=planner 连续=%d次 等待=%gs",
			c.runtime.LogPrefix(), c.backoff.Count(), c.backoff.backoffSeconds(),
		))
	}
}

// GetDelaySeconds 返回当前消息触发还需被空闲退避推迟的秒数；不退避时为 0
func (c *IdleBackoffController) GetDelaySeconds(pendingCount int) float64 {
	if c.runtime.FocusModeActive() {
		c.backoff.Reset()
		return 0
	}
	if !c.runtime.IsGroupSession() {
		return 0
	}
	remaining := c.backoff.RemainingDelay(c.nowOrNow(), pendingCount, true, false)
	if remaining <= 0 {
		return 0
	}
	bypass := c.backoff.cfg.BypassPendingCount
	if bypass > 0 && pendingCount >= bypass {
		zap.L().Info(c.runtime.LogPrefix()+" 空闲退避被待处理消息数绕过",
			zap.Int("待处理", pendingCount),
			zap.Int("阈值", bypass),
		)
		return 0
	}
	return remaining.Seconds()
}

// SetClock 注入时钟，主要供测试
func (c *IdleBackoffController) SetClock(now func() time.Time) {
	if now != nil {
		c.now = now
	}
}

func (c *IdleBackoffController) nowOrNow() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}
