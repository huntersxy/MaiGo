//go:build windows

package utils

// Windows 磁盘剩余空间查询

import (
	"syscall"
	"unsafe"
)

var webImportGetDiskFreeSpaceEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

// webImportDiskFree 返回目标路径所在卷的可用字节数，失败时返回最大无符号数表示未知
func webImportDiskFree(path string) uint64 {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return ^uint64(0)
	}
	var freeBytesAvailable uint64
	var totalBytes uint64
	var totalFreeBytes uint64
	result, _, _ := webImportGetDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(pointer)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if result == 0 {
		return ^uint64(0)
	}
	return freeBytesAvailable
}
