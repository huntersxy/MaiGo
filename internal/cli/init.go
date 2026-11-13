package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

var instanceDataDirs = []string{"data", "logs", "plugins", "temp"}

func runInit(directory string) (int, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return 1, fmt.Errorf("创建实例目录: %w", err)
	}
	cfgDir := filepath.Join(directory, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return 1, fmt.Errorf("创建 config 目录: %w", err)
	}

	botPath := filepath.Join(cfgDir, "bot_config.toml")
	modelPath := filepath.Join(cfgDir, "model_config.toml")
	if err := writeFileIfMissing(botPath, []byte(botConfigPlaceholder())); err != nil {
		return 1, err
	}
	if err := writeFileIfMissing(modelPath, []byte(modelConfigPlaceholder())); err != nil {
		return 1, err
	}

	for _, name := range instanceDataDirs {
		if err := os.MkdirAll(filepath.Join(directory, name), 0o755); err != nil {
			return 1, fmt.Errorf("创建 %s 目录: %w", name, err)
		}
	}

	fmt.Printf("MaiBot 实例已初始化：%s\n", directory)
	fmt.Printf("配置文件：%s、%s\n", botPath, modelPath)
	fmt.Println("填写模型配置后，执行 maibot run --directory <实例目录>")
	fmt.Println("默认使用实例目录下 data/MaiBot.db（SQLite）；可选 MAIBOT_DATABASE_DSN 覆盖，详见 .env.example")
	return 0, nil
}

func writeFileIfMissing(path string, content []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查 %s: %w", path, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("写入 %s: %w", path, err)
	}
	return nil
}
