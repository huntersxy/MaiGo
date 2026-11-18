//go:build linux

package system_probe

import (
	"os"
	"syscall"
)

// OwnerUID 用 os.Stat 读取文件属主 uid；这是探针唯一的 uid 来源。
func (osFileReader) OwnerUID(path string) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, ErrUnsupported
	}
	return int(stat.Uid), nil
}
