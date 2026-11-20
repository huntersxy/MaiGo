package contract

// 对齐 MaiBot src/llm_models/exceptions.py 的错误分类
// 重试/切模型的判定集中在 ClassifyAttemptError，供 failover/usage 包装器复用

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// maiBotErrorCodeMapping 对齐 MaiBot exceptions.error_code_mapping
var maiBotErrorCodeMapping = map[int]string{
	400: "参数不正确",
	401: "API-Key错误，认证失败，请检查/config/model_list.toml中的配置是否正确",
	402: "账号余额不足",
	403: "模型拒绝访问，可能需要实名或余额不足",
	404: "Not Found",
	413: "请求体过大，请尝试压缩图片或减少输入内容",
	429: "请求过于频繁，请稍后再试",
	500: "服务器内部故障",
	503: "服务器负载过高",
}

// HTTPStatusError 对齐 MaiBot RespNotOkException：Provider 返回非 2xx
type HTTPStatusError struct {
	StatusCode int
	Message    string // Provider 原始错误体，仅用于日志与快照
	Op         string
	// RequestID 上游响应头里的请求编号，便于向供应商对账
	RequestID string
}

func (e *HTTPStatusError) Error() string {
	if text, ok := maiBotErrorCodeMapping[e.StatusCode]; ok {
		return text
	}
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("未知的异常响应代码：%d", e.StatusCode)
}

// EmptyResponseError 对齐 MaiBot EmptyResponseException：响应既无正文也无工具调用
type EmptyResponseError struct {
	Message string
}

func (e *EmptyResponseError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "响应内容为空，这可能是一个临时性问题"
}

// ParseError 对齐 MaiBot RespParseException：响应格式无法解析
type ParseError struct {
	Message string
	Err     error
}

func (e *ParseError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "解析响应内容时发生未知错误，请检查是否配置了正确的解析方法"
}

func (e *ParseError) Unwrap() error { return e.Err }

// NetworkError 对齐 MaiBot NetworkConnectionError：连接异常
type NetworkError struct {
	Message string
	Err     error
}

func (e *NetworkError) Error() string {
	// 对齐 Python __str__：只返回 message 或默认中文，不拼接 cause（cause 走 Unwrap）
	if e.Message != "" {
		return e.Message
	}
	return "连接异常，请检查网络连接状态或URL是否正确"
}

func (e *NetworkError) Unwrap() error { return e.Err }

// AbortError 对齐 MaiBot ReqAbortException：请求被外部中断
type AbortError struct {
	Message string
}

func (e *AbortError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "请求因未知原因异常终止"
}

// TaskTimeoutError 对齐 MaiBot LLMTaskTimeoutError：任务级 hard_timeout 触发
// 语义上等价 ModelAttemptFailed，由调度层按「切换下一个模型」处理
type TaskTimeoutError struct {
	Task    string
	Model   string
	Timeout time.Duration
}

func (e *TaskTimeoutError) Error() string {
	return fmt.Sprintf("任务 '%s' 模型 '%s' 触发硬超时 %s", e.Task, e.Model, formatSeconds(e.Timeout))
}

func formatSeconds(d time.Duration) string {
	seconds := d.Seconds()
	if seconds == float64(int64(seconds)) {
		return fmt.Sprintf("%ds", int64(seconds))
	}
	return fmt.Sprintf("%.3fs", seconds)
}

// RetryAction 单次尝试失败后的处置方式
type RetryAction int

const (
	// RetryActionFail 不再重试，交给调度层切换下一个模型
	RetryActionFail RetryAction = iota
	// RetryActionRetry 同一模型内按 retry_interval 重试
	RetryActionRetry
	// RetryActionAbort 外部取消/中断，直接上抛
	RetryActionAbort
)

// ClassifyAttemptError 对齐 MaiBot _attempt_request_on_model 的异常分支顺序
func ClassifyAttemptError(err error) RetryAction {
	if err == nil {
		return RetryActionFail
	}
	var abortErr *AbortError
	if errors.As(err, &abortErr) {
		return RetryActionAbort
	}
	var timeoutErr *TaskTimeoutError
	if errors.As(err, &timeoutErr) {
		return RetryActionFail
	}
	var emptyErr *EmptyResponseError
	if errors.As(err, &emptyErr) {
		return RetryActionRetry
	}
	var networkErr *NetworkError
	if errors.As(err, &networkErr) {
		return RetryActionRetry
	}
	var parseErr *ParseError
	if errors.As(err, &parseErr) {
		return RetryActionRetry
	}
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		if statusErr.StatusCode == 429 || statusErr.StatusCode >= 500 {
			return RetryActionRetry
		}
		return RetryActionFail
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// 调用方取消或本层超时，按中断处理，不再重试
		return RetryActionAbort
	}
	return RetryActionFail
}

// abortErrorFromContext 把 ctx 取消转换为 AbortError，保证错误分类稳定
// AbortErrorFromContext 把上下文取消/超时转成中断错误
//
// 客户端与快照层都要判断「这次失败是否由外部中断引起」，
// 放入 contract 供两侧共用。
func AbortErrorFromContext(ctx context.Context) error {
	return abortErrorFromContext(ctx)
}

func abortErrorFromContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return &AbortError{Message: "请求被外部信号中断"}
		}
		return &AbortError{Message: "请求超时中断"}
	}
	return nil
}
