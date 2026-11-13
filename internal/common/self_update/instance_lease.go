package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// InstanceLease 对齐 ../MaiBot/src/common/package_update.py 的 register_instance：
// 按实例根 sha256 命名 .maibot-instance-locks/<hash>.lock，进程退出即释放

type InstanceLease struct {
	Root    string
	LockDir string

	mu   sync.Mutex
	path string
	file *os.File
}

// NewInstanceLease 按当前运行环境推断锁目录并创建实例租约
func NewInstanceLease(root string) *InstanceLease {
	return NewInstanceLeaseInDir(root, DefaultInstanceLockDir(root))
}

// NewInstanceLeaseInDir 使用指定锁目录创建实例租约，便于嵌入方与测试注入
func NewInstanceLeaseInDir(root, lockDir string) *InstanceLease {
	if strings.TrimSpace(lockDir) == "" {
		lockDir = DefaultInstanceLockDir(root)
	}
	return &InstanceLease{
		Root:    root,
		LockDir: lockDir,
		path:    filepath.Join(lockDir, packageInstanceKey(root)+".lock"),
	}
}

// Path 返回实例锁文件路径
func (l *InstanceLease) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Held 返回当前实例是否持有租约
func (l *InstanceLease) Held() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file != nil
}

// Register 获取实例租约；同一工具环境已有其他实例或正在更新时返回错误
func (l *InstanceLease) Register() error {
	if l == nil {
		return errorf("package_busy", "%s", BlockerMessage("package_busy"))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		return errorf("package_busy", "这个实例正在运行，或工具环境正在更新，请等待完成")
	}
	if err := os.MkdirAll(l.LockDir, 0o777); err != nil {
		return errorf("package_busy", "%s", BlockerMessage("package_busy"))
	}
	guard, err := lockFile(filepath.Join(l.LockDir, "update.guard"))
	if err != nil {
		return errorf("package_busy", "%s", BlockerMessage("package_busy"))
	}
	if err := ensureOtherInstancesStopped(l.LockDir, l.path); err != nil {
		unlockFile(guard)
		guard.Close()
		return err
	}
	handle, err := lockFile(l.path)
	if err != nil {
		unlockFile(guard)
		guard.Close()
		return errorf("package_busy", "这个实例正在运行，或工具环境正在更新，请等待完成")
	}
	unlockFile(guard)
	guard.Close()
	l.file = handle
	return nil
}

// EnsureOtherInstancesStopped 扫描并锁定其他实例锁；任一被占用即报错
func (l *InstanceLease) EnsureOtherInstancesStopped() error {
	if l == nil {
		return errorf("other_instances_running", "%s", BlockerMessage("other_instances_running"))
	}
	return ensureOtherInstancesStopped(l.LockDir, l.path)
}

// Release 释放实例租约；未持有时为空操作
func (l *InstanceLease) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	handle := l.file
	l.file = nil
	unlockErr := unlockFile(handle)
	closeErr := handle.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

// DefaultInstanceLockDir 对齐 ToolEnvironment.locks：
// 优先 MAIBOT_INSTANCE_LOCK_DIR，其次 uv tool 环境父目录，最后实例根目录
func DefaultInstanceLockDir(root string) string {
	if value := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_LOCK_DIR")); value != "" {
		return value
	}
	if prefix := detectToolPrefix(root); prefix != "" {
		return instanceLockDirForPrefix(prefix)
	}
	return filepath.Join(root, ".maibot-instance-locks")
}

func detectToolPrefix(root string) string {
	candidates := make([]string, 0, 6)
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Dir(executable), filepath.Dir(filepath.Dir(executable)))
	}
	candidates = append(candidates,
		strings.TrimSpace(os.Getenv("VIRTUAL_ENV")),
		root,
		filepath.Join(root, ".venv"),
	)
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if pathExists(filepath.Join(candidate, "uv-receipt.toml")) {
			return candidate
		}
	}
	return ""
}

func instanceLockDirForPrefix(prefix string) string {
	return filepath.Join(filepath.Dir(strings.TrimRight(prefix, "\\/")), ".maibot-instance-locks")
}

func ensureOtherInstancesStopped(lockDir, ownPath string) error {
	entries, err := os.ReadDir(lockDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errorf("other_instances_running", "%s", BlockerMessage("other_instances_running"))
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".lock") {
			continue
		}
		path := filepath.Join(lockDir, name)
		if strings.EqualFold(path, ownPath) {
			continue
		}
		handle, err := lockFile(path)
		if err != nil {
			return errorf("other_instances_running", "%s", BlockerMessage("other_instances_running"))
		}
		unlockFile(handle)
		handle.Close()
	}
	return nil
}

func packageInstanceKey(root string) string {
	resolved := filepath.Clean(root)
	if absolute, err := filepath.Abs(resolved); err == nil {
		resolved = absolute
	}
	resolved = filepath.Clean(resolved)
	if runtime.GOOS == "windows" {
		resolved = strings.ToLower(resolved)
	}
	sum := sha256.Sum256([]byte(resolved))
	return hex.EncodeToString(sum[:])
}

func lockFile(path string) (*os.File, error) {
	handle, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o666)
	if err != nil {
		return nil, err
	}
	info, err := handle.Stat()
	if err != nil {
		handle.Close()
		return nil, err
	}
	if info.Size() == 0 {
		if _, err := handle.Write([]byte("0")); err != nil {
			handle.Close()
			return nil, err
		}
	}
	if _, err := handle.Seek(0, 0); err != nil {
		handle.Close()
		return nil, err
	}
	if err := lockHandle(handle); err != nil {
		handle.Close()
		return nil, err
	}
	return handle, nil
}
