package turn_trigger

import (
	"fmt"
	"sort"
	"time"

	"go.uber.org/zap"
)

// RecentPresenceWindowSeconds 统计近期消息量与麦麦发言占比的时间窗口
const RecentPresenceWindowSeconds = 300.0

// DynamicReplyTurnGate 按估计的 reply 概率与回复频率动态决定是否进入 Planner
type DynamicReplyTurnGate struct {
	runtime RuntimeView
	gate    *DynamicReplyGate
	// roundMessageIDs 当前虚拟轮次内已到达的外部消息 ID
	roundMessageIDs map[string]struct{}
	// now 时钟注入，nil 时使用 time.Now，便于单测
	now func() time.Time
}

// NewDynamicReplyTurnGate 创建动态回复门控
func NewDynamicReplyTurnGate(runtime RuntimeView) *DynamicReplyTurnGate {
	return &DynamicReplyTurnGate{
		runtime:         runtime,
		gate:            NewDynamicReplyGate(),
		roundMessageIDs: map[string]struct{}{},
	}
}

// SetClock 注入时钟，主要供测试
func (g *DynamicReplyTurnGate) SetClock(now func() time.Time) {
	if now != nil {
		g.now = now
	}
}

func (g *DynamicReplyTurnGate) currentTime() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// RecordReply 记录 Planner 实际调用了一次 reply
func (g *DynamicReplyTurnGate) RecordReply() {
	g.gate.RecordReply(float64(g.currentTime().UnixNano()) / float64(time.Second))
}

// RecordForcedTurn @ 强制触发时把这批消息计入预计回复数；同一批消息只计一次
func (g *DynamicReplyTurnGate) RecordForcedTurn(pendingMessages []PendingMessage) {
	external := g.filterExternalMessages(pendingMessages)
	ids := make([]string, 0, len(external))
	for _, message := range external {
		ids = append(ids, message.MessageID)
	}
	if len(g.gate.TakeUncountedMessageIDs(ids)) > 0 {
		g.gate.RecordForcedTurn(float64(g.currentTime().UnixNano()) / float64(time.Second))
	}
}

// Evaluate 返回动态门控的触发判定
func (g *DynamicReplyTurnGate) Evaluate(pendingMessages []PendingMessage, frequency float64) TurnGateResult {
	external := g.filterExternalMessages(pendingMessages)
	if len(external) == 0 {
		return TurnGateResult{Decision: DecisionWait, Detail: "没有新的外部消息 判定=等待更多消息"}
	}
	if !g.runtime.IsGroupSession() {
		// 拟合数据只覆盖群聊；私聊里每条消息都是对麦麦说的，不设门控
		return TurnGateResult{Decision: DecisionTrigger, Detail: "私聊不设动态门控 判定=进入Planner"}
	}

	now := float64(g.currentTime().UnixNano()) / float64(time.Second)
	ids := make([]string, 0, len(external))
	for _, message := range external {
		ids = append(ids, message.MessageID)
	}
	newMessageIDs := g.gate.TakeUncountedMessageIDs(ids)
	if len(newMessageIDs) > 0 {
		// 预计回复数按虚拟轮次计：只用本轮次内到达的消息估计，被门控拦下而积压的旧消息不重复计入
		if !g.gate.IsRoundOpen(now) {
			g.roundMessageIDs = map[string]struct{}{}
		}
		for _, id := range newMessageIDs {
			g.roundMessageIDs[id] = struct{}{}
		}
		roundMessages := make([]PendingMessage, 0, len(external))
		for _, message := range external {
			if _, ok := g.roundMessageIDs[message.MessageID]; ok {
				roundMessages = append(roundMessages, message)
			}
		}
		g.gate.RecordProactiveDemand(
			EstimateReplyProbability(g.buildLikelihoodInput(roundMessages, now, external)),
			now,
		)
	}

	probability := EstimateReplyProbability(g.buildLikelihoodInput(external, now, external))
	decision := g.gate.Evaluate(probability, frequency, now)
	if decision.ShouldTrigger {
		g.gate.CloseRound()
	}
	label := "等待更多消息"
	if decision.ShouldTrigger {
		label = "进入Planner"
	}
	result := DecisionWait
	if decision.ShouldTrigger {
		result = DecisionTrigger
	}
	return TurnGateResult{
		Decision: result,
		Detail:   fmt.Sprintf("动态门控: %s 判定=%s", decision.Detail(), label),
	}
}

// filterExternalMessages 过滤平台在线机器人自身账号的消息
func (g *DynamicReplyTurnGate) filterExternalMessages(pendingMessages []PendingMessage) []PendingMessage {
	platforms := make([]string, 0, 2)
	seen := map[string]struct{}{}
	for _, message := range pendingMessages {
		if _, ok := seen[message.Platform]; ok {
			continue
		}
		seen[message.Platform] = struct{}{}
		platforms = append(platforms, message.Platform)
	}
	onlineAccounts := make(map[string]map[string]struct{}, len(platforms))
	for _, platform := range platforms {
		onlineAccounts[platform] = g.runtime.OnlineBotAccountIDs(platform)
	}
	external := make([]PendingMessage, 0, len(pendingMessages))
	filteredDetails := map[string]struct{}{}
	for _, message := range pendingMessages {
		userID := message.UserID
		if _, isSelf := onlineAccounts[message.Platform][userID]; isSelf {
			filteredDetails[fmt.Sprintf("platform=%s 过滤bot_id = %s（自身消息）", message.Platform, userID)] = struct{}{}
			continue
		}
		external = append(external, message)
	}
	details := make([]string, 0, len(filteredDetails))
	for detail := range filteredDetails {
		details = append(details, detail)
	}
	sort.Strings(details)
	for _, detail := range details {
		zap.L().Info(g.runtime.LogPrefix() + " " + detail)
	}
	return external
}

// buildLikelihoodInput 按当前 runtime 快照为一批外部消息构造概率函数的输入
func (g *DynamicReplyTurnGate) buildLikelihoodInput(messages []PendingMessage, now float64, pendingMessages []PendingMessage) ReplyLikelihoodInput {
	texts := make([]string, 0, len(messages))
	hasAtBot := false
	mentionBot := false
	for _, message := range messages {
		texts = append(texts, message.ProcessedPlainText)
		if message.IsAt {
			hasAtBot = true
		}
		if message.IsMentioned {
			mentionBot = true
		}
	}
	atOther, hasQuestionMark, placeholderOnly := SummarizePendingTexts(texts, hasAtBot)
	recentSelfCount, recentHistoryCount, secondsSinceBot := g.summarizeRecentHistory(now)
	// 待处理消息尚未写入内部历史，只补上最近 5 分钟内的消息，避免旧积压持续压低概率
	recentPendingCount := 0
	currentTime := time.Unix(0, int64(now*float64(time.Second)))
	for _, message := range pendingMessages {
		if message.ReceivedAt == nil {
			continue
		}
		if currentTime.Sub(*message.ReceivedAt).Seconds() <= RecentPresenceWindowSeconds {
			recentPendingCount++
		}
	}
	recentMessageCount := recentHistoryCount + recentPendingCount
	ratioDenominator := recentMessageCount
	if ratioDenominator < 1 {
		ratioDenominator = 1
	}
	return ReplyLikelihoodInput{
		MentionBot:             mentionBot,
		AtOther:                atOther,
		HasQuestionMark:        hasQuestionMark,
		PlaceholderOnly:        placeholderOnly,
		RecentSelfRatio:        float64(recentSelfCount) / float64(ratioDenominator),
		RecentMessageCount:     recentMessageCount,
		SecondsSinceBotMessage: secondsSinceBot,
		PendingCount:           len(messages),
	}
}

// summarizeRecentHistory 返回近期麦麦发言数、近期历史消息数，以及距麦麦上一次发言的秒数
func (g *DynamicReplyTurnGate) summarizeRecentHistory(now float64) (int, int, float64) {
	currentTime := time.Unix(0, int64(now*float64(time.Second)))
	recentSelfCount := 0
	recentTotalCount := 0
	secondsSinceBot := -1.0
	history := g.runtime.ChatHistory()
	for index := len(history) - 1; index >= 0; index-- {
		message := history[index]
		if !message.CountInContext {
			continue
		}
		isSelfMessage := message.Source == SelfMessageSource
		// 外部消息按接收时刻统计活跃度；自身发言仍按本机发送时间计算
		var activityTime time.Time
		if isSelfMessage {
			activityTime = message.Timestamp
		} else if message.ReceivedAt != nil {
			activityTime = *message.ReceivedAt
		} else {
			continue
		}
		elapsedSeconds := currentTime.Sub(activityTime).Seconds()
		if isSelfMessage && secondsSinceBot < 0 {
			secondsSinceBot = elapsedSeconds
			if secondsSinceBot < 0 {
				secondsSinceBot = 0
			}
		}
		if elapsedSeconds > RecentPresenceWindowSeconds {
			// 媒体处理可能乱序完成，历史列表不保证按接收时刻排序
			continue
		}
		recentTotalCount++
		if isSelfMessage {
			recentSelfCount++
		}
	}
	if secondsSinceBot < 0 {
		secondsSinceBot = defaultSecondsSinceBotMessage
	}
	return recentSelfCount, recentTotalCount, secondsSinceBot
}
