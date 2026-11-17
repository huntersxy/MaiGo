//go:build windows

package host

import (
	"net"
	"os/exec"
	"syscall"
	"unsafe"
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procGenerateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
)

// signalGracefulStop Windows 没有可用的单进程信号；用 CTRL_BREAK 通知进程组
func signalGracefulStop(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	_, _, err := procGenerateConsoleCtrlEvent.Call(1, uintptr(command.Process.Pid))
	if err != nil {
		return command.Process.Kill()
	}
	return nil
}

// killProcessTree 用 taskkill 结束整棵进程树
func killProcessTree(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", itoa(command.Process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	_ = kill.Run()
}

// setChildProcessAttrs 隐藏控制台并建立独立进程组
func setChildProcessAttrs(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x08000000,
		HideWindow:    true,
	}
}

// openNamedPipe 连接命名管道
func openNamedPipe(address string) (net.Conn, error) {
	handle, err := winioDialPipe(address)
	if err != nil {
		return nil, err
	}
	return handle, nil
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	buffer := make([]byte, 0, 12)
	for value > 0 {
		buffer = append([]byte{byte('0' + value%10)}, buffer...)
		value /= 10
	}
	if negative {
		buffer = append([]byte{'-'}, buffer...)
	}
	return string(buffer)
}

var _ = unsafe.Pointer(nil)
