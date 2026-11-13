package hooks

import (
	"context"
	"sync"
)

// Emitter 命名 Hook 触发器；*pluginruntime.Host 满足该接口。
// 低层子系统（emoji / learner / tools）不应反向依赖 pluginruntime，
// 因此由 agent 注入全局触发器，子系统通过 EmitGlobal 发送 Hook。
type Emitter interface {
	EmitHook(ctx context.Context, hookName string, kwargs map[string]any) DispatchResult
}

var (
	globalEmitterMu sync.RWMutex
	globalEmitter   Emitter
)

// SetGlobalEmitter 注入全局 Hook 触发器；传 nil 清除（测试收尾用）
func SetGlobalEmitter(emitter Emitter) {
	globalEmitterMu.Lock()
	globalEmitter = emitter
	globalEmitterMu.Unlock()
}

// GlobalEmitter 返回当前全局 Hook 触发器（未注入时为 nil）
func GlobalEmitter() Emitter {
	globalEmitterMu.RLock()
	defer globalEmitterMu.RUnlock()
	return globalEmitter
}

// EmitGlobal 通过全局触发器发送 Hook；未注入时按无订阅处理，原样返回入参 kwargs
func EmitGlobal(ctx context.Context, hookName string, kwargs map[string]any) DispatchResult {
	emitter := GlobalEmitter()
	if emitter == nil {
		if kwargs == nil {
			kwargs = map[string]any{}
		}
		return DispatchResult{HookName: hookName, Kwargs: kwargs}
	}
	return emitter.EmitHook(ctx, hookName, kwargs)
}
