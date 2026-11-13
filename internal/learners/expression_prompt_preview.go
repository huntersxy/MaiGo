package learners

import (
	"fmt"

	"go.uber.org/zap"
)

// saveExpressionPromptPreview 保存表达学习可重放 Prompt，失败仅告警不阻断学习
func saveExpressionPromptPreview(sessionID, learnerSessionID string, sourceCount, builtCount int, systemPrompt string, userTexts []string, outputText string) {
	if _, err := SaveLearningPromptPreview(expressionPromptPreviewInput(
		sessionID, learnerSessionID, sourceCount, builtCount, systemPrompt, userTexts, outputText)); err != nil {
		zap.L().Warn("表达学习 Prompt 预览保存失败",
			zap.String("session_id", sessionID),
			zap.Error(err))
	}
}

func expressionPromptPreviewInput(sessionID, learnerSessionID string, sourceCount, builtCount int, systemPrompt string, userTexts []string, outputText string) LearningPromptPreviewInput {
	return LearningPromptPreviewInput{
		SessionID:        sessionID,
		LearnerSessionID: learnerSessionID,
		Category:         "expression_learner",
		RequestKind:      "expression_learner",
		SelectionReason: fmt.Sprintf(
			"会话ID: %s\nLearner会话ID: %s\n来源: trimmed_history\n真实聊天消息数: %d\n构建消息数: %d",
			sessionID, learnerSessionID, sourceCount, builtCount,
		),
		SystemPrompt: systemPrompt,
		UserTexts:    userTexts,
		OutputText:   outputText,
	}
}
