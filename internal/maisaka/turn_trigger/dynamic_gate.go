package turn_trigger

import (
	"fmt"
	"math"
	"sort"
)

// 常量与 MaiBot src/maisaka/turn_trigger/dynamic_gate.py 一致
const (
	// ReplyWindowSeconds 预计/实际回复数的统计窗口
	ReplyWindowSeconds = 3600.0
	// ForcedTurnExpectedReplies @ 强制触发预计带来的回复次数
	ForcedTurnExpectedReplies = 0.91
	// MinWindowScoreCount 切换动态阈值所需的最小样本数
	MinWindowScoreCount = 20
	// VirtualRoundSeconds 虚拟轮次时长，同一轮次只计一次预计回复
	VirtualRoundSeconds = 40.0
	// MaxCountedMessageIDs 已计入消息 ID 的保留上限
	MaxCountedMessageIDs = 2000
)

// 保留比例到概率阈值的离线统计表，样本不足时线性插值
var staticKeepRatioThresholds = [][2]float64{
	{0.0, 1.0}, {0.1, 0.605}, {0.2, 0.482}, {0.3, 0.430}, {0.4, 0.376},
	{0.5, 0.336}, {0.6, 0.313}, {0.7, 0.290}, {0.8, 0.250}, {0.9, 0.206}, {1.0, 0.0},
}

// StaticThreshold 按离线统计表线性插值出保留比例对应的概率阈值
func StaticThreshold(keepRatio float64) float64 {
	return staticThreshold(keepRatio)
}

// staticThreshold 按离线统计表线性插值出保留比例对应的概率阈值
func staticThreshold(keepRatio float64) float64 {
	for index := 0; index+1 < len(staticKeepRatioThresholds); index++ {
		low := staticKeepRatioThresholds[index]
		high := staticKeepRatioThresholds[index+1]
		if keepRatio <= high[0] {
			progress := (keepRatio - low[0]) / (high[0] - low[0])
			return low[1] + (high[1]-low[1])*progress
		}
	}
	return 0
}

// DynamicGateDecision 动态门控的一次判定
type DynamicGateDecision struct {
	ShouldTrigger   bool
	Probability     float64
	Threshold       float64
	ExpectedReplies float64
	TargetReplies   float64
	ActualReplies   int
	KeepRatio       float64
}

// Detail 对齐 DynamicGateDecision.detail 文案
func (d DynamicGateDecision) Detail() string {
	return fmt.Sprintf(
		"概率=%.2f 阈值=%.2f 预计回复=%.1f 目标=%.1f 已回复=%d 保留比例=%.2f",
		d.Probability, d.Threshold, d.ExpectedReplies, d.TargetReplies, d.ActualReplies, d.KeepRatio,
	)
}

type timedScore struct {
	at    float64
	value float64
}

// DynamicReplyGate 单个聊天流的动态回复门控状态
type DynamicReplyGate struct {
	windowSeconds       float64
	proactiveScores     []timedScore
	forcedExpectations  []timedScore
	replyTimes          []float64
	countedMessageIDs   map[string]struct{}
	countedMessageOrder []string
	openRoundStartedAt  *float64
}

// NewDynamicReplyGate 使用默认窗口长度
func NewDynamicReplyGate() *DynamicReplyGate {
	return NewDynamicReplyGateWithWindow(ReplyWindowSeconds)
}

// CountedMessageCount 已计入预计回复数的消息数量
func (g *DynamicReplyGate) CountedMessageCount() int { return len(g.countedMessageIDs) }

// CountedMessageOrderCount 计数消息的淘汰顺序长度
func (g *DynamicReplyGate) CountedMessageOrderCount() int { return len(g.countedMessageOrder) }

// ProactiveDemandCount 窗口内主动意愿分数量
func (g *DynamicReplyGate) ProactiveDemandCount() int { return len(g.proactiveScores) }

// ProactiveDemandAt 第 index 个主动意愿分
func (g *DynamicReplyGate) ProactiveDemandAt(index int) float64 {
	if index < 0 || index >= len(g.proactiveScores) {
		return 0
	}
	return g.proactiveScores[index].value
}

// ForcedExpectationCount 窗口内强制轮次期望数量
func (g *DynamicReplyGate) ForcedExpectationCount() int { return len(g.forcedExpectations) }

// ForcedExpectationAt 第 index 个强制轮次期望值
func (g *DynamicReplyGate) ForcedExpectationAt(index int) float64 {
	if index < 0 || index >= len(g.forcedExpectations) {
		return 0
	}
	return g.forcedExpectations[index].value
}

// NewDynamicReplyGateWithWindow 指定窗口长度，主要供测试
func NewDynamicReplyGateWithWindow(windowSeconds float64) *DynamicReplyGate {
	return &DynamicReplyGate{
		windowSeconds:     windowSeconds,
		countedMessageIDs: map[string]struct{}{},
	}
}

// TakeUncountedMessageIDs 返回尚未计入预计回复数的消息 ID，并把它们标记为已计入
func (g *DynamicReplyGate) TakeUncountedMessageIDs(messageIDs []string) []string {
	var newIDs []string
	for _, messageID := range messageIDs {
		if messageID == "" {
			continue
		}
		if _, counted := g.countedMessageIDs[messageID]; counted {
			continue
		}
		newIDs = append(newIDs, messageID)
		g.countedMessageIDs[messageID] = struct{}{}
		g.countedMessageOrder = append(g.countedMessageOrder, messageID)
	}
	for len(g.countedMessageOrder) > MaxCountedMessageIDs {
		oldest := g.countedMessageOrder[0]
		g.countedMessageOrder = g.countedMessageOrder[1:]
		delete(g.countedMessageIDs, oldest)
	}
	return newIDs
}

// IsRoundOpen 当前是否仍处在一个尚未结束的虚拟轮次内
func (g *DynamicReplyGate) IsRoundOpen(now float64) bool {
	return g.openRoundStartedAt != nil && now-*g.openRoundStartedAt < VirtualRoundSeconds
}

// RecordProactiveDemand 记录当前虚拟轮次预计带来的 reply 次数
func (g *DynamicReplyGate) RecordProactiveDemand(probability, now float64) {
	if g.IsRoundOpen(now) && len(g.proactiveScores) > 0 {
		g.proactiveScores[len(g.proactiveScores)-1].value = probability
		return
	}
	start := now
	g.openRoundStartedAt = &start
	g.proactiveScores = append(g.proactiveScores, timedScore{at: now, value: probability})
}

// CloseRound Planner 已实际运行，结束当前虚拟轮次
func (g *DynamicReplyGate) CloseRound() {
	g.openRoundStartedAt = nil
}

// RecordForcedTurn 记录一次 @ 强制触发预计带来的回复次数
func (g *DynamicReplyGate) RecordForcedTurn(now float64) {
	g.CloseRound()
	g.forcedExpectations = append(g.forcedExpectations, timedScore{at: now, value: ForcedTurnExpectedReplies})
}

// RecordReply 记录 Planner 实际调用了一次 reply
func (g *DynamicReplyGate) RecordReply(now float64) {
	g.replyTimes = append(g.replyTimes, now)
}

func (g *DynamicReplyGate) prune(now float64) {
	expireBefore := now - g.windowSeconds
	g.proactiveScores = pruneTimedScores(g.proactiveScores, expireBefore)
	g.forcedExpectations = pruneTimedScores(g.forcedExpectations, expireBefore)
	kept := g.replyTimes[:0]
	for _, at := range g.replyTimes {
		if at >= expireBefore {
			kept = append(kept, at)
		}
	}
	g.replyTimes = kept
}

func pruneTimedScores(values []timedScore, expireBefore float64) []timedScore {
	kept := values[:0]
	for _, item := range values {
		if item.at >= expireBefore {
			kept = append(kept, item)
		}
	}
	return kept
}

// Evaluate 判断当前这批待处理消息是否放行进入 Planner
func (g *DynamicReplyGate) Evaluate(probability, frequency, now float64) DynamicGateDecision {
	g.prune(now)
	scores := make([]float64, 0, len(g.proactiveScores))
	expectedProactive := 0.0
	for _, item := range g.proactiveScores {
		scores = append(scores, item.value)
		expectedProactive += item.value
	}
	expectedForced := 0.0
	for _, item := range g.forcedExpectations {
		expectedForced += item.value
	}
	expectedTotal := expectedProactive + expectedForced
	actualReplies := len(g.replyTimes)

	normalizedFrequency := math.Min(1, math.Max(0, frequency))
	targetReplies := normalizedFrequency * expectedTotal

	var keepRatio float64
	switch {
	case normalizedFrequency >= 1:
		keepRatio = 1
	case expectedProactive <= 0:
		keepRatio = normalizedFrequency
	default:
		feedforwardRatio := (targetReplies - expectedForced) / expectedProactive
		feedbackFactor := math.Min(1, math.Max(0, 1+(targetReplies-float64(actualReplies))/math.Max(1, targetReplies)))
		keepRatio = math.Min(1, math.Max(0, feedforwardRatio*feedbackFactor))
	}

	threshold := resolveThreshold(scores, expectedProactive, keepRatio)
	return DynamicGateDecision{
		ShouldTrigger:   probability >= threshold,
		Probability:     probability,
		Threshold:       threshold,
		ExpectedReplies: expectedTotal,
		TargetReplies:   targetReplies,
		ActualReplies:   actualReplies,
		KeepRatio:       keepRatio,
	}
}

// resolveThreshold 按概率从高到低累计，累计到保留额度时的概率即阈值
func resolveThreshold(proactiveScores []float64, expectedProactive, keepRatio float64) float64 {
	if keepRatio >= 1 {
		return 0
	}
	if keepRatio <= 0 {
		return 1
	}
	if len(proactiveScores) < MinWindowScoreCount {
		return staticThreshold(keepRatio)
	}
	budget := keepRatio * expectedProactive
	sortedScores := append([]float64(nil), proactiveScores...)
	sort.Sort(sort.Reverse(sort.Float64Slice(sortedScores)))
	accumulated := 0.0
	for _, score := range sortedScores {
		accumulated += score
		if accumulated >= budget {
			return score
		}
	}
	return 0
}
