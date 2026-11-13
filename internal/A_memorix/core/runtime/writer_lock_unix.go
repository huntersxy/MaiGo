//go:build !windows

package memruntime

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockHandle 对齐 fcntl.flock(fd, LOCK_EX | LOCK_NB)
func lockHandle(handle *os.File) error {
	return unix.Flock(int(handle.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

// unlockHandle 对齐 fcntl.flock(fd, LOCK_UN)
func unlockHandle(handle *os.File) error {
	return unix.Flock(int(handle.Fd()), unix.LOCK_UN)
}
