//go:build windows

package agent

import (
	"os/exec"
	"strconv"
	"syscall"
)

func bridgeSysProcAttr() *syscall.SysProcAttr {
	const createNoWindow = 0x08000000
	return &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

func killBridgeProcessTree(pid int) error {
	const createNoWindow = 0x08000000
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}
