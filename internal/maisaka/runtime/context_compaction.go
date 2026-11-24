package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"MaiGo/internal/maisaka"
	"MaiGo/internal/prompt"

	"go.uber.org/zap"
)

const compactionRetryInstruction = "上一次未能生成可用的对话摘要。请重新根据前面的对话资料和摘要要求生成摘要。\n" +
	"这是摘要任务，禁止调用任何工具，包括 reply、wait 或其他工具；" +
	"不要执行或继续此前 Planner 的动作流程。\n" +
	"直接输出合法的摘要 JSON 对象，不加代码块、分析或解释。" +
	"使用 topics、background、progress、pending、agreements 五个分类，值均为字符串数组；" +
	"没有内容的分类使用空数组。保留仍有用的实际对话信息，不要返回空摘要。"

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
	if input.Snapshot == nil || input.Snapshot.Client == nil || len(input.Snapshot.Items) == 0 {
		return nil, fmt.Errorf("缺少 Planner 请求快照，无法生成状态摘要")
	}
	instruction, err := a.contextCompactionInstruction(input.Focus)
	if err != nil {
		return nil, err
	}
	data := maisaka.BuildCompactionData(input.Removed, input.Recent, input.Previous, input.Snapshot.CoveredHistory)
	tail := maisaka.BuildCompactionTail(data, input.SourceContext, instruction)
	items := make([]maisaka.ContextItem, 0, len(input.Snapshot.Items)+2)
	items = append(items, input.Snapshot.Items...)
	items = append(items, maisaka.ContextItem{Role: maisaka.RoleUser, Text: tail})
	request := maisaka.ChatRequest{
		RequestKind:   "maisaka.context_compaction",
		ModelTaskName: "planner",
		Items:         items,
		Tools:         input.Snapshot.Tools,
		SessionID:     input.SessionID,
		SystemPrompt:  input.Snapshot.SystemPrompt,
		Stream:        input.Snapshot.Stream,
		MaxImageSide:  input.Snapshot.MaxImageSide,
	}
	rendered, err := generateCompactionSummary(ctx, input.Snapshot.Client, request)
	if err != nil && ctx.Err() == nil {
		zap.L().Warn("上下文压缩首次失败，追加禁止工具调用的摘要指令后重试一次", zap.Error(err))
		request.Items = append(request.Items, maisaka.ContextItem{Role: maisaka.RoleUser, Text: compactionRetryInstruction})
		rendered, err = generateCompactionSummary(ctx, input.Snapshot.Client, request)
	}
	if err != nil {
		return nil, err
	}
	timestamp := input.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	summary := maisaka.BuildSummaryMessage(rendered, timestamp, input.DisplayPrefix)
	return &summary, nil
}

func generateCompactionSummary(ctx context.Context, client maisaka.ChatClient, request maisaka.ChatRequest) (string, error) {
	response, err := client.Chat(ctx, request)
	if err != nil {
		return "", fmt.Errorf("上下文压缩模型调用失败: %w", err)
	}
	if len(response.ToolCalls) > 0 || len(response.NativeToolCalls) > 0 {
		return "", fmt.Errorf("压缩响应包含被禁止的工具调用，拒绝提交摘要")
	}
	state, err := maisaka.ParseConversationState(response.Content)
	if err != nil {
		return "", fmt.Errorf("解析状态摘要失败: %w", err)
	}
	rendered := state.Render()
	if rendered == "" {
		return "", fmt.Errorf("状态摘要为空")
	}
	return rendered, nil
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
