package learners

import (
	"strings"

	"github.com/bytedance/sonic"
)

type extractedExpression struct {
	Situation string
	Style     string
	SourceID  string
}

func parseExpressionBatchJSON(raw string) []extractedExpression {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if list, ok := decodeExpressionList(raw); ok {
		return list
	}
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start >= 0 && end > start {
		if list, ok := decodeExpressionList(raw[start : end+1]); ok {
			return list
		}
	}
	start = strings.Index(raw, "{")
	end = strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		if list, ok := decodeExpressionList("[" + raw[start:end+1] + "]"); ok {
			return list
		}
	}
	return nil
}

func decodeExpressionList(raw string) ([]extractedExpression, bool) {
	var items []map[string]any
	if err := sonic.UnmarshalString(raw, &items); err != nil {
		var one map[string]any
		if err2 := sonic.UnmarshalString(raw, &one); err2 != nil {
			return nil, false
		}
		items = []map[string]any{one}
	}
	out := make([]extractedExpression, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		situation := strings.TrimSpace(anyString(item["situation"]))
		style := NormalizeExpressionStyleForLearning(anyString(item["style"]))
		sourceID := strings.TrimSpace(anyString(item["id"]))
		if situation == "" || style == "" || sourceID == "" {
			continue
		}
		out = append(out, extractedExpression{
			Situation: situation,
			Style:     style,
			SourceID:  sourceID,
		})
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func anyString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return itoaInt(int(t))
		}
	}
	return ""
}

func itoaInt(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

func normalizeStyle(style string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(strings.TrimSpace(style)), " "))
}
