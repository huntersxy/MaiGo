//go:build windows

package prompt

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32Locale               = windows.NewLazySystemDLL("kernel32.dll")
	procGetUserDefaultLocaleName = kernel32Locale.NewProc("GetUserDefaultLocaleName")
)

// systemLocale 读取 Windows 用户默认语言（等价 Python locale.getlocale 的系统来源）
func systemLocale() string {
	buffer := make([]uint16, 85)
	length, _, _ := procGetUserDefaultLocaleName.Call(
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
	)
	if length == 0 {
		return ""
	}
	return windows.UTF16ToString(buffer[:length-1])
}
