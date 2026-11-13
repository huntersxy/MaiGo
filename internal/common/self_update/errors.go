package selfupdate

import (
	"errors"
	"fmt"
)

// Error 自更新流程中的可预期失败
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string {
	return e.Message
}

// NewError 构造带错误码的自更新错误
func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// ErrorCode 读取错误码，非自更新错误返回空
func ErrorCode(err error) string {
	var value *Error
	if errors.As(err, &value) {
		return value.Code
	}
	return "unexpected_error"
}

func errorf(code, format string, args ...any) *Error {
	return NewError(code, fmt.Sprintf(format, args...))
}
