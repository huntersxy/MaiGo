//go:build windows

package runtime

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procReplaceFileW   = modKernel32.NewProc("ReplaceFileW")
	replIgnoreMergeErr = uintptr(0x00000002)
)

func replaceExecutable(stagedPath, currentPath string) error {
	stagedPath = filepath.Clean(stagedPath)
	currentPath = filepath.Clean(currentPath)

	info, err := os.Stat(stagedPath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("runtime: 待安装路径是目录: %s", stagedPath)
	}

	dir := filepath.Dir(currentPath)
	pending := filepath.Join(dir, filepath.Base(currentPath)+".pending-"+randomHex(8))
	if err := copyFile(stagedPath, pending, info.Mode()); err != nil {
		return err
	}

	var lastErr error
	for attempt := range 8 {
		if err := replaceFileWindows(currentPath, pending); err == nil {
			_ = os.Remove(pending)
			return nil
		} else {
			lastErr = err
			if !isSharingViolation(err) || attempt == 7 {
				break
			}
			time.Sleep(time.Duration(10*(1<<attempt)) * time.Millisecond)
		}
	}
	_ = os.Remove(pending)

	if lastErr == nil {
		return fmt.Errorf("runtime: 替换可执行文件失败")
	}
	// ReplaceFile 在部分锁文件场景仍失败时，尝试 .old 重命名链
	if err := replaceExecutableRename(pending, currentPath); err != nil {
		return lastErr
	}
	_ = os.Remove(pending)
	return nil
}

func replaceFileWindows(target, source string) error {
	tPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	sPtr, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	r0, _, e1 := procReplaceFileW.Call(
		uintptr(unsafe.Pointer(tPtr)),
		uintptr(unsafe.Pointer(sPtr)),
		0,
		replIgnoreMergeErr,
		0,
		0,
	)
	if r0 == 0 {
		if e1 != nil && e1 != windows.ERROR_SUCCESS {
			return e1
		}
		return windows.GetLastError()
	}
	return nil
}

func replaceExecutableRename(pending, currentPath string) error {
	oldPath := currentPath + ".old"
	_ = os.Remove(oldPath)
	if err := os.Rename(currentPath, oldPath); err != nil {
		return err
	}
	if err := os.Rename(pending, currentPath); err != nil {
		_ = os.Rename(oldPath, currentPath)
		return err
	}
	_ = os.Remove(oldPath)
	return nil
}

func isSharingViolation(err error) bool {
	var errno windows.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case windows.ERROR_SHARING_VIOLATION, windows.ERROR_ACCESS_DENIED, windows.ERROR_USER_MAPPED_FILE:
		return true
	default:
		return false
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	perm := mode & os.ModePerm
	if perm == 0 {
		perm = 0o755
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
