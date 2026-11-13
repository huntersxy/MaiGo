//go:build !windows

package learners

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockReviewLogHandle Unix flock：共享读锁 LOCK_SH，写锁 LOCK_EX
func lockReviewLogHandle(handle *os.File, exclusive bool) error {
	op := unix.LOCK_SH
	if exclusive {
		op = unix.LOCK_EX
	}
	return unix.Flock(int(handle.Fd()), op)
}

func unlockReviewLogHandle(handle *os.File) error {
	return unix.Flock(int(handle.Fd()), unix.LOCK_UN)
}
