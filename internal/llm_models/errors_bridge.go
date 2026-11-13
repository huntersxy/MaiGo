package llm

// 错误类型别名。
//
// 定义位于 contract 包（对齐 Python src/llm_models/exceptions.py 被
// model_client 各客户端与快照/统计层共同引用）。客户端与快照层分属
// 不同包，错误类型必须位于两者之下。

import (
	"context"

	"maibot-go/internal/llm_models/contract"
)

type (
	// HTTPStatusError Provider 返回非 2xx 状态
	HTTPStatusError = contract.HTTPStatusError
	// EmptyResponseError 响应体为空
	EmptyResponseError = contract.EmptyResponseError
	// ParseError 响应解析失败
	ParseError = contract.ParseError
	// NetworkError 网络层失败
	NetworkError = contract.NetworkError
	// AbortError 请求被上下文取消
	AbortError = contract.AbortError
	// TaskTimeoutError 任务级超时
	TaskTimeoutError = contract.TaskTimeoutError
)

// abortErrorFromContext 保持父包内原有调用名
func abortErrorFromContext(ctx context.Context) error {
	return contract.AbortErrorFromContext(ctx)
}

// RetryAction 重试决策
type RetryAction = contract.RetryAction

const (
	// RetryActionFail 本模型失败，交给上层换候选
	RetryActionFail = contract.RetryActionFail
	// RetryActionRetry 同一模型内按 retry_interval 重试
	RetryActionRetry = contract.RetryActionRetry
	// RetryActionAbort 外部取消/中断，直接上抛
	RetryActionAbort = contract.RetryActionAbort
)

// classifyAttemptError 对齐 MaiBot _attempt_request_on_model 的异常分支顺序
func classifyAttemptError(err error) RetryAction {
	return contract.ClassifyAttemptError(err)
}
