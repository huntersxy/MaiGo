//go:build !windows

package pluginmarket

import "syscall"

// gitSysProcAttr 让 git 子进程独占进程组，便于取消时整组终止
func gitSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// killGitProcessTree 终止 git 及其子进程（ssh、credential helper 等）
func killGitProcessTree(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}
