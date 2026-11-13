package maisaka

import (
	"context"
	"strings"

	"go.uber.org/zap"
)

// ToolRecord 一条待落库的工具执行记录
type ToolRecord struct {
	ToolID        string
	SessionID     string
	ToolName      string
	ToolReasoning string
	ToolData      map[string]any
}

// ToolRecordStore 工具执行记录落库端口，由宿主适配 internal/memory
type ToolRecordStore interface {
	// StoreToolExecution 落库并返回已保存记录，nil 表示未保存
	StoreToolExecution(ctx context.Context, invocation ToolInvocation, result ToolExecutionResult, spec *ToolSpec) (map[string]any, error)
}

// ToolPostExecutionHandler 工具记录落库成功后的非落库副作用处理端口
type ToolPostExecutionHandler func(ctx context.Context, invocation ToolInvocation, result ToolExecutionResult, savedRecord map[string]any, sessionID, logPrefix string)

// recordToolExecution 调用宿主记录工具执行结果；失败只打日志，不阻断主循环
func (e *ReasoningEngine) recordToolExecution(ctx context.Context, invocation ToolInvocation, result ToolExecutionResult, spec *ToolSpec) {
	if e == nil || e.runtime == nil || e.runtime.opts.ToolRecords == nil {
		return
	}
	if strings.TrimSpace(invocation.SessionID) == "" {
		return
	}
	savedRecord, err := e.runtime.opts.ToolRecords.StoreToolExecution(ctx, invocation, result, spec)
	if err != nil {
		zap.L().Warn(e.runtime.logPrefix+" 写入工具记录失败",
			zap.String("tool", invocation.ToolName), zap.String("tool_call_id", invocation.CallID), zap.Error(err))
		return
	}
	if e.runtime.opts.ToolPostExecution == nil {
		return
	}
	e.runtime.opts.ToolPostExecution(ctx, invocation, result, savedRecord, invocation.SessionID, e.runtime.logPrefix)
}
