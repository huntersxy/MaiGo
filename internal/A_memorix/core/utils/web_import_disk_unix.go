//go:build !windows

package utils

// 非 Windows 磁盘剩余空间查询

import "syscall"

// webImportDiskFree 返回目标路径所在文件系统的可用字节数，失败时返回最大无符号数表示未知
func webImportDiskFree(path string) uint64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return ^uint64(0)
	}
	return stat.Bavail * uint64(stat.Bsize)
}
