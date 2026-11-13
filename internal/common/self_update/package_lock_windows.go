//go:build windows

package selfupdate

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockHandle 对齐 msvcrt.locking(fd, LK_NBLCK, 1)
func lockHandle(handle *os.File) error {
	return windows.LockFileEx(
		windows.Handle(handle.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, new(windows.Overlapped),
	)
}

// unlockFile 释放锁并保留 Python 兼容的解锁语义
func unlockFile(handle *os.File) error {
	return windows.UnlockFileEx(windows.Handle(handle.Fd()), 0, 1, 0, new(windows.Overlapped))
}
