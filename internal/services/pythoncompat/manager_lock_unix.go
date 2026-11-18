//go:build !windows

package pythoncompat

import (
	"os"

	"golang.org/x/sys/unix"
)

// acquireStartLock 阻塞独占锁，保证同容器多 Worker 只有一个去 spawn。
func acquireStartLock(path string) (*os.File, error) {
	handle, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(handle.Fd()), unix.LOCK_EX); err != nil {
		_ = handle.Close()
		return nil, err
	}
	return handle, nil
}

func releaseStartLock(handle *os.File) {
	if handle == nil {
		return
	}
	_ = unix.Flock(int(handle.Fd()), unix.LOCK_UN)
	_ = handle.Close()
}
