package imagesystem

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"MaiGo/internal/llm_models"
	"MaiGo/internal/prompt"

	"github.com/cloudwego/eino/schema"
)

// RouterDescriber 基于 TaskVLM 的图片描述器，对应 Python vlm = LLMServiceClient(task_name="vlm")
type RouterDescriber struct {
	Router  *llm.Router
	Prompts *prompt.Manager
}

// Available 判断是否配置了视觉模型任务与提示词
func (d *RouterDescriber) Available() bool {
	return d != nil && d.Router != nil && d.Prompts != nil && d.Router.HasTaskModels(llm.TaskVLM)
}

// Describe 渲染 image_description 并调用 TaskVLM
func (d *RouterDescriber) Describe(ctx context.Context, data []byte, format string) (string, error) {
	if !d.Available() {
		return "", fmt.Errorf("imagesystem: VLM 未配置")
	}
	if len(data) == 0 {
		return "", fmt.Errorf("imagesystem: 图片字节数据为空")
	}
	promptText, err := d.Prompts.Render("image_description", nil)
	if err != nil {
		return "", fmt.Errorf("imagesystem: 渲染图片描述提示词失败: %w", err)
	}
	_, client, err := d.Router.NewClientForTaskWithFailover(ctx, llm.TaskVLM)
	if err != nil {
		return "", fmt.Errorf("imagesystem: VLM 路由失败: %w", err)
	}
	block := schema.NewContentBlock(&schema.UserInputText{Text: promptText})
	imageBlock := schema.NewContentBlock(&schema.UserInputImage{
		Base64Data: base64.StdEncoding.EncodeToString(data),
		MIMEType:   imageMIMEType(data, format),
	})
	message := &schema.AgenticMessage{
		Role:          schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{block, imageBlock},
	}
	output, err := client.Generate(ctx, []*schema.AgenticMessage{message})
	if err != nil {
		return "", fmt.Errorf("imagesystem: VLM 生成失败: %w", err)
	}
	return strings.TrimSpace(agenticText(output)), nil
}

func imageMIMEType(data []byte, format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpeg", "jpg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "bmp":
		return "image/bmp"
	case "tiff", "tif":
		return "image/tiff"
	case "ico":
		return "image/x-icon"
	}
	detected := http.DetectContentType(data)
	if strings.HasPrefix(detected, "image/") {
		return detected
	}
	return "application/octet-stream"
}

func agenticText(message *schema.AgenticMessage) string {
	if message == nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil {
				builder.WriteString(block.AssistantGenText.Text)
			}
		case schema.ContentBlockTypeUserInputText:
			if block.UserInputText != nil {
				builder.WriteString(block.UserInputText.Text)
			}
		}
	}
	return builder.String()
}
