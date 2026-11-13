package maisaka

import "maibot-go/internal/maisaka/turn_trigger"

// IdleBackoffConfig 对应 MaiBot chat.reply_timing 空闲退避参数
type IdleBackoffConfig = turn_trigger.IdleBackoffConfig

// IdleBackoff 维护连续空闲结束后的消息触发退避
type IdleBackoff = turn_trigger.IdleBackoff

// IdleBackoffController 按整轮结束原因驱动的退避控制器，对齐 idle_backoff.IdleBackoffController
type IdleBackoffController = turn_trigger.IdleBackoffController

// NewIdleBackoff 创建空闲退避控制器
func NewIdleBackoff(cfg IdleBackoffConfig) *IdleBackoff { return turn_trigger.NewIdleBackoff(cfg) }

// IsIdleCycleReason 判断整轮结束原因是否属于空闲退避，对齐 mode_policy.is_idle_cycle_reason
func IsIdleCycleReason(cycleEndReason string) bool {
	return turn_trigger.IsIdleCycleReason(cycleEndReason)
}
