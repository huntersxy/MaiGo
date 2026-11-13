//go:build windows

package runtime

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func setChildProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func requestWorkerStop(p *os.Process) error {
	// 独立进程组内用 CTRL_BREAK 触发 Worker 的关闭 handler，对齐 Python process_runner
	// 发送失败不伪装成已通知：仍保留等待窗口，超时后强制结束
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(p.Pid))
}
