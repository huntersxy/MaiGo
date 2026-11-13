package imagememory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
)

// ValidationError 对齐 Python ValueError
type ValidationError struct {
	Message string
}

// Error 返回错误文案
func (e *ValidationError) Error() string { return e.Message }

// RuntimeError 对齐 Python RuntimeError
type RuntimeError struct {
	Message string
}

// Error 返回错误文案
func (e *RuntimeError) Error() string { return e.Message }

// NotFoundError 对齐 Python FileNotFoundError
type NotFoundError struct {
	Message string
}

// Error 返回错误文案
func (e *NotFoundError) Error() string { return e.Message }

// validationErrorf 构造 ValueError 等价错误
func validationErrorf(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

// runtimeErrorf 构造 RuntimeError 等价错误
func runtimeErrorf(format string, args ...any) error {
	return &RuntimeError{Message: fmt.Sprintf(format, args...)}
}

// notFoundErrorf 构造 FileNotFoundError 等价错误
func notFoundErrorf(format string, args ...any) error {
	return &NotFoundError{Message: fmt.Sprintf(format, args...)}
}

// errorReason 对齐 Python type(exc).__name__ 的探测失败原因
func errorReason(err error) string {
	if err == nil {
		return ""
	}
	var validation *ValidationError
	if errors.As(err, &validation) {
		return "ValueError"
	}
	var runtimeFailure *RuntimeError
	if errors.As(err, &runtimeFailure) {
		return "RuntimeError"
	}
	var notFound *NotFoundError
	if errors.As(err, &notFound) {
		return "FileNotFoundError"
	}
	if errors.Is(err, context.Canceled) {
		return "CancelledError"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TimeoutError"
	}
	errorType := reflect.TypeOf(err)
	for errorType.Kind() == reflect.Ptr {
		errorType = errorType.Elem()
	}
	if errorType.Name() != "" {
		return errorType.Name()
	}
	return "Exception"
}
