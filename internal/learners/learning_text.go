package learners

import "strings"

// ExtractLearningText 只取发送者本次顶层文字，不渲染或解析引用、转发、媒体等组件
// 对齐 src/learners/learning_text.py extract_learning_text
func ExtractLearningText(texts []string) string {
	parts := make([]string, 0, len(texts))
	for _, text := range texts {
		parts = append(parts, text)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
