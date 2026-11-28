package turn_trigger

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
)

type MessageTurnScheduler struct {
	runtime RuntimeView

	mu                      sync.Mutex
	viewFrequency           ViewFrequencyController
	group                   groupRoundState
	lastBatch               []string
	acceptedWaitStartedAt   *time.Time
	silenceTriggered        bool
	silenceHandledAt        time.Time
	silenceCancel           context.CancelFunc
	silenceGeneration       uint64
	currentContextInterval  *float64
	previousContextInterval *float64
	now                     func() time.Time
}

func NewMessageTurnScheduler(runtime RuntimeView) *MessageTurnScheduler {
	return &MessageTurnScheduler{runtime: runtime}
}

func (s *MessageTurnScheduler) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	s.mu.Lock()
	s.now = now
	s.mu.Unlock()
}

func (s *MessageTurnScheduler) AllowsTimedWakeup() bool {
	return s.group.allowsTimedWakeup()
}

func (s *MessageTurnScheduler) RoundDebt() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.group.roundDebt
}
func (s *MessageTurnScheduler) ScheduleMessageTurn() {
	runtime := s.runtime
	s.mu.Lock()
	inPlannerTurn := s.group.inPlannerTurn
	s.mu.Unlock()
	state := runtime.AgentState()
	if !inPlannerTurn && state == StateRunning {
		return
	}
	if state == StateWait && runtime.IsReplyFrequencySilent() {
		runtime.EnterStopState()
		state = StateStop
	}
	if runtime.MessageTurnScheduled() || runtime.PendingMessageCount() <= 0 {
		return
	}
	result := s.Evaluate(runtime.PendingMessageCount())
	if result.Decision == DecisionDelay {
		zap.L().Debug(runtime.LogPrefix()+" 查看调度",
			zap.Int("待处理", runtime.PendingMessageCount()),
			zap.String("detail", result.Detail))
		runtime.DeferMessageTurnCheck(result.DelaySeconds)
		return
	}
	zap.L().Debug(runtime.LogPrefix()+" 查看调度",
		zap.Int("待处理", runtime.PendingMessageCount()),
		zap.String("detail", result.Detail))
	if !result.ShouldTrigger() {
		return
	}
	if state == StateWait {
		runtime.CancelWaitTimeout()
		runtime.UpdateStageStatus("等待已打断", "收到新消息，准备继续思考")
	}
	runtime.EnqueueMessageTurn()
}

func (s *MessageTurnScheduler) Evaluate(pendingCount int) TurnGateResult {
	runtime := s.runtime
	quietSeconds := runtime.MessageQuietRemainingSeconds()
	if quietSeconds > 0 {
		return TurnGateResult{
			Decision:     DecisionDelay,
			Detail:       fmt.Sprintf("等待消息防抖收齐本批 剩余=%.3fs", quietSeconds),
			DelaySeconds: quietSeconds,
		}
	}

	frequency := runtime.EffectiveReplyFrequency()
	state := runtime.AgentState()
	if state == StateWait && !runtime.IsReplyFrequencySilent() {
		if !runtime.IsGroupSession() {
			s.setAcceptedWait()
			return TurnGateResult{Decision: DecisionTrigger, Detail: "私聊收到新消息，提前结束 wait 并进入 Planner"}
		}
		return s.evaluateScene(pendingCount, frequency)
	}
	if runtime.IsReplyFrequencySilent() {
		return TurnGateResult{Decision: DecisionTrigger, Detail: fmt.Sprintf("调度=静默消费 频率=%.3f", frequency)}
	}
	return s.evaluateScene(pendingCount, frequency)
}

func (s *MessageTurnScheduler) evaluateScene(pendingCount int, frequency float64) TurnGateResult {
	runtime := s.runtime
	if runtime.HasForcedTurnTrigger() {
		return TurnGateResult{Decision: DecisionTrigger, Detail: fmt.Sprintf("调度=强制触发 频率=%.3f", frequency)}
	}
	external := filterExternalMessages(runtime, runtime.PendingMessages())
	if len(external) == 0 {
		return TurnGateResult{Decision: DecisionWait, Detail: "没有新的外部消息 判定=等待更多消息"}
	}
	if !runtime.IsGroupSession() {
		return TurnGateResult{Decision: DecisionTrigger, Detail: "私聊不设概率门控 判定=进入Planner"}
	}
	batch := make([]string, 0, len(external))
	for _, message := range external {
		batch = append(batch, message.MessageID)
	}
	s.mu.Lock()
	if equalMessageIDs(batch, s.lastBatch) {
		s.mu.Unlock()
		return TurnGateResult{Decision: DecisionWait, Detail: "本批已判定，等待新批次或静默判定"}
	}
	s.lastBatch = append(s.lastBatch[:0], batch...)
	probability := s.viewFrequency.Probability(frequency)
	accepted := s.viewFrequency.Decide(frequency, rand.Float64())
	internal := s.viewFrequency.Internal
	phase := s.viewFrequency.Phase
	s.mu.Unlock()
	runtime.ConsumeMessageDebounce()
	if accepted {
		if paid, debt := s.group.applyDebtPayoff(s); paid {
			s.armSilenceCheck()
			return TurnGateResult{Decision: DecisionWait, Detail: fmt.Sprintf("本批查看抵扣 1 轮欠债 剩余=%d", debt)}
		}
	}
	s.armSilenceCheck()
	label := "等待更多消息"
	decision := DecisionWait
	if accepted {
		if runtime.AgentState() == StateWait {
			s.mu.Lock()
			s.acceptedWaitStartedAt = cloneTimestamp(runtime.PendingWaitStartedAt())
			s.mu.Unlock()
		}
		label = "进入Planner"
		decision = DecisionTrigger
	}
	return TurnGateResult{
		Decision: decision,
		Detail: fmt.Sprintf("概率查看 外部频率=%.3f 内部频率=%.3f 放行概率=%.3f 阶段=%s 判定=%s",
			frequency, internal, probability, phase, label),
	}
}

func (s *MessageTurnScheduler) ObserveMessage() {
	if s.runtime.IsGroupSession() {
		s.armSilenceCheck()
	}
}

func (s *MessageTurnScheduler) StartPlannerTurn(logicalTurnID string) int {
	s.Close()
	return s.group.startPlannerTurn(s, logicalTurnID)
}

func (s *MessageTurnScheduler) RecordPlannerRun() {
	if !s.runtime.IsGroupSession() || s.runtime.FocusModeActive() {
		return
	}
	interval := s.contextInterval()
	s.mu.Lock()
	s.previousContextInterval = cloneFloat(s.currentContextInterval)
	s.currentContextInterval = interval
	s.viewFrequency.RecordPlannerRun()
	s.mu.Unlock()
	s.group.recordPlannerRun(s)
}

func (s *MessageTurnScheduler) FinishPlannerTurn() {
	s.group.finishPlannerTurn(s)
	if !s.runtime.RuntimeRunning() {
		return
	}
	s.ScheduleMessageTurn()
	s.armSilenceCheck()
}

func (s *MessageTurnScheduler) ConsumeSilenceTrigger() bool {
	s.mu.Lock()
	triggered := s.silenceTriggered
	s.silenceTriggered = false
	s.mu.Unlock()
	return triggered
}

func (s *MessageTurnScheduler) AcceptedWaitStartedAt() *time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneTimestamp(s.acceptedWaitStartedAt)
}

func (s *MessageTurnScheduler) setAcceptedWait() {
	startedAt := s.runtime.PendingWaitStartedAt()
	s.mu.Lock()
	s.acceptedWaitStartedAt = cloneTimestamp(startedAt)
	s.mu.Unlock()
}

func (s *MessageTurnScheduler) ConsumeAcceptedWaitStartedAt() *time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	startedAt := cloneTimestamp(s.acceptedWaitStartedAt)
	s.acceptedWaitStartedAt = nil
	return startedAt
}

func (s *MessageTurnScheduler) Close() {
	s.mu.Lock()
	s.silenceGeneration++
	if s.silenceCancel != nil {
		s.silenceCancel()
		s.silenceCancel = nil
	}
	s.mu.Unlock()
}

func (s *MessageTurnScheduler) contextInterval() *float64 {
	limit := s.runtime.MaxContextSize()
	if limit <= 0 {
		return nil
	}
	received := make([]float64, 0, limit)
	history := s.runtime.ChatHistory()
	counted := 0
	for index := len(history) - 1; index >= 0 && counted < limit; index-- {
		message := history[index]
		if !message.CountInContext {
			continue
		}
		if message.Source == "user" && message.ReceivedAt != nil {
			received = append(received, unixSeconds(*message.ReceivedAt))
		}
		counted++
	}
	for _, message := range filterExternalMessages(s.runtime, s.runtime.PendingMessages()) {
		if message.ReceivedAt != nil {
			received = append(received, unixSeconds(*message.ReceivedAt))
		}
	}
	sort.Float64s(received)
	if len(received) > limit {
		received = received[len(received)-limit:]
	}
	if interval, ok := MeanMessageInterval(received); ok {
		return &interval
	}
	return nil
}

func (s *MessageTurnScheduler) armSilenceCheck() {
	s.mu.Lock()
	s.silenceGeneration++
	generation := s.silenceGeneration
	if s.silenceCancel != nil {
		s.silenceCancel()
		s.silenceCancel = nil
	}
	s.mu.Unlock()
	runtime := s.runtime
	s.mu.Lock()
	inPlannerTurn := s.group.inPlannerTurn
	allowsWakeup := s.group.allowsTimedWakeup()
	s.mu.Unlock()
	lastReceived := runtime.LastMessageReceivedAt()
	if !runtime.RuntimeRunning() || inPlannerTurn || !allowsWakeup || !runtime.IsGroupSession() || runtime.AgentState() == StateRunning || runtime.FocusModeActive() ||
		runtime.MessageTurnScheduled() || runtime.IsReplyFrequencySilent() || lastReceived.IsZero() {
		return
	}
	s.mu.Lock()
	if !s.silenceHandledAt.IsZero() && !lastReceived.After(s.silenceHandledAt) {
		s.mu.Unlock()
		return
	}
	previous := cloneFloat(s.previousContextInterval)
	s.mu.Unlock()
	current := s.contextInterval()
	interval, ok := PredictMessageInterval(valueOrZero(current), valueOrZero(previous), current != nil, previous != nil)
	if !ok || interval <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if generation != s.silenceGeneration {
		s.mu.Unlock()
		cancel()
		return
	}
	s.silenceCancel = cancel
	s.mu.Unlock()
	go s.checkSilence(ctx, generation, lastReceived, interval)
}

func (s *MessageTurnScheduler) checkSilence(ctx context.Context, generation uint64, receivedAt time.Time, interval float64) {
	deadline := receivedAt.Add(time.Duration(interval * float64(time.Second)))
	for {
		delay := deadline.Sub(s.currentTime())
		if delay < 0 {
			delay = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		runtime := s.runtime
		s.mu.Lock()
		allowsWakeup := s.group.allowsTimedWakeup()
		s.mu.Unlock()
		if !runtime.LastMessageReceivedAt().Equal(receivedAt) || !allowsWakeup || runtime.AgentState() == StateRunning ||
			runtime.FocusModeActive() || runtime.MessageTurnScheduled() || runtime.IsReplyFrequencySilent() {
			return
		}
		if quiet := runtime.MessageQuietRemainingSeconds(); quiet > 0 {
			deadline = s.currentTime().Add(time.Duration(quiet * float64(time.Second)))
			continue
		}
		frequency := runtime.EffectiveReplyFrequency()
		now := s.currentTime()
		s.mu.Lock()
		if generation != s.silenceGeneration {
			s.mu.Unlock()
			return
		}
		probability := s.viewFrequency.Probability(frequency)
		accepted := s.viewFrequency.Decide(frequency, rand.Float64())
		if accepted {
			s.silenceHandledAt = receivedAt
			s.silenceTriggered = true
			if runtime.AgentState() == StateWait {
				s.acceptedWaitStartedAt = cloneTimestamp(runtime.PendingWaitStartedAt())
			}
			s.silenceCancel = nil
		}
		phase := s.viewFrequency.Phase
		internal := s.viewFrequency.Internal
		if !accepted {
			deadline = now.Add(time.Duration(interval * float64(time.Second)))
		}
		s.mu.Unlock()
		zap.L().Debug(runtime.LogPrefix()+" 静默查看判定",
			zap.Float64("间隔", interval), zap.Float64("概率", probability), zap.Bool("放行", accepted),
			zap.Float64("内部频率", internal), zap.String("阶段", string(phase)))
		if accepted {
			if runtime.AgentState() == StateWait {
				runtime.CancelWaitTimeout()
			}
			runtime.EnqueueMessageTurn()
			return
		}
	}
}

func (s *MessageTurnScheduler) currentTime() time.Time {
	s.mu.Lock()
	now := s.now
	s.mu.Unlock()
	if now != nil {
		return now()
	}
	return time.Now()
}

func filterExternalMessages(runtime RuntimeView, messages []PendingMessage) []PendingMessage {
	accounts := make(map[string]map[string]struct{})
	out := make([]PendingMessage, 0, len(messages))
	for _, message := range messages {
		set, ok := accounts[message.Platform]
		if !ok {
			set = runtime.OnlineBotAccountIDs(message.Platform)
			accounts[message.Platform] = set
		}
		if _, isSelf := set[message.UserID]; !isSelf {
			out = append(out, message)
		}
	}
	return out
}

func equalMessageIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func unixSeconds(value time.Time) float64 { return float64(value.UnixNano()) / 1e9 }

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func valueOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func cloneTimestamp(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
