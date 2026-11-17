package host

// 实例配置生成：宿主创建 Bot 时复用配置生成器，对齐 Python src.host.initialize。

import (
	"fmt"
	"os"
	"path/filepath"

	maibotcfg "MaiGo/internal/config/maibot"
)

// writeDefaultInstanceConfigs 为工作区内新建的 Bot 生成默认配置
func writeDefaultInstanceConfigs(configDir string) error {
	if err := os.MkdirAll(configDir, 0o777); err != nil {
		return fmt.Errorf("创建配置目录: %w", err)
	}
	if err := maibotcfg.WriteDefaultBotConfig(filepath.Join(configDir, "bot_config.toml")); err != nil {
		return err
	}
	if err := maibotcfg.WriteDefaultModelConfig(filepath.Join(configDir, "model_config.toml")); err != nil {
		return err
	}
	return nil
}
