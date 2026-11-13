//go:build windows

package utils

import "golang.org/x/sys/windows"

func setSockReuseAddr(fd uintptr) error {
	return windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
}
