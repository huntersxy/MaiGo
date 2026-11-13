package learners

import (
	"fmt"
	"strings"
)

// BuildExpressionQueryText 对齐 MaisakaExpressionSelector._build_expression_query_text
// 优先使用 Planner 的 expression_intent 与 reply_reference，缺省回退 Planner 推理
func BuildExpressionQueryText(replyReason string, replyToolArgs map[string]any) string {
	parts := make([]string, 0, 2)
	if intent := formatExpressionIntent(replyToolArgs); intent != "" {
		parts = append(parts, intent)
	}
	reference := ""
	if replyToolArgs != nil {
		reference = strings.TrimSpace(stringValue(replyToolArgs["reply_reference"]))
	}
	if reference != "" {
		parts = append(parts, "回复信息参考：\n"+reference)
	} else if reason := strings.TrimSpace(replyReason); reason != "" {
		parts = append(parts, "Planner 推理：\n"+reason)
	}
	return strings.Join(parts, "\n\n")
}

// formatExpressionIntent 对齐 _format_expression_intent 的字段顺序与文案
func formatExpressionIntent(replyToolArgs map[string]any) string {
	if replyToolArgs == nil {
		return ""
	}
	raw, ok := replyToolArgs["expression_intent"]
	if !ok || raw == nil {
		return ""
	}
	lines := make([]string, 0, 6)
	if intent, ok := raw.(map[string]any); ok {
		labels := []struct {
			field string
			label string
		}{
			{"focus", "贴合对象"},
			{"reply_act", "回复动作"},
			{"scene", "表达场景"},
			{"tone", "期望语气"},
			{"prefer", "优先表达"},
			{"avoid", "避免表达"},
		}
		for _, item := range labels {
			value := formatExpressionIntentValue(intent[item.field])
			if value != "" {
				lines = append(lines, "- "+item.label+"："+value)
			}
		}
	} else if value := strings.TrimSpace(stringValue(raw)); value != "" {
		lines = append(lines, "- 表达意图："+value)
	}
	if len(lines) == 0 {
		return ""
	}
	return "表达选择意图：\n" + strings.Join(lines, "\n")
}

func formatExpressionIntentValue(raw any) string {
	switch value := raw.(type) {
	case nil:
		return ""
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if text := strings.TrimSpace(stringValue(item)); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "、")
	case []string:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if text := strings.TrimSpace(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "、")
	default:
		return strings.TrimSpace(stringValue(raw))
	}
}

func stringValue(raw any) string {
	if raw == nil {
		return ""
	}
	if text, ok := raw.(string); ok {
		return text
	}
	return fmt.Sprint(raw)
}
