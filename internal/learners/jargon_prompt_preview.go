package learners

import (
	"fmt"

	"go.uber.org/zap"
)

// saveJargonLearningPromptPreview 保存黑话抽取可重放 Prompt，失败仅告警不阻断学习
func saveJargonLearningPromptPreview(sessionID, learnerSessionID string, sourceCount, builtCount int, systemPrompt string, userTexts []string, outputText, modelName string) {
	if _, err := SaveLearningPromptPreview(jargonLearningPromptPreviewInput(
		sessionID, learnerSessionID, sourceCount, builtCount, systemPrompt, userTexts, outputText, modelName)); err != nil {
		zap.L().Warn("黑话抽取 Prompt 保存失败",
			zap.String("session_id", sessionID),
			zap.Error(err))
	}
}

func jargonLearningPromptPreviewInput(sessionID, learnerSessionID string, sourceCount, builtCount int, systemPrompt string, userTexts []string, outputText, modelName string) LearningPromptPreviewInput {
	metadata := map[string]any{}
	if modelName != "" {
		metadata["model_name"] = modelName
	}
	return LearningPromptPreviewInput{
		SessionID:        sessionID,
		LearnerSessionID: learnerSessionID,
		Category:         "jargon_learner",
		RequestKind:      "jargon_learner",
		SelectionReason: fmt.Sprintf(
			"会话ID: %s\nLearner会话ID: %s\n来源: trimmed_history\n学习素材数: %d\n构建消息数: %d\n用途: 从聊天记录中抽取黑话候选，本记录保存完整 LLM messages，可在推理过程页面直接重放。",
			sessionID, learnerSessionID, sourceCount, builtCount,
		),
		OutputTitle:  "黑话抽取 LLM 输出",
		Metadata:     metadata,
		SystemPrompt: systemPrompt,
		UserTexts:    userTexts,
		OutputText:   outputText,
	}
}

// saveJargonInferencePromptPreview 保存黑话含义推断可重放 Prompt，失败仅告警
func saveJargonInferencePromptPreview(sessionID, sessionName, jargonContent, stage, prompt, outputText, modelName string) {
	if _, err := SaveLearningPromptPreview(jargonInferencePromptPreviewInput(
		sessionID, sessionName, jargonContent, stage, prompt, outputText, modelName)); err != nil {
		zap.L().Warn("黑话含义推断 Prompt 保存失败",
			zap.String("session_id", sessionID),
			zap.String("content", jargonContent),
			zap.String("stage", stage),
			zap.Error(err))
	}
}

func jargonInferencePromptPreviewInput(sessionID, sessionName, jargonContent, stage, prompt, outputText, modelName string) LearningPromptPreviewInput {
	metadata := map[string]any{}
	if modelName != "" {
		metadata["model_name"] = modelName
	}
	return LearningPromptPreviewInput{
		SessionID:   sessionID,
		Category:    "jargon_learning_update",
		RequestKind: "jargon_learning_update",
		SelectionReason: fmt.Sprintf(
			"会话ID: %s\n会话名: %s\n词条: %s\n推断阶段: %s\n用途: 基于数据库消息证据推断或更新黑话含义，本记录保存单次 LLM prompt，可在推理过程页面直接重放。",
			sessionID, sessionName, jargonContent, stage,
		),
		OutputTitle: "黑话含义推断输出 - " + stage,
		Metadata:    metadata,
		UserTexts:   []string{prompt},
		OutputText:  outputText,
	}
}
