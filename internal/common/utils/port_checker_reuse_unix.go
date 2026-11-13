//go:build unix

package utils

import "golang.org/x/sys/unix"

func setSockReuseAddr(fd uintptr) error {
	return unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
}
