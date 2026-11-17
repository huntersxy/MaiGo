//go:build !windows

package host

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockInstanceHandle(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func unlockInstanceHandle(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
