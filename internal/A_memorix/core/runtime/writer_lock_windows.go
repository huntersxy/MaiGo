//go:build windows

package memruntime

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockHandle 对齐 msvcrt.locking(fd, LK_NBLCK, 1)：锁定文件首字节且不阻塞
func lockHandle(handle *os.File) error {
	overlapped := new(windows.Overlapped)
	return windows.LockFileEx(
		windows.Handle(handle.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, overlapped,
	)
}

// unlockHandle 对齐 msvcrt.locking(fd, LK_UNLCK, 1)
func unlockHandle(handle *os.File) error {
	overlapped := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(handle.Fd()), 0, 1, 0, overlapped)
}
