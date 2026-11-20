package llm

// 失败诊断字段
// 错误文案保持与 MaiBot 映射表一致；状态码、上游请求编号与逻辑请求编号
// 只进入日志、监控与快照，便于按供应商原始响应排查。

import (
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// ErrorDiagnostics 从错误链提取的诊断信息
type ErrorDiagnostics struct {
	Type              string
	StatusCode        int
	ProviderRequestID string
	ProviderMessage   string
	RequestID         string
	SnapshotPath      string
	ReplayCommand     string
}

// DiagnoseError 提取失败链上的状态码、上游请求编号与快照信息
func DiagnoseError(err error) ErrorDiagnostics {
	if err == nil {
		return ErrorDiagnostics{}
	}
	out := ErrorDiagnostics{Type: fmt.Sprintf("%T", err)}
	if statusErr := asHTTPStatusError(err); statusErr != nil {
		out.Type = "HTTPStatusError"
		out.StatusCode = statusErr.StatusCode
		out.ProviderRequestID = statusErr.RequestID
		out.ProviderMessage = statusErr.Message
	}
	if path, _, ok := RequestSnapshotInfo(err); ok {
		out.SnapshotPath = path
		out.ReplayCommand = snapshotReplayCommand(path)
	}
	out.RequestID = RequestTraceIDFromError(err)
	return out
}

// ErrorLogFields 把诊断信息转成 zap 字段；无对应值时省略
func ErrorLogFields(err error) []zap.Field {
	diag := DiagnoseError(err)
	if err == nil {
		return nil
	}
	fields := []zap.Field{zap.String("error_type", diag.Type)}
	if diag.StatusCode > 0 {
		fields = append(fields, zap.Int("status_code", diag.StatusCode))
	}
	if diag.ProviderRequestID != "" {
		fields = append(fields, zap.String("provider_request_id", diag.ProviderRequestID))
	}
	if diag.ProviderMessage != "" {
		fields = append(fields, zap.String("provider_message", diag.ProviderMessage))
	}
	if diag.RequestID != "" {
		fields = append(fields, zap.String("request_id", diag.RequestID))
	}
	if diag.SnapshotPath != "" {
		fields = append(fields, zap.String("snapshot_path", diag.SnapshotPath))
	}
	if diag.ReplayCommand != "" {
		fields = append(fields, zap.String("replay_command", diag.ReplayCommand))
	}
	return fields
}

func asHTTPStatusError(err error) *HTTPStatusError {
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr
	}
	return nil
}

// retryReason 对齐 Python _schedule_llm_retry_event 的 reason 取值
func retryReason(err error) string {
	if err == nil {
		return "模型请求失败"
	}
	if statusErr := asHTTPStatusError(err); statusErr != nil {
		return fmt.Sprintf("HTTP %d", statusErr.StatusCode)
	}
	var emptyErr *EmptyResponseError
	if errors.As(err, &emptyErr) {
		return "模型返回空回复"
	}
	var parseErr *ParseError
	if errors.As(err, &parseErr) {
		return "响应解析失败"
	}
	var networkErr *NetworkError
	if errors.As(err, &networkErr) {
		return "网络错误"
	}
	return err.Error()
}

type requestTraceIDCarrier interface {
	error
	RequestTraceID() string
}

type requestTraceIDError struct {
	cause     error
	requestID string
}

func (e *requestTraceIDError) Error() string          { return e.cause.Error() }
func (e *requestTraceIDError) Unwrap() error          { return e.cause }
func (e *requestTraceIDError) RequestTraceID() string { return e.requestID }

// attachRequestTraceID 给失败链挂上逻辑请求编号；已有编号时不覆盖
func attachRequestTraceID(err error, requestID string) error {
	requestID = strings.TrimSpace(requestID)
	if err == nil || requestID == "" {
		return err
	}
	var existing requestTraceIDCarrier
	if errors.As(err, &existing) && existing.RequestTraceID() != "" {
		return err
	}
	return &requestTraceIDError{cause: err, requestID: requestID}
}

// RequestTraceIDFromError 提取失败链上的逻辑请求编号
func RequestTraceIDFromError(err error) string {
	var carrier requestTraceIDCarrier
	if errors.As(err, &carrier) {
		return carrier.RequestTraceID()
	}
	return ""
}

// snapshotReplayCommand 返回快照对应的重放命令
func snapshotReplayCommand(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	info := snapshotReplayInfo(path)
	command, _ := info["command"].(string)
	return command
}
