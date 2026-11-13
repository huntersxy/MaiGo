package runner

import (
	"context"
	"os"
	"time"
)

// Child 已拉起的 Python Runner 子进程句柄
type Child = child

// SpawnProcess 拉起 Python Runner 子进程
func SpawnProcess(ctx context.Context, opts StartOptions, state *State) (*Child, error) {
	return spawnRunnerProcess(ctx, opts, state)
}

// StopChild 停止子进程：先排空通知，再按 timings 终止
func StopChild(child *Child, group Group, timings ShutdownTimings) {
	stopRunnerChild(child, group, timings)
}

// GroupOrDefault 归一 Runner 分组，空值回落到第三方
func GroupOrDefault(group Group) Group {
	return runnerGroupOrDefault(group)
}

// Child 返回子进程句柄，未启动时为 nil
func (s *State) Child() *Child {
	if s == nil {
		return nil
	}
	return s.child
}

// SetChild 设置子进程句柄
func (s *State) SetChild(child *Child) {
	if s == nil {
		return
	}
	s.child = child
}

// HasExited 子进程是否已退出
func (c *Child) HasExited() bool {
	if c == nil {
		return false
	}
	return c.hasExited()
}

// NewChildForTest 构造测试用子进程句柄；done 关闭表示进程已退出
//
// 仅供父包测试构造「子进程仍在运行 / 已退出」两种状态，
// 避免把 child 的内部字段（cmd/process）暴露成公开 API。
func NewChildForTest(done chan struct{}) *Child {
	return &child{done: done}
}

// KillProcess 强杀子进程，供父包测试提前结束进程
func (c *Child) KillProcess() error {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	return c.cmd.Process.Kill()
}

// HasProcess 是否已绑定真实进程
func (c *Child) HasProcess() bool {
	return c != nil && c.cmd != nil && c.cmd.Process != nil
}

// ProcessID 返回子进程 pid，未绑定时为 0
func (c *Child) ProcessID() int {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

// ExitError 返回进程退出错误
func (c *Child) ExitError() error {
	return c.exitError()
}

// ProcessState 返回进程退出后的等待状态，未退出时为 nil
func (c *Child) ProcessState() *os.ProcessState {
	if c == nil || c.cmd == nil {
		return nil
	}
	return c.cmd.ProcessState
}

// WaitExit 等待进程退出，超时返回 false
func (c *Child) WaitExit(timeout time.Duration) bool {
	return waitRunnerExit(c, timeout)
}
