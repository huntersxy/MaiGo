package protocol

import (
	"errors"
	"fmt"
	"reflect"
)

// ErrorCode RPC 错误码，对齐 protocol.errors.ErrorCode
type ErrorCode string

const (
	ErrorCodeOK               ErrorCode = "OK"
	ErrorCodeUnknown          ErrorCode = "E_UNKNOWN"
	ErrorCodeTimeout          ErrorCode = "E_TIMEOUT"
	ErrorCodeBadPayload       ErrorCode = "E_BAD_PAYLOAD"
	ErrorCodeProtocolMismatch ErrorCode = "E_PROTOCOL_MISMATCH"
	ErrorCodeShuttingDown     ErrorCode = "E_SHUTTING_DOWN"
	ErrorCodeUnauthorized     ErrorCode = "E_UNAUTHORIZED"
	ErrorCodeMethodNotAllowed ErrorCode = "E_METHOD_NOT_ALLOWED"
	ErrorCodeBackPressure     ErrorCode = "E_BACK_PRESSURE"
	ErrorCodeHostOverloaded   ErrorCode = "E_HOST_OVERLOADED"
	ErrorCodePluginCrashed    ErrorCode = "E_PLUGIN_CRASHED"
	ErrorCodePluginNotFound   ErrorCode = "E_PLUGIN_NOT_FOUND"
	ErrorCodeReloadInProgress ErrorCode = "E_RELOAD_IN_PROGRESS"
	ErrorCodeCapabilityDenied ErrorCode = "E_CAPABILITY_DENIED"
	ErrorCodeCapabilityFailed ErrorCode = "E_CAPABILITY_FAILED"
	ErrorCodeWebConfigInvalid ErrorCode = "E_WEB_SERVICE_CONFIG_INVALID"
)

// RPCError Host ↔ Runner RPC 异常
type RPCError struct {
	Code    ErrorCode
	Message string
	Details map[string]any
}

func (e *RPCError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("[%s] %s", e.Code, e.Message)
	}
	return fmt.Sprintf("[%s]", e.Code)
}

func (e *RPCError) ToMap() map[string]any {
	return map[string]any{
		"code":    string(e.Code),
		"message": e.Message,
		"details": e.Details,
	}
}

func RPCErrorFromMap(data map[string]any) *RPCError {
	code := ErrorCodeUnknown
	if raw, ok := data["code"].(string); ok && raw != "" {
		code = ErrorCode(raw)
	}
	msg, _ := data["message"].(string)
	details, _ := data["details"].(map[string]any)
	if details == nil {
		details = map[string]any{}
	}
	return &RPCError{Code: code, Message: msg, Details: details}
}

// RPCErrorFromException 把普通异常包装成 RPCError，对齐 RPCError.from_exception
//
// codeMapping 用 error 类型到错误码的映射表；Go 没有 Python 的 isinstance 继承匹配，
// 调用方按需给出具体类型→错误码的对应关系，未命中时回落 E_UNKNOWN。
func RPCErrorFromException(err error, codeMapping map[reflect.Type]ErrorCode) *RPCError {
	if err == nil {
		return &RPCError{Code: ErrorCodeUnknown, Details: map[string]any{}}
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr
	}
	if len(codeMapping) > 0 {
		value := reflect.ValueOf(err)
		for exceptionType, code := range codeMapping {
			if exceptionType != nil && value.Type().AssignableTo(exceptionType) {
				return &RPCError{Code: code, Message: err.Error(), Details: map[string]any{}}
			}
		}
	}
	return &RPCError{Code: ErrorCodeUnknown, Message: err.Error(), Details: map[string]any{}}
}
