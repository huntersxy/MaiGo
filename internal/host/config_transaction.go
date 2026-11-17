package host

// 公共配置与 Bot 配置的文件事务；启动时回滚未完成的提交。
// 对齐 src/host/config_transaction.py。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RecoverConfigTransaction 回滚遗留的配置事务
func RecoverConfigTransaction(hostDir string) {
	journal := filepath.Join(hostDir, "model-config-transaction.json")
	raw, err := os.ReadFile(journal)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		return
	}
	var originals map[string]*string
	if err := json.Unmarshal(raw, &originals); err != nil || originals == nil {
		quarantineConfigJournal(journal)
		return
	}
	for filename, original := range originals {
		if !filepath.IsAbs(filename) {
			quarantineConfigJournal(journal)
			return
		}
		if original == nil {
			_ = os.Remove(filename)
			continue
		}
		_ = WritePrivate(filename, *original)
	}
	_ = os.Remove(journal)
}

func quarantineConfigJournal(journal string) {
	backup := fmt.Sprintf("%s.corrupt-%d", journal, time.Now().UnixNano())
	_ = os.Rename(journal, backup)
}

// CommitConfigFiles 在单个事务内写入多个配置文件
func CommitConfigFiles(hostDir string, files map[string]string) error {
	RecoverConfigTransaction(hostDir)
	journal := filepath.Join(hostDir, "model-config-transaction.json")
	originals := make(map[string]*string, len(files))
	for path := range files {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		raw, readErr := os.ReadFile(absolute)
		if readErr != nil {
			if !os.IsNotExist(readErr) {
				return readErr
			}
			originals[absolute] = nil
			continue
		}
		content := string(raw)
		originals[absolute] = &content
	}
	encoded, err := json.Marshal(originals)
	if err != nil {
		return err
	}
	if err := WritePrivate(journal, string(encoded)); err != nil {
		return err
	}
	for path, content := range files {
		if err := WritePrivate(path, content); err != nil {
			RecoverConfigTransaction(hostDir)
			return err
		}
	}
	return os.Remove(journal)
}

// WritePrivate 同目录写入并刷盘后原子替换，权限 0600
func WritePrivate(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.%d.tmp", filepath.Base(path), time.Now().UnixNano()))
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(content)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(temporary)
		return errors.Join(writeErr, syncErr, closeErr)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
