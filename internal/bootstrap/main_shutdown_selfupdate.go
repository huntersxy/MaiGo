package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"MaiGo/internal/common/self_update"
)

// cleanupSelfUpdateTempFiles 只清理自更新状态目录中的原子写临时文件
// 不删除 pending.json、暂存二进制或备份目录，避免关停破坏尚未执行的更新请求
func cleanupSelfUpdateTempFiles(root string) error {
	dir := selfupdate.StateDir(strings.TrimSpace(root))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
