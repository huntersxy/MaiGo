//go:build windows

package pythoncompat

import (
	"os"

	"golang.org/x/sys/windows"
)

// acquireStartLock 阻塞独占锁，保证同容器多 Worker 只有一个去 spawn。
func acquireStartLock(path string) (*os.File, error) {
	handle, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(
		windows.Handle(handle.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, 1, 0, overlapped,
	); err != nil {
		_ = handle.Close()
		return nil, err
	}
	return handle, nil
}

func releaseStartLock(handle *os.File) {
	if handle == nil {
		return
	}
	_ = windows.UnlockFileEx(windows.Handle(handle.Fd()), 0, 1, 0, new(windows.Overlapped))
	_ = handle.Close()
}
