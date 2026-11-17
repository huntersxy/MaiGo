package host

// Bot 模板克隆和可恢复归档，对齐 src/host/bot_operations.py。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type archiveRecord struct {
	BotID            string  `json:"bot_id"`
	Name             string  `json:"name"`
	Directory        string  `json:"directory"`
	Owned            bool    `json:"owned"`
	CreatedAt        string  `json:"created_at"`
	Initialization   string  `json:"initialization"`
	Error            string  `json:"error"`
	DesiredState     string  `json:"desired_state"`
	RestartPolicy    string  `json:"restart_policy"`
	ArchiveDirectory *string `json:"archive_directory"`
}

// ArchiveBot 移入 host/archives/<id>/instance 并删除登记；外部目录只注销不动文件
func ArchiveBot(workspace Workspace, bot BotRecord) (map[string]any, error) {
	archive := filepath.Join(workspace.HostDirectory(), "archives", bot.BotID)
	if err := os.MkdirAll(archive, 0o777); err != nil {
		return nil, err
	}
	target := filepath.Join(archive, "instance")
	if bot.Owned {
		expected := filepath.Join(workspace.Root, "bots", bot.BotID)
		if !sameFilePath(bot.Directory, expected) || !pathContains(workspace.Root, bot.Directory) {
			return nil, errors.New("Bot 目录不在预期的宿主管理路径，拒绝归档移动")
		}
	}
	if isDirectoryLink(bot.Directory) {
		return nil, errors.New("Bot 目录不能是链接或目录联接")
	}
	lease, err := LockInstance(bot.Directory)
	if err != nil {
		return nil, err
	}
	lease.Release()
	record := archiveRecord{
		BotID: bot.BotID, Name: bot.Name, Directory: bot.Directory, Owned: bot.Owned,
		CreatedAt: bot.CreatedAt, Initialization: bot.Initialization, Error: bot.Error,
		DesiredState: bot.DesiredState, RestartPolicy: bot.RestartPolicy,
	}
	if bot.Owned {
		record.ArchiveDirectory = &target
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	journal := filepath.Join(archive, "record.json")
	temporary := filepath.Join(archive, "record.tmp")
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(temporary, journal); err != nil {
		_ = os.Remove(temporary)
		return nil, err
	}
	if bot.Owned {
		if err := os.Rename(bot.Directory, target); err != nil {
			return nil, err
		}
	}
	if err := workspace.Registry.RemoveBot(bot.BotID); err != nil {
		return nil, err
	}
	directory := bot.Directory
	if bot.Owned {
		directory = target
	}
	return map[string]any{"archived": true, "record": journal, "directory": directory}, nil
}

// RecoverArchives 完成“目录已归档但登记未删”的中断恢复
func RecoverArchives(workspace Workspace) error {
	bots, err := workspace.Registry.ListBots()
	if err != nil {
		return err
	}
	for _, bot := range bots {
		archive := filepath.Join(workspace.HostDirectory(), "archives", bot.BotID)
		if !bot.Owned {
			continue
		}
		if _, err := os.Stat(bot.Directory); !os.IsNotExist(err) {
			continue
		}
		if record, err := os.Stat(filepath.Join(archive, "record.json")); err != nil || record.IsDir() {
			continue
		}
		if info, err := os.Stat(filepath.Join(archive, "instance")); err != nil || !info.IsDir() {
			continue
		}
		if err := workspace.Registry.RemoveBot(bot.BotID); err != nil {
			return err
		}
	}
	return nil
}

var cloneExcludedDirs = map[string]struct{}{".git": {}, "__pycache__": {}, ".venv": {}, "old": {}}

// CloneBot 复制源实例的 config 与 plugins，不复制仓库历史、缓存和配置备份
func CloneBot(workspace Workspace, source BotRecord, name string) (BotRecord, error) {
	validated, err := validateBotName(name)
	if err != nil {
		return BotRecord{}, err
	}
	if source.Initialization != "initialized" {
		return BotRecord{}, errors.New("源 Bot 尚未初始化完成")
	}
	lease, err := LockInstance(source.Directory)
	if err != nil {
		return BotRecord{}, err
	}
	defer lease.Release()
	identifier := newBotID()
	destination := filepath.Join(workspace.Root, "bots", identifier)
	if !pathContains(workspace.Root, destination) {
		return BotRecord{}, errors.New("克隆目标超出当前工作区")
	}
	if _, err := workspace.Registry.AddBotWithID(identifier, validated, destination, true, "initializing"); err != nil {
		return BotRecord{}, err
	}
	if _, err := workspace.Registry.SetDesiredState(identifier, "stopped"); err != nil {
		return BotRecord{}, err
	}
	if err := os.MkdirAll(destination, 0o777); err != nil {
		_, _ = workspace.Registry.SetInitialization(identifier, "failed", "克隆失败："+err.Error())
		return BotRecord{}, err
	}
	for _, section := range []string{"config", "plugins"} {
		sourcePath := filepath.Join(source.Directory, section)
		if _, err := os.Stat(sourcePath); err != nil {
			if section == "config" {
				_, _ = workspace.Registry.SetInitialization(identifier, "failed", "克隆失败："+err.Error())
				return BotRecord{}, err
			}
			continue
		}
		if err := copyCloneTree(sourcePath, filepath.Join(destination, section)); err != nil {
			_, _ = workspace.Registry.SetInitialization(identifier, "failed", "克隆失败："+err.Error())
			return BotRecord{}, err
		}
	}
	for _, section := range []string{"data", "logs", "temp"} {
		if err := os.MkdirAll(filepath.Join(destination, section), 0o777); err != nil {
			_, _ = workspace.Registry.SetInitialization(identifier, "failed", "克隆失败："+err.Error())
			return BotRecord{}, err
		}
	}
	return workspace.Registry.SetInitialization(identifier, "initialized", "")
}

func copyCloneTree(source, target string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.MkdirAll(target, 0o777)
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		for _, part := range parts {
			if _, excluded := cloneExcludedDirs[part]; excluded {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if isDirectoryLink(path) {
			return fmt.Errorf("模板目录包含链接，无法安全复制：%s", path)
		}
		destination := filepath.Join(target, relative)
		if info.IsDir() {
			return os.MkdirAll(destination, 0o777)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o777); err != nil {
			return err
		}
		sourceFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer sourceFile.Close()
		targetFile, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(targetFile, sourceFile)
		closeErr := targetFile.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		modTime := info.ModTime()
		return os.Chtimes(destination, time.Now(), modTime)
	})
}
