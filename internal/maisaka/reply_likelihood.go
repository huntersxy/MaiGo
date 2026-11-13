package maisaka

import "maibot-go/internal/maisaka/turn_trigger"

// ReplyLikelihoodInput 估计一批外部消息触发 reply 的概率所需快照
type ReplyLikelihoodInput = turn_trigger.ReplyLikelihoodInput

// SummarizePendingTexts 从新消息文本得到 at_other、问号、纯媒体占位特征
func SummarizePendingTexts(texts []string, hasAtBot bool) (atOther, hasQuestionMark, placeholderOnly bool) {
	return turn_trigger.SummarizePendingTexts(texts, hasAtBot)
}

// EstimateReplyProbability 估计本批消息进入 Planner 后会调用 reply 的概率
func EstimateReplyProbability(in ReplyLikelihoodInput) float64 {
	return turn_trigger.EstimateReplyProbability(in)
}
