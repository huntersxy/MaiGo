package runtime

import (
	"fmt"
	"math/rand"
	"slices"
	"sync"
	"time"

	database "MaiGo/internal/common/database"
	"MaiGo/internal/config"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/turn_trigger"
	"MaiGo/internal/services"

	"go.uber.org/zap"
)

type gateStore struct {
	mu    sync.Mutex
	views map[int64]*groupViewState
}

type groupViewState struct {
	frequency        turn_trigger.ViewFrequencyController
	lastBatch        []int64
	interval         float64
	previousInterval float64
	silenceHandledAt time.Time
	silenceTriggered bool
}

func newGateStore() *gateStore {
	return &gateStore{views: map[int64]*groupViewState{}}
}

func (s *gateStore) viewLocked(groupID int64) *groupViewState {
	view := s.views[groupID]
	if view == nil {
		view = &groupViewState{}
		s.views[groupID] = view
	}
	return view
}

func (s *gateStore) activate(groupID int64, frequency float64) {
	s.mu.Lock()
	s.viewLocked(groupID).frequency.Activate(frequency)
	s.mu.Unlock()
}

func (s *gateStore) recordPlannerRun(groupID int64, interval float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	view := s.viewLocked(groupID)
	view.previousInterval, view.interval = view.interval, interval
	view.frequency.RecordPlannerRun()
}

func (s *gateStore) consumeSilenceTrigger(groupID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	view := s.viewLocked(groupID)
	triggered := view.silenceTriggered
	view.silenceTriggered = false
	return triggered
}

func (a *Agent) groupContextInterval(groupID int64) (time.Time, float64) {
	received := a.sessions.get(database.ConversationKindGroup, groupID).ContextReceivedAtTimes(a.maisakaMaxContextSize(true))
	if len(received) == 0 {
		return time.Time{}, 0
	}
	timestamps := make([]float64, 0, len(received))
	for _, at := range received {
		timestamps = append(timestamps, float64(at.UnixNano())/1e9)
	}
	interval, _ := turn_trigger.MeanMessageInterval(timestamps)
	return received[len(received)-1], interval
}

func (a *Agent) gateMessageBatch(groupID int64, pending []*sessionUser, frequency float64) maisaka.TurnGateResult {
	batch := make([]int64, 0, len(pending))
	for _, user := range pending {
		if user != nil && !user.IsAgentMessage && user.MessageID != 0 && user.SenderID != a.SelfID() {
			batch = append(batch, user.MessageID)
		}
	}
	a.gates.mu.Lock()
	view := a.gates.viewLocked(groupID)
	if len(batch) == 0 || slices.Equal(view.lastBatch, batch) {
		a.gates.mu.Unlock()
		return maisaka.TurnGateResult{Decision: maisaka.TurnWait, Detail: "本批已判定，等待新批次或静默判定"}
	}
	view.lastBatch = batch
	probability := view.frequency.Probability(frequency)
	accepted := view.frequency.Decide(frequency, rand.Float64())
	phase, internal := view.frequency.Phase, view.frequency.Internal
	a.gates.mu.Unlock()
	a.consumeMessageDebounce(database.ConversationKindGroup, groupID)
	decision := maisaka.TurnWait
	if accepted {
		decision = maisaka.TurnTrigger
	}
	return maisaka.TurnGateResult{Decision: decision, Detail: fmt.Sprintf(
		"概率查看 外部频率=%.3f 内部频率=%.3f 放行概率=%.3f 阶段=%s", frequency, internal, probability, phase,
	)}
}

func (a *Agent) armGroupSilenceCheck(groupID int64, after time.Time) {
	if a.gateFrequency(groupID) <= 0 || a.groupConcurrency.IsBusy(groupID) {
		return
	}
	session := a.sessions.get(database.ConversationKindGroup, groupID)
	if runtime := session.MaisakaRuntime(); runtime != nil && (runtime.FocusModeActive() || runtime.AgentState() == turn_trigger.StateRunning) {
		return
	}
	receivedAt, current := a.groupContextInterval(groupID)
	a.gates.mu.Lock()
	view := a.gates.viewLocked(groupID)
	previous := view.previousInterval
	handled := !receivedAt.After(view.silenceHandledAt)
	a.gates.mu.Unlock()
	interval, ok := turn_trigger.PredictMessageInterval(current, previous, current > 0, previous > 0)
	if handled || !ok {
		return
	}
	delay := time.Duration(interval * float64(time.Second))
	deadline := receivedAt.Add(delay)
	if !after.IsZero() {
		deadline = after.Add(delay)
	}
	a.armGroupTimer(groupID, max(0, time.Until(deadline)), true, func(pending *pendingThink, generation uint64) {
		a.checkGroupSilence(groupID, receivedAt, pending, generation)
	})
}

func (a *Agent) checkGroupSilence(groupID int64, receivedAt time.Time, pending *pendingThink, generation uint64) {
	if !a.takeGroupTimer(groupID, pending, generation) || a.groupConcurrency.IsBusy(groupID) {
		return
	}
	latest, _ := a.groupContextInterval(groupID)
	if !latest.Equal(receivedAt) {
		return
	}
	session := a.sessions.get(database.ConversationKindGroup, groupID)
	if runtime := session.MaisakaRuntime(); runtime != nil && (runtime.FocusModeActive() || runtime.AgentState() == turn_trigger.StateRunning) {
		return
	}
	if sessionID := a.resolveMaiBotSessionID(a.ctx, database.ConversationKindGroup, groupID); sessionID != "" && services.ChatMigrationGateInstance.Blocked(sessionID) {
		return
	}
	frequency := a.gateFrequency(groupID)
	if frequency <= 0 {
		return
	}
	if quiet := a.messageQuietRemaining(database.ConversationKindGroup, groupID, latest); quiet > 0 {
		a.armGroupTimer(groupID, quiet, true, func(pending *pendingThink, generation uint64) {
			a.checkGroupSilence(groupID, receivedAt, pending, generation)
		})
		return
	}
	a.gates.mu.Lock()
	view := a.gates.viewLocked(groupID)
	if !receivedAt.After(view.silenceHandledAt) {
		a.gates.mu.Unlock()
		return
	}
	probability := view.frequency.Probability(frequency)
	accepted := view.frequency.Decide(frequency, rand.Float64())
	if accepted {
		view.silenceHandledAt = receivedAt
		view.silenceTriggered = true
	}
	a.gates.mu.Unlock()
	zap.L().Debug("群聊静默查看判定", zap.Int64("group_id", groupID), zap.Float64("probability", probability), zap.Bool("accepted", accepted))
	if accepted {
		a.groupConcurrency.Submit(groupID)
	} else {
		a.armGroupSilenceCheck(groupID, time.Now())
	}
}

func (a *Agent) gateFrequencyFromRuntime(groupID int64) float64 {
	cfg := config.Snapshot()
	base := cfg.Chat.TalkFrequency
	if cfg.Chat.EnableTimeRules {
		for _, rule := range cfg.Chat.TimeRules {
			if rule.GroupID != 0 && rule.GroupID != groupID {
				continue
			}
			start, end, ok := parseTimeRange(rule.TimeRange)
			if !ok {
				continue
			}
			now := time.Now()
			current := now.Hour()*60 + now.Minute()
			if start <= end && current >= start && current < end {
				return rule.TalkValue
			}
			if start > end && (current >= start || current < end) {
				return rule.TalkValue
			}
		}
	}
	return base
}

func parseTimeRange(value string) (int, int, bool) {
	var startHour, startMin, endHour, endMin int
	if _, err := fmt.Sscanf(value, "%d:%d-%d:%d", &startHour, &startMin, &endHour, &endMin); err != nil {
		return 0, 0, false
	}
	if startHour > 23 || endHour > 23 || startMin > 59 || endMin > 59 {
		return 0, 0, false
	}
	return startHour*60 + startMin, endHour*60 + endMin, true
}
