package learners

import (
	"context"
	"strings"
)

// TextEmbedder 表达向量与 query 嵌入；由 agent 从 llm.Router 注入，未配置时为 nil
type TextEmbedder interface {
	EmbedText(ctx context.Context, text string) ([]float32, error)
}

// ExpressionEmbeddingText 对齐 MaiBot expression_vector_index.expression_embedding_text
func ExpressionEmbeddingText(situation, style string) string {
	situation = strings.TrimSpace(situation)
	style = NormalizeExpressionStyleForLearning(style)
	if situation == "" && style == "" {
		return ""
	}
	return "情景：" + situation + "\n风格：" + style
}

// ExpressionQueryEmbeddingText 构建回复侧 query 嵌入文本（target + reply_reason）
func ExpressionQueryEmbeddingText(targetMessage, replyReason string) string {
	var parts []string
	if s := strings.TrimSpace(targetMessage); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(replyReason); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n")
}
