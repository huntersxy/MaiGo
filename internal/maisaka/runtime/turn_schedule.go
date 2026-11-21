package runtime

import (
	"sync"
	"time"

	"MaiGo/internal/chat/debounce"
	"MaiGo/internal/chat/session"
	"MaiGo/internal/common/database"
	"MaiGo/internal/maisaka"

	"go.uber.org/zap"
)

type conversationTurnScheduleStore struct {
	mu              sync.Mutex
	byID            map[string]*conversationTurnState
	groupDebounce   debounce.Mode
	privateDebounce debounce.Mode
}

type conversationTurnState struct {
	debouncer      *debounce.Debouncer
	inWait         bool
	waitGeneration uint64
	waitStartedAt  time.Time
	requestedWait  time.Duration
}

func newConversationTurnScheduleStore() *conversationTurnScheduleStore {
	return &conversationTurnScheduleStore{
		byID: map[string]*conversationTurnState{},
	}
}

func (s *conversationTurnScheduleStore) stateLocked(key string) *conversationTurnState {
	st, ok := s.byID[key]
	if !ok {
		st = &conversationTurnState{
			debouncer: debounce.NewDebouncer(),
		}
		s.byID[key] = st
	}
	return st
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

func (s *conversationTurnScheduleStore) beginWait(kind string, targetID int64, delay time.Duration) uint64 {
	var generation uint64
	s.withState(kind, targetID, func(st *conversationTurnState) {
		st.waitGeneration++
		generation = st.waitGeneration
		st.inWait = true
		st.waitStartedAt = time.Now()
		st.requestedWait = max(0, delay)
	})
	return generation
}

func (s *conversationTurnScheduleStore) finishWait(kind string, targetID int64, generation uint64) (elapsed, requested time.Duration, ok bool) {
	s.withState(kind, targetID, func(st *conversationTurnState) {
		if !st.inWait || (generation != 0 && st.waitGeneration != generation) {
			return
		}
		started := st.waitStartedAt
		requested = st.requestedWait
		st.inWait = false
		st.waitStartedAt = time.Time{}
		st.requestedWait = 0
		if !started.IsZero() {
			elapsed = time.Since(started)
		}
		ok = true
	})
	return elapsed, requested, ok
}

func (a *Agent) evaluateGroupMessageTurn(groupID int64, pending []*sessionUser, quietRemaining time.Duration) maisaka.TurnGateResult {
	frequency := a.gateFrequency(groupID)
	forced := a.hasStrongInteraction(pending)
	return maisaka.EvaluateMessageTurn(quietRemaining, frequency, forced, func() maisaka.TurnGateResult {
		return a.gateMessageBatch(groupID, pending, frequency)
	})
}

func (a *Agent) evaluatePrivateMessageTurn(targetID int64, quietRemaining time.Duration) maisaka.TurnGateResult {
	return maisaka.EvaluateMessageTurn(quietRemaining, a.privateReplyFrequency(targetID), false, func() maisaka.TurnGateResult {
		return maisaka.TurnGateResult{Decision: maisaka.TurnTrigger, Detail: "私聊不设概率门控"}
	})
}

func (a *Agent) applyGroupTurnDecision(groupID int64, result maisaka.TurnGateResult) {
	switch result.Decision {
	case maisaka.TurnDelay:
		a.armGroupThinkTimer(groupID, result.Delay)
		zap.L().Debug("群聊思考推迟", zap.Int64("group_id", groupID), zap.String("detail", result.Detail), zap.Duration("delay", result.Delay))
	case maisaka.TurnWait:
		zap.L().Debug("群聊思考等待更多消息", zap.Int64("group_id", groupID), zap.String("detail", result.Detail))
		a.armGroupSilenceCheck(groupID, time.Time{})
	case maisaka.TurnTrigger:
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
		a.privateConcurrency.Submit(targetID)
		zap.L().Info("私聊思考触发", zap.Int64("target_id", targetID), zap.String("detail", result.Detail))
	}
}

func (a *Agent) armGroupThinkTimer(groupID int64, delay time.Duration) {
	a.armGroupTimer(groupID, delay, false, func(p *pendingThink, generation uint64) {
		a.flushPendingThink(groupID, p, generation)
	})
}

func (a *Agent) armGroupTimer(groupID int64, delay time.Duration, onlyIfIdle bool, fire func(*pendingThink, uint64)) {
	a.pendingGroupMu.Lock()
	defer a.pendingGroupMu.Unlock()
	if a.stopping.Load() || a.ctx.Err() != nil || a.groupConcurrency.IsBusy(groupID) {
		return
	}
	pending := a.pendingGroupThinks[groupID]
	if pending != nil && onlyIfIdle {
		return
	}
	if pending == nil {
		pending = &pendingThink{}
		a.pendingGroupThinks[groupID] = pending
	}
	pending.resetTimer(delay, fire)
}

func (a *Agent) armPrivateThinkTimer(targetID int64, delay time.Duration) {
	a.privateMu.Lock()
	defer a.privateMu.Unlock()
	if a.privateStopped || a.stopping.Load() || a.ctx.Err() != nil || a.privateConcurrency.IsBusy(targetID) {
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
