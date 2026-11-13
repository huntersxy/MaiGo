package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"maibot-go/internal/chat/replyer"
	"maibot-go/internal/llm_models"
	"maibot-go/internal/maisaka"
	maisakactx "maibot-go/internal/maisaka/context"
	"maibot-go/internal/maisaka/focus"
	"maibot-go/internal/plugin_runtime/hostports"
	"maibot-go/internal/prompt"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

// maisakaChatClient 把 Maisaka 对话循环的请求落到真实模型客户端
type maisakaChatClient struct {
	model     model.AgenticModel
	modelName string
}

// Chat 执行一次 Planner 模型请求并把原生响应投影回 Maisaka 结构
func (c *maisakaChatClient) Chat(ctx context.Context, request maisaka.ChatRequest) (maisaka.ChatResponse, error) {
	if c == nil || c.model == nil {
		return maisaka.ChatResponse{}, fmt.Errorf("Maisaka ChatClient 缺少可用的模型客户端")
	}
	interruptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if request.InterruptFlag != nil {
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-request.InterruptFlag:
				cancel()
			case <-done:
			}
		}()
	}
	ctx = interruptCtx
	maxImageSide := request.MaxImageSide
	if maxImageSide <= 0 {
		maxImageSide = 512
	}
	messages := maisakaItemsToAgenticMessages(request.Items, maxImageSide)
	if len(messages) == 0 {
		return maisaka.ChatResponse{}, fmt.Errorf("Maisaka ChatClient 请求没有可发送的消息")
	}
	options := make([]model.Option, 0, 1)
	if len(request.Tools) > 0 {
		toolInfos, err := maisakaToolInfos(request.Tools)
		if err != nil {
			return maisaka.ChatResponse{}, err
		}
		if len(toolInfos) > 0 {
			options = append(options, model.WithTools(toolInfos))
		}
	}
	if request.ResponseFormat != nil {
		if format, ok := request.ResponseFormat.(string); !ok || strings.TrimSpace(format) != "" {
			ctx = llm.WithResponseFormat(ctx, request.ResponseFormat)
		}
	}
	ctx = llm.WithModelUsageMetadata(ctx, request.ModelTaskName, request.RequestKind, request.SessionID)
	var reply *schema.AgenticMessage
	var err error
	if request.Stream {
		reply, err = c.streamResponse(ctx, messages, options, request.InterruptFlag)
	} else {
		reply, err = c.model.Generate(ctx, messages, options...)
		if plannerInterruptRequested(request.InterruptFlag) {
			return maisaka.ChatResponse{}, maisaka.ErrPlannerInterrupted
		}
	}
	if err != nil {
		if errors.Is(err, maisaka.ErrPlannerInterrupted) {
			return maisaka.ChatResponse{}, err
		}
		return maisaka.ChatResponse{}, fmt.Errorf("Maisaka Planner 模型调用失败: %w", err)
	}
	response := maisakaResponseFromAgentic(reply)
	response.ModelName = c.modelName
	return response, nil
}

// streamResponse 走 eino 流并累积整条响应；打断时保留已收到的原始片段
func (c *maisakaChatClient) streamResponse(ctx context.Context, messages []*schema.AgenticMessage, options []model.Option, interruptFlag <-chan struct{}) (*schema.AgenticMessage, error) {
	sink := &llm.StreamPartialSink{}
	reader, err := c.model.Stream(llm.WithStreamPartialSink(ctx, sink), messages, options...)
	if err != nil {
		if plannerInterruptRequested(interruptFlag) {
			return nil, interruptedSinkError(sink)
		}
		return nil, err
	}
	defer reader.Close()
	chunks := make([]*schema.AgenticMessage, 0, 8)
	for {
		message, recvErr := reader.Recv()
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				break
			}
			if plannerInterruptRequested(interruptFlag) {
				return nil, interruptedSinkError(sink)
			}
			return nil, recvErr
		}
		if message != nil {
			chunks = append(chunks, message)
		}
	}
	reply, err := schema.ConcatAgenticMessages(chunks)
	if err != nil {
		return nil, err
	}
	if reply == nil {
		return nil, fmt.Errorf("Maisaka Planner 流式响应为空")
	}
	// 与 await_task_with_interrupt 一致：同时完成时优先中断
	if plannerInterruptRequested(interruptFlag) {
		return nil, interruptedSinkError(sink)
	}
	return reply, nil
}

// interruptedSinkError 把底层流记录的未完成片段转换成 Planner 打断错误
func interruptedSinkError(sink *llm.StreamPartialSink) error {
	var partial *maisaka.InterruptedStreamOutput
	if snapshot := sink.Snapshot(); snapshot != nil {
		calls := make([]maisaka.InterruptedToolCall, 0, len(snapshot.ToolCalls))
		for _, call := range snapshot.ToolCalls {
			calls = append(calls, maisaka.InterruptedToolCall{Name: call.Name, Arguments: call.Arguments})
		}
		partial = &maisaka.InterruptedStreamOutput{
			Reasoning: snapshot.Reasoning,
			Content:   snapshot.Content,
			ToolCalls: calls,
			ModelName: snapshot.ModelName,
		}
	}
	return &maisaka.PlannerInterruptError{Partial: partial}
}

func plannerInterruptRequested(flag <-chan struct{}) bool {
	if flag == nil {
		return false
	}
	select {
	case <-flag:
		return true
	default:
		return false
	}
}

func maisakaItemsToAgenticMessages(items []maisaka.ContextItem, maxImageSide int) []*schema.AgenticMessage {
	messages := make([]*schema.AgenticMessage, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(item.Text)
		switch item.Role {
		case maisaka.RoleSystem:
			if text != "" {
				messages = append(messages, schema.SystemAgenticMessage(text))
			}
		case maisaka.RoleAssistant:
			blocks := make([]*schema.ContentBlock, 0, len(item.ToolCalls)+1)
			if text != "" {
				blocks = append(blocks, schema.NewContentBlock(&schema.AssistantGenText{Text: item.Text}))
			}
			blocks = append(blocks, maisakaItemMediaBlocks(item, true, maxImageSide)...)
			for _, call := range item.ToolCalls {
				blocks = append(blocks, schema.NewContentBlock(&schema.FunctionToolCall{
					CallID:    call.CallID,
					Name:      call.Name,
					Arguments: encodeToolArguments(call.Arguments),
				}))
			}
			if len(blocks) == 0 {
				continue
			}
			messages = append(messages, &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: blocks})
		case maisaka.RoleTool:
			blocks := []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolResult{
				CallID: item.ToolCallID,
				Name:   item.ToolName,
				Content: []*schema.FunctionToolResultContentBlock{{
					Type: schema.FunctionToolResultContentBlockTypeText,
					Text: &schema.UserInputText{Text: item.Text},
				}},
			})}
			messages = append(messages, &schema.AgenticMessage{Role: schema.AgenticRoleTypeUser, ContentBlocks: blocks})
		default:
			blocks := make([]*schema.ContentBlock, 0, 1)
			if text != "" {
				blocks = append(blocks, schema.NewContentBlock(&schema.UserInputText{Text: item.Text}))
			}
			blocks = append(blocks, maisakaItemMediaBlocks(item, false, maxImageSide)...)
			if len(blocks) == 0 {
				continue
			}
			messages = append(messages, &schema.AgenticMessage{Role: schema.AgenticRoleTypeUser, ContentBlocks: blocks})
		}
	}
	return messages
}

// maisakaEmojiMaxSide 表情包上下文最大边，对齐 emoji_image_cache 的 256
const maisakaEmojiMaxSide = 256

// maisakaItemMediaBlocks 把已开启视觉且可内联的图片/表情投影为多模态内容块
func maisakaItemMediaBlocks(item maisaka.ContextItem, assistant bool, maxImageSide int) []*schema.ContentBlock {
	if !item.Visual || (len(item.Images) == 0 && len(item.Emojis) == 0) {
		return nil
	}
	blocks := make([]*schema.ContentBlock, 0, len(item.Images)+len(item.Emojis))
	for _, image := range item.Images {
		if block := maisakaMediaContentBlock(image, assistant, maxImageSide); block != nil {
			blocks = append(blocks, block)
		}
	}
	for _, emoji := range item.Emojis {
		if block := maisakaMediaContentBlock(emoji, assistant, maisakaEmojiMaxSide); block != nil {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func maisakaMediaContentBlock(component maisaka.ImageComponent, assistant bool, maxSide int) *schema.ContentBlock {
	encoded, mime, ok := maisaka.ImageComponentPayload(component)
	if !ok {
		return nil
	}
	if maxSide > 0 {
		if scaled, scaledMime, scaledOK := replyer.TranscodeContextImage(encoded, maxSide); scaledOK {
			encoded = scaled
			mime = scaledMime
		}
	}
	if assistant {
		return schema.NewContentBlock(&schema.AssistantGenImage{Base64Data: encoded, MIMEType: mime})
	}
	return schema.NewContentBlock(&schema.UserInputImage{Base64Data: encoded, MIMEType: mime})
}

func parseToolArguments(raw string) map[string]any {
	arguments := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return arguments
	}
	if err := sonic.UnmarshalString(raw, &arguments); err != nil {
		return map[string]any{}
	}
	return arguments
}

func encodeToolArguments(arguments map[string]any) string {
	if len(arguments) == 0 {
		return "{}"
	}
	encoded, err := sonic.MarshalString(arguments)
	if err != nil {
		return "{}"
	}
	return encoded
}

func maisakaToolInfos(tools []maisaka.ToolSpec) ([]*schema.ToolInfo, error) {
	infos := make([]*schema.ToolInfo, 0, len(tools))
	for _, spec := range tools {
		if strings.TrimSpace(spec.Name) == "" {
			continue
		}
		parameters := spec.Parameters
		if len(parameters) == 0 {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		encoded, err := sonic.Marshal(parameters)
		if err != nil {
			return nil, fmt.Errorf("编码工具 %s 参数结构失败: %w", spec.Name, err)
		}
		parsed := &jsonschema.Schema{}
		if err := sonic.Unmarshal(encoded, parsed); err != nil {
			return nil, fmt.Errorf("解析工具 %s 参数结构失败: %w", spec.Name, err)
		}
		infos = append(infos, &schema.ToolInfo{
			Name:        spec.Name,
			Desc:        spec.Description,
			ParamsOneOf: schema.NewParamsOneOfByJSONSchema(parsed),
		})
	}
	return infos, nil
}

func maisakaResponseFromAgentic(reply *schema.AgenticMessage) maisaka.ChatResponse {
	response := maisaka.ChatResponse{}
	if reply == nil {
		return response
	}
	var text strings.Builder
	var reasoning strings.Builder
	for _, block := range reply.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil {
				text.WriteString(block.AssistantGenText.Text)
			}
		case schema.ContentBlockTypeReasoning:
			if block.Reasoning != nil {
				reasoning.WriteString(block.Reasoning.Text)
			}
		case schema.ContentBlockTypeFunctionToolCall:
			call := block.FunctionToolCall
			if call == nil {
				continue
			}
			arguments := map[string]any{}
			if strings.TrimSpace(call.Arguments) != "" {
				if err := sonic.UnmarshalString(call.Arguments, &arguments); err != nil {
					return maisaka.ChatResponse{}
				}
			}
			response.ToolCalls = append(response.ToolCalls, maisaka.ToolCall{
				CallID:        call.CallID,
				Name:          call.Name,
				Arguments:     arguments,
				FromReasoning: toolCallFromReasoning(block),
				ExtraContent:  cloneBlockExtra(block),
			})
		}
	}
	response.Content = strings.TrimSpace(text.String())
	response.Reasoning = strings.TrimSpace(reasoning.String())
	response.NativeToolCalls = extractNativeToolSummaries(reply.ContentBlocks)
	if reply.ResponseMeta != nil && reply.ResponseMeta.TokenUsage != nil {
		usage := reply.ResponseMeta.TokenUsage
		response.Usage = maisaka.ChatUsage{
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			CacheHitTokens:   usage.PromptTokenDetails.CachedTokens,
			CacheMissTokens:  usage.PromptTokens - usage.PromptTokenDetails.CachedTokens,
		}
	}
	return response
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// replyerGeneratorConfig 由实例配置构造 replyer 提示词参数
func (a *Agent) replyerGeneratorConfig(isGroup bool) replyer.GeneratorConfig {
	cfg := replyer.GeneratorConfig{IsGroupChat: &isGroup, PromptLocale: prompt.InitialLocale()}
	bot := a.instanceBotConfig()
	if bot == nil {
		if a != nil {
			cfg.BotName = a.botName()
		}
		return cfg
	}
	cfg.BotName = firstNonEmptyString(strings.TrimSpace(bot.Bot.Nickname), a.botName())
	cfg.BotAliases = append([]string(nil), bot.Bot.AliasNames...)
	cfg.Personality = strings.TrimSpace(bot.Personality.Personality)
	cfg.ReplyStyle = strings.TrimSpace(bot.Personality.ReplyStyle)
	cfg.MultipleReplyStyles = append([]string(nil), bot.Personality.MultipleReplyStyle...)
	cfg.MultipleProbability = bot.Personality.MultipleProbability
	cfg.GroupChatPrompt = strings.TrimSpace(bot.Chat.ReplyStyle.GroupChatPrompt)
	cfg.PrivateChatPrompt = strings.TrimSpace(bot.Chat.ReplyStyle.PrivateChatPrompts)
	cfg.ReplyerRetroPrompt = bot.Debug.ReplyerRetroPrompt
	cfg.SaveReasoning = bot.Debug.SaveMaisakaReasoning
	cfg.KeepPreviewBase64 = bot.Debug.KeepPromptPreviewJsonBase64
	cfg.KeywordRules = replyer.KeywordRulesFromConfig(bot.KeywordReaction.KeywordRules)
	cfg.RegexRules = replyer.RegexRulesFromConfig(bot.KeywordReaction.RegexRules)
	return cfg
}

// replyerHistoryMessages 把 Maisaka 历史投影为 replyer 可渲染的聊天记录
func replyerHistoryMessages(history []maisaka.ContextMessage) []replyer.ChatMessage {
	out := make([]replyer.ChatMessage, 0, len(history))
	for _, message := range history {
		if !replyerHistoryMessageAllowed(message) {
			continue
		}
		if strings.TrimSpace(message.Content) == "" && len(message.Images) == 0 && len(message.Emojis) == 0 {
			continue
		}
		role := "user"
		if message.Role == maisaka.RoleAssistant {
			role = "assistant"
		}
		sourceKind := message.Source
		if sourceKind == "" {
			if message.Role == maisaka.RoleUser {
				sourceKind = "user"
			} else {
				sourceKind = "assistant"
			}
		}
		out = append(out, replyer.ChatMessage{
			Role:        role,
			MessageID:   message.MessageID,
			IsNotify:    message.MetadataBool("is_notify"),
			SourceKind:  sourceKind,
			IsBotSelf:   message.Role == maisaka.RoleAssistant,
			SenderName:  firstNonEmptyString(message.MetadataString("sender_name"), message.MetadataString("nickname"), message.UserID),
			SenderID:    message.UserID,
			Platform:    message.Platform,
			Timestamp:   message.Timestamp,
			Content:     replyerHistoryContent(message),
			VisibleText: strings.TrimSpace(message.VisibleText),
			Parts:       replyerMessageParts(message),
			Images:      replyerMessageImages(message),
			Quotes:      replyerQuotes(message),
		})
	}
	return out
}

// replyerHistoryContent 返回 replyer 使用的正文
//
// 对齐 Python replyer：guided_reply 取 visible_text 去掉说话人前缀后的正文，
// 避免把 Planner 前缀与 [时间][昵称] 一起送进提示词。
func replyerHistoryContent(message maisaka.ContextMessage) string {
	if message.Source == maisaka.SourceGuidedReply {
		if visible := strings.TrimSpace(message.VisibleText); visible != "" {
			if _, body := maisakactx.ParseSpeakerContent(visible); strings.TrimSpace(body) != "" {
				return strings.TrimSpace(body)
			}
		}
	}
	return message.Content
}

// replyerHistoryMessageAllowed 对齐 _is_replyer_filtered_history_message：
// 只保留真实收发消息与 bot 自身写回，排除工具结果、参考、压缩摘要等内部上下文
func replyerHistoryMessageAllowed(message maisaka.ContextMessage) bool {
	if message.Role != maisaka.RoleUser && message.Role != maisaka.RoleAssistant {
		return false
	}
	if message.MetadataBool("is_notify") {
		return false
	}
	switch message.Source {
	case "", maisaka.SourceUser, maisaka.SourceGuidedReply:
		return true
	default:
		return false
	}
}

// replyerMessageParts 保留消息内文本与图片的相对顺序；文本在前、图片按上下文顺序追加
func replyerMessageParts(message maisaka.ContextMessage) []replyer.MessagePart {
	parts := make([]replyer.MessagePart, 0, len(message.Images)+len(message.Emojis)+1)
	if text := replyerHistoryContent(message); strings.TrimSpace(text) != "" {
		parts = append(parts, replyer.MessagePart{Kind: replyer.PartText, Text: text})
	}
	for _, image := range message.Images {
		parts = append(parts, replyerMediaPart(replyer.PartImage, image))
	}
	for _, emoji := range message.Emojis {
		parts = append(parts, replyerMediaPart(replyer.PartEmoji, emoji))
	}
	return parts
}

// replyerMediaPart 统一归一化图片/表情的内联内容与格式；无法内联时保留描述供文本回退
func replyerMediaPart(kind replyer.MessagePartKind, component maisaka.ImageComponent) replyer.MessagePart {
	part := replyer.MessagePart{
		Kind:        kind,
		ImageFormat: strings.TrimSpace(component.Format),
		ImageBase64: strings.TrimSpace(component.MediaRef),
		Description: strings.TrimSpace(component.Description),
	}
	if encoded, mime, ok := maisaka.ImageComponentPayload(component); ok {
		part.ImageBase64 = encoded
		if part.ImageFormat == "" {
			part.ImageFormat = mime
		}
	}
	return part
}

func replyerMessageImages(message maisaka.ContextMessage) []replyer.MessageImage {
	if len(message.Images) == 0 {
		return nil
	}
	out := make([]replyer.MessageImage, 0, len(message.Images))
	for _, image := range message.Images {
		entry := replyer.MessageImage{Format: strings.TrimSpace(image.Format), Base64: strings.TrimSpace(image.MediaRef)}
		if encoded, mime, ok := maisaka.ImageComponentPayload(image); ok {
			entry.Base64 = encoded
			entry.Format = mime
		}
		out = append(out, entry)
	}
	return out
}

// generateReplyerText 用独立 Replyer 生成循环产出可见回复
// Hook/重试/视觉开关/模型重选/表达选择/PromptBuilder 统一由 replyer.ReplyGenerator 负责
func (a *Agent) generateReplyerText(ctx context.Context, plannerContent string, target *maisaka.ContextMessage, history []maisaka.ContextMessage, isGroup bool, replyToolArgs map[string]any) (string, error) {
	if a == nil {
		return "", fmt.Errorf("Agent 未初始化")
	}
	generation := a.currentGeneration()
	if generation == nil || generation.model == nil {
		return "", fmt.Errorf("Replyer 模型未就绪")
	}
	manager, err := prompt.NewManager()
	if err != nil {
		return "", fmt.Errorf("加载 Replyer 提示词失败: %w", err)
	}
	generatorConfig := a.replyerGeneratorConfig(isGroup)
	imageQuality := ""
	maxImageNum := 0
	if instance := a.instanceConfig(); instance != nil {
		if strings.TrimSpace(instance.Dir) != "" {
			if err := manager.LoadFromDir(prompt.CustomPromptsRoot(instance.Dir)); err != nil {
				return "", fmt.Errorf("加载自定义 Replyer 提示词失败: %w", err)
			}
		}
		imageQuality = instance.Bot.Visual.ImageQuality
		maxImageNum = instance.Bot.Visual.MaxImageNum
		generatorConfig.AttentionDriftBlock = focus.BuildAttentionDriftPromptBlock(&isGroup, prompt.InitialLocale(), instance.Bot.Experimental.GroupAttentionDrift, instance.Bot.Experimental.PrivateAttentionDrift)
	}
	builder := &replyer.PromptBuilder{
		PromptLoader: replyer.NewPromptManagerLoader(manager),
		Config:       generatorConfig,
		Now:          time.Now,
	}
	targetMessage := replyerTarget(target)
	if targetMessage == nil {
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Role == maisaka.RoleUser && replyerHistoryMessageAllowed(history[i]) && strings.TrimSpace(history[i].Content) != "" {
				message := history[i]
				targetMessage = replyerTarget(&message)
				break
			}
		}
	}
	reference := strings.TrimSpace(plannerContent)
	replyStyle := replyer.ToolArgString(replyToolArgs, "reply_style")
	if value := replyer.ToolArgString(replyToolArgs, "reply_reference"); value != "" {
		reference = value
	}
	sessionID := replyer.SessionIDFromContext(target, history)
	visualBridge := a.agentVisual()
	replyerHistory := replyerHistoryMessages(history)
	req := replyer.ReplyRequest{
		History:             replyerHistory,
		Target:              targetMessage,
		ReplyReason:         strings.TrimSpace(plannerContent),
		ReplyReference:      reference,
		ReplyStyle:          replyStyle,
		ReplyToolArgs:       replyToolArgs,
		SessionID:           sessionID,
		ImageQuality:        imageQuality,
		MaxImageNum:         maxImageNum,
		EnableVisualPlanner: visualBridge.visualPlannerEnabled(),
		ImageRecognitionRefresher: func(refreshCtx context.Context, messages []replyer.ChatMessage) int {
			return visualBridge.refreshReplyerVisualPlaceholders(refreshCtx, messages)
		},
	}
	client := newReplyerAgenticClient(a, generation, "replyer", generation.model, generation.modelName)
	generator := replyer.NewReplyGenerator(builder, client)
	generator.Hooks = hostports.ReplyerHookInvoker{}
	generator.Expressions = replyer.LearnersExpressionSelector{}
	generator.LLMFactory = a.replyerLLMFactory(ctx, generation, client)
	success, result := generator.GenerateReplyWithContext(ctx, req)
	if !success {
		message := strings.TrimSpace(result.ErrorMessage)
		if message == "" {
			message = "Replyer 生成失败"
		}
		return "", fmt.Errorf("%s", message)
	}
	text := strings.TrimSpace(result.Completion.ResponseText)
	if text == "" {
		return "", fmt.Errorf("Replyer 返回空回复")
	}
	return text, nil
}

func replyerTarget(target *maisaka.ContextMessage) *replyer.ReplyTarget {
	if target == nil {
		return nil
	}
	return &replyer.ReplyTarget{
		MessageID:  target.MessageID,
		SenderName: firstNonEmptyString(target.MetadataString("sender_name"), target.MetadataString("nickname"), target.UserID),
		SenderID:   target.UserID,
		Platform:   target.Platform,
		Content:    target.Content,
		IsBotSelf:  target.Role == maisaka.RoleAssistant,
		Parts:      replyerMessageParts(*target),
		Images:     replyerMessageImages(*target),
		Quotes:     replyerQuotes(*target),
	}
}

// replyerQuotes 把 Maisaka 元数据里的引用目标投影为 replyer 引用组件
func replyerQuotes(message maisaka.ContextMessage) []replyer.QuoteReference {
	quoteIDs := maisakactx.ExtractQuoteIDs([]maisaka.ContextMessage{message})
	if len(quoteIDs) == 0 {
		return nil
	}
	sender := message.MetadataString("quote_sender")
	content := message.MetadataString("quote_content")
	quotes := make([]replyer.QuoteReference, 0, len(quoteIDs))
	for index, quoteID := range quoteIDs {
		quote := replyer.QuoteReference{MessageID: quoteID}
		if index == 0 {
			quote.Sender = sender
			quote.Content = content
		}
		quotes = append(quotes, quote)
	}
	return quotes
}
