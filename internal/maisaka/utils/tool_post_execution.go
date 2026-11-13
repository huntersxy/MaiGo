package utils

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"maibot-go/internal/maisaka"
)

// MemoryFeedbackTaskMetadataKey 记忆反馈任务标记键
const MemoryFeedbackTaskMetadataKey = "memory_feedback_task"

// WithMemoryFeedbackTask 为工具结果 metadata 标记需要创建记忆反馈任务
func WithMemoryFeedbackTask(metadata map[string]any) map[string]any {
	marked := make(map[string]any, len(metadata)+1)
	for key, value := range metadata {
		marked[key] = value
	}
	marked[MemoryFeedbackTaskMetadataKey] = map[string]any{"enabled": true}
	return marked
}

// MemoryFeedbackService 记忆反馈纠错任务端口
type MemoryFeedbackService interface {
	// EnqueueFeedbackTask 入队一次反馈纠错任务
	EnqueueFeedbackTask(ctx context.Context, queryToolID, sessionID string, queryTimestamp any, structuredContent map[string]any) (map[string]any, error)
}

// PostExecutionInput 工具执行后的非落库副作用输入
type PostExecutionInput struct {
	Invocation  maisaka.ToolInvocation
	Result      maisaka.ToolExecutionResult
	SavedRecord map[string]any
	SessionID   string
	Service     MemoryFeedbackService
	LogPrefix   string
}

// HandleToolPostExecutionEffects 处理工具执行后的非落库副作用
func HandleToolPostExecutionEffects(ctx context.Context, input PostExecutionInput) {
	rawTask, ok := input.Result.Metadata[MemoryFeedbackTaskMetadataKey]
	if !ok {
		return
	}
	task, ok := rawTask.(map[string]any)
	if !ok {
		return
	}
	enabled, _ := task["enabled"].(bool)
	if !enabled {
		return
	}
	enqueueMemoryFeedbackTask(ctx, input)
}

func enqueueMemoryFeedbackTask(ctx context.Context, input PostExecutionInput) {
	if input.SavedRecord == nil || input.Service == nil {
		return
	}
	normalized := NormalizeToolRecordValue(structuredContent(input.Result))
	structured, ok := normalized.(map[string]any)
	if !ok {
		structured = map[string]any{}
	}
	queryToolID := stringField(input.SavedRecord, "tool_id")
	if strings.TrimSpace(queryToolID) == "" {
		queryToolID = input.Invocation.CallID
	}
	sessionID := stringField(input.SavedRecord, "session_id")
	if strings.TrimSpace(sessionID) == "" {
		sessionID = input.SessionID
	}
	// 对齐 Python tool_post_execution.py:50-52 chat_stream 缺失时直接跳过
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	payload, err := input.Service.EnqueueFeedbackTask(
		ctx,
		strings.TrimSpace(queryToolID),
		strings.TrimSpace(sessionID),
		input.SavedRecord["timestamp"],
		structured,
	)
	if err != nil {
		zap.L().Error(fmt.Sprintf("%s 反馈纠错任务入队失败: tool_call_id=%s", input.LogPrefix, input.Invocation.CallID),
			zap.String("tool_call_id", input.Invocation.CallID), zap.Error(err))
		return
	}
	success, _ := payload["success"].(bool)
	if !success {
		reason, _ := payload["reason"].(string)
		zap.L().Debug(fmt.Sprintf("%s 反馈纠错任务未入队: tool_call_id=%s reason=%s", input.LogPrefix, input.Invocation.CallID, reason),
			zap.String("tool_call_id", input.Invocation.CallID), zap.String("reason", reason))
	}
}
