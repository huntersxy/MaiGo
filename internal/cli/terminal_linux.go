//go:build linux

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// isTerminalFile 判断文件是否绑定交互式终端
// /dev/null 也是字符设备但不是终端，必须用 ioctl 而非 ModeCharDevice
func isTerminalFile(file *os.File) bool {
	if file == nil {
		return false
	}
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS)
	return err == nil
}
