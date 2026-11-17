//go:build !windows

package host

import (
	"os"
	"syscall"
)

// isDirectoryLink 识别符号链接
func isDirectoryLink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

// sameDevice 判断两个路径是否位于同一文件系统
func sameDevice(left, right os.FileInfo) bool {
	leftStat, leftOK := left.Sys().(*syscall.Stat_t)
	rightStat, rightOK := right.Sys().(*syscall.Stat_t)
	return leftOK && rightOK && leftStat.Dev == rightStat.Dev
}
