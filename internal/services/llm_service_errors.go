package services

// LLM 服务错误分类
// Python llm_service 把底层异常收敛为 LLMServiceResult.error；Go 保留错误链并给出稳定分类，
// 重试与模型切换仍由 internal/llm 的 attemptRunner/failoverModel 负责，不在服务层重复。

import (
	"errors"
	"net"

	"MaiGo/internal/llm_models"
)

// LLMServiceErrorKind 服务层错误分类
type LLMServiceErrorKind string

const (
	LLMServiceErrorUnknown LLMServiceErrorKind = "unknown"
	LLMServiceErrorNetwork LLMServiceErrorKind = "network"
	LLMServiceErrorHTTP    LLMServiceErrorKind = "http"
	LLMServiceErrorParse   LLMServiceErrorKind = "parse"
	LLMServiceErrorTimeout LLMServiceErrorKind = "timeout"
	LLMServiceErrorAborted LLMServiceErrorKind = "aborted"
	LLMServiceErrorEmpty   LLMServiceErrorKind = "empty"
)

// ClassifyLLMServiceError 按错误链分类；分类只描述失败边界，不改变错误
func ClassifyLLMServiceError(err error) LLMServiceErrorKind {
	if err == nil {
		return ""
	}
	var (
		timeoutErr *llm.TaskTimeoutError
		abortErr   *llm.AbortError
		httpErr    *llm.HTTPStatusError
		parseErr   *llm.ParseError
		netErr     *llm.NetworkError
		emptyErr   *llm.EmptyResponseError
	)
	switch {
	case errors.As(err, &timeoutErr):
		return LLMServiceErrorTimeout
	case errors.As(err, &abortErr):
		return LLMServiceErrorAborted
	case errors.As(err, &emptyErr):
		return LLMServiceErrorEmpty
	case errors.As(err, &parseErr):
		return LLMServiceErrorParse
	case errors.As(err, &httpErr):
		return LLMServiceErrorHTTP
	case errors.As(err, &netErr):
		return LLMServiceErrorNetwork
	}
	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return LLMServiceErrorTimeout
	}
	return LLMServiceErrorUnknown
}
