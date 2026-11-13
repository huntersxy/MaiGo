package host

// 本文件把 runner 子包的进程管理能力桥接回父包。
//
// 对齐 Python src/plugin_runtime/runner/。本包只做类型别名与薄转发，
// Host 的编排方法（StartRunner/StopRunner/StopAllRunners）留在父包，
// 因为它们要操作 h.runners 与 RPCServer——这两者属于宿主状态与传输层。

import (
	"context"

	"maibot-go/internal/plugin_runtime/runner"
)

// RunnerGroup Runner 分组
type RunnerGroup = runner.Group

const (
	// RunnerGroupBuiltin 内置插件分组
	RunnerGroupBuiltin = runner.GroupBuiltin
	// RunnerGroupThirdParty 第三方插件分组
	RunnerGroupThirdParty = runner.GroupThirdParty
)

// StartRunnerOptions 启动 Runner 子进程的参数
type StartRunnerOptions = runner.StartOptions

// runnerChild 已拉起的 Runner 子进程句柄
type runnerChild = runner.Child

// spawnRunnerProcess 拉起 Python Runner 子进程
func spawnRunnerProcess(ctx context.Context, opts StartRunnerOptions, state *RunnerState) (*runnerChild, error) {
	return runner.SpawnProcess(ctx, opts, state.runnerState())
}

// stopRunnerChild 停止 Runner 子进程
func stopRunnerChild(child *runnerChild, group RunnerGroup, timings runnerShutdownTimings) {
	runner.StopChild(child, group, timings)
}

// runnerGroupOrDefault 归一 Runner 分组，空值回落到第三方
func runnerGroupOrDefault(group RunnerGroup) RunnerGroup {
	return runner.GroupOrDefault(group)
}
