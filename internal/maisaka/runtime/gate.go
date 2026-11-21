package runtime

import (
	"fmt"
	"math/rand"
	"sync"
	"time"

	"MaiGo/internal/common/database"
	"MaiGo/internal/config"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/turn_trigger"
)

type gateStore struct {
	mu          sync.Mutex
	views       map[int64]*turn_trigger.ViewFrequencyController
	current     map[int64]float64
	previous    map[int64]float64
	hasCurrent  map[int64]bool
	hasPrevious map[int64]bool
}

func newGateStore() *gateStore {
	return &gateStore{
		views: map[int64]*turn_trigger.ViewFrequencyController{}, current: map[int64]float64{},
		previous: map[int64]float64{}, hasCurrent: map[int64]bool{}, hasPrevious: map[int64]bool{},
	}
}

func (s *gateStore) viewLocked(groupID int64) *turn_trigger.ViewFrequencyController {
	view := s.views[groupID]
	if view == nil {
		view = &turn_trigger.ViewFrequencyController{}
		s.views[groupID] = view
	}
	return view
}

func (s *gateStore) activate(groupID int64, frequency float64) {
	s.mu.Lock()
	s.viewLocked(groupID).Activate(frequency)
	s.mu.Unlock()
}

func (s *gateStore) recordPlannerRun(groupID int64) {
	s.mu.Lock()
	s.viewLocked(groupID).RecordPlannerRun()
	s.mu.Unlock()
}

func (s *gateStore) recordMessageInterval(groupID int64, interval float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasCurrent[groupID] {
		s.previous[groupID], s.hasPrevious[groupID] = s.current[groupID], true
	}
	s.current[groupID], s.hasCurrent[groupID] = interval, true
}

func (s *gateStore) predictedMessageInterval(groupID int64) (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return turn_trigger.PredictMessageInterval(
		s.current[groupID], s.previous[groupID], s.hasCurrent[groupID], s.hasPrevious[groupID],
	)
}

func (s *gateStore) decide(groupID int64, frequency, draw float64) (probability float64, accepted bool, phase turn_trigger.ViewFrequencyPhase, internal float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	view := s.viewLocked(groupID)
	probability = view.Probability(frequency)
	accepted = view.Decide(frequency, draw)
	return probability, accepted, view.Phase, view.Internal
}

func (a *Agent) gateDynamicTurn(groupID int64, pending []*sessionUser, forced bool, frequency float64) maisaka.TurnGateResult {
	external := filterExternalPending(pending)
	if len(external) == 0 {
		return maisaka.NoExternalMessagesGate()
	}
	if forced {
		return maisaka.TurnGateResult{Decision: maisaka.TurnTrigger, Detail: "强制触发跳过概率查看"}
	}
	probability, accepted, phase, internal := a.gates.decide(groupID, frequency, rand.Float64())
	a.consumeMessageDebounce(database.ConversationKindGroup, groupID)
	if accepted {
		return maisaka.TurnGateResult{Decision: maisaka.TurnTrigger, Detail: fmt.Sprintf(
			"概率查看 外部频率=%.3f 内部频率=%.3f 放行概率=%.3f 阶段=%s", frequency, internal, probability, phase,
		)}
	}
	if interval, ok := a.gates.predictedMessageInterval(groupID); ok && interval > 0 {
		return maisaka.TurnGateResult{Decision: maisaka.TurnDelay, Delay: time.Duration(interval * float64(time.Second)), Detail: fmt.Sprintf(
			"静默查看概率=%.3f 下次判定间隔=%.3fs", probability, interval,
		)}
	}
	return maisaka.TurnGateResult{Decision: maisaka.TurnWait, Detail: fmt.Sprintf("静默查看概率=%.3f，等待新消息", probability)}
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

func filterExternalPending(pending []*sessionUser) []*sessionUser {
	out := make([]*sessionUser, 0, len(pending))
	for _, user := range pending {
		if user != nil && !user.IsAgentMessage && user.MessageID != 0 {
			out = append(out, user)
		}
	}
	return out
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
