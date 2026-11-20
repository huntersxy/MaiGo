//go:build !windows

package host

import (
	"context"
	"net"
	"os/exec"
	"syscall"
)

// signalGracefulStop 向 Worker 发送 SIGTERM，由业务关闭流程处理
func signalGracefulStop(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return command.Process.Signal(syscall.SIGTERM)
}

// killProcessTree 结束整个进程组
func killProcessTree(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}

// setChildProcessAttrs 让 Worker 拥有独立进程组，便于整组回收
func setChildProcessAttrs(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// openNamedPipe Unix 平台没有命名管道
func openNamedPipe(ctx context.Context, address string) (net.Conn, error) {
	_ = ctx
	_ = address
	return nil, errNamedPipeUnsupported
}

var errNamedPipeUnsupported = &namedPipeUnsupportedError{}

type namedPipeUnsupportedError struct{}

func (*namedPipeUnsupportedError) Error() string { return "Unix 平台不支持 Windows 命名管道" }
