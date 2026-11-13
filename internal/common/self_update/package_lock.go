package selfupdate

import (
	"os"
	"path/filepath"
)

// packageLock 对齐 ../MaiBot/src/common/package_update.py 的 update.guard：
// 同一工具环境同一时刻只允许一个包更新流程；持有期间拒绝其他实例启动更新

type packageLock struct {
	file *os.File
}

func acquirePackageUpdateLock(root, prefix string) (*packageLock, error) {
	lockDir := instanceLockDirForPrefix(prefix)
	if err := os.MkdirAll(lockDir, 0o777); err != nil {
		return nil, errorf("package_busy", "%s", BlockerMessage("package_busy"))
	}
	guard, err := lockFile(filepath.Join(lockDir, "update.guard"))
	if err != nil {
		return nil, errorf("package_busy", "%s", BlockerMessage("package_busy"))
	}
	ownPath := filepath.Join(lockDir, packageInstanceKey(root)+".lock")
	if err := ensureOtherInstancesStopped(lockDir, ownPath); err != nil {
		unlockFile(guard)
		guard.Close()
		return nil, err
	}
	return &packageLock{file: guard}, nil
}

func (l *packageLock) Release() {
	if l == nil || l.file == nil {
		return
	}
	_ = unlockFile(l.file)
	_ = l.file.Close()
	l.file = nil
}
