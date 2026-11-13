package learners

import (
	"regexp"
	"strings"
)

const (
	MaxJargonReferenceMatches    = 10
	jargonReferenceHeader        = "以下释义来自当前上下文中部分词汇的猜测含义，仅作理解聊天语境的参考，不一定为真，需要根据情景理解："
	jargonReferenceDisplayPrefix = "<jargon>"
)

var (
	plannerMessagePrefixRE = regexp.MustCompile(`(?i)^<message\b[^>]*>\s*`)
	jargonReferenceItemRE  = regexp.MustCompile(`^\s*(?:\d+\.\s*)?(?P<content>.+?)：`)
)

// JargonMatch 机械命中结果，对齐 maisaka/jargon_context_matcher._JargonMatch
type JargonMatch struct {
	Content            string
	Meaning            string
	Score              float64
	FirstMessageIndex  int
	JargonCount        int
	HighFrequencyTerm  string
	HighFrequencyRank  int
	HighFrequencyCount int
}

// MatchJargonsForContext 返回当前上下文机械命中的黑话
func MatchJargonsForContext(
	sessionID string,
	messages []ContextMessage,
	candidates []JargonCandidate,
	highFrequency map[string]HighFrequencyTermSummary,
	limit int,
) []JargonMatch {
	if limit < 1 {
		limit = MaxJargonReferenceMatches
	}
	userTexts := OtherUserTexts(messages)
	if len(userTexts) == 0 || len(candidates) == 0 {
		return nil
	}
	if highFrequency == nil {
		highFrequency = map[string]HighFrequencyTermSummary{}
	}

	matchesByContent := map[string]JargonMatch{}
	for messageIndex, text := range userTexts {
		normalizedText := NormalizeMatchText(text)
		if normalizedText == "" {
			continue
		}
		for _, candidate := range candidates {
			contentKey := NormalizeMatchText(candidate.Content)
			if contentKey == "" {
				continue
			}
			if _, ok := matchesByContent[contentKey]; ok {
				continue
			}
			if !strings.Contains(normalizedText, contentKey) {
				continue
			}
			hf := highFrequency[contentKey]
			hfRank := hf.Rank
			hfCount := hf.OccurrenceCount
			hitHF := hf.Term != ""
			matchesByContent[contentKey] = JargonMatch{
				Content:            candidate.Content,
				Meaning:            candidate.Meaning,
				Score:              calculateJargonMatchScore(candidate.Count, messageIndex, hfRank, hfCount, hitHF),
				FirstMessageIndex:  messageIndex,
				JargonCount:        candidate.Count,
				HighFrequencyTerm:  hf.Term,
				HighFrequencyRank:  hfRank,
				HighFrequencyCount: hfCount,
			}
		}
	}

	out := make([]JargonMatch, 0, len(matchesByContent))
	for _, match := range matchesByContent {
		out = append(out, match)
	}
	sortJargonMatches(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func sortJargonMatches(matches []JargonMatch) {
	// 简单插入排序，条目数通常很小
	for i := 1; i < len(matches); i++ {
		j := i
		for j > 0 && jargonMatchLess(matches[j], matches[j-1]) {
			matches[j], matches[j-1] = matches[j-1], matches[j]
			j--
		}
	}
}

func jargonMatchLess(a, b JargonMatch) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if a.FirstMessageIndex != b.FirstMessageIndex {
		return a.FirstMessageIndex < b.FirstMessageIndex
	}
	if len(a.Content) != len(b.Content) {
		return len(a.Content) > len(b.Content)
	}
	return a.Content < b.Content
}

func calculateJargonMatchScore(candidateCount, firstMessageIndex, hfRank, hfCount int, hitHF bool) float64 {
	var hfScore float64
	if hitHF {
		hfScore = 1000.0 + float64(hfCount)*2.0
		if hfRank > 0 {
			hfScore += max(0.0, 100.0-float64(hfRank))
		}
	}
	return float64(candidateCount) + hfScore - float64(firstMessageIndex)*0.01
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// NormalizeMatchText 对齐 _normalize_match_text
func NormalizeMatchText(text string) string {
	return strings.TrimSpace(strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(text)), " ")))
}

// IsJargonReferenceText 判断文本是否为已注入的黑话参考消息
func IsJargonReferenceText(content string) bool {
	text := plannerMessagePrefixRE.ReplaceAllString(strings.TrimSpace(content), "")
	text = strings.TrimLeft(text, " \t")
	return strings.HasPrefix(text, jargonReferenceDisplayPrefix) || strings.HasPrefix(text, jargonReferenceHeader)
}

// BuildJargonReferenceBody 构造注入 Maisaka 的黑话参考正文
func BuildJargonReferenceBody(matches []JargonMatch) string {
	if len(matches) == 0 {
		return ""
	}
	lines := []string{jargonReferenceHeader}
	for _, match := range matches {
		lines = append(lines, match.Content+"："+match.Meaning)
	}
	return strings.Join(lines, "\n")
}

// ExtractJargonReferenceContents 从已有参考正文中提取词条
func ExtractJargonReferenceContents(content string) []string {
	var contents []string
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == jargonReferenceHeader {
			continue
		}
		if term := extractJargonReferenceContentFromLine(line); term != "" {
			contents = append(contents, term)
		}
	}
	return contents
}

func extractJargonReferenceContentFromLine(line string) string {
	normalized := regexp.MustCompile(`^\s*\d+\.\s*`).ReplaceAllString(strings.TrimSpace(line), "")
	m := jargonReferenceItemRE.FindStringSubmatch(normalized)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}
