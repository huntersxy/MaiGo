package maisaka

import "MaiGo/internal/maisaka/turn_trigger"

// 动态门控常量对齐 dynamic_gate.py；实现见 internal/maisaka/turn_trigger
const (
	DynamicGateWindowSeconds         = turn_trigger.ReplyWindowSeconds
	DynamicGateForcedExpectedReplies = turn_trigger.ForcedTurnExpectedReplies
	DynamicGateMinWindowScoreCount   = turn_trigger.MinWindowScoreCount
	DynamicGateVirtualRoundSeconds   = turn_trigger.VirtualRoundSeconds
	DynamicGateMaxCountedMessageIDs  = turn_trigger.MaxCountedMessageIDs
)

// DynamicGateDecision 动态门控的一次判定
type DynamicGateDecision = turn_trigger.DynamicGateDecision

// DynamicReplyGate 单个聊天流的动态回复门控状态
type DynamicReplyGate = turn_trigger.DynamicReplyGate

// NewDynamicReplyGate 使用默认窗口长度
func NewDynamicReplyGate() *DynamicReplyGate { return turn_trigger.NewDynamicReplyGate() }

// NewDynamicReplyGateWithWindow 指定窗口长度，主要供测试
func NewDynamicReplyGateWithWindow(windowSeconds float64) *DynamicReplyGate {
	return turn_trigger.NewDynamicReplyGateWithWindow(windowSeconds)
}

// DynamicGateStaticThreshold 按离线统计表插值出保留比例对应的概率阈值
func DynamicGateStaticThreshold(keepRatio float64) float64 {
	return turn_trigger.StaticThreshold(keepRatio)
}
