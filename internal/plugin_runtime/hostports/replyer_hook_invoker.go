package hostports

// Replyer 生成循环与全局插件 Hook 的桥接

import (
	"context"

	"MaiGo/internal/chat/replyer"
	"MaiGo/internal/plugin_runtime/hooks"

	"go.uber.org/zap"
)

// ReplyerHookInvoker 把 replyer 端口接到 pluginruntime 全局 Hook
type ReplyerHookInvoker struct{}

// InvokeHook 发射命名 Hook；Hook 执行错误只记录，不打断回复生成
func (ReplyerHookInvoker) InvokeHook(ctx context.Context, name string, kwargs map[string]any) (replyer.HookResult, error) {
	result := hooks.EmitGlobal(ctx, name, kwargs)
	if len(result.Errors) > 0 {
		zap.L().Warn("Replyer Hook 执行失败", zap.String("hook", name), zap.Strings("errors", result.Errors))
	}
	return replyer.HookResult{
		Aborted:     result.Aborted,
		Kwargs:      result.Kwargs,
		HookName:    result.HookName,
		HandlerName: result.StoppedBy,
		Reason:      result.AbortReason,
	}, nil
}
