package bootstrap

import (
	"context"
	"time"

	"MaiGo/internal/runtime"

	"go.uber.org/zap"
)

// workerShutdownHooks 汇总 main 中可安全执行的关停动作
// 所有字段都可为空：未接线的能力不会伪造成功，而是由调用方在报告中列明
type workerShutdownHooks struct {
	StopLogBroadcast  func(context.Context) error
	StopMessageEntry  func(context.Context) error
	StopDesktopPet    func(context.Context) error
	StopACPAgent      func(context.Context) error
	StopComputer      func(context.Context) error
	StopChatLoop      func(context.Context) error
	StopHTTP          func(context.Context) error
	StopConfigWatcher func(context.Context) error
	EmitOnStop        func(context.Context) error
	StopPluginRuntime func(context.Context) error
	StopImageSync     func(context.Context) error
	StopMemoryWriter  func(context.Context) error
	StopMemoryKernel  func(context.Context) error
	ShutdownEmoji     func(context.Context) error
	CloseMCP          func(context.Context) error
	StopAsyncTasks    func(context.Context) error
	CancelRemaining   func(context.Context) error
	StopStats         func(context.Context) error
	CloseTelemetry    func(context.Context) error
	CloseHTMLRender   func(context.Context) error
	CleanupUpdateTemp func(context.Context) error
	CloseMemoryDB     func(context.Context) error
}

// newWorkerShutdownPlan 按 MaiBot graceful_shutdown 的阶段顺序构建关停计划
// 前置阶段共享 10 秒，记忆写入者 5 秒、内核 30 秒、最终清理 5 秒各自独立推进截止时间
func newWorkerShutdownPlan(hooks workerShutdownHooks) runtime.ShutdownPlan {
	plan := runtime.ShutdownPlan{
		TotalTimeout:  runtime.ShutdownTimeout,
		StuckAfter:    3 * time.Second,
		StuckInterval: 5 * time.Second,
		OnStepStart: func(step runtime.ShutdownStep) {
			zap.L().Info("关停步骤开始",
				zap.String("step", step.Name),
				zap.String("phase", string(step.Phase)),
				zap.Duration("timeout", step.Timeout))
		},
		OnStep: func(result runtime.ShutdownStepResult) {
			if result.Success {
				zap.L().Info("关停步骤完成",
					zap.String("step", result.Name),
					zap.String("phase", string(result.Phase)),
					zap.Duration("duration", result.Duration))
				return
			}
			fields := []zap.Field{
				zap.String("step", result.Name),
				zap.String("phase", string(result.Phase)),
				zap.Duration("duration", result.Duration),
				zap.Bool("timed_out", result.TimedOut),
			}
			if result.Err != "" {
				fields = append(fields, zap.String("error", result.Err))
			}
			zap.L().Warn("关停步骤失败，继续后续步骤", fields...)
		},
		OnStuck: func(stuck runtime.ShutdownStuck) {
			zap.L().Warn("关停步骤等待现场",
				zap.String("step", stuck.Name),
				zap.String("phase", string(stuck.Phase)),
				zap.Duration("elapsed", stuck.Elapsed),
				zap.Duration("timeout", stuck.Timeout),
				zap.String("stacks", stuck.Stacks))
		},
	}
	add := func(name string, phase runtime.ShutdownPhase, timeout time.Duration, run func(context.Context) error) {
		if run == nil {
			return
		}
		plan.Steps = append(plan.Steps, runtime.ShutdownStep{
			Name:    name,
			Phase:   phase,
			Timeout: timeout,
			Run:     run,
		})
	}

	// 先关闭广播入口，后续关停日志只写文件/终端，不再派生 WebSocket 任务
	add("停止日志广播", runtime.PhasePre, runtime.LogBroadcastTimeout, hooks.StopLogBroadcast)

	// 消息生产者先于插件、电脑及存储关闭，避免在依赖销毁后继续调用工具
	add("停止消息入口", runtime.PhasePre, 5*time.Second, hooks.StopMessageEntry)
	// 先停止桌宠轮询和主动唤醒，窗口退出时仍可调用插件关闭钩子
	add("停止桌宠", runtime.PhasePre, 6*time.Second, hooks.StopDesktopPet)
	add("停止聊天循环", runtime.PhasePre, 5*time.Second, hooks.StopChatLoop)
	add("停止外接 Agent", runtime.PhasePre, 8*time.Second, hooks.StopACPAgent)
	add("保存并断开电脑", runtime.PhasePre, 8*time.Second, hooks.StopComputer)
	add("关闭 HTTP 服务", runtime.PhasePre, 5*time.Second, hooks.StopHTTP)
	add("停止配置监听", runtime.PhasePre, 5*time.Second, hooks.StopConfigWatcher)
	add("触发 ON_STOP 事件", runtime.PhasePre, 5*time.Second, hooks.EmitOnStop)

	// 插件运行时包含桌宠等依赖插件的 UI 资源，统一在此停止
	add("停止插件运行时", runtime.PhasePre, 8*time.Second, hooks.StopPluginRuntime)

	// 与 Python 一致：插件停后等待图片描述写回，再停止记忆写入者及内核
	add("等待图片描述同步", runtime.PhasePre, runtime.ImageSyncTimeout, hooks.StopImageSync)

	// 记忆写入者阶段：先停独立写回队列，再关闭 A_Memorix 内核
	add("停止记忆自动写入", runtime.PhaseMemoryWriter, runtime.MemoryWriterTimeout, hooks.StopMemoryWriter)

	// 内核阶段：关闭 A_Memorix 原生宿主并等待持久化
	add("关闭 A_Memorix 并持久化", runtime.PhaseMemoryKernel, runtime.MemoryKernelTimeout, hooks.StopMemoryKernel)

	// 最终阶段：表情、MCP、异步任务与剩余协程都必须在剩余预算内收口
	add("关闭 Emoji", runtime.PhaseFinal, runtime.FinalCleanupTimeout, hooks.ShutdownEmoji)
	add("关闭 MCP", runtime.PhaseFinal, runtime.FinalCleanupTimeout, hooks.CloseMCP)
	add("停止异步任务", runtime.PhaseFinal, runtime.AsyncTaskTimeout, hooks.StopAsyncTasks)
	add("取消剩余任务", runtime.PhaseFinal, runtime.CancelRemainingTimeout, hooks.CancelRemaining)

	// Go 侧额外资源：统计、遥测、渲染、临时文件与数据库必须在剩余预算内关闭
	add("停止统计采集", runtime.PhaseFinal, runtime.FinalCleanupTimeout, hooks.StopStats)
	add("关闭遥测", runtime.PhaseFinal, runtime.FinalCleanupTimeout, hooks.CloseTelemetry)
	add("关闭 HTML 渲染", runtime.PhaseFinal, runtime.FinalCleanupTimeout, hooks.CloseHTMLRender)
	add("清理自更新临时文件", runtime.PhaseFinal, runtime.FinalCleanupTimeout, hooks.CleanupUpdateTemp)
	add("关闭数据库", runtime.PhaseFinal, runtime.FinalCleanupTimeout, hooks.CloseMemoryDB)

	return plan
}
