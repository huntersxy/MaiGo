package turn_trigger

import (
	"testing"
	"time"
)

type schedulerTestRuntime struct {
	group             bool
	frequency         float64
	maxInternalRounds int
}

func (s *schedulerTestRuntime) LogPrefix() string                              { return "[t] " }
func (s *schedulerTestRuntime) RuntimeRunning() bool                           { return true }
func (s *schedulerTestRuntime) AgentState() AgentState                         { return StateStop }
func (s *schedulerTestRuntime) IsGroupSession() bool                           { return s.group }
func (s *schedulerTestRuntime) MessageTurnScheduled() bool                     { return false }
func (s *schedulerTestRuntime) PendingMessageCount() int                       { return 0 }
func (s *schedulerTestRuntime) PendingMessages() []PendingMessage              { return nil }
func (s *schedulerTestRuntime) MessageQuietRemainingSeconds() float64          { return 0 }
func (s *schedulerTestRuntime) EffectiveReplyFrequency() float64               { return s.frequency }
func (s *schedulerTestRuntime) IsReplyFrequencySilent() bool                   { return s.frequency <= 0 }
func (s *schedulerTestRuntime) HasForcedTurnTrigger() bool                     { return false }
func (s *schedulerTestRuntime) FocusModeActive() bool                          { return false }
func (s *schedulerTestRuntime) ChatHistory() []HistoryMessage                  { return nil }
func (s *schedulerTestRuntime) MaxContextSize() int                            { return 16 }
func (s *schedulerTestRuntime) LastMessageReceivedAt() time.Time               { return time.Time{} }
func (s *schedulerTestRuntime) PendingWaitStartedAt() *time.Time               { return nil }
func (s *schedulerTestRuntime) ConsumeMessageDebounce()                        {}
func (s *schedulerTestRuntime) OnlineBotAccountIDs(string) map[string]struct{} { return nil }
func (s *schedulerTestRuntime) CancelWaitTimeout()                             {}
func (s *schedulerTestRuntime) UpdateStageStatus(string, string)               {}
func (s *schedulerTestRuntime) DeferMessageTurnCheck(float64)                  {}
func (s *schedulerTestRuntime) EnqueueMessageTurn()                            {}
func (s *schedulerTestRuntime) EnterStopState()                                {}
func (s *schedulerTestRuntime) EnterRunningState()                             {}
func (s *schedulerTestRuntime) SetMaxInternalRounds(rounds int)                { s.maxInternalRounds = rounds }
func (s *schedulerTestRuntime) MaxInternalRounds() int                         { return s.maxInternalRounds }
func (s *schedulerTestRuntime) HasPendingWaitToolCall() bool                   { return false }

func TestRoundDebtAllowsWakeupWhenClear(t *testing.T) {
	rt := &schedulerTestRuntime{group: true, frequency: 0.07}
	sched := NewMessageTurnScheduler(rt)
	if sched.StartPlannerTurn("first") != 0 {
		t.Fatal("start")
	}
	for i := 0; i < 4; i++ {
		sched.RecordPlannerRun()
	}
	if sched.RoundDebt() != 2 {
		t.Fatalf("debt=%d", sched.RoundDebt())
	}
	sched.RecordPlannerRun()
	if sched.RoundDebt() != 3 || sched.AllowsTimedWakeup() {
		t.Fatalf("debt=%d allow=%v", sched.RoundDebt(), sched.AllowsTimedWakeup())
	}
}
