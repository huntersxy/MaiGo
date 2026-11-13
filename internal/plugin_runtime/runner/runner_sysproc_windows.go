//go:build windows

package runner

import "syscall"

func runnerSysProcAttr() *syscall.SysProcAttr {
	// 独立进程组，便于 Ctrl+C 时 Host 单独 terminate Runner
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
