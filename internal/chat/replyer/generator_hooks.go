package replyer

import (
	"context"
	"strings"

	"MaiGo/internal/llm_models"

	"go.uber.org/zap"
)

// invokeBeforeRequestHook 发射 maisaka.replyer.before_request
// 失败时对齐 Python：继续使用当前请求参数，并把 plugin prompt 作为 extra_prompt
func (g *ReplyGenerator) invokeBeforeRequestHook(ctx context.Context, sessionID string, history []ChatMessage, req ReplyRequest, defaultTask string, toolArgs map[string]any, retryCount int) HookResult {
	pluginPrompt := stringValue(toolArgs["_plugin_reply_prompt"])
	fallback := HookResult{Kwargs: map[string]any{"extra_prompt": pluginPrompt}}
	if g.Hooks == nil {
		return fallback
	}
	replyMessageID := ""
	if req.Target != nil {
		replyMessageID = strings.TrimSpace(req.Target.MessageID)
	}
	kwargs := map[string]any{
		"session_id":              sessionID,
		"request_type":            g.requestType(),
		"task_name":               defaultTask,
		"model_name":              "",
		"extra_prompt":            pluginPrompt,
		"attempt":                 retryCount + 1,
		"retry_count":             retryCount,
		"max_retries":             g.maxHookRetries(),
		"reply_message_id":        replyMessageID,
		"reply_reason":            strings.TrimSpace(req.ReplyReason),
		"selected_expression_ids": append([]int(nil), req.SelectedExpressionIDs...),
		"reply_tool_args":         copyAnyMap(toolArgs),
	}
	result, err := g.Hooks.InvokeHook(ctx, "maisaka.replyer.before_request", kwargs)
	if err != nil {
		zap.L().Warn("Maisaka 回复器 before_request Hook 调用失败，将继续使用当前请求参数", zap.Error(err))
		return fallback
	}
	if result.Kwargs == nil {
		result.Kwargs = map[string]any{"extra_prompt": pluginPrompt}
	}
	if _, ok := result.Kwargs["extra_prompt"]; !ok {
		result.Kwargs["extra_prompt"] = pluginPrompt
	}
	return result
}

// invokeBeforeModelRequestHook 发射 maisaka.replyer.before_model_request
// 允许插件按 ContextItem 改写请求；反序列化或关系校验失败时保留原请求
func (g *ReplyGenerator) invokeBeforeModelRequestHook(ctx context.Context, items []llm.ContextItem, sessionID, taskName, modelName string, info *ModelInfo, retryCount int, req ReplyRequest, toolArgs map[string]any) []llm.ContextItem {
	if g.Hooks == nil {
		return items
	}
	replyMessageID := ""
	if req.Target != nil {
		replyMessageID = strings.TrimSpace(req.Target.MessageID)
	}
	selectedModelName := ""
	selectedModelVisual := false
	if info != nil {
		selectedModelName = strings.TrimSpace(info.Name)
		selectedModelVisual = info.Visual
	}
	kwargs := map[string]any{
		"items":                   llm.SerializeContextItemsSnapshot(items),
		"item_schema_version":     llm.ContextItemSchemaVersion,
		"session_id":              sessionID,
		"request_type":            g.requestType(),
		"task_name":               taskName,
		"requested_model_name":    modelName,
		"selected_model_name":     selectedModelName,
		"selected_model_visual":   selectedModelVisual,
		"attempt":                 retryCount + 1,
		"retry_count":             retryCount,
		"max_retries":             g.maxHookRetries(),
		"reply_message_id":        replyMessageID,
		"reply_reason":            strings.TrimSpace(req.ReplyReason),
		"selected_expression_ids": append([]int(nil), req.SelectedExpressionIDs...),
		"reply_tool_args":         copyAnyMap(toolArgs),
	}
	result, err := g.Hooks.InvokeHook(ctx, "maisaka.replyer.before_model_request", kwargs)
	if err != nil {
		zap.L().Warn("Maisaka 回复器 before_model_request Hook 调用失败，将继续使用当前请求消息", zap.Error(err))
		return items
	}
	raw, ok := result.Kwargs["items"]
	if !ok {
		return items
	}
	overridden, err := llm.DeserializeContextItemsSnapshot(raw)
	if err != nil {
		zap.L().Warn("Hook maisaka.replyer.before_model_request 返回的 items 无法反序列化，已忽略", zap.Error(err))
		return items
	}
	if err := llm.ValidateContextItems(overridden, llm.ContextProtocolRequestContext, nil); err != nil {
		zap.L().Warn("Hook maisaka.replyer.before_model_request 返回的 items 不符合请求上下文协议，已忽略", zap.Error(err))
		return items
	}
	return overridden
}

// invokeAfterResponseHook 发射 maisaka.replyer.after_response
func (g *ReplyGenerator) invokeAfterResponseHook(ctx context.Context, sessionID string, result GenerationResult, taskName, modelName string, retryCount int, req ReplyRequest, toolArgs map[string]any, responseText string) HookResult {
	if g.Hooks == nil {
		return HookResult{}
	}
	replyMessageID := ""
	if req.Target != nil {
		replyMessageID = strings.TrimSpace(req.Target.MessageID)
	}
	kwargs := map[string]any{
		"response":                responseText,
		"output_items":            llm.SerializeContextItemsSnapshot(result.OutputItems),
		"item_schema_version":     llm.ContextItemSchemaVersion,
		"session_id":              sessionID,
		"request_type":            g.requestType(),
		"task_name":               taskName,
		"requested_model_name":    modelName,
		"attempt":                 retryCount + 1,
		"retry_count":             retryCount,
		"max_retries":             g.maxHookRetries(),
		"reply_message_id":        replyMessageID,
		"selected_expression_ids": append([]int(nil), req.SelectedExpressionIDs...),
		"reply_tool_args":         copyAnyMap(toolArgs),
		"prompt_tokens":           result.PromptTokens,
		"completion_tokens":       result.CompletionTokens,
		"total_tokens":            result.TotalTokens,
		"retry":                   false,
		"retry_reason":            "",
	}
	hookResult, err := g.Hooks.InvokeHook(ctx, "maisaka.replyer.after_response", kwargs)
	if err != nil {
		zap.L().Warn("Maisaka 回复器 after_response Hook 调用失败，将继续使用当前回复", zap.Error(err))
		return HookResult{}
	}
	return hookResult
}

func copyAnyMap(raw map[string]any) map[string]any {
	if raw == nil {
		return map[string]any{}
	}
	copied := make(map[string]any, len(raw))
	for key, value := range raw {
		copied[key] = value
	}
	return copied
}
