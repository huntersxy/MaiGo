// 已安装的官方适配器迁入内置配置布局，对齐 src/plugin_runtime/builtin_adapter_migration.py
package pluginruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	toml "github.com/pelletier/go-toml/v2"

	"MaiGo/internal/plugin_runtime/runner"
)

const (
	qqOfficialAdapterPluginID = "Mai-with-u.qq-adapter"
	telegramAdapterPluginID   = "maibot-team.telegram-adapter"
	telegramAdapterLegacyID   = "Sengoku.telegram-adapter"
	builtinAdapterBackupRoot  = ".builtin-backups"
)

// MigrateQQOfficialAdapter 迁移 QQ 官方适配器的外部安装
func MigrateQQOfficialAdapter(projectRoot, instanceRoot string) (string, error) {
	return migrateBuiltinAdapter(projectRoot, instanceRoot, qqOfficialAdapterPluginID, "qq_official_adapter", "QQ 官方适配器")
}

// MigrateTelegramAdapter 迁移 Telegram 适配器及曾使用个人作者 ID 的版本
func MigrateTelegramAdapter(projectRoot, instanceRoot string) (string, error) {
	return migrateBuiltinAdapter(projectRoot, instanceRoot, telegramAdapterPluginID, "telegram_adapter", "Telegram 适配器", telegramAdapterLegacyID)
}

func migrateBuiltinAdapter(projectRoot, instanceRoot, pluginID, directoryName, displayName string, legacyIDs ...string) (string, error) {
	builtinRoot := runner.DefaultBuiltinPluginDir(projectRoot)
	if strings.TrimSpace(builtinRoot) == "" {
		return "", nil
	}
	builtin := filepath.Join(builtinRoot, directoryName)
	if !isRegularFile(filepath.Join(builtin, "_manifest.json")) {
		return "", nil
	}
	root := runner.DefaultThirdPartyPluginDir(instanceRoot)
	if strings.TrimSpace(root) == "" {
		return "", nil
	}
	for _, oldID := range append([]string{pluginID}, legacyIDs...) {
		if err := migrateBuiltinLegacyData(root, instanceRoot, oldID, pluginID, directoryName, displayName); err != nil {
			return "", err
		}
	}
	for _, oldID := range legacyIDs {
		if err := migrateBuiltinLegacyData(filepath.Join(instanceRoot, "data", "plugins"), instanceRoot, oldID, pluginID, directoryName, displayName); err != nil {
			return "", err
		}
	}
	accepted := make(map[string]struct{}, 1+len(legacyIDs))
	for _, id := range append([]string{pluginID}, legacyIDs...) {
		accepted[strings.ToLower(strings.TrimSpace(id))] = struct{}{}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	candidates := make([]string, 0, 1)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || !entry.IsDir() {
			continue
		}
		directory := filepath.Join(root, entry.Name())
		manifest := filepath.Join(directory, "_manifest.json")
		if !isRegularFile(manifest) {
			continue
		}
		// 不让无关插件的无效清单阻止启动；真正迁移的目录必须严格校验边界
		raw, readErr := os.ReadFile(manifest)
		if readErr != nil {
			continue
		}
		var metadata struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &metadata) != nil {
			continue
		}
		if _, ok := accepted[strings.ToLower(strings.TrimSpace(metadata.ID))]; ok {
			if err := ensureContainedDirectory(root, directory); err != nil {
				return "", err
			}
			if isSymlink(manifest) {
				return "", fmt.Errorf("%s迁移目录包含链接或越界: %s", displayName, directory)
			}
			candidates = append(candidates, directory)
		}
	}
	if len(candidates) == 0 {
		return "", nil
	}
	if len(candidates) != 1 {
		return "", fmt.Errorf("%s存在多个旧安装，请先处理重复目录: %v", displayName, candidates)
	}

	source := candidates[0]
	oldConfig := filepath.Join(source, "config.toml")
	config := filepath.Join(builtin, "config.toml")
	if isSymlink(oldConfig) {
		return "", fmt.Errorf("%s配置不能是符号链接: %s", displayName, oldConfig)
	}
	if isRegularFile(oldConfig) && !pathExists(config) {
		raw, readErr := os.ReadFile(oldConfig)
		if readErr != nil {
			return "", readErr
		}
		var parsed map[string]any
		if err := toml.Unmarshal(raw, &parsed); err != nil {
			return "", fmt.Errorf("%s配置无效: %w", displayName, err)
		}
		if err := copyFileAtomic(oldConfig, config); err != nil {
			return "", err
		}
	}

	// 隐藏目录不参与插件扫描；完整保留旧安装、配置备份及可能的历史数据
	backup, err := backupPluginDirectory(root, source, directoryName)
	if err != nil {
		return "", err
	}
	return backup, nil
}

// migrateBuiltinLegacyData 迁移历史插件 ID 的数据，兼容旧 Docker 把数据挂在插件扫描目录的布局
func migrateBuiltinLegacyData(root, instanceRoot, pluginID, targetID, directoryName, displayName string) error {
	source := filepath.Join(root, pluginID)
	target := filepath.Join(instanceRoot, "data", "plugins", targetID)
	if !pathExists(source) {
		return nil
	}
	if samePath(source, target) {
		return nil
	}
	for _, marker := range []string{"_manifest.json", "plugin.py", ".git"} {
		if pathExists(filepath.Join(source, marker)) {
			return nil
		}
	}
	if isSymlink(source) {
		return fmt.Errorf("%s旧数据目录无效: %s", displayName, source)
	}
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%s旧数据目录无效: %s", displayName, source)
	}
	if err := ensureWithin(instanceRoot, target); err != nil {
		return fmt.Errorf("%s数据目录越界: %s", displayName, target)
	}
	if isSymlink(target) {
		return fmt.Errorf("%s数据目录越界: %s", displayName, target)
	}

	// 改名后的版本已写入当前数据库时，整套保留该库，避免混用两个库的游标或 SQLite 日志文件
	if pluginID == telegramAdapterLegacyID && targetID == telegramAdapterPluginID {
		oldDatabase := filepath.Join(source, "telegram.sqlite3")
		currentDatabase := filepath.Join(target, "telegram.sqlite3")
		if isRegularFile(oldDatabase) && isRegularFile(currentDatabase) {
			backup, backupErr := backupPluginDirectory(root, source, directoryName+"-data")
			if backupErr != nil {
				return backupErr
			}
			_ = backup
			return nil
		}
	}

	type entry struct {
		source      string
		destination string
		directory   bool
	}
	files := make([]entry, 0)
	err = filepath.WalkDir(source, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		relative, relErr := filepath.Rel(source, path)
		if relErr != nil {
			return relErr
		}
		destination := filepath.Join(target, relative)
		if item.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s旧数据包含链接: %s", displayName, path)
		}
		if err := ensureWithin(target, destination); err != nil {
			return fmt.Errorf("%s数据路径越界: %s", displayName, destination)
		}
		files = append(files, entry{source: path, destination: destination, directory: item.IsDir()})
		return nil
	})
	if err != nil {
		return err
	}
	// 先检查全部冲突；不猜测哪个映射更新，也不以空数据覆盖历史绑定
	for _, item := range files {
		destinationInfo, statErr := os.Stat(item.destination)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return statErr
		}
		if item.directory {
			if !destinationInfo.IsDir() {
				return fmt.Errorf("%s新旧数据冲突，请核对后再启动: %s -> %s", displayName, item.source, item.destination)
			}
			continue
		}
		if destinationInfo.IsDir() {
			return fmt.Errorf("%s新旧数据冲突，请核对后再启动: %s -> %s", displayName, item.source, item.destination)
		}
		sourceBytes, readErr := os.ReadFile(item.source)
		if readErr != nil {
			return readErr
		}
		destinationBytes, readErr := os.ReadFile(item.destination)
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(sourceBytes, destinationBytes) {
			return fmt.Errorf("%s新旧数据冲突，请核对后再启动: %s -> %s", displayName, item.source, item.destination)
		}
	}
	for _, item := range files {
		if item.directory {
			if err := os.MkdirAll(item.destination, 0o755); err != nil {
				return err
			}
			continue
		}
		if pathExists(item.destination) {
			continue
		}
		if err := copyFileAtomic(item.source, item.destination); err != nil {
			return err
		}
	}
	if _, err := backupPluginDirectory(root, source, directoryName+"-data"); err != nil {
		return err
	}
	return nil
}

func backupPluginDirectory(root, source, label string) (string, error) {
	backups := filepath.Join(root, builtinAdapterBackupRoot)
	if isSymlink(backups) {
		return "", fmt.Errorf("内置适配器备份目录包含链接或越界: %s", backups)
	}
	absoluteBackups, err := filepath.Abs(backups)
	if err != nil {
		return "", err
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if filepath.Dir(absoluteBackups) != filepath.Clean(absoluteRoot) {
		return "", fmt.Errorf("内置适配器备份目录包含链接或越界: %s", backups)
	}
	if err := os.MkdirAll(backups, 0o755); err != nil {
		return "", err
	}
	if err := ensureContainedDirectory(root, source); err != nil {
		return "", err
	}
	backup := filepath.Join(backups, label+"-"+uuid.NewString())
	if err := os.Rename(source, backup); err != nil {
		return "", err
	}
	return backup, nil
}

func ensureContainedDirectory(root, directory string) error {
	if isSymlink(directory) {
		return fmt.Errorf("内置适配器待备份目录越界: %s", directory)
	}
	absoluteDirectory, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if filepath.Dir(filepath.Clean(absoluteDirectory)) != filepath.Clean(absoluteRoot) {
		return fmt.Errorf("内置适配器待备份目录越界: %s", directory)
	}
	return nil
}

func ensureWithin(base, target string) error {
	absoluteBase, err := filepath.Abs(base)
	if err != nil {
		return err
	}
	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(filepath.Clean(absoluteBase), filepath.Clean(absoluteTarget))
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("路径越界: %s", target)
	}
	return nil
}

func copyFileAtomic(source, destination string) error {
	raw, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(destination), ".migration-"+uuid.NewString())
	if err := os.WriteFile(temporary, raw, info.Mode().Perm()); err != nil {
		return err
	}
	defer os.Remove(temporary)
	return os.Rename(temporary, destination)
}

func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func samePath(left, right string) bool {
	absoluteLeft, leftErr := filepath.Abs(left)
	absoluteRight, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && filepath.Clean(absoluteLeft) == filepath.Clean(absoluteRight)
}
