package utils

// A_memorix 原子写入工具，对齐 Python core/utils/io.py
// 流程与 Python 一致：先写同目录的 <名字>.tmp，成功后 os.replace 原子替换目标文件；
// 任一步失败都删除临时文件并把原始错误抛给调用方

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AtomicTempPath 返回临时文件路径，对齐 path.with_suffix(path.suffix + ".tmp")
// 结果等价于把目标路径最后一个分量追加 ".tmp"：a.json -> a.json.tmp、a -> a.tmp、
// .bashrc -> .bashrc.tmp、dir/.. -> dir/...tmp；路径没有文件名时返回错误
func AtomicTempPath(filePath string) (string, error) {
	parent, name, err := splitAtomicPath(filePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, name+".tmp"), nil
}

// AtomicWrite 把 data 原子写入 filePath，简化的 atomic_write 用法
func AtomicWrite(filePath string, data []byte) error {
	return AtomicWriteFile(filePath, 0o666, func(file *os.File) error {
		_, err := file.Write(data)
		return err
	})
}

// AtomicWriteFile 对齐 atomic_write 上下文管理器
// 先创建父目录与临时文件，write 成功返回后 flush（Sync）并原子替换；失败时清理临时文件。
// perm 对齐 Python open() 的默认权限，实际权限仍受 umask 影响
func AtomicWriteFile(filePath string, perm os.FileMode, write func(file *os.File) error) error {
	tmpPath, err := AtomicTempPath(filePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o777); err != nil {
		return err
	}
	file, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if err := write(file); err != nil {
		_ = file.Close()
		removeIfExists(tmpPath)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		removeIfExists(tmpPath)
		return err
	}
	if err := file.Close(); err != nil {
		removeIfExists(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		removeIfExists(tmpPath)
		return err
	}
	return nil
}

// AtomicSavePath 对齐 atomic_save_path 上下文管理器
// save 拿到临时路径自行落盘，返回成功后若临时文件存在则原子替换目标；
// save 报错或替换失败都会删除临时文件
func AtomicSavePath(filePath string, save func(tmpPath string) error) error {
	tmpPath, err := AtomicTempPath(filePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o777); err != nil {
		return err
	}
	if err := save(tmpPath); err != nil {
		removeIfExists(tmpPath)
		return err
	}
	if _, statErr := os.Stat(tmpPath); statErr == nil {
		if err := os.Rename(tmpPath, filePath); err != nil {
			removeIfExists(tmpPath)
			return err
		}
	}
	return nil
}

// splitAtomicPath 复刻 pathlib 的父目录与文件名拆分：丢弃空分量与 "." 分量，
// ".." 按普通分量保留（与 Path("dir/..") .name == ".." 一致）
func splitAtomicPath(filePath string) (string, string, error) {
	volume := filepath.VolumeName(filePath)
	rest := filePath[len(volume):]
	rooted := strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, "\\")
	isSeparator := func(r rune) bool { return r == '/' || r == '\\' }
	rawParts := strings.FieldsFunc(rest, isSeparator)
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		if part == "." {
			continue
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "", "", fmt.Errorf("路径没有文件名: %s", filePath)
	}
	name := parts[len(parts)-1]
	parent := "."
	if len(parts) > 1 {
		parent = filepath.Join(parts[:len(parts)-1]...)
	}
	if volume != "" {
		if parent == "." {
			parent = volume + string(os.PathSeparator)
		} else {
			parent = volume + string(os.PathSeparator) + parent
		}
	} else if rooted {
		if parent == "." {
			parent = string(os.PathSeparator)
		} else {
			parent = string(os.PathSeparator) + parent
		}
	}
	return parent, name, nil
}

func removeIfExists(path string) {
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(path)
	}
}
