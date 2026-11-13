package computer

// 电脑配置加载：对齐 ComputerConfig 默认值与 bot_config.toml 的 [computer] 表

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	envInstanceRoot = "MAIBOT_INSTANCE_DIR"
	botConfigSubdir = "config"
	botConfigName   = "bot_config.toml"
)

// Config 对齐 ComputerConfig；字段名与默认值按 config.py
type Config struct {
	Enabled           bool
	Networking        bool
	MemoryMB          int
	StartupTimeout    float64
	RequestTimeout    float64
	ScreenshotMaxEdge int
}

// DefaultConfig 返回 MaiBot ComputerConfig 默认值
func DefaultConfig() Config {
	return Config{
		Enabled:           false,
		Networking:        true,
		MemoryMB:          4096,
		StartupTimeout:    120,
		RequestTimeout:    60,
		ScreenshotMaxEdge: 1600,
	}
}

// InstanceRoot 对齐 get_instance_root：优先 MAIBOT_INSTANCE_DIR，否则进程工作目录
func InstanceRoot() string {
	if root := strings.TrimSpace(os.Getenv(envInstanceRoot)); root != "" {
		if absolute, err := filepath.Abs(root); err == nil {
			return absolute
		}
		return filepath.Clean(root)
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

// LoadConfig 读取实例 bot_config.toml 的 [computer]；文件不存在时返回默认配置
func LoadConfig(instanceRoot string) (Config, error) {
	cfg := DefaultConfig()
	root := strings.TrimSpace(instanceRoot)
	if root == "" {
		root = InstanceRoot()
	}
	path := filepath.Join(root, botConfigSubdir, botConfigName)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("读取电脑配置失败: %w", err)
	}
	document := map[string]any{}
	if err := toml.Unmarshal(raw, &document); err != nil {
		return cfg, fmt.Errorf("解析电脑配置失败: %w", err)
	}
	section, _ := document["computer"].(map[string]any)
	if section == nil {
		return cfg, nil
	}
	if value, ok := configBool(section, "enabled"); ok {
		cfg.Enabled = value
	}
	if value, ok := configBool(section, "networking"); ok {
		cfg.Networking = value
	}
	if value, ok := configInt(section, "memory_mb"); ok {
		cfg.MemoryMB = value
	}
	if value, ok := configFloat(section, "startup_timeout"); ok {
		cfg.StartupTimeout = value
	}
	if value, ok := configFloat(section, "request_timeout"); ok {
		cfg.RequestTimeout = value
	}
	if value, ok := configInt(section, "screenshot_max_edge"); ok {
		cfg.ScreenshotMaxEdge = value
	}
	if err := cfg.Validate(); err != nil {
		return DefaultConfig(), err
	}
	return cfg, nil
}

// Validate 对齐 pydantic 的 ge/gt 约束
func (c Config) Validate() error {
	if c.MemoryMB < 2048 {
		return fmt.Errorf("computer.memory_mb 必须大于等于 2048")
	}
	if c.StartupTimeout <= 0 {
		return fmt.Errorf("computer.startup_timeout 必须大于 0")
	}
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("computer.request_timeout 必须大于 0")
	}
	if c.ScreenshotMaxEdge < 0 {
		return fmt.Errorf("computer.screenshot_max_edge 必须大于等于 0")
	}
	return nil
}

func configBool(section map[string]any, key string) (bool, bool) {
	value, ok := section[key]
	if !ok {
		return false, false
	}
	typed, ok := value.(bool)
	return typed, ok
}

func configInt(section map[string]any, key string) (int, bool) {
	value, ok := section[key]
	if !ok {
		return 0, false
	}
	number, ok := numberValue(value)
	if !ok || number != float64(int(number)) {
		return 0, false
	}
	return int(number), true
}

func configFloat(section map[string]any, key string) (float64, bool) {
	value, ok := section[key]
	if !ok {
		return 0, false
	}
	return numberValue(value)
}
