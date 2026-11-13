package hooks

import "context"

// RunnerBridge Host 侧 Runner RPC 桥，供 Emit 转发 plugin.invoke_hook
type RunnerBridge interface {
	HasActiveRunner() bool
	InvokeHookHandler(ctx context.Context, sub Subscription, hookName string, kwargs map[string]any) (HandlerResult, error)
}
