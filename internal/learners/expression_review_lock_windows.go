//go:build windows

package learners

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockReviewLogHandle Windows 字节范围锁：共享锁不加独占标志，写锁独占
func lockReviewLogHandle(handle *os.File, exclusive bool) error {
	flags := uint32(0)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	return windows.LockFileEx(
		windows.Handle(handle.Fd()),
		flags,
		0, 1, 0, new(windows.Overlapped),
	)
}

func unlockReviewLogHandle(handle *os.File) error {
	return windows.UnlockFileEx(windows.Handle(handle.Fd()), 0, 1, 0, new(windows.Overlapped))
}
