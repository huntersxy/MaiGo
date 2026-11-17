//go:build windows

package host

import (
	"os"

	"golang.org/x/sys/windows"
)

// isDirectoryLink 同时识别符号链接与目录联接
func isDirectoryLink(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attributes, err := windows.GetFileAttributes(pointer)
	if err != nil {
		return false
	}
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// sameDevice 判断两个路径是否位于同一文件系统
func sameDevice(left, right os.FileInfo) bool {
	return true
}
