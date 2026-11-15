package runtime

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/llm_models"

	"github.com/cloudwego/eino/schema"
)

// generateSplitterText 供 replyer LLM 断句调用：任务 fast_model（空列表时按 MaiBot 继用链回退 utils）
func (a *Agent) generateSplitterText(ctx context.Context, prompt string) (string, error) {
	if a == nil {
		return "", fmt.Errorf("Agent 未初始化")
	}
	router := a.llmRouterSnapshot()
	if router == nil {
		return "", fmt.Errorf("LLM 路由器未接入")
	}
	_, client, err := router.NewClientForTaskWithFailover(ctx, llm.TaskFastModel)
	if err != nil {
		return "", err
	}
	msg, err := client.Generate(ctx, []*schema.AgenticMessage{
		schema.UserAgenticMessage(prompt),
	})
	if err != nil {
		return "", err
	}
	return splitterPlainText(msg), nil
}

// splitterPlainText 只提取文本块，忽略工具调用等非文本内容
func splitterPlainText(msg *schema.AgenticMessage) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	for _, block := range msg.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeUserInputText:
			if block.UserInputText != nil {
				b.WriteString(block.UserInputText.Text)
			}
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil {
				b.WriteString(block.AssistantGenText.Text)
			}
		}
	}
	return strings.TrimSpace(b.String())
}
