package desktoppet

// 桌宠配置读取，对齐 src/desktop_pet/config.py 的 desktop_pet 段

import (
	"os"
	"path/filepath"
	"strings"

	"maibot-go/internal/config/maibot"
	"maibot-go/internal/plugin_runtime"

	"github.com/pelletier/go-toml/v2"
)

// DesktopPetSettings 对齐 MaiBot DesktopPetConfig
type DesktopPetSettings struct {
	Enabled          bool
	ChatToolsEnabled bool
	SkinID           string
}

// DefaultDesktopPetSettings 返回 Python 侧默认值
func DefaultDesktopPetSettings() DesktopPetSettings {
	return DesktopPetSettings{SkinID: "builtin"}
}

// Normalize 补齐空皮肤 ID
func (s DesktopPetSettings) Normalize() DesktopPetSettings {
	s.SkinID = strings.TrimSpace(s.SkinID)
	if s.SkinID == "" {
		s.SkinID = "builtin"
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
				settings.SkinID = file.DesktopPet.SkinId
			}
		}
	}
	if envTruthy(EnvDesktopPet) {
		settings.Enabled = true
	}
	return settings.Normalize()
}
