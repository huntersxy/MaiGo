package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"MaiGo/internal/maisaka"
	"MaiGo/internal/prompt"
)

// contextSummaryInput 一次 ConversationState 压缩所需的完整资料
type contextSummaryInput struct {
	Snapshot      *maisaka.PlannerRequestSnapshot
	Removed       []maisaka.ContextMessage
	Recent        []maisaka.ContextMessage
	Previous      *maisaka.ContextMessage
	SourceContext string
	SessionID     string
	Timestamp     time.Time
	Focus         bool
	DisplayPrefix string
}

// buildContextSummary 复用 Planner 请求快照，只追加新增资料和压缩指令
func (a *Agent) buildContextSummary(ctx context.Context, input contextSummaryInput) (*maisaka.ContextMessage, error) {
	if a == nil {
		return nil, fmt.Errorf("上下文压缩缺少 Agent")
	}
	if len(input.Removed) == 0 {
		return nil, fmt.Errorf("没有可压缩的历史内容")
	}
	if input.Snapshot == nil || len(input.Snapshot.Items) == 0 {
		return nil, fmt.Errorf("缺少 Planner 请求快照，无法生成状态摘要")
	}
	generation := a.acquireGeneration()
	if generation == nil {
		return nil, fmt.Errorf("上下文压缩缺少可用的 Planner 模型")
	}
	defer generation.wg.Done()
	selectedModel := generation.plannerModel
	modelName := generation.plannerModelName
	if selectedModel == nil {
		selectedModel = generation.model
		modelName = generation.modelName
	}
	if selectedModel == nil {
		return nil, fmt.Errorf("上下文压缩缺少可用的 Planner 模型")
	}
	instruction, err := a.contextCompactionInstruction(input.Focus)
	if err != nil {
		return nil, err
	}
	data := maisaka.BuildCompactionData(input.Removed, input.Recent, input.Previous, input.Snapshot.CoveredHistory)
	tail := maisaka.BuildCompactionTail(data, input.SourceContext, instruction)
	items := append([]maisaka.ContextItem(nil), input.Snapshot.Items...)
	items = append(items, maisaka.ContextItem{Role: maisaka.RoleUser, Text: tail})
	client := &maisakaChatClient{model: selectedModel, modelName: modelName}
	response, err := client.Chat(ctx, maisaka.ChatRequest{
		RequestKind:   "planner",
		ModelTaskName: "planner",
		Items:         items,
		Tools:         input.Snapshot.Tools,
		SessionID:     input.SessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("上下文压缩模型调用失败: %w", err)
	}
	if len(response.ToolCalls) > 0 || len(response.NativeToolCalls) > 0 {
		return nil, fmt.Errorf("压缩响应包含被禁止的工具调用，拒绝提交摘要")
	}
	state, err := maisaka.ParseConversationState(strings.TrimSpace(response.Content))
	if err != nil {
		return nil, fmt.Errorf("解析状态摘要失败: %w", err)
	}
	rendered := state.Render()
	if strings.TrimSpace(rendered) == "" {
		return nil, fmt.Errorf("状态摘要为空")
	}
	timestamp := input.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	summary := maisaka.BuildSummaryMessage(rendered, timestamp, input.DisplayPrefix)
	return &summary, nil
}

// contextCompactionInstruction 按普通/Focus 选择模板
func (a *Agent) contextCompactionInstruction(focus bool) (string, error) {
	manager, err := prompt.NewManager()
	if err != nil {
		return "", fmt.Errorf("加载上下文压缩提示词失败: %w", err)
	}
	if instance := a.instanceConfigSnapshot(); instance != nil && strings.TrimSpace(instance.Dir) != "" {
		if err := manager.LoadFromDir(prompt.CustomPromptsRoot(instance.Dir)); err != nil {
			return "", fmt.Errorf("加载自定义上下文压缩提示词失败: %w", err)
		}
	}
	name := "context_compaction"
	if focus {
		name = "context_compaction_focus"
	}
	rendered, err := manager.LoadPrompt(name, prompt.InitialLocale(), nil)
	if err != nil {
		return "", fmt.Errorf("渲染上下文压缩提示词失败: %w", err)
	}
	return rendered, nil
}
