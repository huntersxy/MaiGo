//go:build windows

package pluginmarket

import (
	"os/exec"
	"strconv"
	"syscall"
)

// gitSysProcAttr Windows 没有进程组语义，取消时改用 taskkill /T
func gitSysProcAttr() *syscall.SysProcAttr {
	const createNoWindow = 0x08000000
	return &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

// killGitProcessTree 用 taskkill /T 连同子进程一起终止
func killGitProcessTree(pid int) error {
	const createNoWindow = 0x08000000
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	return cmd.Run()
}
