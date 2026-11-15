package learners

import (
	"fmt"
	"strings"
	"time"

	"MaiGo/internal/maisaka/display"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

// LearningPromptPreviewInput 学习链路可重放 Prompt 预览参数
type LearningPromptPreviewInput struct {
	SessionID        string
	LearnerSessionID string
	Category         string
	RequestKind      string
	SelectionReason  string
	OutputTitle      string
	Metadata         map[string]any
	SystemPrompt     string
	UserTexts        []string
	OutputText       string
}

const learningPromptPreviewSchemaVersion = 6

var learningPromptPreviewJSONConfig = sonic.Config{EscapeHTML: false}.Froze()

// BuildLearningPromptPreviewPayload 对齐 Python PromptCLIVisualizer v6 预览 schema
func BuildLearningPromptPreviewPayload(input LearningPromptPreviewInput) map[string]any {
	requestKind := strings.TrimSpace(input.RequestKind)
	if requestKind == "" {
		requestKind = strings.TrimSpace(input.Category)
	}
	outputTitle := strings.TrimSpace(input.OutputTitle)
	if outputTitle == "" {
		outputTitle = "输出结果"
	}
	requestItems := make([]any, 0, len(input.UserTexts)+1)
	if systemPrompt := strings.TrimSpace(input.SystemPrompt); systemPrompt != "" {
		requestItems = append(requestItems, newLearningPreviewItem("SystemMessageItem", systemPrompt))
	}
	for _, userText := range input.UserTexts {
		if text := strings.TrimSpace(userText); text != "" {
			requestItems = append(requestItems, newLearningPreviewItem("UserMessageItem", text))
		}
	}
	outputItems := make([]any, 0, 1)
	if outputText := strings.TrimSpace(input.OutputText); outputText != "" {
		outputItems = append(outputItems, newLearningPreviewItem("AssistantMessageItem", outputText))
	}
	return map[string]any{
		"schema_version": learningPromptPreviewSchemaVersion,
		"request": map[string]any{
			"kind":             requestKind,
			"selection_reason": input.SelectionReason,
		},
		"metadata":            display.NormalizePreviewMetadata(input.Metadata),
		"presentation":        map[string]any{"output_title": outputTitle},
		"request_items":       requestItems,
		"output_items":        outputItems,
		"tool_definitions":    []any{},
		"generation_attempts": []any{},
	}
}

func newLearningPreviewItem(itemType, text string) map[string]any {
	return map[string]any{
		"item_type": itemType,
		"meta": map[string]any{
			"item_id":         uuid.NewString(),
			"logical_turn_id": nil,
			"timestamp":       time.Now().Format(time.RFC3339Nano),
		},
		"parts": []any{
			map[string]any{"type": "text", "text": text},
		},
	}
}

// SaveLearningPromptPreview 使用默认落盘器保存学习 Prompt 预览
func SaveLearningPromptPreview(input LearningPromptPreviewInput) (string, error) {
	return SaveLearningPromptPreviewWithLogger(input, display.DefaultPromptPreviewLogger())
}

// SaveLearningPromptPreviewWithLogger 使用指定落盘器保存学习 Prompt 预览
func SaveLearningPromptPreviewWithLogger(input LearningPromptPreviewInput, logger *display.PromptPreviewLogger) (string, error) {
	if logger == nil {
		return "", fmt.Errorf("学习 Prompt 预览落盘器不可用")
	}
	category := strings.TrimSpace(input.Category)
	if category == "" {
		return "", fmt.Errorf("学习 Prompt 预览缺少 category")
	}
	raw, err := learningPromptPreviewJSONConfig.MarshalIndent(BuildLearningPromptPreviewPayload(input), "", "  ")
	if err != nil {
		return "", err
	}
	return logger.SavePreviewFile(input.SessionID, category, string(raw), nil, nil)
}
