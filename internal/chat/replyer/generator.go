package replyer

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"maibot-go/internal/llm_models"
	"maibot-go/internal/maisaka/display"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ReplyGenerator 对齐 BaseMaisakaReplyGenerator 的生成循环
// PromptBuilder 负责提示词，LLMClient 负责实际模型调用，HookInvoker/ExpressionSelector 为插件端口
type ReplyGenerator struct {
	Builder         *PromptBuilder
	DocumentBuilder *DocumentBuilder

	LLM         LLMClient
	LLMFactory  LLMClientFactory
	Hooks       HookInvoker
	Expressions ExpressionSelector

	RequestType    string
	DefaultTask    string
	MaxHookRetries int
	Now            func() time.Time

	// DocumentMode 对齐 DocumentReplyGenerator：不注入表达习惯、不走 retro、独立写作模板
	DocumentMode  bool
	DocumentModel string

	// PreviewLogger 供测试替换预览落盘目录；为空时使用默认日志目录
	PreviewLogger *display.PromptPreviewLogger
}

// NewReplyGenerator 创建 replyer 生成器
func NewReplyGenerator(builder *PromptBuilder, client LLMClient) *ReplyGenerator {
	return &ReplyGenerator{
		Builder:        builder,
		LLM:            client,
		RequestType:    "maisaka.replyer",
		DefaultTask:    "replyer",
		MaxHookRetries: MaxHookRetries,
		Now:            time.Now,
	}
}

// NewDocumentReplyGenerator 对齐 DocumentReplyGenerator 构造
func NewDocumentReplyGenerator(builder *DocumentBuilder, client LLMClient, modelName string) *ReplyGenerator {
	generator := NewReplyGenerator(nil, client)
	if builder != nil {
		generator.Builder = &builder.PromptBuilder
	}
	generator.DocumentBuilder = builder
	generator.DocumentMode = true
	generator.DocumentModel = strings.TrimSpace(modelName)
	return generator
}

// GenerateReplyWithContext 对齐 generate_reply_with_context，返回 (success, result)
func (g *ReplyGenerator) GenerateReplyWithContext(ctx context.Context, req ReplyRequest) (bool, ReplyGenerationResult) {
	startedAt := g.now()
	result := ReplyGenerationResult{}
	finalize := func(success bool) (bool, ReplyGenerationResult) {
		result.Success = success
		return success, result
	}

	if g.LLM == nil {
		result.ErrorMessage = "回复模型尚未初始化"
		return finalize(false)
	}
	if req.History == nil {
		result.ErrorMessage = "聊天历史为空"
		return finalize(false)
	}

	activeToolArgs := normalizeReplyToolArgs(req.ReplyToolArgs)
	pluginReplyPrompt := stringValue(activeToolArgs["_plugin_reply_prompt"])
	filteredHistory := filterReplyerHistory(req.History)
	sessionID := g.resolveSessionID(req)

	replyContext, err := g.buildReplyContext(ctx, filteredHistory, req.Target, req.ReplyReason, sessionID, req.SubAgentRunner, activeToolArgs)
	if err != nil {
		zap.L().Error("构建回复上下文失败", zap.Error(err))
		result.ErrorMessage = "构建回复上下文失败: " + err.Error()
		result.Metrics = GenerationMetrics{OverallMS: durationMS(startedAt, g.now())}
		return finalize(false)
	}

	mergedExpressionHabits := strings.TrimSpace(req.ExpressionHabits)
	if mergedExpressionHabits == "" {
		mergedExpressionHabits = replyContext.ExpressionHabits
	}
	if req.SelectedExpressionIDs != nil {
		result.SelectedExpressionIDs = append([]int(nil), req.SelectedExpressionIDs...)
	} else {
		result.SelectedExpressionIDs = append([]int(nil), replyContext.SelectedExpressionIDs...)
	}
	if req.SelectedExpressions != nil {
		result.SelectedExpressionDetails = cloneExpressionMaps(req.SelectedExpressions)
	} else {
		result.SelectedExpressionDetails = cloneExpressionMaps(replyContext.SelectedExpressions)
	}
	// 对齐 Python：合并后的选择结果贯穿 Hook 与重试，调用方显式传入时优先
	req.SelectedExpressionIDs = append([]int(nil), result.SelectedExpressionIDs...)
	req.SelectedExpressions = cloneExpressionMaps(result.SelectedExpressionDetails)

	retryConstraints := make([]string, 0)
	retryReasons := make([]string, 0)
	retryEvents := make([]map[string]any, 0)
	hookRewriteEvents := make([]map[string]string, 0)
	retryCount := 0
	allAttempts := make([]GenerationAttempt, 0)
	aggregatePromptTokens := 0
	aggregateCompletionTokens := 0
	aggregateTotalTokens := 0
	reasoningContinuationCount := 0
	reasoningRegenerationRequested := false
	retroPeriodRegenerationRequested := false

	defaultTaskName := g.defaultTaskName()
	var responseText string
	var finalResult GenerationResult
	var finalRequestItems []llm.ContextItem
	var promptMS, llmMS float64
	var promptPreview string

	for {
		beforeRequest := g.invokeBeforeRequestHook(ctx, sessionID, filteredHistory, req, defaultTaskName, activeToolArgs, retryCount)
		if raw, ok := beforeRequest.Kwargs["reply_tool_args"].(map[string]any); ok {
			activeToolArgs = normalizeReplyToolArgs(raw)
		}
		activeTaskName := strings.TrimSpace(stringValue(beforeRequest.Kwargs["task_name"]))
		if activeTaskName == "" {
			activeTaskName = defaultTaskName
		}
		activeModelName := strings.TrimSpace(stringValue(beforeRequest.Kwargs["model_name"]))
		extraPrompt := stringValue(beforeRequest.Kwargs["extra_prompt"])
		if _, ok := beforeRequest.Kwargs["extra_prompt"]; !ok {
			extraPrompt = pluginReplyPrompt
		}

		promptStarted := g.now()
		attemptRequest := req
		attemptRequest.History = filteredHistory
		attemptRequest.ReplyToolArgs = activeToolArgs
		attemptRequest.ExpressionHabits = mergedExpressionHabits
		attemptRequest.ReplyRequirements = joinNonEmpty(strings.TrimSpace(req.ReplyRequirements), strings.TrimSpace(extraPrompt), "\n\n")
		attemptRequest.RetryConstraints = append([]string(nil), retryConstraints...)
		attemptRequest.EnableVisualMessage = g.resolveEnableVisualMessage(req, nil)

		requestItems, buildErr := g.buildRequestMessages(ctx, attemptRequest, nil)
		if buildErr != nil {
			zap.L().Error("构建提示词失败", zap.Error(buildErr))
			result.ErrorMessage = "构建提示词失败: " + buildErr.Error()
			result.Metrics = GenerationMetrics{OverallMS: durationMS(startedAt, g.now())}
			return finalize(false)
		}

		activeClient := g.LLM
		if activeTaskName != defaultTaskName && g.LLMFactory != nil {
			activeClient, err = g.LLMFactory(activeTaskName, g.requestType(), sessionID)
			if err != nil {
				result.ErrorMessage = err.Error()
				result.Metrics = GenerationMetrics{OverallMS: durationMS(startedAt, g.now())}
				return finalize(false)
			}
			if activeClient == nil {
				result.ErrorMessage = "回复模型尚未初始化"
				result.Metrics = GenerationMetrics{OverallMS: durationMS(startedAt, g.now())}
				return finalize(false)
			}
		}

		factory := func(factoryCtx context.Context, info *ModelInfo) ([]llm.ContextItem, error) {
			visualRequest := req
			visualRequest.History = filteredHistory
			visualRequest.ReplyToolArgs = activeToolArgs
			visualRequest.ExpressionHabits = mergedExpressionHabits
			visualRequest.ReplyRequirements = joinNonEmpty(strings.TrimSpace(req.ReplyRequirements), strings.TrimSpace(extraPrompt), "\n\n")
			visualRequest.RetryConstraints = append([]string(nil), retryConstraints...)
			visualRequest.EnableVisualMessage = g.resolveEnableVisualMessage(req, info)
			// 文本模型进入前等待并刷新识图结果，对应 Python image_recognition_refresher
			if !visualRequest.EnableVisualMessage && visualRequest.EnableVisualPlanner && visualRequest.ImageRecognitionRefresher != nil {
				if refreshed := visualRequest.ImageRecognitionRefresher(factoryCtx, filteredHistory); refreshed > 0 {
					filteredHistory = filterReplyerHistory(filteredHistory)
					visualRequest.History = filteredHistory
				}
			}
			items, err := g.buildRequestMessages(factoryCtx, visualRequest, info)
			if err != nil {
				return nil, err
			}
			items = g.invokeBeforeModelRequestHook(factoryCtx, items, sessionID, activeTaskName, activeModelName, info, retryCount, req, activeToolArgs)
			requestItems = items
			return items, nil
		}

		llmStarted := g.now()
		generationResult, generateErr := activeClient.GenerateResponseWithContext(ctx, factory, g.buildGenerationOptions(activeModelName))
		promptMS = durationMS(promptStarted, g.now())
		llmMS = durationMS(llmStarted, g.now())
		if generateErr != nil {
			zap.L().Error("Maisaka 回复器调用失败", zap.Error(generateErr))
			result.ErrorMessage = generateErr.Error()
			result.Metrics = GenerationMetrics{PromptMS: promptMS, LLMMS: llmMS, OverallMS: durationMS(startedAt, g.now())}
			return finalize(false)
		}

		generationResult, err = normalizeGenerationResult(generationResult)
		if err != nil {
			result.ErrorMessage = err.Error()
			result.Metrics = GenerationMetrics{PromptMS: promptMS, LLMMS: llmMS, OverallMS: durationMS(startedAt, g.now())}
			return finalize(false)
		}
		promptPreview = renderContextItemsPrompt(requestItems)
		activeClientType := generationResult.ClientType
		if activeClientType == "" {
			activeClientType = clientTypeOf(activeClient)
		}

		aggregatePromptTokens += generationResult.PromptTokens
		aggregateCompletionTokens += generationResult.CompletionTokens
		aggregateTotalTokens += generationResult.TotalTokens

		if reasoningContinuationCount == 0 && activeClientType == "openai_responses" && isReasoningOnlyResponse(generationResult) {
			reasoningContinuationCount = 1
			continuationModelName := strings.TrimSpace(generationResult.ModelName)
			if continuationModelName == "" {
				continuationModelName = activeModelName
			}
			continuationRequestItems := append([]llm.ContextItem(nil), requestItems...)
			continuationRequestItems = append(continuationRequestItems, generationResult.OutputItems...)
			continuationOptions := g.buildGenerationOptions(continuationModelName)
			continued, continuationErr := activeClient.GenerateResponseWithContext(ctx, func(context.Context, *ModelInfo) ([]llm.ContextItem, error) {
				return continuationRequestItems, nil
			}, continuationOptions)
			if continuationErr != nil {
				result.ErrorMessage = continuationErr.Error()
				result.Metrics = GenerationMetrics{PromptMS: promptMS, LLMMS: llmMS, OverallMS: durationMS(startedAt, g.now())}
				return finalize(false)
			}
			continued, err = normalizeGenerationResult(continued)
			if err != nil {
				result.ErrorMessage = err.Error()
				result.Metrics = GenerationMetrics{PromptMS: promptMS, LLMMS: llmMS, OverallMS: durationMS(startedAt, g.now())}
				return finalize(false)
			}
			continued.GenerationAttempts = rewriteAttemptPurposes(continued.GenerationAttempts, "replyer.reasoning_initial", "replyer.reasoning_continuation")
			generationResult = continued
			requestItems = continuationRequestItems
			aggregatePromptTokens += generationResult.PromptTokens
			aggregateCompletionTokens += generationResult.CompletionTokens
			aggregateTotalTokens += generationResult.TotalTokens
			promptPreview = renderContextItemsPrompt(requestItems)
		}

		generationResult.GenerationAttempts = updateWorkflowAttempt(generationResult.GenerationAttempts, retryCount+1)
		allAttempts = append(allAttempts, generationResult.GenerationAttempts...)
		finalResult = generationResult
		finalRequestItems = requestItems

		result.Completion.RequestPrompt = promptPreview
		result.RequestMessageCount = len(requestItems)
		responseText = strings.TrimSpace(generationResult.ResponseText())

		hookOriginalResponse := responseText
		after := g.invokeAfterResponseHook(ctx, sessionID, generationResult, activeTaskName, activeModelName, retryCount, req, activeToolArgs, responseText)
		finalOutputItems := generationResult.OutputItems
		if rawItems, exists := after.Kwargs["output_items"]; exists && !reflect.DeepEqual(rawItems, llm.SerializeContextItemsSnapshot(generationResult.OutputItems)) {
			if items, err := llm.DeserializeContextItemsSnapshot(rawItems); err == nil {
				if err := llm.ValidateContextItems(items, llm.ContextProtocolModelOutput, nil); err == nil {
					finalOutputItems = items
					responseText = strings.TrimSpace(llm.GetContextResponseText(items))
				} else {
					zap.L().Warn("Hook maisaka.replyer.after_response 返回的 output_items 无法反序列化，已忽略", zap.Error(err))
				}
			} else {
				zap.L().Warn("Hook maisaka.replyer.after_response 返回的 output_items 无法反序列化，已忽略", zap.Error(err))
			}
		} else if _, exists := after.Kwargs["response"]; exists {
			hookModifiedResponse := strings.TrimSpace(stringValue(after.Kwargs["response"]))
			finalOutputItems = llm.ReplaceOutputProjection(generationResult.OutputItems, hookModifiedResponse, hookModifiedResponse != responseText, nil, false)
			if hookModifiedResponse != responseText {
				hookRewriteEvents = append(hookRewriteEvents, map[string]string{
					"attempt": fmt.Sprint(retryCount + 1),
					"before":  hookOriginalResponse,
					"after":   hookModifiedResponse,
				})
			}
			responseText = hookModifiedResponse
		}
		generationResult.OutputItems = finalOutputItems
		generationResult.Response = responseText

		retryRequested := coerceHookBool(after.Kwargs["retry"], false)
		matchedRegex := strings.TrimSpace(stringValue(after.Kwargs["matched_regex"]))
		matchedRegexPattern := strings.TrimSpace(stringValue(after.Kwargs["matched_regex_pattern"]))
		matchedRegexDescription := strings.TrimSpace(stringValue(after.Kwargs["matched_regex_description"]))
		retryReason := strings.TrimSpace(stringValue(after.Kwargs["retry_reason"]))

		if g.usesRetroPrompt() && !retroPeriodRegenerationRequested && containsAny(responseText, "发个句号", "一个句号") && retryCount < g.maxHookRetries() {
			retroPeriodRegenerationRequested = true
			retryRequested = true
			retryReason = joinNonEmpty(retryReason, "上一次回复出现了异常的句号问话，请根据实际聊天内容重新生成回复。", "；")
		}
		if activeClientType == "openai" && !reasoningRegenerationRequested && responseText == "" && len(generationResult.OutputItems) > 0 && allReasoningItems(generationResult.OutputItems) && retryCount < g.maxHookRetries() {
			reasoningRegenerationRequested = true
			retryRequested = true
			retryReason = joinNonEmpty(retryReason, "上一次只返回了思考内容，正文为空。请生成要发送的回复正文，不要只输出思考过程。", "；")
		}

		if retryRequested && retryCount < g.maxHookRetries() {
			reasonParts := make([]string, 0, 4)
			if matchedRegex != "" {
				reasonParts = append(reasonParts, "命中规则: "+matchedRegex)
			}
			if matchedRegexDescription != "" {
				reasonParts = append(reasonParts, "规则说明: "+matchedRegexDescription)
			}
			if retryReason != "" {
				reasonParts = append(reasonParts, retryReason)
			}
			if responseText != "" {
				reasonParts = append(reasonParts, fmt.Sprintf("被拦截回复: %q", responseText))
			}
			retryLogReason := strings.Join(reasonParts, "；")
			if retryLogReason == "" {
				retryLogReason = "Hook 请求重生成"
			}
			retryEvents = append(retryEvents, map[string]any{
				"attempt":                   retryCount + 1,
				"matched_regex":             matchedRegex,
				"matched_regex_pattern":     matchedRegexPattern,
				"matched_regex_description": matchedRegexDescription,
				"retry_reason":              retryReason,
				"rejected_response":         responseText,
			})
			retryReasons = append(retryReasons, retryLogReason)
			if constraint := BuildRetryConstraintSentence(retryReason, responseText); constraint != "" {
				retryConstraints = append(retryConstraints, constraint)
			}
			retryCount++
			continue
		}
		if retryRequested {
			zap.L().Warn("Maisaka 回复器已达到重生成上限，将使用最后一次回复",
				zap.Int("retry", retryCount), zap.Int("max", g.maxHookRetries()))
		}
		break
	}

	finalResult.GenerationAttempts = allAttempts
	result.OutputItems = finalResult.OutputItems
	result.GenerationAttempts = allAttempts
	result.Completion = LLMCompletionResult{
		RequestPrompt:    promptPreview,
		ResponseText:     responseText,
		ReasoningText:    finalResult.ReasoningText(),
		ModelName:        finalResult.ModelName,
		ToolCalls:        llm.GetContextResponseToolCalls(finalResult.OutputItems),
		PromptTokens:     finalResult.PromptTokens,
		CompletionTokens: finalResult.CompletionTokens,
		TotalTokens:      finalResult.TotalTokens,
	}
	result.Metrics = GenerationMetrics{
		PromptMS:  promptMS,
		LLMMS:     llmMS,
		OverallMS: durationMS(startedAt, g.now()),
		StageLogs: []string{
			fmt.Sprintf("prompt: %.2f ms", promptMS),
			fmt.Sprintf("llm: %.2f ms", llmMS),
			fmt.Sprintf("reasoning_continuation: %d", reasoningContinuationCount),
		},
		Extra: map[string]any{},
	}
	cacheHit := finalResult.PromptCacheHitTokens
	cacheMiss := finalResult.PromptCacheMissTokens
	if cacheMiss == 0 && cacheHit > 0 {
		cacheMiss = maxInt(finalResult.PromptTokens-cacheHit, 0)
	}
	cacheTotal := cacheHit + cacheMiss
	hitRate := 0.0
	if cacheTotal > 0 {
		hitRate = float64(cacheHit) / float64(cacheTotal) * 100
	}
	result.Metrics.Extra["prompt_cache_hit_tokens"] = cacheHit
	result.Metrics.Extra["prompt_cache_miss_tokens"] = cacheMiss
	result.Metrics.Extra["prompt_cache_hit_rate"] = round2(hitRate)
	result.Metrics.Extra["replyer_retry_count"] = retryCount
	result.Metrics.Extra["replyer_reasoning_continuation_count"] = reasoningContinuationCount
	result.Metrics.Extra["replyer_attempt_count"] = retryCount + reasoningContinuationCount + 1
	result.Metrics.Extra["replyer_aggregate_prompt_tokens"] = aggregatePromptTokens
	result.Metrics.Extra["replyer_aggregate_completion_tokens"] = aggregateCompletionTokens
	result.Metrics.Extra["replyer_aggregate_total_tokens"] = aggregateTotalTokens
	if len(result.SelectedExpressionIDs) > 0 && strings.TrimSpace(mergedExpressionHabits) != "" {
		result.Metrics.Extra["selected_expression_habits"] = strings.TrimSpace(mergedExpressionHabits)
	}
	if len(retryReasons) > 0 {
		result.Metrics.Extra["replyer_retry_reasons"] = retryReasons
	}
	if len(retryEvents) > 0 {
		result.Metrics.Extra["replyer_retry_events"] = retryEvents
	}
	if len(retryConstraints) > 0 {
		result.Metrics.Extra["replyer_retry_constraints"] = retryConstraints
	}
	if len(hookRewriteEvents) > 0 {
		result.Metrics.Extra["replyer_hook_rewrite_events"] = hookRewriteEvents
	}

	result.RequestMessages = finalRequestItems
	g.persistReplyPreview(&result, sessionID, req.ReplyReason)
	if strings.TrimSpace(responseText) == "" {
		result.ErrorMessage = "回复器返回了空内容"
		return finalize(false)
	}
	result.TextFragments = []string{responseText}
	return finalize(true)
}

func (g *ReplyGenerator) buildReplyContext(ctx context.Context, history []ChatMessage, target *ReplyTarget, replyReason, sessionID string, runner func(context.Context, string) (string, error), replyToolArgs map[string]any) (ReplyContext, error) {
	if g.usesRetroPrompt() {
		return ReplyContext{}, nil
	}
	if sessionID == "" {
		zap.L().Warn("构建 Maisaka 回复上下文失败：缺少会话标识")
		return ReplyContext{}, nil
	}
	if g.Expressions == nil {
		return ReplyContext{}, nil
	}
	selection, err := g.Expressions.SelectForReply(ctx, ExpressionSelectRequest{
		SessionID:      sessionID,
		ChatHistory:    history,
		Target:         target,
		ReplyReason:    replyReason,
		ReplyToolArgs:  replyToolArgs,
		SubAgentRunner: runner,
	})
	if err != nil {
		return ReplyContext{}, err
	}
	return ReplyContext{
		ExpressionHabits:      selection.ExpressionHabits,
		SelectedExpressionIDs: append([]int(nil), selection.SelectedExpressionIDs...),
		SelectedExpressions:   cloneExpressionMaps(selection.SelectedExpressions),
	}, nil
}

func (g *ReplyGenerator) buildRequestMessages(ctx context.Context, req ReplyRequest, info *ModelInfo) ([]llm.ContextItem, error) {
	var messages []RequestMessage
	var err error
	if g.DocumentMode {
		if g.DocumentBuilder == nil {
			return nil, fmt.Errorf("长文 replyer 提示装配器未配置")
		}
		messages, err = g.DocumentBuilder.BuildDocumentMessages(req)
	} else {
		if g.Builder == nil {
			return nil, fmt.Errorf("replyer 提示装配器未配置")
		}
		messages, err = g.Builder.RequestMessages(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	return requestMessagesToContextItems(messages)
}

func (g *ReplyGenerator) buildGenerationOptions(modelName string) GenerationOptions {
	if g.DocumentMode && strings.TrimSpace(modelName) == "" {
		if g.DocumentBuilder != nil {
			modelName = g.DocumentBuilder.ResolveDocumentModel("")
		}
		if strings.TrimSpace(modelName) == "" {
			modelName = g.DocumentModel
		}
	}
	return GenerationOptions{ModelName: strings.TrimSpace(modelName)}
}

func (g *ReplyGenerator) resolveEnableVisualMessage(req ReplyRequest, info *ModelInfo) bool {
	if info != nil {
		return info.Visual
	}
	return req.EnableVisualMessage
}

func (g *ReplyGenerator) resolveSessionID(req ReplyRequest) string {
	// 对齐 Python _resolve_session_id：仅 stream_id / chat_stream.session_id，
	// 不得用目标消息 ID 冒充会话 ID（否则 attention/chat prompt 会错位）。
	return strings.TrimSpace(req.SessionID)
}

func (g *ReplyGenerator) defaultTaskName() string {
	if name := strings.TrimSpace(g.DefaultTask); name != "" {
		return name
	}
	if g.LLM != nil {
		if name := strings.TrimSpace(g.LLM.TaskName()); name != "" {
			return name
		}
	}
	return "replyer"
}

func (g *ReplyGenerator) requestType() string {
	if strings.TrimSpace(g.RequestType) != "" {
		return strings.TrimSpace(g.RequestType)
	}
	return "maisaka.replyer"
}

func (g *ReplyGenerator) usesRetroPrompt() bool {
	if g.DocumentMode || g.Builder == nil {
		return false
	}
	return g.Builder.Config.ReplyerRetroPrompt
}

func (g *ReplyGenerator) maxHookRetries() int {
	if g.MaxHookRetries > 0 {
		return g.MaxHookRetries
	}
	return MaxHookRetries
}

func (g *ReplyGenerator) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func normalizeGenerationResult(result GenerationResult) (GenerationResult, error) {
	if len(result.OutputItems) == 0 {
		items, err := llm.BuildPortableOutputItems(result.Response, result.Reasoning, result.ToolCalls, uuid.NewString())
		if err != nil {
			return result, fmt.Errorf("构建模型输出 Items 失败: %w", err)
		}
		result.OutputItems = items
	} else if err := llm.ValidateContextItems(result.OutputItems, llm.ContextProtocolModelOutput, nil); err != nil {
		return result, fmt.Errorf("模型输出 Items 无效: %w", err)
	}
	if result.Response == "" {
		result.Response = llm.GetContextResponseText(result.OutputItems)
	}
	if result.Reasoning == "" {
		result.Reasoning = llm.GetContextResponseReasoning(result.OutputItems)
	}
	if len(result.ToolCalls) == 0 {
		result.ToolCalls = llm.GetContextResponseToolCalls(result.OutputItems)
	}
	if result.TotalTokens == 0 {
		result.TotalTokens = result.PromptTokens + result.CompletionTokens
	}
	return result, nil
}

func isReasoningOnlyResponse(result GenerationResult) bool {
	hasReasoning := false
	hasVisible := false
	for _, item := range result.OutputItems {
		switch item.Type {
		case llm.ContextItemReasoning:
			hasReasoning = true
		case llm.ContextItemAssistantMessage:
			if strings.TrimSpace(llm.GetContextItemText(item)) != "" {
				hasVisible = true
			}
		}
	}
	completed := result.GenerationTrace != nil && result.GenerationTrace.Status == "completed"
	return hasReasoning && !hasVisible && completed
}

func allReasoningItems(items []llm.ContextItem) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if item.Type != llm.ContextItemReasoning {
			return false
		}
	}
	return true
}

func rewriteAttemptPurposes(attempts []GenerationAttempt, initialPurpose, continuationPurpose string) []GenerationAttempt {
	if len(attempts) == 0 {
		return attempts
	}
	out := make([]GenerationAttempt, len(attempts))
	copy(out, attempts)
	for index := range out {
		out[index].WorkflowPurpose = continuationPurpose
	}
	return out
}

func updateWorkflowAttempt(attempts []GenerationAttempt, workflowAttempt int) []GenerationAttempt {
	if len(attempts) == 0 {
		return attempts
	}
	out := make([]GenerationAttempt, len(attempts))
	copy(out, attempts)
	for index := range out {
		out[index].WorkflowAttempt = workflowAttempt
	}
	return out
}

func renderContextItemsPrompt(items []llm.ContextItem) string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(llm.GetContextItemText(item))
		if text == "" {
			continue
		}
		lines = append(lines, string(item.Type)+": "+text)
	}
	return strings.Join(lines, "\n")
}

func clientTypeOf(client LLMClient) string {
	type clientTypeProvider interface {
		ClientType() string
	}
	if provider, ok := client.(clientTypeProvider); ok {
		return provider.ClientType()
	}
	return ""
}

func normalizeReplyToolArgs(raw map[string]any) map[string]any {
	if raw == nil {
		return map[string]any{}
	}
	copied := make(map[string]any, len(raw))
	for key, value := range raw {
		copied[key] = value
	}
	return copied
}

func cloneExpressionMaps(raw []map[string]any) []map[string]any {
	if raw == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		copied := make(map[string]any, len(item))
		for key, value := range item {
			copied[key] = value
		}
		out = append(out, copied)
	}
	return out
}

func coerceHookBool(value any, fallback bool) bool {
	switch typed := value.(type) {
	case nil:
		return fallback
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on", "retry":
			return true
		case "0", "false", "no", "off", "continue":
			return false
		}
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	}
	return fallback
}

func joinNonEmpty(first, second, separator string) string {
	if strings.TrimSpace(first) == "" {
		return second
	}
	if strings.TrimSpace(second) == "" {
		return first
	}
	return first + separator + second
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func durationMS(start, end time.Time) float64 {
	return float64(end.Sub(start).Microseconds()) / 1000
}

func round2(value float64) float64 {
	return float64(int64(value*100+0.5)) / 100
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
