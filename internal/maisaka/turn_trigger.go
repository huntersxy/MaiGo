package maisaka

import (
	"fmt"
	"time"

	"MaiGo/internal/maisaka/turn_trigger"
)

// TurnGateDecision 单个门控步骤的判定
type TurnGateDecision = turn_trigger.TurnGateDecision

const (
	// TurnTrigger 放行进入 Planner
	TurnTrigger = turn_trigger.DecisionTrigger
	// TurnWait 继续等待更多消息
	TurnWait = turn_trigger.DecisionWait
	// TurnDelay 延迟后重新检查
	TurnDelay = turn_trigger.DecisionDelay
)

// TurnGateResult 触发调度流水线某一步的结论
//
// 与 turn_trigger.TurnGateResult 的差异：这里用 time.Duration 暴露延迟，便于 agent 侧直接定时
type TurnGateResult struct {
	Decision TurnGateDecision
	Detail   string
	Delay    time.Duration
}

// ShouldTrigger 是否应投递 message turn
func (r TurnGateResult) ShouldTrigger() bool { return r.Decision == TurnTrigger }

// FromTurnTriggerResult 转换 turn_trigger 判定
func FromTurnTriggerResult(result turn_trigger.TurnGateResult) TurnGateResult {
	return TurnGateResult{
		Decision: result.Decision,
		Detail:   result.Detail,
		Delay:    result.Delay(),
	}
}

func EvaluateMessageTurn(quiet time.Duration, frequency float64, forced bool, evaluateScene func() TurnGateResult) TurnGateResult {
	if quiet > 0 {
		return TurnGateResult{
			Decision: TurnDelay,
			Detail:   fmt.Sprintf("等待消息防抖收齐本批 剩余=%s", quiet.Round(time.Millisecond)),
			Delay:    quiet,
		}
	}
	if frequency <= 0 {
		return TurnGateResult{
			Decision: TurnTrigger,
			Detail:   fmt.Sprintf("调度=静默消费 频率=%.3f", frequency),
		}
	}
	if forced {
		return TurnGateResult{
			Decision: TurnTrigger,
			Detail:   fmt.Sprintf("调度=强制触发 频率=%.3f", frequency),
		}
	}
	return evaluateScene()
}

// NoExternalMessagesGate 没有新的外部消息时等待
func NoExternalMessagesGate() TurnGateResult {
	return TurnGateResult{
		Decision: TurnWait,
		Detail:   "没有新的外部消息 判定=等待更多消息",
	}
}
