//go:build !windows

package runtime

import (
	"os"
	"os/exec"
	"syscall"
)

func setChildProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func requestWorkerStop(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
