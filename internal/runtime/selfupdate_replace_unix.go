//go:build !windows

package runtime

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	temp := filepath.Join(dir, filepath.Base(currentPath)+".pending-"+randomHex(8))
	if err := copyFile(stagedPath, temp, info.Mode()); err != nil {
		return err
	}
	defer os.Remove(temp)

	oldPath := currentPath + ".old"
	_ = os.Remove(oldPath)
	if err := os.Rename(currentPath, oldPath); err != nil {
		if err2 := os.Rename(temp, currentPath); err2 == nil {
			_ = os.Remove(oldPath)
			return nil
		}
		return fmt.Errorf("runtime: 替换当前可执行文件失败: %w", err)
	}
	if err := os.Rename(temp, currentPath); err != nil {
		_ = os.Rename(oldPath, currentPath)
		return fmt.Errorf("runtime: 安装新可执行文件失败: %w", err)
	}
	_ = os.Remove(oldPath)
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode|0o111)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
