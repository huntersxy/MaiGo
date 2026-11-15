package bootstrap

import (
	"context"

	"MaiGo/internal/runtime/execution/policy"
	"MaiGo/internal/runtime/execution/scheduler"
)

// nativeSessionSchedulerAdapter 把 Go 原生会话调度器适配成 policy.SessionScheduler。
//
// 适配器只存在于启动装配层，业务代码只依赖 policy 接口；替换实现时不需要改主链。
type nativeSessionSchedulerAdapter struct {
	engine *scheduler.Scheduler
}

func (a nativeSessionSchedulerAdapter) Submit(ctx context.Context, sessionKey string, task policy.SessionTask) error {
	return a.engine.Submit(ctx, sessionKey, scheduler.Task(task))
}

func (a nativeSessionSchedulerAdapter) Shutdown(ctx context.Context) error {
	return a.engine.Shutdown(ctx)
}

func (a nativeSessionSchedulerAdapter) Stats() any {
	return a.engine.Stats()
}
