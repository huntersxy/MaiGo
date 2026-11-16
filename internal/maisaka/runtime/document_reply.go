package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"MaiGo/internal/chat/replyer"
	"MaiGo/internal/llm_models"
	maisakafocus "MaiGo/internal/maisaka/focus"
	"MaiGo/internal/plugin_runtime/hostports"
	"MaiGo/internal/prompt"
)

// documentReplyRequirements 对齐 document_generator：format_instruction 为空时保持空要求
func documentReplyRequirements(formatInstruction string) string {
	return strings.TrimSpace(formatInstruction)
}

// GenerateDocumentReply 对齐插件能力 maisaka.reply.generate：
// 复用 Replyer 生成循环与人格，但使用独立长文写作模板
func (a *Agent) GenerateDocumentReply(ctx context.Context, streamID, reference, formatInstruction, modelName string) (map[string]any, error) {
	if a == nil {
		return nil, fmt.Errorf("Agent 未初始化")
	}
	streamID = strings.TrimSpace(streamID)
	if streamID == "" {
		return nil, fmt.Errorf("缺少必要参数 stream_id")
	}
	rawReference := reference
	reference = strings.TrimSpace(reference)
	// 对齐 Python：长度按原始入参计，空白不豁免上限
	if reference == "" || len([]rune(rawReference)) > 500000 {
		return nil, fmt.Errorf("reference 必须是 1–500000 字的非空文本")
	}
	if len([]rune(formatInstruction)) > 8000 {
		return nil, fmt.Errorf("format_instruction 必须是不超过 8000 字的文本")
	}
	rawModelName := modelName
	modelName = strings.TrimSpace(modelName)
	if len([]rune(rawModelName)) > 200 {
		return nil, fmt.Errorf("model_name 必须是不超过 200 字的宿主模型名称")
	}
	if a.chatSessions == nil {
		return nil, fmt.Errorf("聊天会话管理器未初始化")
	}
	record, err := a.chatSessions.GetExistingBySessionID(ctx, streamID)
	if err != nil {
		return nil, fmt.Errorf("查询聊天流失败: %w", err)
	}
	if record == nil {
		return nil, fmt.Errorf("未找到已存在的聊天流: %s", streamID)
	}

	generation := a.currentGeneration()
	if generation == nil || generation.model == nil {
		return nil, fmt.Errorf("Replyer 模型未就绪")
	}
	manager, err := prompt.NewManager()
	if err != nil {
		return nil, fmt.Errorf("加载 Replyer 提示词失败: %w", err)
	}
	instance := a.instanceConfig()
	isGroup := record.IsGroup()
	generatorConfig := a.replyerGeneratorConfig(isGroup)
	if instance != nil {
		if strings.TrimSpace(instance.Dir) != "" {
			if err := manager.LoadFromDir(prompt.CustomPromptsRoot(instance.Dir)); err != nil {
				return nil, fmt.Errorf("加载自定义 Replyer 提示词失败: %w", err)
			}
		}
		generatorConfig.AttentionDriftBlock = maisakafocus.BuildAttentionDriftPromptBlock(
			&isGroup,
			prompt.InitialLocale(),
			instance.Bot.Experimental.GroupAttentionDrift,
			instance.Bot.Experimental.PrivateAttentionDrift,
		)
	}
	builder := &replyer.DocumentBuilder{
		PromptBuilder: replyer.PromptBuilder{
			PromptLoader:       replyer.NewPromptManagerLoader(manager),
			LocalePromptLoader: replyer.NewCreativeLocaleLoader(manager),
			Config:             generatorConfig,
			Now:                time.Now,
		},
		DocumentModel: modelName,
	}
	resolvedModel := builder.ResolveDocumentModel("")
	activeModel := generation.model
	activeModelName := generation.modelName
	if resolvedModel != "" && resolvedModel != generation.modelName {
		// 显式文档模型同样走路由层，保留重试、任务级 hard_timeout 与失败快照
		router := a.llmRouterSnapshot()
		if router == nil {
			return nil, fmt.Errorf("%w: 文档生成模型 %q 无法选择", ErrModelRouterUnavailable, resolvedModel)
		}
		_, direct, err := router.NewClientForTaskWithFailoverAndModel(ctx, llm.TaskReplyer, resolvedModel)
		if err != nil {
			return nil, fmt.Errorf("文档生成模型 %q 不可用: %w", resolvedModel, err)
		}
		activeModel = direct
		activeModelName = resolvedModel
	}

	replyToolArgs := map[string]any{
		"reply_reference": reference,
		"reply_style":     "长回复",
	}
	requirements := documentReplyRequirements(formatInstruction)
	req := replyer.ReplyRequest{
		History:           []replyer.ChatMessage{},
		ReplyReason:       reference,
		ReplyReference:    reference,
		ReplyStyle:        "长回复",
		ReplyToolArgs:     replyToolArgs,
		SessionID:         streamID,
		ReplyRequirements: requirements,
	}

	client := newReplyerAgenticClient(a, generation, "replyer", activeModel, activeModelName)
	generator := replyer.NewDocumentReplyGenerator(builder, client, resolvedModel)
	generator.Hooks = hostports.ReplyerHookInvoker{}
	generator.Expressions = replyer.LearnersExpressionSelector{}
	generator.LLMFactory = a.replyerLLMFactory(ctx, generation, client)
	success, result := generator.GenerateReplyWithContext(ctx, req)
	if !success {
		message := strings.TrimSpace(result.ErrorMessage)
		if message == "" {
			message = "Replyer 生成失败"
		}
		return nil, fmt.Errorf("%s", message)
	}
	text := strings.TrimSpace(result.Completion.ResponseText)
	if text == "" {
		return nil, fmt.Errorf("Replyer 返回了空文本")
	}
	ids := make([]uint, 0, len(result.SelectedExpressionIDs))
	for _, id := range result.SelectedExpressionIDs {
		ids = append(ids, uint(id))
	}
	return map[string]any{
		"text":                    text,
		"selected_expression_ids": ids,
	}, nil
}
