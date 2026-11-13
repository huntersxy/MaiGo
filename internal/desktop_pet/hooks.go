package desktoppet

import (
	"context"

	"go.uber.org/zap"
)

// HookEmitter 桌宠观察型事件的派发方，对齐 plugin_runtime.integration.invoke_hook
type HookEmitter func(ctx context.Context, hookName string, kwargs map[string]any)

// SetHookEmitter 注入 desktop_pet.* 钩子派发器
func (s *Service) SetHookEmitter(emitter HookEmitter) {
	s.mu.Lock()
	s.hooks = emitter
	s.mu.Unlock()
}

// emitHook 异步触发 desktop_pet.<name>，与 Python 的 _observe 一致不阻塞事件处理
func (s *Service) emitHook(ctx context.Context, name string, kwargs map[string]any) {
	s.mu.RLock()
	emitter := s.hooks
	s.mu.RUnlock()
	if emitter == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if kwargs == nil {
		kwargs = map[string]any{}
	}
	hookName := "desktop_pet." + name
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				zap.L().Error("桌宠钩子派发异常", zap.String("hook", hookName), zap.Any("panic", recovered))
			}
		}()
		emitter(ctx, hookName, kwargs)
	}()
}
