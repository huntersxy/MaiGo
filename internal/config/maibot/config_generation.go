package maibot

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed default_model_config.toml
var defaultModelConfigTOML []byte

// ensureInstanceConfigFiles 对齐 Python generate_new_config_file：
// 配置文件缺失时按官方默认值生成，而不是直接启动失败
func ensureInstanceConfigFiles(root string) error {
	cfgDir := filepath.Join(root, configSubdir)
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return fmt.Errorf("创建配置目录: %w", err)
	}
	botPath := filepath.Join(cfgDir, botConfigFileName)
	if err := writeGeneratedConfigIfMissing(botPath, BotConfigTargetVersion, defaultBotConfigTOML); err != nil {
		return err
	}
	modelPath := filepath.Join(cfgDir, modelConfigFileName)
	if err := writeGeneratedConfigIfMissing(modelPath, ModelConfigTargetVersion, defaultModelConfigTOML); err != nil {
		return err
	}
	return nil
}

func writeGeneratedConfigIfMissing(path, version string, body []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查配置文件 %s: %w", path, err)
	}
	var builder strings.Builder
	builder.WriteString("# 本文件由 MaiGo 按 MaiBot 1.4.5 默认配置生成\n")
	builder.WriteString("[inner]\nversion = ")
	builder.WriteString(fmt.Sprintf("%q", version))
	builder.WriteString("\n\n")
	builder.Write(body)
	if !strings.HasSuffix(builder.String(), "\n") {
		builder.WriteString("\n")
	}
	temporary := path + ".tmp-" + fmt.Sprint(time.Now().UnixNano())
	if err := os.WriteFile(temporary, []byte(builder.String()), 0o600); err != nil {
		return fmt.Errorf("生成配置文件 %s: %w", path, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("生成配置文件 %s: %w", path, err)
	}
	return nil
}

// WriteDefaultBotConfig 在缺失时生成默认 bot_config.toml
func WriteDefaultBotConfig(path string) error {
	return writeGeneratedConfigIfMissing(path, BotConfigTargetVersion, defaultBotConfigTOML)
}

// WriteDefaultModelConfig 在缺失时生成默认 model_config.toml
func WriteDefaultModelConfig(path string) error {
	return writeGeneratedConfigIfMissing(path, ModelConfigTargetVersion, defaultModelConfigTOML)
}

// DefaultModelConfigTOML 返回内置默认 model_config.toml
func DefaultModelConfigTOML() []byte { return append([]byte(nil), defaultModelConfigTOML...) }

// DefaultBotConfigTOML 返回内置默认 bot_config.toml
func DefaultBotConfigTOML() []byte { return append([]byte(nil), defaultBotConfigTOML...) }
