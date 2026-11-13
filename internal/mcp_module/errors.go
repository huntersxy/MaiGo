package mcp

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"

	mcpprotocol "github.com/mark3labs/mcp-go/mcp"
)

// ErrorKind MCP 错误分类，供日志、WebUI 与测试断言使用
type ErrorKind string

const (
	// ErrorKindTimeout 连接或读取超时
	ErrorKindTimeout ErrorKind = "timeout"
	// ErrorKindAuth 认证失败（401/403）
	ErrorKindAuth ErrorKind = "auth"
	// ErrorKindNotFound 目标工具、Prompt 或 Resource 不存在
	ErrorKindNotFound ErrorKind = "not_found"
	// ErrorKindMethodNotFound 服务端未实现对应 MCP 方法
	ErrorKindMethodNotFound ErrorKind = "method_not_found"
	// ErrorKindConnection 传输层连接失败
	ErrorKindConnection ErrorKind = "connection"
	// ErrorKindProtocol 协议层错误
	ErrorKindProtocol ErrorKind = "protocol"
	// ErrorKindInternal 其它内部错误
	ErrorKindInternal ErrorKind = "internal"
)

// Error 可向上层展示的 MCP 调用错误
type Error struct {
	msg    string
	kind   ErrorKind
	err    error
	server string
	op     string
}

func (e *Error) Error() string { return e.msg }

// Unwrap 保留底层错误，便于 errors.Is/As 判断
func (e *Error) Unwrap() error { return e.err }

// Kind 返回错误分类
func (e *Error) Kind() ErrorKind {
	if e == nil {
		return ErrorKindInternal
	}
	return e.kind
}

// Server 返回关联的 MCP 服务器名
func (e *Error) Server() string {
	if e == nil {
		return ""
	}
	return e.server
}

// NewError 构造 MCP 错误
func NewError(msg string) error {
	return &Error{msg: msg, kind: ErrorKindInternal}
}

// newError 构造带分类的 MCP 错误
func newError(kind ErrorKind, server, op, msg string, cause error) error {
	return &Error{msg: msg, kind: kind, err: cause, server: server, op: op}
}

// ErrorKindOf 返回错误分类，未包装的错误按底层原因推断
func ErrorKindOf(err error) ErrorKind {
	if err == nil {
		return ""
	}
	var target *Error
	if errors.As(err, &target) && target.kind != "" {
		return target.kind
	}
	return classifyError(err)
}

// classifyError 按底层原因推断错误分类
func classifyError(err error) ErrorKind {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorKindTimeout
	}
	if errors.Is(err, context.Canceled) {
		return ErrorKindTimeout
	}
	if errors.Is(err, mcpprotocol.ErrMethodNotFound) {
		return ErrorKindMethodNotFound
	}
	if errors.Is(err, mcpprotocol.ErrResourceNotFound) {
		return ErrorKindNotFound
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrorKindTimeout
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return ErrorKindTimeout
		}
		return ErrorKindConnection
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "401"), strings.Contains(text, "403"),
		strings.Contains(text, "unauthorized"), strings.Contains(text, "forbidden"):
		return ErrorKindAuth
	case strings.Contains(text, "method not found"):
		return ErrorKindMethodNotFound
	case strings.Contains(text, "timeout"), strings.Contains(text, "deadline exceeded"):
		return ErrorKindTimeout
	case strings.Contains(text, "connection refused"), strings.Contains(text, "no such host"),
		strings.Contains(text, "broken pipe"), strings.Contains(text, "connection reset"):
		return ErrorKindConnection
	}
	return ErrorKindInternal
}
