package messagereceive

import (
	"context"

	"MaiGo/internal/plugin_runtime/hooks"
)

// GlobalHookInvoker 使用全局 Hook 触发器，供生产环境注入
type GlobalHookInvoker struct{}

// Invoke 调用 hooks.EmitGlobal 并投影为本包的 HookResult
func (GlobalHookInvoker) Invoke(ctx context.Context, hookName string, kwargs map[string]any) HookResult {
	result := hooks.EmitGlobal(ctx, hookName, kwargs)
	return HookResult{
		Kwargs:    result.Kwargs,
		Aborted:   result.Aborted,
		StoppedBy: result.StoppedBy,
		Errors:    result.Errors,
	}
}
