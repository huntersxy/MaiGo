package emojisystem

import (
	"context"
	"fmt"
	"strings"

	"maibot-go/internal/llm_models"
	"maibot-go/internal/prompt"

	"github.com/cloudwego/eino/schema"
)

func generateEmojiDescription(ctx context.Context, router *llm.Router, prompts *prompt.Manager, imageB64, mime, imageType string) (string, error) {
	if imageType == "" {
		imageType = "STATIC"
	}
	return generateEmojiVLM(ctx, router, prompts, "emoji_content_analysis", map[string]string{"image_type": imageType}, imageB64, mime)
}

func generateEmojiVLM(ctx context.Context, router *llm.Router, prompts *prompt.Manager, promptName string, vars map[string]string, imageB64, mime string) (string, error) {
	if router == nil {
		return "", errVLMUnavailable
	}
	if !router.HasTaskModels(llm.TaskVLM) {
		return "", errVLMNotConfigured
	}
	_, client, err := router.NewClientForTaskWithFailover(ctx, llm.TaskVLM)
	if err != nil {
		return "", fmt.Errorf("emojisystem: VLM 路由失败: %w", err)
	}
	if prompts == nil {
		return "", errPromptUnavailable
	}
	promptText, err := prompts.Render(promptName, vars)
	if err != nil {
		return "", err
	}
	msg := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.UserInputText{Text: promptText}),
			schema.NewContentBlock(&schema.UserInputImage{Base64Data: imageB64, MIMEType: mime}),
		},
	}
	out, err := client.Generate(ctx, []*schema.AgenticMessage{msg})
	if err != nil {
		return "", fmt.Errorf("emojisystem: VLM 生成失败: %w", err)
	}
	return strings.TrimSpace(agenticPlainText(out)), nil
}

func agenticPlainText(msg *schema.AgenticMessage) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	for _, block := range msg.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil {
				b.WriteString(block.AssistantGenText.Text)
			}
		case schema.ContentBlockTypeUserInputText:
			if block.UserInputText != nil {
				b.WriteString(block.UserInputText.Text)
			}
		}
	}
	return b.String()
}
