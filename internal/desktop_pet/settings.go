package desktoppet

// 桌宠配置读取，对齐 src/desktop_pet/config.py 的 desktop_pet 段

import (
	"os"
	"path/filepath"
	"strings"

	"MaiGo/internal/config/maibot"
	"MaiGo/internal/plugin_runtime"

	"github.com/pelletier/go-toml/v2"
)

// DesktopPetSettings 对齐 MaiBot DesktopPetConfig
type DesktopPetSettings struct {
	Enabled                      bool
	Mode                         string
	ChatToolsEnabled             bool
	ShowCurrentTask              bool
	SkinID                       string
	MessageSound                 string
	ProactiveWakeEnabled         bool
	ProactiveWakeIntervalMinutes int
	ScreenReadEnabled            bool
	ScreenReadIntervalSeconds    int
	ScreenSampleIntervalSeconds  float64
	ScreenChangeThreshold        float64
	ExplorationEnabled           bool
}

// DefaultDesktopPetSettings 返回 Python 侧默认值
func DefaultDesktopPetSettings() DesktopPetSettings {
	return DesktopPetSettings{
		Mode: "local", SkinID: "builtin", MessageSound: "wood", ProactiveWakeIntervalMinutes: 30,
		ScreenReadIntervalSeconds: 10, ScreenSampleIntervalSeconds: 2, ScreenChangeThreshold: 0.015,
	}
}

// Normalize 补齐空皮肤 ID 与主动思考间隔
func (s DesktopPetSettings) Normalize() DesktopPetSettings {
	s.SkinID = strings.TrimSpace(s.SkinID)
	if s.SkinID == "" {
		s.SkinID = "builtin"
	}
	s.MessageSound = strings.TrimSpace(s.MessageSound)
	if s.MessageSound == "soft" {
		s.MessageSound = "wood"
	}
	switch s.MessageSound {
	case "wood", "bubbles", "bell", "water", "off":
	default:
		s.MessageSound = "wood"
	}
	if s.ProactiveWakeIntervalMinutes <= 0 {
		s.ProactiveWakeIntervalMinutes = 30
	}
	if s.ProactiveWakeIntervalMinutes > 1440 {
		s.ProactiveWakeIntervalMinutes = 1440
	}
	if s.Mode != "remote" {
		s.Mode = "local"
	}
	if s.ScreenReadIntervalSeconds <= 0 {
		s.ScreenReadIntervalSeconds = 10
	}
	if s.ScreenSampleIntervalSeconds <= 0 {
		s.ScreenSampleIntervalSeconds = 2
	}
	if s.ScreenChangeThreshold <= 0 {
		s.ScreenChangeThreshold = 0.015
	}
	return s
}

// desktopPetInstanceRoot 按 Python get_instance_root 解析实例根目录
func desktopPetInstanceRoot() string {
	if value := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")); value != "" {
		return filepath.Clean(value)
	}
	return resolveInstanceRoot(pluginruntime.LoadSpawnConfigFromEnv().ProjectRoot)
}

// LoadDesktopPetSettings 读取 bot_config.toml 的 [desktop_pet] 段
//
// 环境变量 MAIBOT_DESKTOP_PET 仍可强制启用，兼容既有 Go 启动方式
func LoadDesktopPetSettings() DesktopPetSettings {
	settings := DefaultDesktopPetSettings()
	root := desktopPetInstanceRoot()
	if root != "" {
		path := filepath.Join(root, "config", "bot_config.toml")
		if raw, err := os.ReadFile(path); err == nil {
			var file maibot.BotConfigFile
			if err := toml.Unmarshal(raw, &file); err == nil {
				settings.Enabled = file.DesktopPet.Enabled
				settings.ChatToolsEnabled = file.DesktopPet.ChatToolsEnabled
				settings.ShowCurrentTask = file.DesktopPet.ShowCurrentTask
				settings.SkinID = file.DesktopPet.SkinId
				settings.MessageSound = file.DesktopPet.MessageSound
				settings.Mode = file.DesktopPet.Mode
				settings.ProactiveWakeEnabled = file.DesktopPet.ProactiveWakeEnabled
				settings.ProactiveWakeIntervalMinutes = file.DesktopPet.ProactiveWakeIntervalMinutes
				settings.ScreenReadEnabled = file.DesktopPet.ScreenReadEnabled
				settings.ScreenReadIntervalSeconds = file.DesktopPet.ScreenReadIntervalSeconds
				settings.ScreenSampleIntervalSeconds = file.DesktopPet.ScreenSampleIntervalSeconds
				settings.ScreenChangeThreshold = file.DesktopPet.ScreenChangeThreshold
				settings.ExplorationEnabled = file.Experimental.DesktopPetExplorationEnabled
			}
		}
	}
	if envTruthy(EnvDesktopPet) {
		settings.Enabled = true
	}
	return settings.Normalize()
}
