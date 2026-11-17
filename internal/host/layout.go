package host

// 工作区布局：首次启动判定新旧用户，之后以注册表为准，对齐 src/host/layout.py。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// MovedMarker adopt --mode move 留下的旧目录标记
const MovedMarker = "MOVED_TO_WORKSPACE.txt"

// Workspace 宿主工作区
type Workspace struct {
	Root                string
	Registry            *Registry
	Config              HostConfig
	WebUIMigrationError string
}

// HostDirectory 宿主目录
func (w Workspace) HostDirectory() string { return filepath.Join(w.Root, "host") }

// MovedTarget adopt --mode move 后旧目录记录的新位置
func MovedTarget(directory string) string {
	raw, err := os.ReadFile(filepath.Join(directory, MovedMarker))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// OpenWorkspace 打开或初始化工作区
func OpenWorkspace(root string) (Workspace, error) {
	resolved, err := filepath.Abs(root)
	if err != nil {
		return Workspace{}, err
	}
	resolved = filepath.Clean(resolved)
	if target := MovedTarget(resolved); target != "" {
		return Workspace{}, fmt.Errorf("此目录的 Bot 已移动到宿主工作区，请在新位置启动：%s", target)
	}
	if err := os.MkdirAll(filepath.Join(resolved, "host"), 0o777); err != nil {
		return Workspace{}, err
	}
	registry := NewRegistry(resolved)
	if !registry.Exists() {
		if _, err := os.Stat(filepath.Join(resolved, "config", "bot_config.toml")); err == nil {
			if err := adoptInPlace(resolved, registry); err != nil {
				return Workspace{}, err
			}
		} else {
			if err := createFresh(resolved, registry); err != nil {
				return Workspace{}, err
			}
		}
	}
	config, err := LoadHostConfig(resolved)
	if err != nil {
		return Workspace{}, err
	}
	return Workspace{Root: resolved, Registry: registry, Config: config}, nil
}

// adoptInPlace 旧用户：工作区根目录原地登记为 Bot
func adoptInPlace(root string, registry *Registry) error {
	botConfig := filepath.Join(root, "config", "bot_config.toml")
	if _, err := os.Stat(hostConfigPath(root)); os.IsNotExist(err) {
		if err := SaveHostConfig(root, HostConfigFromBot(botConfig)); err != nil {
			return err
		}
	}
	raw, err := os.ReadFile(botConfig)
	if err != nil {
		return err
	}
	var payload struct {
		Bot struct {
			Nickname string `toml:"nickname"`
		} `toml:"bot"`
	}
	_ = toml.Unmarshal(raw, &payload)
	name := strings.TrimSpace(payload.Bot.Nickname)
	if name == "" {
		name = "MaiBot"
	}
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}
	if err := CopyBuiltinPluginConfigs(root); err != nil {
		return err
	}
	if err := registry.Create(); err != nil {
		return err
	}
	_, err = registry.AddBot(name, root, false, "initialized")
	return err
}

// createFresh 新用户：在 bots/<id>/ 下创建第一个 Bot
func createFresh(root string, registry *Registry) error {
	if err := os.MkdirAll(root, 0o777); err != nil {
		return err
	}
	if _, err := os.Stat(hostConfigPath(root)); os.IsNotExist(err) {
		if err := SaveHostConfig(root, DefaultHostConfig()); err != nil {
			return err
		}
	}
	if err := registry.Create(); err != nil {
		return err
	}
	config, err := LoadHostConfig(root)
	if err != nil {
		return err
	}
	_, err = CreateBot(Workspace{Root: root, Registry: registry, Config: config}, "MaiBot")
	return err
}

// BotEnvironment 在某个 Bot 的实例目录中运行程序代码的环境
func BotEnvironment(directory string) []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env)+2)
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case RuntimeContextEnv, HostControlEndpointEnv, "MAIBOT_RUNTIME_MODE", "MAIBOT_WORKER_PROCESS", "MAIBOT_PACKAGE_RUNNER":
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, InstanceDirEnv+"="+directory, "PYTHONIOENCODING=utf-8")
}

// InstanceDirEnv 实例目录环境变量
const InstanceDirEnv = "MAIBOT_INSTANCE_DIR"

// CreateBot 先登记再初始化；失败时保留记录和目录
func CreateBot(workspace Workspace, name string) (BotRecord, error) {
	record, err := workspace.Registry.AddBot(name, filepath.Join(workspace.Root, "bots", "pending"), true, "initializing")
	if err != nil {
		return BotRecord{}, err
	}
	directory := filepath.Join(workspace.Root, "bots", record.BotID)
	if _, err := workspace.Registry.SetDirectory(record.BotID, directory, true); err != nil {
		return record, err
	}
	if err := os.MkdirAll(directory, 0o777); err != nil {
		return workspace.Registry.SetInitialization(record.BotID, "failed", err.Error())
	}
	if err := InitializeInstance(directory); err != nil {
		return workspace.Registry.SetInitialization(record.BotID, "failed", "InitializeError: "+err.Error())
	}
	return workspace.Registry.SetInitialization(record.BotID, "initialized", "")
}

// InitializeInstance 生成缺失的配置与运行目录，对齐 Python src.host.initialize
func InitializeInstance(directory string) error {
	configDir := filepath.Join(directory, "config")
	if err := os.MkdirAll(configDir, 0o777); err != nil {
		return err
	}
	if err := writeDefaultConfigIfMissing(configDir); err != nil {
		return err
	}
	for _, name := range []string{"data", "logs", "plugins", "temp"} {
		if err := os.MkdirAll(filepath.Join(directory, name), 0o777); err != nil {
			return err
		}
	}
	return nil
}

// ValidateWorkspaceBot 校验 Bot 目录是否已初始化配置
func ValidateWorkspaceBot(directory string) error {
	for _, name := range []string{"bot_config.toml", "model_config.toml"} {
		path := filepath.Join(directory, "config", name)
		if info, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("Bot 实例缺少配置文件：%s", path)
			}
			return err
		} else if info.IsDir() {
			return fmt.Errorf("Bot 配置文件不是普通文件：%s", path)
		}
	}
	return nil
}

// ErrBotNeedsUpgradeConfirmation 0.x 升级未确认
var ErrBotNeedsUpgradeConfirmation = errors.New("Bot 需要确认 0.x 升级")

// writeDefaultConfigIfMissing 生成缺失的 bot_config / model_config，复用实例生成器
func writeDefaultConfigIfMissing(configDir string) error {
	return writeDefaultInstanceConfigs(configDir)
}

// CopyBuiltinPluginConfigs 只迁入旧部署自身的内置插件配置
func CopyBuiltinPluginConfigs(directory string) error {
	sourceRoot := filepath.Join(directory, "src", "plugins", "built_in")
	entries, err := os.ReadDir(sourceRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		source := filepath.Join(sourceRoot, entry.Name(), "config.toml")
		if _, err := os.Stat(source); err != nil {
			continue
		}
		target := filepath.Join(directory, "config", "builtin_plugins", entry.Name(), "config.toml")
		if _, err := os.Stat(target); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
			return err
		}
		raw, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, raw, 0o666); err != nil {
			return err
		}
	}
	return nil
}
