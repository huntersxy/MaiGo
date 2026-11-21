// 群聊思考调度：MaiBot 顺序由 maisaka.EvaluateMessageTurn 编排；动态门控步骤仍委托 agent/gate.go 的阈值窗口（非 Maisaka DynamicReplyGate 虚拟轮次）
package runtime

import (
	"context"
	"sync"
	"time"

	"MaiGo/internal/chat/debounce"
	"MaiGo/internal/chat/session"
	"MaiGo/internal/common/database"
	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/builtin_tool"

	"go.uber.org/zap"
)

func markRepliedThisTurn(ctx context.Context) {
	tc := tools.GetToolContext(ctx)
	if tc != nil && tc.RepliedThisTurn != nil {
		*tc.RepliedThisTurn = true
	}
}

// conversationTurnScheduleStore 维护 wait 与空闲退避；动态门控仍由 gateStore 负责
type conversationTurnScheduleStore struct {
	mu              sync.Mutex
	byID            map[string]*conversationTurnState
	idleCfg         maisaka.IdleBackoffConfig
	groupDebounce   debounce.Mode
	privateDebounce debounce.Mode
}

type conversationTurnState struct {
	idle           *maisaka.IdleBackoff
	debouncer      *debounce.Debouncer
	inWait         bool
	waitUntil      time.Time
	waitGeneration uint64
	waitStartedAt  time.Time
	requestedWait  time.Duration
}

func newConversationTurnScheduleStore() *conversationTurnScheduleStore {
	return &conversationTurnScheduleStore{
		byID:    map[string]*conversationTurnState{},
		idleCfg: maibotcfg.DefaultIdleBackoffConfig(),
	}
}

func (s *conversationTurnScheduleStore) stateLocked(key string) *conversationTurnState {
	st, ok := s.byID[key]
	if !ok {
		st = &conversationTurnState{
			idle:      maisaka.NewIdleBackoff(s.idleCfg),
			debouncer: debounce.NewDebouncer(),
		}
		s.byID[key] = st
	}
	return st
}

func (s *conversationTurnScheduleStore) applyIdleBackoff(cfg maisaka.IdleBackoffConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idleCfg = cfg
	for _, st := range s.byID {
		st.idle.SetConfig(cfg)
	}
}

func (s *conversationTurnScheduleStore) applyDebounceModes(groupMode, privateMode debounce.Mode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groupDebounce = groupMode
	s.privateDebounce = privateMode
	for key, st := range s.byID {
		mode := groupMode
		if kind, _, err := chatsession.ParseConversationKey(key); err == nil && kind == memory.ConversationKindPrivate {
			mode = privateMode
		}
		st.debouncer.SyncMode(mode)
	}
}

func (s *conversationTurnScheduleStore) syncDebounceMode(kind string, targetID int64, mode debounce.Mode) {
	s.withState(kind, targetID, func(st *conversationTurnState) {
		st.debouncer.SyncMode(mode)
	})
}

func (s *conversationTurnScheduleStore) observeDebounce(kind string, targetID int64, in debounce.ObserveInput) string {
	var reason string
	s.withState(kind, targetID, func(st *conversationTurnState) {
		reason = st.debouncer.Observe(in)
	})
	return reason
}

func (s *conversationTurnScheduleStore) debounceDeadline(kind string, targetID int64) (time.Time, bool) {
	var deadline time.Time
	var ok bool
	s.withState(kind, targetID, func(st *conversationTurnState) {
		deadline, ok = st.debouncer.Deadline()
	})
	return deadline, ok
}

func (s *conversationTurnScheduleStore) debounceRemaining(kind string, targetID int64, now time.Time) time.Duration {
	var remain time.Duration
	s.withState(kind, targetID, func(st *conversationTurnState) {
		remain = st.debouncer.Remaining(now)
	})
	return remain
}

func (s *conversationTurnScheduleStore) consumeDebounce(kind string, targetID int64) {
	s.withState(kind, targetID, func(st *conversationTurnState) {
		st.debouncer.Consume()
	})
}

func (s *conversationTurnScheduleStore) withState(kind string, targetID int64, fn func(*conversationTurnState)) {
	key := chatsession.ConversationKey(kind, targetID)
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.stateLocked(key))
}

func (s *conversationTurnScheduleStore) inWait(kind string, targetID int64) bool {
	waiting := false
	s.withState(kind, targetID, func(st *conversationTurnState) {
		if !st.inWait {
			return
		}
		if !st.waitUntil.IsZero() && time.Now().After(st.waitUntil) {
			st.inWait = false
			st.waitUntil = time.Time{}
			return
		}
		waiting = st.inWait
	})
	return waiting
}

func (s *conversationTurnScheduleStore) beginWait(kind string, targetID int64, seconds int) uint64 {
	if seconds < 0 {
		seconds = 0
	}
	var generation uint64
	s.withState(kind, targetID, func(st *conversationTurnState) {
		st.waitGeneration++
		generation = st.waitGeneration
		st.inWait = true
		st.waitStartedAt = time.Now()
		st.requestedWait = time.Duration(seconds) * time.Second
		if seconds == 0 {
			st.waitUntil = time.Time{}
			return
		}
		st.waitUntil = st.waitStartedAt.Add(st.requestedWait)
	})
	return generation
}

func (s *conversationTurnScheduleStore) clearWait(kind string, targetID int64) {
	s.withState(kind, targetID, func(st *conversationTurnState) {
		st.inWait = false
		st.waitUntil = time.Time{}
		st.waitStartedAt = time.Time{}
		st.requestedWait = 0
	})
}

// finishWaitByTimeout 在定时器触发且代次仍匹配时结束 wait
func (s *conversationTurnScheduleStore) finishWaitByTimeout(kind string, targetID int64, generation uint64) (elapsed, requested time.Duration, ok bool) {
	s.withState(kind, targetID, func(st *conversationTurnState) {
		if !st.inWait || st.waitGeneration != generation {
			return
		}
		started := st.waitStartedAt
		requested = st.requestedWait
		st.inWait = false
		st.waitUntil = time.Time{}
		st.waitStartedAt = time.Time{}
		st.requestedWait = 0
		if !started.IsZero() {
			elapsed = time.Since(started)
		}
		ok = true
	})
	return elapsed, requested, ok
}

func (s *conversationTurnScheduleStore) agentInWait(kind string, targetID int64) bool {
	return s.inWait(kind, targetID)
}

func (s *conversationTurnScheduleStore) idleRemaining(kind string, targetID int64, pendingCount int, isGroup, focusActive bool) time.Duration {
	var idle *maisaka.IdleBackoff
	s.withState(kind, targetID, func(st *conversationTurnState) {
		idle = st.idle
	})
	return idle.RemainingDelay(time.Now(), pendingCount, isGroup, focusActive)
}

// shouldHoldGroupScheduleDuringWait 群聊非快速反应模式下 wait 期间不因新消息重排定时器
func (a *Agent) shouldHoldGroupScheduleDuringWait(groupID int64) bool {
	return a.turnSchedule.inWait(memory.ConversationKindGroup, groupID) && !a.plannerMessageSteeringEnabled()
}

func (a *Agent) planGroupThinkDelay(groupID int64, receivedAt time.Time) time.Duration {
	quiet := a.messageQuietRemaining(memory.ConversationKindGroup, groupID, receivedAt)
	session := a.sessions.get(memory.ConversationKindGroup, groupID)
	pending := session.PendingMessages()
	result := a.evaluateGroupMessageTurn(groupID, session, pending, quiet)
	if result.Decision == maisaka.TurnDelay {
		return result.Delay
	}
	return 0
}

func (a *Agent) evaluateGroupMessageTurn(groupID int64, session *Session, pending []*sessionUser, quietRemaining time.Duration) maisaka.TurnGateResult {
	frequency := a.gateFrequency(groupID)
	forced := a.hasStrongInteraction(pending)
	var dynamic maisaka.TurnGateResult
	if quietRemaining <= 0 {
		dynamic = a.gateDynamicTurn(groupID, pending, forced, frequency)
	}
	input := maisaka.ScheduleTurnInput{
		MessageQuietRemaining:  quietRemaining,
		AgentInWaitState:       a.turnSchedule.agentInWait(memory.ConversationKindGroup, groupID),
		PlannerMessageSteering: a.plannerMessageSteeringEnabled(),
		IsGroupSession:         true,
		ReplyFrequencySilent:   frequency <= 0,
		ReplyFrequency:         frequency,
		HasForcedTurnTrigger:   forced,
		IdleBackoffRemaining:   0,
		Dynamic:                dynamic,
	}
	return maisaka.EvaluateMessageTurn(input)
}

func (a *Agent) evaluatePrivateMessageTurn(targetID int64, session *Session, pending []*sessionUser, quietRemaining time.Duration) maisaka.TurnGateResult {
	frequency := a.privateReplyFrequency(targetID)
	input := maisaka.ScheduleTurnInput{
		MessageQuietRemaining:  quietRemaining,
		AgentInWaitState:       a.turnSchedule.agentInWait(memory.ConversationKindPrivate, targetID),
		PlannerMessageSteering: a.plannerMessageSteeringEnabled(),
		IsGroupSession:         false,
		ReplyFrequencySilent:   frequency <= 0,
		ReplyFrequency:         frequency,
		HasForcedTurnTrigger:   false,
		IdleBackoffRemaining:   0,
		Dynamic:                maisaka.PrivateChatDynamicGate(),
	}
	return maisaka.EvaluateMessageTurn(input)
}

func (a *Agent) applyGroupTurnDecision(groupID int64, result maisaka.TurnGateResult) {
	switch result.Decision {
	case maisaka.TurnDelay:
		a.armGroupThinkTimer(groupID, result.Delay)
		zap.L().Debug("群聊思考推迟", zap.Int64("group_id", groupID), zap.String("detail", result.Detail), zap.Duration("delay", result.Delay))
	case maisaka.TurnWait:
		zap.L().Debug("群聊思考等待更多消息", zap.Int64("group_id", groupID), zap.String("detail", result.Detail))
	case maisaka.TurnTrigger:
		a.gates.activate(groupID, a.gateFrequency(groupID))
		consumeMaisakaWaitState(a.sessions.get(memory.ConversationKindGroup, groupID))
		a.turnSchedule.clearWait(memory.ConversationKindGroup, groupID)
		a.cancelWaitTimeout(memory.ConversationKindGroup, groupID)
		a.groupConcurrency.Submit(groupID)
		zap.L().Info("群聊思考触发", zap.Int64("group_id", groupID), zap.String("detail", result.Detail))
	}
}

func (a *Agent) applyPrivateTurnDecision(targetID int64, result maisaka.TurnGateResult) {
	switch result.Decision {
	case maisaka.TurnDelay:
		a.armPrivateThinkTimer(targetID, result.Delay)
		zap.L().Debug("私聊思考推迟", zap.Int64("target_id", targetID), zap.String("detail", result.Detail), zap.Duration("delay", result.Delay))
	case maisaka.TurnWait:
		zap.L().Debug("私聊思考等待", zap.Int64("target_id", targetID), zap.String("detail", result.Detail))
	case maisaka.TurnTrigger:
		consumeMaisakaWaitState(a.sessions.get(memory.ConversationKindPrivate, targetID))
		if a.turnSchedule.inWait(memory.ConversationKindPrivate, targetID) && !a.plannerMessageSteeringEnabled() {
			a.turnSchedule.clearWait(memory.ConversationKindPrivate, targetID)
			a.cancelWaitTimeout(memory.ConversationKindPrivate, targetID)
		}
		a.privateConcurrency.Submit(targetID)
		zap.L().Info("私聊思考触发", zap.Int64("target_id", targetID), zap.String("detail", result.Detail))
	}
}

func (a *Agent) armGroupThinkTimer(groupID int64, delay time.Duration) {
	a.pendingGroupMu.Lock()
	defer a.pendingGroupMu.Unlock()
	if a.stopping.Load() || a.ctx.Err() != nil || a.groupConcurrency.IsRunning(groupID) {
		return
	}
	pending := a.pendingGroupThinks[groupID]
	if pending == nil {
		pending = &pendingThink{}
		a.pendingGroupThinks[groupID] = pending
	}
	pending.resetTimer(delay, func(p *pendingThink, generation uint64) {
		a.flushPendingThink(groupID, p, generation)
	})
}

func (a *Agent) armPrivateThinkTimer(targetID int64, delay time.Duration) {
	a.privateMu.Lock()
	defer a.privateMu.Unlock()
	if a.privateStopped || a.stopping.Load() || a.ctx.Err() != nil || a.privateConcurrency.IsRunning(targetID) {
		return
	}
	pending := a.pendingPrivateThinks[targetID]
	if pending == nil {
		pending = &pendingThink{}
		a.pendingPrivateThinks[targetID] = pending
	}
	pending.resetTimer(delay, func(p *pendingThink, generation uint64) {
		a.flushPrivateThink(targetID, p, generation)
	})
}
