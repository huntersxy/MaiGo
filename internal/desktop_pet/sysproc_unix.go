//go:build !windows

package desktoppet

import "syscall"

func petSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
