package model_client

// OpenAI Responses 客户端错误分类与流式错误重放
//
// 对齐 MaiBot src/llm_models/model_client/openai_responses_client.py：
//   - get_response 的 APIConnectionError → NetworkConnectionError（:780-785）
//   - APIStatusError → RespNotOkException(status)（:786-791），由 utils_model.py:1186-1249 按 429/5xx 重试
//   - _consume_response_stream 的 response.failed / response.incomplete / error 事件 → RespParseException（:649-657），
//     缺少 response.completed 事件 → RespParseException（:661-662），由 utils_model.py:1251-1274 重试
//
// eino-ext 的 ResponsesModel 会把错误包装成 fmt.Errorf("failed to ...: %w")，流内错误则以
// fmt.Errorf("received error event: ...") / fmt.Errorf("failed to read stream: %w") 下发；
// 这里统一还原为 MaiBot 的异常分类，供 failover / usage 包装层按 retry.go 的语义重试或切换模型

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"

	"github.com/cloudwego/eino/schema"
	einoopenai "github.com/cloudwego/eino/schema/openai"
	openaisdk "github.com/openai/openai-go/v3"
)

const (
	// responsesStreamErrorPrefix eino-ext 流内 error 事件前缀（见 responses_event_convertor.go）
	responsesStreamErrorPrefix = "received error event:"
	// responsesReadErrorPrefix eino-ext 流读取错误前缀
	responsesReadErrorPrefix = "failed to read stream:"
	// responsesMissingCompletedMessage 对齐 openai_responses_client.py:661-662
	responsesMissingCompletedMessage = "Responses 流结束时缺少 response.completed 事件"
)

// normalizeResponsesCallError 还原 Generate / 建流阶段的 MaiBot 异常分类
// 调用方取消或超时按中断上抛；Provider 自身超时对齐 Python APITimeoutError 归为可重试网络错误
func normalizeResponsesCallError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if abortErr := abortErrorFromContext(ctx); abortErr != nil {
		return abortErr
	}
	var apiErr *openaisdk.Error
	if errors.As(err, &apiErr) && apiErr != nil {
		message := strings.TrimSpace(apiErr.Message)
		if message == "" {
			message = truncateErrBody([]byte(apiErr.Error()))
		}
		return &HTTPStatusError{StatusCode: apiErr.StatusCode, Message: message, Op: "responses"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &NetworkError{Message: "Responses 请求超时", Err: err}
	}
	if errors.Is(err, context.Canceled) {
		return &AbortError{Message: "请求被外部信号中断"}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return &NetworkError{Err: err}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return &NetworkError{Err: err}
	}
	return err
}

// normalizeResponsesStreamError 还原流读取阶段的错误分类
// 流内 error 事件与中途中止对齐 Python RespParseException（可重试）；连接截断/Provider 超时归为可重试网络错误；
// 未知的 eino-ext 事件转换错误保持原样（与 Python generic 异常一致，按硬错误处理）
func normalizeResponsesStreamError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if abortErr := abortErrorFromContext(ctx); abortErr != nil {
		return abortErr
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, responsesStreamErrorPrefix):
		detail := strings.TrimSpace(strings.TrimPrefix(message, responsesStreamErrorPrefix))
		return &ParseError{Message: "Responses 流式请求返回错误: " + detail}
	case strings.HasPrefix(message, responsesReadErrorPrefix):
		if errors.Is(err, context.DeadlineExceeded) {
			return &NetworkError{Message: "Responses 流式请求超时", Err: err}
		}
		if errors.Is(err, context.Canceled) {
			return &AbortError{Message: "请求被外部信号中断"}
		}
		return &NetworkError{Err: err}
	default:
		return err
	}
}

// wrapResponsesStreamErrors 追踪最终响应状态并把 eino-ext 的流错误还原为 MaiBot 分类
// 对齐 openai_responses_client.py:641-662：failed / incomplete / error 事件与缺失 completed 都按
// RespParseException（可重试）处理，不能把未完成的 Responses 流当成功
func wrapResponsesStreamErrors(ctx context.Context, reader *schema.StreamReader[*schema.AgenticMessage]) *schema.StreamReader[*schema.AgenticMessage] {
	if reader == nil {
		return nil
	}
	var lastStatus einoopenai.ResponseStatus
	var lastError *einoopenai.ResponseError
	var lastIncomplete string
	return schema.StreamReaderWithConvert(reader, func(message *schema.AgenticMessage) (*schema.AgenticMessage, error) {
		if message != nil && message.ResponseMeta != nil && message.ResponseMeta.OpenAIExtension != nil {
			extension := message.ResponseMeta.OpenAIExtension
			if extension.Status != "" {
				lastStatus = extension.Status
			}
			lastError = extension.Error
			lastIncomplete = ""
			if extension.IncompleteDetails != nil {
				lastIncomplete = strings.TrimSpace(extension.IncompleteDetails.Reason)
			}
		}
		return message, nil
	}, schema.WithErrWrapper(func(err error) error {
		return normalizeResponsesStreamError(ctx, err)
	}), schema.WithOnEOF(func() (any, error) {
		// eino-ext 可能把调用方取消导致的读取错误吞成正常 EOF，这里优先按中断上抛
		if abortErr := abortErrorFromContext(ctx); abortErr != nil {
			return nil, abortErr
		}
		return nil, responsesStreamEndError(lastStatus, lastError, lastIncomplete)
	}))
}

// responsesStreamEndError 校验流结束时是否收到 response.completed
func responsesStreamEndError(status einoopenai.ResponseStatus, responseErr *einoopenai.ResponseError, incompleteReason string) error {
	switch status {
	case einoopenai.ResponseStatusCompleted:
		return io.EOF
	case einoopenai.ResponseStatusFailed:
		detail := strings.TrimSpace(incompleteReason)
		if responseErr != nil {
			code := strings.TrimSpace(string(responseErr.Code))
			message := strings.TrimSpace(responseErr.Message)
			switch {
			case code != "" && message != "":
				detail = code + ": " + message
			case message != "":
				detail = message
			case code != "":
				detail = code
			}
		}
		if detail == "" {
			return &ParseError{Message: "Responses 流式请求未完整完成: status=failed"}
		}
		return &ParseError{Message: "Responses 流式请求未完整完成: status=failed " + detail}
	case einoopenai.ResponseStatusIncomplete:
		if incompleteReason == "" {
			return &ParseError{Message: "Responses 流式请求未完整完成: status=incomplete"}
		}
		return &ParseError{Message: fmt.Sprintf("Responses 流式请求未完整完成: status=incomplete reason=%s", incompleteReason)}
	default:
		return &ParseError{Message: responsesMissingCompletedMessage}
	}
}
