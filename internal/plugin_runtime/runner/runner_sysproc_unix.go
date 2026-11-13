//go:build !windows

package runner

import "syscall"

func runnerSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
