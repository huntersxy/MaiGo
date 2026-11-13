//go:build windows

package runner

import "os"

// terminateRunnerProcess Windows 下等价 Python process.terminate() 的 TerminateProcess
func terminateRunnerProcess(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	return proc.Kill()
}
