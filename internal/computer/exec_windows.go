//go:build windows

package computer

import (
	"os/exec"
	"syscall"
)

// hideWindow 避免 wsb.exe 与查看窗口在宿主终端弹出控制台
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
