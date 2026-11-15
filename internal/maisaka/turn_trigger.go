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

// ScheduleTurnInput 对应 MessageTurnScheduler._evaluate 的输入快照
type ScheduleTurnInput struct {
	MessageQuietRemaining  time.Duration
	AgentInWaitState       bool
	PlannerMessageSteering bool
	IsGroupSession         bool
	ReplyFrequencySilent   bool
	ReplyFrequency         float64
	HasForcedTurnTrigger   bool
	IdleBackoffRemaining   time.Duration
	Dynamic                TurnGateResult
}

// EvaluateMessageTurn 按 MaiBot 顺序评估：防抖 → wait 续接 → 静默消费 → @ 强制 → 空闲退避 → 动态门控
//
// 无状态版本，供 agent 侧已有调度路径使用；完整 runtime 绑定的流水线见 turn_trigger.MessageTurnScheduler
func EvaluateMessageTurn(in ScheduleTurnInput) TurnGateResult {
	if in.MessageQuietRemaining > 0 {
		return TurnGateResult{
			Decision: TurnDelay,
			Detail:   fmt.Sprintf("等待消息防抖收齐本批 剩余=%s", in.MessageQuietRemaining.Round(time.Millisecond)),
			Delay:    in.MessageQuietRemaining,
		}
	}
	// 对齐 Python MessageTurnScheduler._evaluate：走到此处仍 wait 的只剩快速反应续接。
	// 群聊非 steering 的 wait 拦截在 schedule 预检（shouldHoldGroupScheduleDuringWait），不在本函数再要求 PlannerMessageSteering。
	if in.AgentInWaitState {
		return TurnGateResult{
			Decision: TurnTrigger,
			Detail:   "快速反应模式收到新消息，提前结束 wait 并进入 Planner",
		}
	}
	if in.ReplyFrequencySilent {
		return TurnGateResult{
			Decision: TurnTrigger,
			Detail:   fmt.Sprintf("调度=静默消费 频率=%.3f", in.ReplyFrequency),
		}
	}
	if in.HasForcedTurnTrigger {
		return TurnGateResult{
			Decision: TurnTrigger,
			Detail:   fmt.Sprintf("调度=强制触发 频率=%.3f", in.ReplyFrequency),
		}
	}
	if in.IdleBackoffRemaining > 0 {
		return TurnGateResult{
			Decision: TurnDelay,
			Detail:   fmt.Sprintf("空闲退避中 剩余=%s", in.IdleBackoffRemaining.Round(10*time.Millisecond)),
			Delay:    in.IdleBackoffRemaining,
		}
	}
	dynamic := in.Dynamic
	if dynamic.Decision == "" {
		dynamic = TurnGateResult{Decision: TurnWait, Detail: "动态门控未提供判定"}
	}
	return TurnGateResult{
		Decision: dynamic.Decision,
		Detail:   fmt.Sprintf("频率=%.3f %s", in.ReplyFrequency, dynamic.Detail),
	}
}

// PrivateChatDynamicGate 私聊不设动态门控
func PrivateChatDynamicGate() TurnGateResult {
	return TurnGateResult{
		Decision: TurnTrigger,
		Detail:   "私聊不设动态门控 判定=进入Planner",
	}
}

// NoExternalMessagesGate 没有新的外部消息时等待
func NoExternalMessagesGate() TurnGateResult {
	return TurnGateResult{
		Decision: TurnWait,
		Detail:   "没有新的外部消息 判定=等待更多消息",
	}
}

// DynamicGateFromProbability 把概率门控结果包装成流水线可用的动态步骤
func DynamicGateFromProbability(probability, threshold float64, shouldTrigger bool) TurnGateResult {
	label := "等待更多消息"
	decision := TurnWait
	if shouldTrigger {
		label = "进入Planner"
		decision = TurnTrigger
	}
	return TurnGateResult{
		Decision: decision,
		Detail: fmt.Sprintf(
			"动态门控: 概率=%.2f 阈值=%.2f 判定=%s",
			probability, threshold, label,
		),
	}
}
