package learners

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// 对齐 src/learners/expression_utils.py

// ExpressionLearningCandidate 表达学习候选，SourceID 为聊天记录中的来源行号
type ExpressionLearningCandidate struct {
	Situation string
	Style     string
	SourceID  string
}

// JargonLearningCandidate 黑话学习候选，SourceID 为聊天记录中的来源行号
type JargonLearningCandidate struct {
	Content  string
	SourceID string
}

const jsonFence = "```"

var (
	evaluationSuitableRE = regexp.MustCompile(`(?i)["“”]?suitable["“”]?\s*:\s*(true|false)`)
	evaluationReasonRE   = regexp.MustCompile(`(?i)["“”]?reason["“”]?\s*:\s*`)
	jsonFenceRE          = regexp.MustCompile("(?s)" + jsonFence + `json\s*(.*?)\s*` + jsonFence)
	openFenceRE          = regexp.MustCompile("(?m)^" + jsonFence + `\s*`)
	closeFenceRE         = regexp.MustCompile("(?m)" + jsonFence + `\s*$`)
)

// StripMarkdownCodeFence 移除 LLM 可能附带的 Markdown 代码块包裹
func StripMarkdownCodeFence(text string) string {
	raw := strings.TrimSpace(text)
	if match := jsonFenceRE.FindStringSubmatch(raw); match != nil {
		return strings.TrimSpace(match[1])
	}
	raw = openFenceRE.ReplaceAllString(raw, "")
	raw = closeFenceRE.ReplaceAllString(raw, "")
	return strings.TrimSpace(raw)
}

func extractJSONObjectCandidate(text string) string {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end >= 0 && start < end {
		return strings.TrimSpace(text[start : end+1])
	}
	return strings.TrimSpace(text)
}

// FixChineseQuotesInJSON 修复 JSON 字符串值中的中文引号，对齐 fix_chinese_quotes_in_json
func FixChineseQuotesInJSON(text string) string {
	var b strings.Builder
	inString := false
	escapeNext := false
	for _, r := range text {
		if escapeNext {
			b.WriteRune(r)
			escapeNext = false
			continue
		}
		if r == '\\' {
			b.WriteRune(r)
			escapeNext = true
			continue
		}
		if r == '"' {
			inString = !inString
			b.WriteRune(r)
			continue
		}
		if inString && (r == '“' || r == '”') {
			b.WriteByte('\\')
			b.WriteByte('"')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func extractReasonFromText(text string) (string, bool) {
	match := evaluationReasonRE.FindStringIndex(text)
	if match == nil {
		return "", false
	}
	value := strings.TrimSpace(text[match[1]:])
	if value == "" {
		return "", false
	}
	if strings.HasSuffix(value, "}") {
		value = strings.TrimSpace(value[:len(value)-1])
	}
	if strings.HasSuffix(value, ",") {
		value = strings.TrimSpace(value[:len(value)-1])
	}
	if value == "" {
		return "", false
	}
	if isQuoteRune(rune(value[0])) {
		value = value[1:]
		for value != "" && isQuoteRune(rune(value[len(value)-1])) {
			value = strings.TrimRight(value[:len(value)-1], " \t\r\n")
		}
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

func isQuoteRune(r rune) bool {
	switch r {
	case '"', '\'', '“', '”', '‘', '’':
		return true
	default:
		return false
	}
}

// NormalizeReasonText 清理解析后 reason 中残留的包裹引号
func NormalizeReasonText(reason any) string {
	normalized := strings.TrimSpace(pythonString(reason))
	if len(normalized) >= 2 {
		first := rune(normalized[0])
		last := rune(normalized[len(normalized)-1])
		if first == last && isQuoteRune(first) {
			normalized = strings.TrimSpace(normalized[1 : len(normalized)-1])
		}
	}
	if strings.HasSuffix(normalized, "\"") && strings.Count(normalized, "\"")%2 == 1 {
		normalized = strings.TrimRight(normalized[:len(normalized)-1], " \t\r\n")
	}
	if strings.HasSuffix(normalized, "'") && strings.Count(normalized, "'")%2 == 1 {
		normalized = strings.TrimRight(normalized[:len(normalized)-1], " \t\r\n")
	}
	if strings.HasSuffix(normalized, "\"") && !strings.HasPrefix(normalized, "\"") {
		normalized = strings.TrimRight(normalized[:len(normalized)-1], " \t\r\n")
	}
	if strings.HasSuffix(normalized, "'") && !strings.HasPrefix(normalized, "'") {
		normalized = strings.TrimRight(normalized[:len(normalized)-1], " \t\r\n")
	}
	return normalized
}

// ParseEvaluationResponse 解析表达方式评估结果，兼容不完全合法的 JSON
func ParseEvaluationResponse(response string) (map[string]any, error) {
	raw := StripMarkdownCodeFence(response)
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("LLM 响应为空")
	}

	candidates := []string{raw}
	jsonCandidate := extractJSONObjectCandidate(raw)
	if jsonCandidate != "" && jsonCandidate != raw {
		candidates = append(candidates, jsonCandidate)
	}

	for _, candidate := range candidates {
		if parsed, ok := ParseJSONLoose(candidate).(map[string]any); ok {
			if _, hasReason := parsed["reason"]; hasReason {
				parsed["reason"] = NormalizeReasonText(parsed["reason"])
			}
			return parsed, nil
		}
		fixedCandidate := FixChineseQuotesInJSON(candidate)
		if fixedCandidate != candidate {
			if parsed, ok := ParseJSONLoose(fixedCandidate).(map[string]any); ok {
				if _, hasReason := parsed["reason"]; hasReason {
					parsed["reason"] = NormalizeReasonText(parsed["reason"])
				}
				return parsed, nil
			}
		}
	}

	suitableMatch := evaluationSuitableRE.FindStringSubmatch(raw)
	reasonSource := raw
	if jsonCandidate != "" {
		reasonSource = jsonCandidate
	}
	reason, reasonOK := extractReasonFromText(reasonSource)
	if suitableMatch == nil || !reasonOK {
		return nil, fmt.Errorf("无法解析 LLM 响应为评估结果 JSON: %s", response)
	}
	return map[string]any{
		"suitable": strings.EqualFold(suitableMatch[1], "true"),
		"reason":   NormalizeReasonText(reason),
	}, nil
}

// ParseExpressionResponse 解析表达学习 JSON，返回表达与黑话两个列表
func ParseExpressionResponse(response string) ([]ExpressionLearningCandidate, []JargonLearningCandidate) {
	if strings.TrimSpace(response) == "" {
		return nil, nil
	}
	raw := StripMarkdownCodeFence(response)
	parsed := ParseJSONLoose(raw)
	if parsed == nil {
		parsed = ParseJSONLoose(FixChineseQuotesInJSON(raw))
	}
	if parsed == nil {
		return nil, nil
	}

	var list []any
	switch value := parsed.(type) {
	case map[string]any:
		list = []any{value}
	case []any:
		list = value
	default:
		return nil, nil
	}

	var expressions []ExpressionLearningCandidate
	var jargonEntries []JargonLearningCandidate
	for _, entry := range list {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		situation := strings.TrimSpace(jsonAnyText(item["situation"]))
		style := NormalizeExpressionStyleForLearning(strings.TrimSpace(jsonAnyText(item["style"])))
		sourceID := strings.TrimSpace(jsonAnyText(item["id"]))
		if situation != "" && style != "" && sourceID != "" {
			expressions = append(expressions, ExpressionLearningCandidate{Situation: situation, Style: style, SourceID: sourceID})
			continue
		}
		content := strings.TrimSpace(jsonAnyText(item["content"]))
		if content != "" && sourceID != "" {
			jargonEntries = append(jargonEntries, JargonLearningCandidate{Content: content, SourceID: sourceID})
		}
	}
	return expressions, jargonEntries
}

// ParseJargonResponse 解析独立黑话学习 LLM 响应，提取 (content, id) 列表
func ParseJargonResponse(response string) []JargonLearningCandidate {
	_, jargonEntries := ParseExpressionResponse(response)
	return jargonEntries
}

// jsonAnyText 对齐 Python str(value) 的常用分支，兼容模型返回数字 id
func jsonAnyText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "True"
		}
		return "False"
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func pythonString(value any) string {
	return jsonAnyText(value)
}
