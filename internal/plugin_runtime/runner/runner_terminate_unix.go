//go:build !windows

package runner

import (
	"os"
	"syscall"
)

// terminateRunnerProcess 向 Runner 发送 SIGTERM，对齐 Python process.terminate()
func terminateRunnerProcess(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	return proc.Signal(syscall.SIGTERM)
}
