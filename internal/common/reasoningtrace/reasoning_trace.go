// Package reasoningtrace 在上下文里传递一轮推理的 trace id
package reasoningtrace

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

type traceIDKey struct{}

// WithTraceID 把本轮推理的 trace id 挂到上下文
func WithTraceID(ctx context.Context, traceID string) context.Context {
	if strings.TrimSpace(traceID) == "" {
		return ctx
	}
	return context.WithValue(ctx, traceIDKey{}, traceID)
}

// TraceID 读取上下文中的 trace id
func TraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(traceIDKey{}).(string)
	return strings.TrimSpace(value)
}

// NewTraceID 生成 32 位十六进制 trace id，对齐 uuid4().hex
func NewTraceID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}
