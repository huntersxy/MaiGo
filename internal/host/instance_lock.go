package host

// 实例锁：宿主在归档、克隆或数据导入前确认没有 Worker 正在使用目录。

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrInstanceBusy 目录正被另一个进程占用
var ErrInstanceBusy = errors.New("Bot 目录正被其他进程使用")

// InstanceLease 一次持有的实例锁
type InstanceLease struct {
	file *os.File
}

// LockInstance 阻塞等待目录锁；目录不存在或没有 temp 时视为空闲
func LockInstance(directory string) (*InstanceLease, error) {
	lockPath := filepath.Join(directory, "temp", "instance.lock")
	if info, err := os.Stat(filepath.Dir(lockPath)); err != nil || !info.IsDir() {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o777); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return nil, err
	}
	if err := lockInstanceHandle(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &InstanceLease{file: file}, nil
}

// Release 释放实例锁
func (l *InstanceLease) Release() {
	if l == nil || l.file == nil {
		return
	}
	_ = unlockInstanceHandle(l.file)
	_ = l.file.Close()
	l.file = nil
}

// LockFilePath 锁定指定锁文件；调用方负责创建父目录
func LockFilePath(path string) (*InstanceLease, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return nil, err
	}
	if err := lockInstanceHandle(file); err != nil {
		_ = file.Close()
		return nil, ErrInstanceBusy
	}
	return &InstanceLease{file: file}, nil
}
