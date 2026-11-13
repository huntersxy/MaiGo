package replyer

import (
	"strings"

	"github.com/cloudwego/eino/schema"
)

// ExpressionHabitsReferenceKind 表达习惯注入块的 kind 标记
// 对齐 Python replyer 在生成前注入的 expression_habits 用户块
const ExpressionHabitsReferenceKind = "expression_habits"

// BuildExpressionHabitsMessage 构造表达习惯注入消息；habits 为空时返回 nil
//
// 对齐 Python maisaka_generator_base.py:728-730：非空时以 User 角色追加一条独立文本块。
// Go 侧额外带上 kind 与选中的表达 ID，供上下文去重与调试追踪。
func BuildExpressionHabitsMessage(habits string, selectedIDs []uint) *schema.AgenticMessage {
	trimmed := strings.TrimSpace(habits)
	if trimmed == "" {
		return nil
	}
	message := schema.UserAgenticMessage("[系统消息：表达习惯] " + trimmed)
	message.Extra = map[string]any{
		"source":                  "internal_context",
		"kind":                    ExpressionHabitsReferenceKind,
		"selected_expression_ids": selectedIDs,
	}
	return message
}

// IsExpressionHabitsMessage 判断消息是否为表达习惯注入块
func IsExpressionHabitsMessage(message *schema.AgenticMessage) bool {
	if message == nil || message.Extra == nil {
		return false
	}
	kind, _ := message.Extra["kind"].(string)
	return kind == ExpressionHabitsReferenceKind
}
