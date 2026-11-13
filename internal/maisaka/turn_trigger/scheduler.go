package turn_trigger

import (
	"fmt"

	"go.uber.org/zap"
)

// MessageTurnScheduler 决定外部消息何时进入 Maisaka 内部循环
//
// 每次检查依次经过：消息防抖 → 快速反应续接 wait → 静默消费 → @ 强制触发 → 空闲退避 → 动态门控
// 每一步只返回判定，由 ScheduleMessageTurn 统一投递或延迟重查
type MessageTurnScheduler struct {
	runtime          RuntimeView
	dynamicReplyGate *DynamicReplyTurnGate
	idleBackoff      *IdleBackoffController
}

// NewMessageTurnScheduler 创建触发调度器
func NewMessageTurnScheduler(runtime RuntimeView) *MessageTurnScheduler {
	return &MessageTurnScheduler{
		runtime:          runtime,
		dynamicReplyGate: NewDynamicReplyTurnGate(runtime),
		idleBackoff:      NewIdleBackoffController(runtime),
	}
}

// DynamicReplyGate 暴露动态门控，供 agent 侧记录 reply 与强制触发
func (s *MessageTurnScheduler) DynamicReplyGate() *DynamicReplyTurnGate {
	return s.dynamicReplyGate
}

// IdleBackoff 暴露空闲退避控制器，供整轮结束时记录结束原因
func (s *MessageTurnScheduler) IdleBackoff() *IdleBackoffController {
	return s.idleBackoff
}

// RecordReply 记录 Planner 实际调用了一次 reply，供动态门控统计回复次数
func (s *MessageTurnScheduler) RecordReply() {
	s.dynamicReplyGate.RecordReply()
}

// ScheduleMessageTurn 检查并投递 message turn
func (s *MessageTurnScheduler) ScheduleMessageTurn() {
	runtime := s.runtime
	if runtime.AgentState() == StateWait {
		if runtime.IsReplyFrequencySilent() {
			runtime.EnterStopState()
		} else if !runtime.PlannerMessageSteering() {
			if runtime.IsGroupSession() {
				return
			}
			zap.L().Info(runtime.LogPrefix() + " 私聊 wait 期间收到新消息，结束等待并进入 Planner")
			runtime.EnterRunningState()
		}
	}

	if runtime.MessageTurnScheduled() {
		return
	}

	pendingCount := runtime.PendingMessageCount()
	if pendingCount <= 0 {
		return
	}

	result := s.Evaluate(pendingCount)
	if result.Decision == DecisionDelay {
		zap.L().Debug(runtime.LogPrefix()+" 回复调度",
			zap.Int("待处理", pendingCount),
			zap.String("detail", result.Detail),
		)
		runtime.DeferMessageTurnCheck(result.DelaySeconds)
		return
	}

	zap.L().Info(runtime.LogPrefix()+" 回复调度",
		zap.Int("待处理", pendingCount),
		zap.String("detail", result.Detail),
	)
	if result.ShouldTrigger() {
		if runtime.AgentState() == StateWait {
			runtime.CancelWaitTimeout()
			// 保留挂起状态到消费触发时，避免原工具批次收尾清掉 wait 调用关联
			runtime.UpdateStageStatus("等待已打断", "收到新消息，准备继续思考")
		}
		runtime.EnqueueMessageTurn()
	}
}

// Evaluate 按流水线顺序返回第一个给出结论的判定
func (s *MessageTurnScheduler) Evaluate(pendingCount int) TurnGateResult {
	runtime := s.runtime
	// 先收齐本批：后续门控按整批消息判断，防止只凭半批消息估计回复概率
	quietSeconds := runtime.MessageQuietRemainingSeconds()
	if quietSeconds > 0 {
		return TurnGateResult{
			Decision:     DecisionDelay,
			Detail:       fmt.Sprintf("等待消息防抖收齐本批 剩余=%.3fs", quietSeconds),
			DelaySeconds: quietSeconds,
		}
	}

	// 走到这里仍处于 wait 的只有快速反应模式：直接续接挂起的 wait，不再经过群聊频率与退避门控
	if runtime.AgentState() == StateWait {
		return TurnGateResult{Decision: DecisionTrigger, Detail: "快速反应模式收到新消息，提前结束 wait 并进入 Planner"}
	}

	frequency := runtime.EffectiveReplyFrequency()
	if runtime.IsReplyFrequencySilent() {
		return TurnGateResult{Decision: DecisionTrigger, Detail: fmt.Sprintf("调度=静默消费 频率=%.3f", frequency)}
	}

	if runtime.HasForcedTurnTrigger() {
		// @ 强制触发的回复同样占用动态门控的回复额度
		s.dynamicReplyGate.RecordForcedTurn(runtime.PendingMessages())
		return TurnGateResult{Decision: DecisionTrigger, Detail: fmt.Sprintf("调度=强制触发 频率=%.3f", frequency)}
	}

	backoffSeconds := s.idleBackoff.GetDelaySeconds(pendingCount)
	if backoffSeconds > 0 {
		return TurnGateResult{
			Decision:     DecisionDelay,
			Detail:       fmt.Sprintf("空闲退避中 剩余=%.2fs", backoffSeconds),
			DelaySeconds: backoffSeconds,
		}
	}

	dynamicResult := s.dynamicReplyGate.Evaluate(runtime.PendingMessages(), frequency)
	return TurnGateResult{
		Decision: dynamicResult.Decision,
		Detail:   fmt.Sprintf("频率=%.3f %s", frequency, dynamicResult.Detail),
	}
}
