package replyer

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"MaiGo/internal/llm_models"
	"MaiGo/internal/maisaka/display"
	"MaiGo/internal/plugin_runtime/hooks"

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
	var err error

	// 表达召回推迟到实际模型选定后，避免 Mai Replier 等适配产生无效模型调用
	var replyContext *ReplyContext
	expressionHabitsOverride := strings.TrimSpace(req.ExpressionHabits)
	selectedIDsOverride := req.SelectedExpressionIDs
	selectedDetailsOverride := req.SelectedExpressions
	var activeExpressionHabits string
	var activePromptProfile string

	finalizeAbort := func(exc error) (bool, ReplyGenerationResult) {
		aborted, ok := exc.(*hooks.HookAbortedError)
		if !ok {
			aborted = &hooks.HookAbortedError{Reason: exc.Error()}
		}
		result.ErrorCode = "plugin_aborted"
		result.ErrorMessage = aborted.Error()
		result.ErrorDetails = map[string]any{"hook_abort": aborted.ToDict()}
		result.Metrics.OverallMS = durationMS(startedAt, g.now())
		return finalize(false)
	}

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
	translationCount := 0
	superCreativeMode := g.Builder != nil && g.Builder.Config.SuperCreativeMode
	creativeLanguage := "ja-JP"
	if g.Builder != nil {
		creativeLanguage = CreativeLanguage(g.Builder.Config.SuperCreativeLanguage)
	}
	reasoningRegenerationRequested := false
	retroPeriodRegenerationRequested := false
	geminiRegenerationRequested := false

	defaultTaskName := g.defaultTaskName()
	var responseText string
	var finalResult GenerationResult
	var finalRequestItems []llm.ContextItem
	var promptMS, llmMS float64
	var promptPreview string

	for {
		beforeRequest := g.invokeBeforeRequestHook(ctx, sessionID, filteredHistory, req, defaultTaskName, activeToolArgs, retryCount)
		if beforeRequest.Aborted {
			return finalizeAbort(beforeRequest.AbortError())
		}
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
		var requestItems []llm.ContextItem

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
			attemptRequest := req
			attemptRequest.ModelInfo = info
			attemptRequest.PromptProfile = g.promptProfileFor(info)
			activePromptProfile = attemptRequest.PromptProfile
			attemptRequest.History = filteredHistory
			attemptRequest.ReplyToolArgs = activeToolArgs
			attemptRequest.ReplyRequirements = joinNonEmpty(strings.TrimSpace(req.ReplyRequirements), strings.TrimSpace(extraPrompt), "\n\n")
			attemptRequest.RetryConstraints = append([]string(nil), retryConstraints...)
			attemptRequest.EnableVisualMessage = g.resolveEnableVisualMessage(req, info)
			// 文本模型进入前等待并刷新识图结果，对应 Python image_recognition_refresher
			if !attemptRequest.EnableVisualMessage && attemptRequest.EnableVisualPlanner && attemptRequest.ImageRecognitionRefresher != nil {
				if refreshed := attemptRequest.ImageRecognitionRefresher(factoryCtx, filteredHistory); refreshed > 0 {
					filteredHistory = filterReplyerHistory(filteredHistory)
					attemptRequest.History = filteredHistory
				}
			}
			attemptExpressionHabits := ""
			if activePromptProfile != "mai_replier" {
				if replyContext == nil {
					built, buildErr := g.buildReplyContext(factoryCtx, filteredHistory, req.Target, req.ReplyReason, sessionID, req.SubAgentRunner, activeToolArgs)
					if buildErr != nil {
						return nil, fmt.Errorf("构建回复上下文失败: %w", buildErr)
					}
					replyContext = &built
				}
				attemptExpressionHabits = expressionHabitsOverride
				if attemptExpressionHabits == "" {
					attemptExpressionHabits = replyContext.ExpressionHabits
				}
				if selectedIDsOverride != nil {
					result.SelectedExpressionIDs = append([]int(nil), selectedIDsOverride...)
				} else {
					result.SelectedExpressionIDs = append([]int(nil), replyContext.SelectedExpressionIDs...)
				}
				if selectedDetailsOverride != nil {
					result.SelectedExpressionDetails = cloneExpressionMaps(selectedDetailsOverride)
				} else {
					result.SelectedExpressionDetails = cloneExpressionMaps(replyContext.SelectedExpressions)
				}
			} else {
				result.SelectedExpressionIDs = nil
				result.SelectedExpressionDetails = nil
			}
			activeExpressionHabits = attemptExpressionHabits
			attemptRequest.ExpressionHabits = attemptExpressionHabits
			attemptRequest.SelectedExpressionIDs = append([]int(nil), result.SelectedExpressionIDs...)
			attemptRequest.SelectedExpressions = cloneExpressionMaps(result.SelectedExpressionDetails)
			items, err := g.buildRequestMessages(factoryCtx, attemptRequest, info)
			if err != nil {
				return nil, err
			}
			items = g.invokeBeforeModelRequestHook(factoryCtx, items, sessionID, activeTaskName, activeModelName, info, retryCount, attemptRequest, activeToolArgs)
			requestItems = items
			return items, nil
		}

		llmStarted := g.now()
		generationResult, generateErr := activeClient.GenerateResponseWithContext(g.modelContext(ctx, activeTaskName, sessionID), factory, g.buildGenerationOptions(activeModelName))
		promptMS = durationMS(promptStarted, g.now())
		llmMS = durationMS(llmStarted, g.now())
		if generateErr != nil {
			var aborted *hooks.HookAbortedError
			if errors.As(generateErr, &aborted) {
				return finalizeAbort(aborted)
			}
			g.logReplyFailure(sessionID, activeTaskName, activeModelName, retryCount, generateErr)
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
			continued, continuationErr := activeClient.GenerateResponseWithContext(g.modelContext(ctx, activeTaskName, sessionID), func(context.Context, *ModelInfo) ([]llm.ContextItem, error) {
				return continuationRequestItems, nil
			}, continuationOptions)
			if continuationErr != nil {
				g.logReplyFailure(sessionID, activeTaskName, continuationModelName, retryCount, continuationErr)
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

		superActive := superCreativeMode && activePromptProfile != "mai_replier"
		var originalAttempts []GenerationAttempt
		if superActive {
			// 先固定本阶段的调用用途与轮次，保证预览与最终诊断一致
			originalAttempts = make([]GenerationAttempt, len(generationResult.GenerationAttempts))
			copy(originalAttempts, generationResult.GenerationAttempts)
			for index := range originalAttempts {
				if originalAttempts[index].WorkflowPurpose == g.requestType() || originalAttempts[index].WorkflowPurpose == "replyer" {
					originalAttempts[index].WorkflowPurpose = "replyer." + creativeLanguage
				}
			}
			originalAttempts = updateWorkflowAttempt(originalAttempts, retryCount+1)
			response := generationResult
			response.GenerationAttempts = originalAttempts
			g.persistStagePreview(ctx, &result, "replyer_creative", sessionID, req.ReplyReason,
				requestItems, response.OutputItems, originalAttempts, llmMS,
				response.ModelName, response.PromptTokens, response.CompletionTokens, response.TotalTokens)
		}
		// 在 after_response Hook 与正文校验前翻译，插件和发送端只处理中文结果
		if superActive && strings.TrimSpace(generationResult.ResponseText()) != "" {
			result.ErrorCode = "replyer_translation_failed"
			translationStarted := g.now()
			translated, translationItems, translationErr := g.translateSuperCreativeReply(g.modelContext(ctx, activeTaskName, sessionID), activeClient, generationResult, creativeLanguage)
			if translationErr != nil {
				g.logReplyFailure(sessionID, activeTaskName, activeModelName, retryCount, translationErr)
				result.ErrorMessage = translationErr.Error()
				result.Metrics = GenerationMetrics{PromptMS: promptMS, LLMMS: llmMS, OverallMS: durationMS(startedAt, g.now())}
				return finalize(false)
			}
			translatedAttempts := make([]GenerationAttempt, len(translated.GenerationAttempts))
			copy(translatedAttempts, translated.GenerationAttempts)
			for index := range translatedAttempts {
				translatedAttempts[index].WorkflowPurpose = "replyer.translation"
			}
			translatedAttempts = updateWorkflowAttempt(translatedAttempts, retryCount+1)
			translated.GenerationAttempts = translatedAttempts
			g.persistStagePreview(ctx, &result, "replyer_translation", sessionID, req.ReplyReason,
				translationItems, translated.OutputItems, translatedAttempts, durationMS(translationStarted, g.now()),
				translated.ModelName, translated.PromptTokens, translated.CompletionTokens, translated.TotalTokens)
			if len(translated.ToolCalls) > 0 {
				result.ErrorMessage = "超级创意模式翻译返回了工具调用，无法输出中文正文"
				result.Metrics = GenerationMetrics{PromptMS: promptMS, LLMMS: llmMS, OverallMS: durationMS(startedAt, g.now())}
				return finalize(false)
			}
			if strings.TrimSpace(translated.ResponseText()) == "" {
				result.ErrorMessage = "超级创意模式翻译未生成中文正文"
				result.Metrics = GenerationMetrics{PromptMS: promptMS, LLMMS: llmMS, OverallMS: durationMS(startedAt, g.now())}
				return finalize(false)
			}
			result.ErrorCode = ""
			translationCount++
			aggregatePromptTokens += translated.PromptTokens
			aggregateCompletionTokens += translated.CompletionTokens
			aggregateTotalTokens += translated.TotalTokens
			requestItems = translationItems
			promptPreview = renderContextItemsPrompt(translationItems)
			translated.GenerationAttempts = append(originalAttempts, translatedAttempts...)
			generationResult = translated
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
		// 主动中止优先于正文改写和重生成，保留原因供 reply 工具反馈给 Planner
		if after.Aborted {
			abortErr, _ := after.AbortError().(*hooks.HookAbortedError)
			result.ErrorCode = "plugin_aborted"
			result.ErrorMessage = abortErr.Error()
			result.ErrorDetails = map[string]any{"hook_abort": abortErr.ToDict()}
			responseText = ""
			break
		}
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
		if responseText == "" && result.ErrorCode == "" {
			if hookOriginalResponse != "" {
				result.ErrorCode = "plugin_empty_response"
			} else {
				result.ErrorCode = "model_empty_response"
			}
		}

		retryRequested := coerceHookBool(after.Kwargs["retry"], false)
		matchedRegex := strings.TrimSpace(stringValue(after.Kwargs["matched_regex"]))
		matchedRegexPattern := strings.TrimSpace(stringValue(after.Kwargs["matched_regex_pattern"]))
		matchedRegexDescription := strings.TrimSpace(stringValue(after.Kwargs["matched_regex_description"]))
		retryReason := strings.TrimSpace(stringValue(after.Kwargs["retry_reason"]))

		// 兼容 Gemini 混入正文的显式思考格式：能可靠剥离则改写正文，否则有限重生成
		if g.Builder != nil && g.Builder.Config.GeminiReplyCompatibility {
			compatible, compatErr := NormalizeGeminiReply(responseText)
			if compatErr != nil {
				responseText = ""
				generationResult.OutputItems = llm.ReplaceOutputProjection(generationResult.OutputItems, "", true, nil, false)
				zap.L().Warn("Gemini 回复兼容处理: " + compatErr.Error())
				if geminiRegenerationRequested || retryCount >= g.maxHookRetries() {
					result.ErrorCode = "gemini_reply_format_error"
					result.ErrorMessage = compatErr.Error()
					break
				}
				geminiRegenerationRequested = true
				retryRequested = true
				retryReason = "请重新生成正式聊天正文，不要输出思考过程、分析标题、草稿或思考标签。"
			} else if compatible != responseText {
				zap.L().Info(fmt.Sprintf("Gemini 回复兼容处理: 正文长度=%d->%d", len([]rune(responseText)), len([]rune(compatible))))
				responseText = compatible
				generationResult.OutputItems = llm.ReplaceOutputProjection(generationResult.OutputItems, responseText, true, nil, false)
			}
		}

		if activePromptProfile == "mai_replier" && !retroPeriodRegenerationRequested && containsAny(responseText, "发个句号", "一个句号") && retryCount < g.maxHookRetries() {
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
		cacheMiss = max(finalResult.PromptTokens-cacheHit, 0)
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
	result.Metrics.Extra["replyer_super_creative_mode"] = superCreativeMode && activePromptProfile != "mai_replier"
	result.Metrics.Extra["replyer_translation_count"] = translationCount
	result.Metrics.Extra["replyer_generation_language"] = creativeLanguage
	result.Metrics.Extra["replyer_attempt_count"] = retryCount + reasoningContinuationCount + translationCount + 1
	result.Metrics.Extra["replyer_aggregate_prompt_tokens"] = aggregatePromptTokens
	result.Metrics.Extra["replyer_aggregate_completion_tokens"] = aggregateCompletionTokens
	result.Metrics.Extra["replyer_aggregate_total_tokens"] = aggregateTotalTokens
	if len(result.SelectedExpressionIDs) > 0 && strings.TrimSpace(activeExpressionHabits) != "" {
		result.Metrics.Extra["selected_expression_habits"] = strings.TrimSpace(activeExpressionHabits)
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
	g.persistReplyPreview(ctx, &result, sessionID, req.ReplyReason)
	if strings.TrimSpace(responseText) == "" {
		if result.ErrorCode != "plugin_aborted" && result.ErrorCode != "gemini_reply_format_error" {
			if result.ErrorCode == "plugin_empty_response" {
				result.ErrorMessage = "插件 Hook 将回复正文修改为空。请调整回复目标或停止本次回复，勿原样重复调用。"
			} else {
				result.ErrorMessage = "模型未生成回复正文。"
			}
		}
		zap.L().Warn(result.ErrorMessage)
		return finalize(false)
	}
	result.TextFragments = []string{responseText}
	return finalize(true)
}

func (g *ReplyGenerator) buildReplyContext(ctx context.Context, history []ChatMessage, target *ReplyTarget, replyReason, sessionID string, runner func(context.Context, string) (string, error), replyToolArgs map[string]any) (ReplyContext, error) {
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
	var builder *PromptBuilder
	profile := strings.TrimSpace(req.PromptProfile)
	if profile == "" && req.ModelInfo != nil && g.Builder != nil {
		profile = g.Builder.ResolvePromptProfile(req.ModelInfo)
	}
	superActive := g.Builder != nil && g.Builder.Config.SuperCreativeMode && profile != "mai_replier"
	language := "ja-JP"
	if g.Builder != nil {
		language = CreativeLanguage(g.Builder.Config.SuperCreativeLanguage)
	}
	promptLocale := language
	if promptLocale == "zh-classical" {
		promptLocale = "zh-CN"
	}
	if g.DocumentMode {
		if g.DocumentBuilder == nil {
			return nil, fmt.Errorf("长文 replyer 提示装配器未配置")
		}
		localized := *g.DocumentBuilder
		if superActive {
			localized.PromptBuilder = *localized.PromptBuilder.ForLocale(promptLocale)
		}
		builder = &localized.PromptBuilder
		messages, err = localized.BuildDocumentMessages(req)
	} else {
		if g.Builder == nil {
			return nil, fmt.Errorf("replyer 提示装配器未配置")
		}
		builder = g.Builder
		if superActive {
			builder = builder.ForLocale(promptLocale)
		}
		messages, err = builder.RequestMessages(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	if superActive {
		instruction := CreativeOutputLanguage[language]
		for index := range messages {
			if strings.ToLower(strings.TrimSpace(messages[index].Role)) != "system" {
				continue
			}
			if len(messages[index].Parts) > 0 {
				messages[index].Parts = append(messages[index].Parts, RequestPart{Kind: "text", Text: "\n" + instruction})
			} else {
				messages[index].Content += "\n" + instruction
			}
		}
	}
	return requestMessagesToContextItems(messages)
}

// translateSuperCreativeReply 用实际生成正文的同一模型翻译；翻译失败时不返回原文
func (g *ReplyGenerator) translateSuperCreativeReply(ctx context.Context, client LLMClient, generationResult GenerationResult, language string) (GenerationResult, []llm.ContextItem, error) {
	modelName := strings.TrimSpace(generationResult.ModelName)
	if modelName == "" {
		return GenerationResult{}, nil, fmt.Errorf("超级创意模式缺少实际 Replyer 模型名称，无法锁定翻译模型")
	}
	if len(generationResult.ToolCalls) > 0 {
		return GenerationResult{}, nil, fmt.Errorf("超级创意模式只允许回复正文，不接受工具调用")
	}
	sourceText := generationResult.Response
	if g.Builder != nil && g.Builder.Config.GeminiReplyCompatibility {
		// 先剥离有明确边界的分析块，避免翻译改变边界后将其当作正式正文
		compatible, err := NormalizeGeminiReply(sourceText)
		if err != nil {
			return GenerationResult{}, nil, err
		}
		sourceText = compatible
	}
	// 文言文本身属于中文，需明确译成白话，避免通用指令将其原样返回
	instruction := CreativeTranslationInstruction
	if language == "zh-classical" {
		instruction = ClassicalTranslationInstruction
	}
	items, err := requestMessagesToContextItems([]RequestMessage{
		{Role: "user", Content: instruction + sourceText},
	})
	if err != nil {
		return GenerationResult{}, nil, err
	}
	translated, err := client.GenerateResponseWithContext(ctx, func(context.Context, *ModelInfo) ([]llm.ContextItem, error) {
		return items, nil
	}, GenerationOptions{ModelName: modelName})
	if err != nil {
		return GenerationResult{}, nil, err
	}
	translated, err = normalizeGenerationResult(translated)
	if err != nil {
		return GenerationResult{}, nil, err
	}
	return translated, items, nil
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

// modelContext 给模型调用附加任务与会话元数据；日志、监控与用量统计共用
func (g *ReplyGenerator) modelContext(ctx context.Context, taskName, sessionID string) context.Context {
	ctx = llm.WithModelUsageMetadata(ctx, taskName, g.requestType(), sessionID)
	return llm.WithCacheStatsMetadata(ctx, taskName, g.requestType(), sessionID)
}

// logReplyFailure 记录回复器失败；会话标识与错误诊断字段一并落日志
func (g *ReplyGenerator) logReplyFailure(sessionID, taskName, modelName string, retryCount int, err error) {
	fields := []zap.Field{
		zap.String("session_id", sessionID),
		zap.String("task", taskName),
		zap.String("model", modelName),
		zap.Int("retry_count", retryCount),
		zap.Error(err),
	}
	fields = append(fields, llm.ErrorLogFields(err)...)
	zap.L().Error("Maisaka 回复器调用失败", fields...)
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

// promptProfileFor 按本次实际模型解析提示词适配
func (g *ReplyGenerator) promptProfileFor(info *ModelInfo) string {
	if g.DocumentMode {
		return "document"
	}
	if g.Builder == nil {
		return "reply"
	}
	return g.Builder.ResolvePromptProfile(info)
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
