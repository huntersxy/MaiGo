package memruntime

// A_memorix 数据目录跨进程独占写者锁，对齐 Python core/runtime/runtime_writer_lock.py
// 语义：同一数据目录同一时刻只允许一个活动 SDK 写者；锁文件首字节记录持有者 JSON

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// RuntimeWriterLock 对应 Python RuntimeWriterLock
type RuntimeWriterLock struct {
	path string

	mu     sync.Mutex
	handle *os.File
}

// NewRuntimeWriterLock 创建写者锁；path 通常为 <data_dir>/runtime_write.lock
func NewRuntimeWriterLock(path string) *RuntimeWriterLock {
	return &RuntimeWriterLock{path: path}
}

// Path 返回锁文件路径
func (l *RuntimeWriterLock) Path() string { return l.path }

// Held 返回当前实例是否持有锁
func (l *RuntimeWriterLock) Held() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.handle != nil
}

// Acquire 获取写者锁；已被其他写者持有时返回错误
func (l *RuntimeWriterLock) Acquire() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.handle != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o777); err != nil {
		return err
	}
	handle, err := os.OpenFile(l.path, os.O_RDWR|os.O_CREATE, 0o666)
	if err != nil {
		return err
	}
	// 与 Python 一致：锁文件为空时先写入一个空格，保证有可锁定的字节
	info, err := handle.Stat()
	if err != nil {
		handle.Close()
		return err
	}
	if info.Size() == 0 {
		if _, err := handle.Write([]byte(" ")); err != nil {
			handle.Close()
			return err
		}
	}
	if _, err := handle.Seek(0, 0); err != nil {
		handle.Close()
		return err
	}
	if err := lockHandle(handle); err != nil {
		owner := readLockOwner(handle)
		handle.Close()
		detail := ""
		if owner != "" {
			detail = "，当前持有者=" + owner
		}
		return fmt.Errorf("A_Memorix 数据目录已有活动写者: %s%s", l.path, detail)
	}
	payload, err := json.Marshal(map[string]any{"pid": os.Getpid(), "lock_path": l.path})
	if err == nil {
		_, err = handle.Seek(0, 0)
	}
	if err == nil {
		_, err = handle.Write(payload)
	}
	if err == nil {
		err = handle.Truncate(int64(len(payload)))
	}
	if err == nil {
		err = handle.Sync()
	}
	if err != nil {
		_ = unlockHandle(handle)
		handle.Close()
		return err
	}
	l.handle = handle
	return nil
}

// Release 释放写者锁；未持有时为空操作
func (l *RuntimeWriterLock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	handle := l.handle
	if handle == nil {
		return nil
	}
	l.handle = nil
	unlockErr := unlockHandle(handle)
	closeErr := handle.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func readLockOwner(handle *os.File) string {
	if _, err := handle.Seek(0, 0); err != nil {
		return ""
	}
	buf := make([]byte, 500)
	n, err := handle.Read(buf)
	if err != nil && n == 0 {
		return ""
	}
	return strings.TrimSpace(string(buf[:n]))
}

// ErrWriterLockNotHeld 供调用方判断重复释放
var ErrWriterLockNotHeld = errors.New("写者锁未被当前实例持有")
