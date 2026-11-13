package learners

import (
	"os"
	"path/filepath"
)

// reviewLogLocker 抽象审核日志文件锁，便于测试替换
type reviewLogLocker interface {
	LockShared() (func() error, error)
	LockExclusive() (func() error, error)
}

// fileReviewLogLocker 基于平台文件锁实现跨进程共享/独占锁
type fileReviewLogLocker struct {
	path string
}

func (l fileReviewLogLocker) LockShared() (func() error, error) {
	return lockReviewLogFile(l.path, false)
}

func (l fileReviewLogLocker) LockExclusive() (func() error, error) {
	return lockReviewLogFile(l.path, true)
}

// lockReviewLogFile 打开锁文件并按共享/独占模式阻塞加锁
func lockReviewLogFile(path string, exclusive bool) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	handle, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := lockReviewLogHandle(handle, exclusive); err != nil {
		_ = handle.Close()
		return nil, err
	}
	return func() error {
		unlockErr := unlockReviewLogHandle(handle)
		closeErr := handle.Close()
		if unlockErr != nil {
			return unlockErr
		}
		return closeErr
	}, nil
}

// withReviewLogLock 在锁内执行 fn，并合并解锁错误
func (s *ExpressionReviewStore) withReviewLogLock(shared bool, fn func() error) (err error) {
	locker := s.locker
	if locker == nil {
		locker = fileReviewLogLocker{path: s.path + ".lock"}
	}
	var unlock func() error
	if shared {
		unlock, err = locker.LockShared()
	} else {
		unlock, err = locker.LockExclusive()
	}
	if err != nil {
		return err
	}
	defer func() {
		if unlock == nil {
			return
		}
		if unlockErr := unlock(); unlockErr != nil && err == nil {
			err = unlockErr
		}
	}()
	return fn()
}
