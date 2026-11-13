package replyer

import (
	"strings"
	"time"

	"maibot-go/internal/llm_models"
	"maibot-go/internal/maisaka/display"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

// persistReplyPreview 对齐 _persist_reply_preview：仅在开启 debug.save_maisaka_reasoning 时
// 把本次 replyer 请求与输出写入结构化 Prompt 预览，供 WebUI 推理页面查看
func (g *ReplyGenerator) persistReplyPreview(result *ReplyGenerationResult, sessionID, replyReason string) {
	if g == nil || result == nil {
		return
	}
	if g.Builder == nil || !g.Builder.Config.SaveReasoning {
		return
	}
	requestItems := sanitizePreviewItems(llm.SerializeContextItemsSnapshot(result.RequestMessages), g.keepPreviewBase64())
	outputItems := sanitizePreviewItems(llm.SerializeContextItemsSnapshot(result.OutputItems), g.keepPreviewBase64())
	if len(requestItems) == 0 && len(outputItems) == 0 {
		return
	}

	attempts := serializeReplyGenerationAttempts(result.GenerationAttempts)
	metadata := display.NormalizePreviewMetadata(map[string]any{
		"model_name":               result.Completion.ModelName,
		"duration_ms":              result.Metrics.OverallMS,
		"prompt_tokens":            result.Completion.PromptTokens,
		"completion_tokens":        result.Completion.CompletionTokens,
		"total_tokens":             result.Completion.TotalTokens,
		"prompt_cache_hit_tokens":  metricInt(result.Metrics.Extra["prompt_cache_hit_tokens"]),
		"prompt_cache_miss_tokens": metricInt(result.Metrics.Extra["prompt_cache_miss_tokens"]),
	})
	for key, value := range display.ExtractTokenMetadataFromAttempts(attempts) {
		if _, exists := metadata[key]; !exists {
			metadata[key] = value
		}
	}

	payload := map[string]any{
		"schema_version": 6,
		"request": map[string]any{
			"kind":             g.requestType(),
			"selection_reason": strings.TrimSpace(replyReason),
		},
		"metadata":            metadata,
		"presentation":        map[string]any{"output_title": "输出结果"},
		"request_items":       requestItems,
		"output_items":        outputItems,
		"tool_definitions":    []any{},
		"generation_attempts": attempts,
	}
	content, err := sonic.ConfigStd.MarshalIndent(payload, "", "  ")
	if err != nil {
		zap.L().Warn("序列化 Replyer Prompt 预览失败", zap.Error(err))
		return
	}
	logger := g.PreviewLogger
	if logger == nil {
		logger = display.DefaultPromptPreviewLogger()
	}
	if _, err := logger.SavePreviewFile(sessionID, g.requestType(), string(content), nil, nil); err != nil {
		zap.L().Warn("写入 Replyer Prompt 预览失败", zap.Error(err))
	}
}

func (g *ReplyGenerator) keepPreviewBase64() bool {
	return g != nil && g.Builder != nil && g.Builder.Config.KeepPreviewBase64
}

func metricInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func sanitizePreviewItems(items []any, keepBase64 bool) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, sanitizePreviewValue(item, keepBase64))
	}
	return out
}

// sanitizePreviewValue 在持久化边界剥离内联图片编码，保留其余结构
func sanitizePreviewValue(value any, keepBase64 bool) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if key == "image_base64" && !keepBase64 {
				out[key] = ""
				continue
			}
			out[key] = sanitizePreviewValue(item, keepBase64)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, sanitizePreviewValue(item, keepBase64))
		}
		return out
	default:
		return value
	}
}

func serializeReplyGenerationAttempts(attempts []GenerationAttempt) []map[string]any {
	out := make([]map[string]any, 0, len(attempts))
	for _, attempt := range attempts {
		item := map[string]any{
			"attempt_id":       attempt.AttemptID,
			"workflow_purpose": attempt.WorkflowPurpose,
			"workflow_attempt": attempt.WorkflowAttempt,
			"provider_attempt": attempt.ProviderAttempt,
			"model_attempt":    attempt.ModelAttempt,
			"status":           attempt.Status,
			"duration_ms":      attempt.DurationMS,
			"provider":         attempt.Provider,
			"endpoint":         attempt.Endpoint,
			"model":            attempt.Model,
			"client_type":      attempt.ClientType,
			"operation":        attempt.Operation,
			"wire_protocol":    attempt.WireProtocol,
		}
		if !attempt.StartedAt.IsZero() {
			item["started_at"] = attempt.StartedAt.Format(time.RFC3339Nano)
		}
		if attempt.Error != nil {
			item["error"] = map[string]any{
				"message":     attempt.Error.Message,
				"status_code": attempt.Error.StatusCode,
				"type":        attempt.Error.Type,
			}
		}
		out = append(out, item)
	}
	return out
}
