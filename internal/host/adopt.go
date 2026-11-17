package host

// 手动迁移旧实例：register 只登记路径；move 在同一文件系统内重命名进工作区。
// 对齐 src/host/adopt.py，每一步落盘后再继续，中断后重跑同一命令即可续做。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const adoptJournalDir = "migrations"

type adoptJournal struct {
	Mode   string `json:"mode"`
	BotID  string `json:"bot_id"`
	Name   string `json:"name,omitempty"`
	Source string `json:"source"`
	Target string `json:"target,omitempty"`
	Step   string `json:"step"`
}

func adoptJournalPath(workspace Workspace, botID string) string {
	return filepath.Join(workspace.HostDirectory(), adoptJournalDir, botID+".json")
}

func writeAdoptJournal(path string, journal adoptJournal) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.%d.tmp", filepath.Base(path), time.Now().UnixNano()))
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func readAdoptJournal(path string) (adoptJournal, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return adoptJournal{}, err
	}
	var journal adoptJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		return adoptJournal{}, fmt.Errorf("迁移日志无效: %s", path)
	}
	if journal.Mode != "register" && journal.Mode != "move" || !botIDPattern.MatchString(journal.BotID) {
		return adoptJournal{}, fmt.Errorf("迁移日志无效: %s", path)
	}
	return journal, nil
}

func pendingAdoptMove(workspace Workspace, source string) (adoptJournal, string, bool) {
	directory := filepath.Join(workspace.HostDirectory(), adoptJournalDir)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return adoptJournal{}, "", false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		journal, err := readAdoptJournal(path)
		if err != nil {
			continue
		}
		if journal.Mode == "move" && journal.Step != "done" && sameFilePath(journal.Source, source) {
			return journal, path, true
		}
	}
	return adoptJournal{}, "", false
}

// Adopt 登记或移动一个已存在的实例
func Adopt(workspace Workspace, source, mode, name string) (BotRecord, error) {
	resolved, err := filepath.Abs(source)
	if err != nil {
		return BotRecord{}, err
	}
	resolved = filepath.Clean(resolved)
	if mode == "" {
		mode = "register"
	}
	if mode != "register" && mode != "move" {
		return BotRecord{}, fmt.Errorf("未知迁移模式: %s", mode)
	}
	if mode == "move" {
		if journal, path, ok := pendingAdoptMove(workspace, resolved); ok {
			return continueAdoptMove(workspace, journal, path)
		}
	}
	if err := validateAdoptSource(workspace, resolved); err != nil {
		return BotRecord{}, err
	}
	botName := strings.TrimSpace(name)
	if botName == "" {
		botName = filepath.Base(resolved)
	}
	if mode == "register" {
		if err := CopyBuiltinPluginConfigs(resolved); err != nil {
			return BotRecord{}, err
		}
		record, err := workspace.Registry.AddBot(botName, resolved, false, "initialized")
		if err != nil {
			return BotRecord{}, err
		}
		_ = writeAdoptJournal(adoptJournalPath(workspace, record.BotID), adoptJournal{
			Mode: "register", BotID: record.BotID, Name: record.Name, Source: resolved, Step: "done",
		})
		return record, nil
	}
	botsRoot := filepath.Join(workspace.Root, "bots")
	if err := os.MkdirAll(botsRoot, 0o777); err != nil {
		return BotRecord{}, err
	}
	sourceInfo, err := os.Stat(resolved)
	if err != nil {
		return BotRecord{}, err
	}
	targetRootInfo, err := os.Stat(botsRoot)
	if err != nil {
		return BotRecord{}, err
	}
	if !sameDevice(sourceInfo, targetRootInfo) {
		return BotRecord{}, errors.New("move 只支持同一文件系统内的重命名；跨盘请自行复制后使用 --mode register")
	}
	botID := newBotID()
	journal := adoptJournal{
		Mode: "move", BotID: botID, Name: botName, Source: resolved,
		Target: filepath.Join(botsRoot, botID), Step: "prepared",
	}
	path := adoptJournalPath(workspace, botID)
	if err := writeAdoptJournal(path, journal); err != nil {
		return BotRecord{}, err
	}
	return continueAdoptMove(workspace, journal, path)
}

func continueAdoptMove(workspace Workspace, journal adoptJournal, path string) (BotRecord, error) {
	source, target := journal.Source, journal.Target
	if journal.Step == "prepared" {
		lease, err := LockInstance(source)
		if err != nil {
			return BotRecord{}, err
		}
		lease.Release()
		if err := os.Rename(source, target); err != nil {
			return BotRecord{}, err
		}
		journal.Step = "moved"
		if err := writeAdoptJournal(path, journal); err != nil {
			return BotRecord{}, err
		}
	}
	if journal.Step == "moved" {
		if err := os.MkdirAll(source, 0o777); err != nil {
			return BotRecord{}, err
		}
		if err := os.WriteFile(filepath.Join(source, MovedMarker), []byte(target), 0o666); err != nil {
			return BotRecord{}, err
		}
		journal.Step = "marked"
		if err := writeAdoptJournal(path, journal); err != nil {
			return BotRecord{}, err
		}
	}
	if journal.Step == "marked" {
		if err := CopyBuiltinPluginConfigs(target); err != nil {
			return BotRecord{}, err
		}
		if _, err := workspace.Registry.AddBotWithID(journal.BotID, journal.Name, target, true, "initialized"); err != nil {
			return BotRecord{}, err
		}
		journal.Step = "done"
		if err := writeAdoptJournal(path, journal); err != nil {
			return BotRecord{}, err
		}
	}
	return workspace.Registry.GetBot(journal.BotID)
}

func validateAdoptSource(workspace Workspace, source string) error {
	if target := MovedTarget(source); target != "" {
		return fmt.Errorf("此目录已迁移到：%s", target)
	}
	configPath := filepath.Join(source, "config", "bot_config.toml")
	if info, err := os.Stat(configPath); err != nil || info.IsDir() {
		return fmt.Errorf("不是 MaiBot 实例目录，缺少 %s", configPath)
	}
	if pathContains(workspace.Root, source) || pathContains(source, workspace.Root) {
		return errors.New("旧实例目录与工作区不能互相包含")
	}
	return nil
}

// UndoAdopt 撤销一次 adopt：register 删除登记；move 把目录重命名回原位置
func UndoAdopt(workspace Workspace, botID string) error {
	path := adoptJournalPath(workspace, botID)
	if _, err := os.Stat(path); err != nil {
		return errors.New("只能撤销通过 adopt 加入的 Bot")
	}
	journal, err := readAdoptJournal(path)
	if err != nil {
		return err
	}
	if journal.Step != "done" {
		return errors.New("迁移尚未完成；请先重新执行同一条 adopt 命令完成迁移，再撤销")
	}
	record, err := workspace.Registry.GetBot(botID)
	if err != nil {
		return err
	}
	lease, err := LockInstance(record.Directory)
	if err != nil {
		return err
	}
	lease.Release()
	if journal.Mode == "move" {
		source := journal.Source
		marker := filepath.Join(source, MovedMarker)
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return err
		}
		if entries, err := os.ReadDir(source); err == nil {
			if len(entries) > 0 {
				return fmt.Errorf("原目录已有新文件，拒绝覆盖：%s", source)
			}
			if err := os.Remove(source); err != nil {
				return err
			}
		}
		if err := os.Rename(record.Directory, source); err != nil {
			return err
		}
	}
	if err := workspace.Registry.RemoveBot(botID); err != nil {
		return err
	}
	return os.Remove(path)
}
