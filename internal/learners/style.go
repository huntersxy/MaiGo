package learners

import "strings"

// 对齐 src/learners/expression_style_utils.py

var promptExampleExpressionStyles = map[string]struct{}{
	"我嘞个xxxx": {},
	"对对对":     {},
	"这么强！":    {},
}

// StripExpressionStyleUsagePrefix 移除表达学习 prompt 示例带出的"使用"前缀
func StripExpressionStyleUsagePrefix(style string) string {
	normalized := strings.TrimSpace(style)
	if strings.HasPrefix(normalized, "使用") {
		return strings.TrimSpace(normalized[len("使用"):])
	}
	return normalized
}

// NormalizeExpressionStyleForLearning 规范化表达风格文本
func NormalizeExpressionStyleForLearning(style string) string {
	return StripExpressionStyleUsagePrefix(style)
}

// IsPromptExampleExpressionStyle 判断是否为 prompt 示例泄漏
func IsPromptExampleExpressionStyle(style string) bool {
	_, ok := promptExampleExpressionStyles[NormalizeExpressionStyleForLearning(style)]
	return ok
}
