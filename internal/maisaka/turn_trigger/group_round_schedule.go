package turn_trigger

import "math"

const MaxInternalRounds = 16

const lowFrequencyRoundLimit = 8

type groupRoundState struct {
	logicalTurnID   string
	inPlannerTurn   bool
	completedRounds int
	roundAllowance  int
	roundDebt       int
}

func (s *groupRoundState) allowsTimedWakeup() bool { return s.roundDebt == 0 }

func (s *groupRoundState) startPlannerTurn(sched *MessageTurnScheduler, logicalTurnID string) int {
	sched.mu.Lock()
	s.inPlannerTurn = true
	sched.acceptedWaitStartedAt = nil
	rt := sched.runtime
	if !rt.IsGroupSession() || rt.FocusModeActive() {
		completed := s.completedRounds
		sched.mu.Unlock()
		return completed
	}
	frequency := math.Min(1.0, rt.EffectiveReplyFrequency())
	sched.viewFrequency.Activate(frequency)
	if logicalTurnID != s.logicalTurnID {
		s.logicalTurnID = logicalTurnID
		s.completedRounds = 0
		s.roundAllowance = int(math.Ceil(frequency * MaxInternalRounds))
		if frequency < 0.5 {
			rt.SetMaxInternalRounds(lowFrequencyRoundLimit)
		} else {
			rt.SetMaxInternalRounds(MaxInternalRounds)
		}
	}
	completed := s.completedRounds
	sched.mu.Unlock()
	return completed
}

func (s *groupRoundState) recordPlannerRun(sched *MessageTurnScheduler) {
	sched.mu.Lock()
	s.completedRounds++
	if s.completedRounds > s.roundAllowance {
		s.roundDebt++
	}
	sched.mu.Unlock()
}

func (s *groupRoundState) applyDebtPayoff(sched *MessageTurnScheduler) (bool, int) {
	sched.mu.Lock()
	defer sched.mu.Unlock()
	if s.roundDebt <= 0 {
		return false, s.roundDebt
	}
	s.roundDebt--
	return true, s.roundDebt
}

func (s *groupRoundState) finishPlannerTurn(sched *MessageTurnScheduler) {
	sched.mu.Lock()
	s.inPlannerTurn = false
	if !sched.runtime.HasPendingWaitToolCall() {
		s.logicalTurnID = ""
	}
	sched.mu.Unlock()
}
