//go:build !windows

package agent

import "syscall"

func bridgeSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func killBridgeProcessTree(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}
