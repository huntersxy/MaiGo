//go:build !linux

package cli

import (
	"os"
	"runtime"
)

// isTerminalFile 非 Linux 平台判断 stdin 是否为交互式终端
//
// 对齐 Python sys.stdin.isatty() 的语义。字符设备判定在 Windows 上有坑：
// NUL（os.DevNull）也是字符设备，Mode()&os.ModeCharDevice 为真，会被误判成
// 可交互终端，于是 `bot_console` 在重定向输入或后台运行时仍尝试读 stdin。
// isatty 的实现会显式排除 NUL，这里同样处理。
func isTerminalFile(file *os.File) bool {
	if file == nil {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if runtime.GOOS == "windows" && isNullDevice(file) {
		return false
	}
	return true
}

// isNullDevice 判断是否为 Windows 的 NUL 设备
//
// NUL 的 Stat 结果与真实控制台一样是字符设备，只能靠路径识别。
func isNullDevice(file *os.File) bool {
	name := file.Name()
	return name == os.DevNull || name == "NUL" || name == "nul"
}
