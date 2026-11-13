package learners

import (
	"strings"

	"golang.org/x/text/cases"
)

const maxExpressionsPerBatch = 20

func filterExtractedExpressions(items []extractedExpression, messages []ContextMessage, botName string, settings LearningSettings) []extractedExpression {
	if len(items) == 0 || len(messages) == 0 {
		return nil
	}
	banned := bannedBotNames(botName, settings.BotAliasNames)
	out := make([]extractedExpression, 0, len(items))
	for _, item := range items {
		sourceID := strings.TrimSpace(item.SourceID)
		if !isDigits(sourceID) {
			continue
		}
		lineIndex := atoi(sourceID) - 1
		if lineIndex < 0 || lineIndex >= len(messages) {
			continue
		}
		msg := messages[lineIndex]
		if msg.IsBotSelf || msg.SourceKind == "assistant" {
			continue
		}
		if IsLearningBlacklisted(settings.ExpressionBlacklist, msg.Platform, msg.UserID) {
			continue
		}
		context := strings.TrimSpace(msg.Text)
		if context == "" {
			continue
		}
		situation := strings.TrimSpace(item.Situation)
		style := NormalizeExpressionStyleForLearning(item.Style)
		if situation == "" || style == "" {
			continue
		}
		if containsSelfMarker(situation, style, context) {
			continue
		}
		if styleBanned(style, banned) {
			continue
		}
		if containsMediaMarker(situation, style, context) {
			continue
		}
		out = append(out, extractedExpression{
			Situation: situation,
			Style:     style,
			SourceID:  sourceID,
		})
	}
	return out
}

func bannedBotNames(botName string, aliasNames []string) map[string]struct{} {
	out := map[string]struct{}{}
	add := func(name string) {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			out[casefoldName(trimmed)] = struct{}{}
		}
	}
	add(botName)
	for _, alias := range aliasNames {
		add(alias)
	}
	return out
}

func styleBanned(style string, banned map[string]struct{}) bool {
	if len(banned) == 0 {
		return false
	}
	_, ok := banned[casefoldName(style)]
	return ok
}

// casefoldName 对齐 Python str.casefold，用于机器人名称黑名单比较
func casefoldName(value string) string {
	return cases.Fold().String(strings.TrimSpace(value))
}

func containsSelfMarker(parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(part, "SELF") {
			return true
		}
	}
	return false
}

func containsMediaMarker(parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(part, "[表情包") || strings.Contains(part, "[图片") {
			return true
		}
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
