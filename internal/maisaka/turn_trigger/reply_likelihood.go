package turn_trigger

import (
	"math"
	"strings"
)

// 权重与 MaiBot src/maisaka/turn_trigger/reply_likelihood.py 一致
const (
	maxPendingCount               = 20
	defaultSecondsSinceBotMessage = 3600.0
)

var mediaPlaceholderPrefixes = []string{
	"[表情包", "[图片", "[消息类型]", "[CQ:image", "[语音", "[文件]", "[卡片",
}

// ReplyLikelihoodInput 估计 reply 概率所需的对话快照
type ReplyLikelihoodInput struct {
	MentionBot             bool
	AtOther                bool
	HasQuestionMark        bool
	PlaceholderOnly        bool
	RecentSelfRatio        float64
	RecentMessageCount     int
	SecondsSinceBotMessage float64
	PendingCount           int
}

// SummarizePendingTexts 从新消息文本得到 at_other、问号、纯媒体占位特征
func SummarizePendingTexts(texts []string, hasAtBot bool) (atOther, hasQuestionMark, placeholderOnly bool) {
	var stripped []string
	for _, text := range texts {
		if t := strings.TrimSpace(text); t != "" {
			stripped = append(stripped, t)
		}
	}
	atOther = !hasAtBot && containsAt(stripped)
	hasQuestionMark = containsQuestion(stripped)
	if len(stripped) == 0 {
		return atOther, hasQuestionMark, false
	}
	placeholderOnly = true
	for _, text := range stripped {
		if !hasMediaPlaceholderPrefix(text) {
			placeholderOnly = false
			break
		}
	}
	return atOther, hasQuestionMark, placeholderOnly
}

func containsAt(texts []string) bool {
	for _, text := range texts {
		if strings.Contains(text, "@") {
			return true
		}
	}
	return false
}

func containsQuestion(texts []string) bool {
	for _, text := range texts {
		if strings.ContainsAny(text, "?？") {
			return true
		}
	}
	return false
}

func hasMediaPlaceholderPrefix(text string) bool {
	for _, prefix := range mediaPlaceholderPrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

// EstimateReplyProbability 估计这批消息进入 Planner 后会调用 reply 的概率
func EstimateReplyProbability(in ReplyLikelihoodInput) float64 {
	logit := -1.2005 +
		1.5852*boolFloat(in.MentionBot) +
		-1.1157*boolFloat(in.AtOther) +
		0.4807*boolFloat(in.HasQuestionMark) +
		-1.0281*boolFloat(in.PlaceholderOnly) +
		2.6525*clamp01(in.RecentSelfRatio) +
		-0.0367*math.Max(0, float64(in.RecentMessageCount)) +
		0.0571*math.Log1p(math.Max(0, in.SecondsSinceBotMessage)) +
		0.1012*math.Min(maxPendingCount, math.Max(0, float64(in.PendingCount)))
	return 1 / (1 + math.Exp(-logit))
}

func boolFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
