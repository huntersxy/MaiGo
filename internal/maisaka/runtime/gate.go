package runtime

import (
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"maibot-go/internal/config"
	"maibot-go/internal/maisaka"
)

// 门控权重来自 MaiBot 的离线拟合，作为上线起点，之后用本项目日志重新拟合
const (
	gateIntercept        = -1.2005
	gateMentionWeight    = 1.5852
	gateAtOtherWeight    = -1.1157
	gateQuestionWeight   = 0.4807
	gateMediaOnlyWeight  = -1.0281
	gateSelfRatioWeight  = 2.6525
	gateMessageNumWeight = -0.0367
	gateIdleWeight       = 0.0571
	gatePendingWeight    = 0.1012
	gateRecentWindow     = 5 * time.Minute
)

// gateStore 管理各群的动态门控窗口，算法与虚拟轮次复用 maisaka.turn_trigger
type gateStore struct {
	mu      sync.Mutex
	windows map[int64]*maisaka.DynamicReplyGate
	// rounds 记录当前虚拟轮次已计入预计回复数的外部消息编号
	rounds map[int64]map[string]struct{}
}

func newGateStore() *gateStore {
	return &gateStore{windows: map[int64]*maisaka.DynamicReplyGate{}, rounds: map[int64]map[string]struct{}{}}
}

// window 取群的动态门控窗口
func (s *gateStore) window(groupID int64) *maisaka.DynamicReplyGate {
	s.mu.Lock()
	defer s.mu.Unlock()
	window, ok := s.windows[groupID]
	if !ok {
		window = maisaka.NewDynamicReplyGate()
		s.windows[groupID] = window
	}
	return window
}

// markReply 记录一次真实发言，参与反馈收紧
func (s *gateStore) markReply(groupID int64) {
	s.window(groupID).RecordReply(float64(time.Now().UnixNano()) / 1e9)
}

// gateDynamicTurn 供 EvaluateMessageTurn 动态门控步骤调用
// 对齐 MaiBot DynamicReplyTurnGate：只把新到消息计入虚拟轮次，拒绝重复计入积压旧消息
func (a *Agent) gateDynamicTurn(groupID int64, session *Session, pending []*sessionUser, forced bool) maisaka.TurnGateResult {
	external := filterExternalPending(pending)
	if len(external) == 0 {
		return maisaka.NoExternalMessagesGate()
	}
	now := float64(time.Now().UnixNano()) / 1e9
	window := a.gates.window(groupID)
	messageIDs := make([]string, 0, len(external))
	for _, user := range external {
		messageIDs = append(messageIDs, strconv.FormatInt(user.MessageID, 10))
	}
	newIDs := window.TakeUncountedMessageIDs(messageIDs)
	if len(newIDs) > 0 {
		a.gates.rememberRound(groupID, window, newIDs, now)
		roundRound := a.gates.roundMessages(groupID, external)
		window.RecordProactiveDemand(a.gateProbability(session, roundRound), now)
	}
	if forced {
		// 强制触发只在首次计入这批消息时累加预计回复数，重复观察不重复计
		if len(newIDs) > 0 {
			window.RecordForcedTurn(now)
		}
		return maisaka.TurnGateResult{
			Decision: maisaka.TurnTrigger,
			Detail:   "强制触发跳过动态门控估计",
		}
	}
	probability := a.gateProbability(session, external)
	decision := window.Evaluate(probability, a.gateFrequency(groupID), now)
	if decision.ShouldTrigger {
		window.CloseRound()
	}
	return maisaka.DynamicGateFromProbability(probability, decision.Threshold, decision.ShouldTrigger)
}

// rememberRound 在虚拟轮次内合并新消息编号，轮次结束时重新开轮
func (s *gateStore) rememberRound(groupID int64, window *maisaka.DynamicReplyGate, ids []string, now float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.rounds[groupID]
	if current == nil || !window.IsRoundOpen(now) {
		current = map[string]struct{}{}
		s.rounds[groupID] = current
	}
	for _, id := range ids {
		current[id] = struct{}{}
	}
}

// roundMessages 取当前虚拟轮次内到达的外部消息
func (s *gateStore) roundMessages(groupID int64, external []*sessionUser) []*sessionUser {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.rounds[groupID]
	if len(current) == 0 {
		return external
	}
	out := make([]*sessionUser, 0, len(external))
	for _, user := range external {
		if user == nil {
			continue
		}
		if _, ok := current[strconv.FormatInt(user.MessageID, 10)]; ok {
			out = append(out, user)
		}
	}
	if len(out) == 0 {
		return external
	}
	return out
}

// filterExternalPending 只保留带真实消息编号的外部消息
func filterExternalPending(pending []*sessionUser) []*sessionUser {
	out := make([]*sessionUser, 0, len(pending))
	for _, user := range pending {
		if user == nil || user.IsAgentMessage {
			continue
		}
		if user.MessageID != 0 {
			out = append(out, user)
		}
	}
	return out
}

// gateFrequencyFromRuntime 从 PG RuntimeConfig 取群聊频率，时段规则优先
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

// gateProbability 按消息内容算出发言意愿分
func (a *Agent) gateProbability(session *Session, pending []*sessionUser) float64 {
	mention := false
	texts := make([]string, 0, len(pending))
	for _, user := range pending {
		if user == nil {
			continue
		}
		if user.Mentioned {
			mention = true
		}
		texts = append(texts, user.Content)
	}
	// 文本特征按 MaiBot reply_likelihood.summarize_pending_texts 提取
	atOther, question, mediaOnly := maisaka.SummarizePendingTexts(texts, mention)
	total, self, lastSelf := session.RecentWindow(time.Now(), gateRecentWindow)
	selfRatio := 0.0
	if total > 0 {
		selfRatio = float64(self) / float64(total)
	}
	idle := 3600.0
	if !lastSelf.IsZero() {
		idle = time.Since(lastSelf).Seconds()
	}
	logit := gateIntercept +
		gateMentionWeight*boolToFloat(mention) +
		gateAtOtherWeight*boolToFloat(atOther) +
		gateQuestionWeight*boolToFloat(question) +
		gateMediaOnlyWeight*boolToFloat(mediaOnly) +
		gateSelfRatioWeight*clampFloat(selfRatio, 0, 1) +
		gateMessageNumWeight*math.Max(0, float64(total)) +
		gateIdleWeight*math.Log1p(math.Max(0, idle)) +
		gatePendingWeight*math.Min(20, float64(len(pending)))
	return 1 / (1 + math.Exp(-logit))
}

// boolToFloat 把布尔转成 0/1
func boolToFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

// clampFloat 限制数值区间
func clampFloat(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// parseTimeRange 解析时段配置
func parseTimeRange(value string) (int, int, bool) {
	var startHour, startMin, endHour, endMin int
	if _, err := fmt.Sscanf(value, "%d:%d-%d:%d", &startHour, &startMin, &endHour, &endMin); err != nil {
		return 0, 0, false
	}
	return startHour*60 + startMin, endHour*60 + endMin, true
}
