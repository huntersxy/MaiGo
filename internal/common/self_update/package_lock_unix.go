//go:build !windows

package selfupdate

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockHandle 对齐 fcntl.flock(fd, LOCK_EX | LOCK_NB)
func lockHandle(handle *os.File) error {
	return unix.Flock(int(handle.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

// unlockFile 释放 flock 锁
func unlockFile(handle *os.File) error {
	return unix.Flock(int(handle.Fd()), unix.LOCK_UN)
}
