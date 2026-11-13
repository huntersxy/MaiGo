package stats

// OnlineTracker 接入统一任务管理器的适配。
//
// MaiBot 把 OnlineTimeRecordTask 注册进 async_task_manager（main.py:226），
// Go 侧 OnlineTracker 自带 tick 循环。这里提供 Runner 适配，
// 使在线时间记录可以由统一管理器调度，与 Python 的注册方式一致。

import (
	"context"
	"time"

	"maibot-go/internal/manager/asynctask"
)

// onlineTimeTask 把 OnlineTracker 的单轮心跳包装成可调度任务
type onlineTimeTask struct {
	asynctask.Task
	tracker *OnlineTracker
}

// OnlineTimeRunner 返回可注册到 asynctask.Manager 的任务
//
// 对齐 Python main.py:226 的 add_task(OnlineTimeRecordTask())：
// 任务名与 60s 间隔与 Python 一致。
func (t *OnlineTracker) OnlineTimeRunner() asynctask.Runner {
	return &onlineTimeTask{
		Task: asynctask.Task{
			TaskName:    "Online Time Record Task",
			RunInterval: onlineRecordInterval,
		},
		tracker: t,
	}
}

// Run 执行一轮：累计秒数、落盘聚合、续写在线区间
//
// 与 OnlineTracker 自带 tick 的单轮逻辑一致。
func (o *onlineTimeTask) Run(ctx context.Context) error {
	if o == nil || o.tracker == nil {
		return nil
	}
	return o.tracker.onceTick(ctx)
}

// onceTick 执行一次在线时间采样
func (t *OnlineTracker) onceTick(ctx context.Context) error {
	if t == nil || t.db == nil {
		return nil
	}
	t.accumulateSinceLastTick()
	t.flush()
	return t.recordOnlineHeartbeat(ctx, time.Now())
}
