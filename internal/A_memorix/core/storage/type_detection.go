package storage

// A_memorix 知识类型启发式识别，对齐 Python core/storage/type_detection.py

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var narrativeMarkers = []string{
	"然后", "接着", "于是", "后来", "最后", "突然", "一天",
	"曾经", "有一次", "从前", "说道", "问道", "想着", "觉得",
}

var factualMarkers = []string{
	"是", "有", "在", "为", "属于", "位于", "包含", "拥有", "成立于", "出生于",
}

// 对齐 Python r'["「『].*?["」』]'，点号不跨行
var dialoguePattern = regexp.MustCompile(`["「『][^\n]*?["」』]`)

// splitPythonLines 对齐 Python str.splitlines 的分隔符集合
func splitPythonLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := make([]string, 0, 8)
	start := 0
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		sepLen := 0
		switch r {
		case '\r':
			sepLen = 1
			if i+1 < len(runes) && runes[i+1] == '\n' {
				sepLen = 2
			}
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			sepLen = 1
		}
		if sepLen == 0 {
			continue
		}
		lines = append(lines, string(runes[start:i]))
		i += sepLen - 1
		start = i + 1
	}
	if start < len(runes) {
		lines = append(lines, string(runes[start:]))
	}
	return lines
}

func nonEmptyLines(content string) []string {
	raw := splitPythonLines(content)
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// LooksLikeStructuredText 判断是否为 a|b|c 三段式结构化文本
func LooksLikeStructuredText(content string) bool {
	text := strings.TrimSpace(content)
	if !strings.Contains(text, "|") || strings.Count(text, "|") < 2 {
		return false
	}
	parts := strings.Split(text, "|")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return false
		}
	}
	return true
}

// LooksLikeQuoteText 判断是否为短行堆叠的引用文本
func LooksLikeQuoteText(content string) bool {
	lines := nonEmptyLines(content)
	if len(lines) < 5 {
		return false
	}
	total := 0
	for _, line := range lines {
		total += utf8.RuneCountInString(line)
	}
	avgLen := float64(total) / float64(len(lines))
	return avgLen < 20
}

// LooksLikeNarrativeText 判断是否为叙事性文本
func LooksLikeNarrativeText(content string) bool {
	text := strings.TrimSpace(content)
	if text == "" {
		return false
	}
	score := 0
	for _, marker := range narrativeMarkers {
		if strings.Contains(text, marker) {
			score++
		}
	}
	hasDialogue := dialoguePattern.MatchString(text)
	head := text
	if utf8.RuneCountInString(head) > 500 {
		head = string([]rune(head)[:500])
	}
	hasChapter := strings.Contains(head, "Chapter") || strings.Contains(head, "CHAPTER") || strings.Contains(head, "###")
	return hasChapter || hasDialogue || score >= 2
}

// LooksLikeFactualText 判断是否为事实陈述
func LooksLikeFactualText(content string) bool {
	text := strings.TrimSpace(content)
	if text == "" {
		return false
	}
	if LooksLikeStructuredText(text) || LooksLikeQuoteText(text) {
		return false
	}
	score := 0
	for _, marker := range factualMarkers {
		if strings.Contains(text, marker) {
			score++
		}
	}
	if score <= 0 {
		return false
	}
	if utf8.RuneCountInString(text) <= 240 {
		return true
	}
	return score >= 2 && !LooksLikeNarrativeText(text)
}

// SelectImportStrategy 策略选择：chat_log > override > quote > factual > narrative
func SelectImportStrategy(content string, override any, chatLog bool) (ImportStrategy, error) {
	if chatLog {
		return ImportNarrative, nil
	}
	strategy, err := ParseImportStrategyOrDefault(override, ImportAuto)
	if err != nil {
		return "", err
	}
	if strategy != ImportAuto {
		return strategy, nil
	}
	if LooksLikeQuoteText(content) {
		return ImportQuote, nil
	}
	if LooksLikeFactualText(content) {
		return ImportFactual, nil
	}
	return ImportNarrative, nil
}

// DetectKnowledgeType 自动检测落库类型，无法可靠判断时回退 mixed
func DetectKnowledgeType(content string) KnowledgeType {
	text := strings.TrimSpace(content)
	if text == "" {
		return KnowledgeMixed
	}
	if LooksLikeStructuredText(text) {
		return KnowledgeStructured
	}
	if LooksLikeQuoteText(text) {
		return KnowledgeQuote
	}
	if LooksLikeFactualText(text) {
		return KnowledgeFactual
	}
	if LooksLikeNarrativeText(text) {
		return KnowledgeNarrative
	}
	return KnowledgeMixed
}

// GetTypeFromUserInput 优先使用显式 type_hint，否则自动检测
func GetTypeFromUserInput(typeHint string, content string) (KnowledgeType, error) {
	if typeHint != "" {
		return ResolveStoredKnowledgeType(typeHint, content, false, nil)
	}
	return DetectKnowledgeType(content), nil
}
