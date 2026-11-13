package desktoppet

import (
	"os"
	"path/filepath"
	"strings"

	"maibot-go/internal/plugin_runtime"
)

const (
	defaultPetDataSubdir    = "data/plugins/sengokucola.desktop-pet"
	defaultPetRuntimeSubdir = "temp/desktop_pet"
	relativePetScript       = "src/desktop_pet/pet_window.py"
)

// SpawnEnv 从环境变量解析的桌宠子进程路径
type SpawnEnv struct {
	PythonBin    string
	ProjectRoot  string
	InstanceRoot string
}

// EnvConfig 桌宠开关、配置段与 Python/项目根解析结果
type EnvConfig struct {
	Enabled          bool
	ChatToolsEnabled bool
	SkinID           string
	SpawnEnv
}

func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// LoadConfigFromEnv 读取 desktop_pet 配置段、MAIBOT_DESKTOP_PET 与 MAIBOT_PLUGIN_* 默认值
func LoadConfigFromEnv() EnvConfig {
	sc := pluginruntime.LoadSpawnConfigFromEnv()
	root := sc.ProjectRoot
	settings := LoadDesktopPetSettings()
	return EnvConfig{
		Enabled:          settings.Enabled,
		ChatToolsEnabled: settings.ChatToolsEnabled,
		SkinID:           settings.SkinID,
		SpawnEnv: SpawnEnv{
			PythonBin:    sc.PythonBin,
			ProjectRoot:  root,
			InstanceRoot: resolveInstanceRoot(root),
		},
	}
}

func resolveInstanceRoot(projectRoot string) string {
	if v := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")); v != "" {
		return filepath.Clean(v)
	}
	return strings.TrimSpace(projectRoot)
}

func petScriptPath(projectRoot string) string {
	return filepath.Join(projectRoot, filepath.FromSlash(relativePetScript))
}

func petDataDir(instanceRoot string) string {
	return filepath.Join(instanceRoot, filepath.FromSlash(defaultPetDataSubdir))
}

func petRuntimeDir(instanceRoot string) string {
	return filepath.Join(instanceRoot, filepath.FromSlash(defaultPetRuntimeSubdir))
}
