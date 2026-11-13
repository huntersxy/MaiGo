package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"maibot-go/internal/emoji_system"
	"maibot-go/internal/llm_models"
	"maibot-go/internal/prompt"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
)

var (
	// errEmojiRouterUnavailable 未配置模型路由器时明确失败，不回退直连
	errEmojiRouterUnavailable = errors.New("未配置模型路由器，无法选择表情包")
	// errEmojiVLMNotConfigured 对齐 send_emoji.py:_EMOJI_VLM_NOT_CONFIGURED_MESSAGE
	errEmojiVLMNotConfigured = errors.New(emojiVLMNotConfiguredMessage)
	// errEmojiSelectionPrompt 表情选择 Prompt 渲染失败
	errEmojiSelectionPrompt = errors.New("表情包选择提示词渲染失败")
)

// emojiSelectionResult 对齐 send_emoji.py:EmojiSelectionResult
type emojiSelectionResult struct {
	EmojiIndex int    `json:"emoji_index"`
	Reason     string `json:"reason"`
}

// emojiSelectorTask 对齐 send_emoji.py:_resolve_emoji_selector_model_task_name
// 优先级：emoji 任务 → Planner 全视觉 → vlm
func emojiSelectorTask(router *llm.Router, plannerVisual bool) (llm.TaskName, error) {
	if router == nil {
		return "", errEmojiRouterUnavailable
	}
	if router.HasTaskModels(llm.TaskEmoji) {
		return llm.TaskEmoji, nil
	}
	if plannerVisual {
		return llm.TaskPlanner, nil
	}
	if !router.HasTaskModels(llm.TaskVLM) {
		return "", errEmojiVLMNotConfigured
	}
	return llm.TaskVLM, nil
}

// emojiSelectionRequest 一次子代理选择的输入
type emojiSelectionRequest struct {
	Manager       *emojisystem.Manager
	Router        *llm.Router
	PlannerVisual bool
	Candidates    []emojisystem.Emoji
	Indices       []int
}

// emojiSelectionOutcome 子代理选择结果与对齐 Python 的选择监控元数据
type emojiSelectionOutcome struct {
	Emoji         emojisystem.Emoji
	Reason        string
	MonitorDetail map[string]any
	PromptHTMLURI string
}

// selectionMetadata 组装 send_emoji.py:selection_metadata 的等价结构
func (o emojiSelectionOutcome) selectionMetadata() map[string]any {
	metadata := map[string]any{"reason": o.Reason, "monitor_detail": o.MonitorDetail}
	if o.PromptHTMLURI != "" {
		metadata["prompt_html_uri"] = o.PromptHTMLURI
	}
	return metadata
}

// selectEmojiWithSubAgent 对齐 send_emoji.py:_select_emoji_with_sub_agent
// 拼图 + 序号提示进入一次多模态子代理请求，返回选中的表情与监控详情
func selectEmojiWithSubAgent(ctx context.Context, request emojiSelectionRequest) (emojiSelectionOutcome, error) {
	if len(request.Candidates) == 0 {
		return emojiSelectionOutcome{}, nil
	}
	if len(request.Candidates) > emojisystem.CollageMaxCandidateCount {
		request.Candidates = request.Candidates[:emojisystem.CollageMaxCandidateCount]
	}
	indices := request.Indices
	if len(indices) != len(request.Candidates) {
		indices = nextEmojiIndices(len(request.Candidates))
	}
	task, err := emojiSelectorTask(request.Router, request.PlannerVisual)
	if err != nil {
		return emojiSelectionOutcome{}, err
	}
	collage, err := buildEmojiCollage(ctx, request.Manager, request.Candidates, indices[0])
	if err != nil {
		return emojiSelectionOutcome{}, fmt.Errorf("生成表情包拼图失败：%w", err)
	}
	rows, columns := emojisystem.CalculateGridShape(len(request.Candidates))
	systemPrompt, err := renderEmojiSelectionPrompt(len(request.Candidates), rows, columns)
	if err != nil {
		return emojiSelectionOutcome{}, err
	}
	hint := fmt.Sprintf("[选择任务]\n候选总数: %d\n拼图布局: %dx%d\n请只输出 JSON。", len(request.Candidates), rows, columns)
	messages := []*schema.AgenticMessage{
		{
			Role:          schema.AgenticRoleTypeSystem,
			ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.UserInputText{Text: systemPrompt})},
		},
		{
			Role:          schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.UserInputText{Text: hint})},
		},
		{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.UserInputText{Text: "[表情包拼图候选]"}),
				schema.NewContentBlock(&schema.UserInputImage{Base64Data: emojiCollageBase64(collage), MIMEType: emojiCollageMIME}),
			},
		},
	}

	route, client, err := request.Router.NewClientForTaskWithFailover(ctx, task)
	if err != nil {
		return emojiSelectionOutcome{}, fmt.Errorf("表情包选择子代理路由失败: %w", err)
	}
	startedAt := time.Now()
	response, err := client.Generate(ctx, messages)
	if err != nil {
		return emojiSelectionOutcome{}, fmt.Errorf("表情包选择子代理调用失败: %w", err)
	}
	elapsedMs := float64(time.Since(startedAt).Milliseconds())
	outputText := strings.TrimSpace(agenticMessageText(response))
	metrics := emojiSelectionMetrics(route, response, elapsedMs)

	selection, parseErr := parseEmojiSelection(outputText)
	if parseErr != nil {
		// 对齐 Python：解析失败回退到候选首项，并把解析异常写进监控详情的 extra_sections
		detail := buildEmojiMonitorDetail(len(messages), "", outputText, metrics, []map[string]any{
			{"title": "解析异常", "content": parseErr.Error()},
		})
		return emojiSelectionOutcome{Emoji: request.Candidates[0], MonitorDetail: detail}, nil
	}
	emojiIndex := selection.EmojiIndex
	if emojiIndex < 1 || emojiIndex > len(request.Candidates) {
		emojiIndex = 1
	}
	detail := buildEmojiMonitorDetail(len(messages), selection.Reason, outputText, metrics, nil)
	return emojiSelectionOutcome{
		Emoji:         request.Candidates[emojiIndex-1],
		Reason:        strings.TrimSpace(selection.Reason),
		MonitorDetail: detail,
	}, nil
}

// renderEmojiSelectionPrompt 渲染 emoji_selection 模板，变量对齐 Python add_context
func renderEmojiSelectionPrompt(emojiCount, gridRows, gridColumns int) (string, error) {
	manager, err := prompt.NewManager()
	if err != nil {
		return "", fmt.Errorf("%w: %v", errEmojiSelectionPrompt, err)
	}
	rendered, err := manager.Render(emojiSelectionPromptName, map[string]string{
		"emoji_count":  fmt.Sprintf("%d", emojiCount),
		"grid_rows":    fmt.Sprintf("%d", gridRows),
		"grid_columns": fmt.Sprintf("%d", gridColumns),
	})
	if err != nil {
		return "", fmt.Errorf("%w: %v", errEmojiSelectionPrompt, err)
	}
	return rendered, nil
}

// parseEmojiSelection 严格解析子代理输出的 JSON，对齐 pydantic model_validate_json
func parseEmojiSelection(output string) (emojiSelectionResult, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return emojiSelectionResult{}, errors.New("子代理输出为空")
	}
	var result emojiSelectionResult
	if err := sonic.UnmarshalString(trimmed, &result); err != nil {
		return emojiSelectionResult{}, fmt.Errorf("子代理输出不是合法 JSON: %v", err)
	}
	return result, nil
}

// emojiSelectionMetrics 对齐 Python selection_metrics 的字段集合
func emojiSelectionMetrics(route llm.ResolvedRoute, response *schema.AgenticMessage, elapsedMs float64) map[string]any {
	metrics := map[string]any{
		"model_name": strings.TrimSpace(route.MaiBotModel),
		"overall_ms": elapsedMs,
	}
	if response != nil && response.ResponseMeta != nil && response.ResponseMeta.TokenUsage != nil {
		usage := response.ResponseMeta.TokenUsage
		metrics["prompt_tokens"] = usage.PromptTokens
		metrics["completion_tokens"] = usage.CompletionTokens
		metrics["total_tokens"] = usage.TotalTokens
	}
	return metrics
}

// agenticMessageText 提取模型正文，供 JSON 解析使用
func agenticMessageText(message *schema.AgenticMessage) string {
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
