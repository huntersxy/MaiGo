package runtime

import (
	"context"
	"strings"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"

	"MaiGo/internal/A_memorix"
	"MaiGo/internal/maisaka"
	maisakautils "MaiGo/internal/maisaka/utils"
)

// memoryFeedbackInvokeTimeoutMS 对齐 Python memory_service.enqueue_feedback_task 的 10 秒超时
const memoryFeedbackInvokeTimeoutMS = 10000

// memorixComponentInvoker A_Memorix 宿主组件调用端口
type memorixComponentInvoker interface {
	Invoke(ctx context.Context, component string, args map[string]any, timeoutMs *int) (any, error)
}

// agentMemoryFeedbackService 把反馈纠错任务入队转给 A_Memorix 宿主
type agentMemoryFeedbackService struct {
	timeoutMS int
	lookup    func() memorixComponentInvoker
}

// EnqueueFeedbackTask 调用 A_Memorix enqueue_feedback_task，失败返回真实的 success=false
func (s *agentMemoryFeedbackService) EnqueueFeedbackTask(ctx context.Context, queryToolID, sessionID string, queryTimestamp any, structuredContent map[string]any) (map[string]any, error) {
	if structuredContent == nil {
		structuredContent = map[string]any{}
	}
	args := map[string]any{
		"query_tool_id":      strings.TrimSpace(queryToolID),
		"session_id":         strings.TrimSpace(sessionID),
		"query_timestamp":    queryTimestamp,
		"structured_content": structuredContent,
	}
	invoker, disabled := s.invoker()
	if disabled {
		return map[string]any{"success": true, "queued": false, "disabled": true, "reason": "a_memorix_disabled"}, nil
	}
	if invoker == nil {
		zap.L().Warn("反馈纠错任务入队失败: A_Memorix 宿主不支持组件调用")
		return map[string]any{"success": false, "queued": false, "reason": "a_memorix_host_unavailable"}, nil
	}
	timeout := s.timeout()
	payload, err := invoker.Invoke(ctx, "enqueue_feedback_task", args, &timeout)
	if err != nil {
		zap.L().Warn("反馈纠错任务入队失败", zap.Error(err))
		return map[string]any{"success": false, "queued": false, "reason": err.Error()}, nil
	}
	return memoryFeedbackPayload(payload)
}

func (s *agentMemoryFeedbackService) invoker() (memorixComponentInvoker, bool) {
	if s != nil && s.lookup != nil {
		return s.lookup(), false
	}
	host := memorix.DefaultRegistry().Host()
	if _, ok := host.(memorix.DisabledHost); ok {
		return nil, true
	}
	invoker, ok := host.(memorixComponentInvoker)
	if !ok {
		return nil, false
	}
	return invoker, false
}

func (s *agentMemoryFeedbackService) timeout() int {
	if s != nil && s.timeoutMS > 0 {
		return s.timeoutMS
	}
	return memoryFeedbackInvokeTimeoutMS
}

// memoryFeedbackPayload 把宿主返回值归一为 map，非 map 记为 invalid_payload
func memoryFeedbackPayload(payload any) (map[string]any, error) {
	if result, ok := payload.(map[string]any); ok {
		return result, nil
	}
	if payload == nil {
		return map[string]any{"success": false, "queued": false, "reason": "invalid_payload"}, nil
	}
	raw, err := sonic.Marshal(payload)
	if err != nil {
		return map[string]any{"success": false, "queued": false, "reason": "invalid_payload"}, nil
	}
	var converted map[string]any
	if err := sonic.Unmarshal(raw, &converted); err != nil || converted == nil {
		return map[string]any{"success": false, "queued": false, "reason": "invalid_payload"}, nil
	}
	return converted, nil
}

// newToolPostExecutionHandler 把 Maisaka 工具后置副作用接到反馈纠错服务
func newToolPostExecutionHandler(service maisakautils.MemoryFeedbackService) maisaka.ToolPostExecutionHandler {
	if service == nil {
		return nil
	}
	return func(ctx context.Context, invocation maisaka.ToolInvocation, result maisaka.ToolExecutionResult, savedRecord map[string]any, sessionID, logPrefix string) {
		maisakautils.HandleToolPostExecutionEffects(ctx, maisakautils.PostExecutionInput{
			Invocation:  invocation,
			Result:      result,
			SavedRecord: savedRecord,
			SessionID:   sessionID,
			Service:     service,
			LogPrefix:   logPrefix,
		})
	}
}
