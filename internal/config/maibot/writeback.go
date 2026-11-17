package maibot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// ConfigWritebackEnv 显式设为 0/false 时关闭升级写回，其余情况默认写回
// 对齐 MaiBot ConfigManager.initialize 的 load_config_from_file 写盘；Go 侧合并原文件注释（见 writeUpgradedConfigFile）
const ConfigWritebackEnv = "MAIBOT_CONFIG_WRITEBACK"

func configWritebackEnabled() bool {
	raw := strings.TrimSpace(os.Getenv(ConfigWritebackEnv))
	if raw == "" {
		return true
	}
	ok, err := strconv.ParseBool(raw)
	if err != nil {
		return raw != "0"
	}
	return ok
}

// maybeWriteBackUpgradedConfigs 在 inner.version、升级钩子或旧版迁移变更后写盘
// 默认开启，MAIBOT_CONFIG_WRITEBACK=0/false 可显式关闭；forceAll 仅供内部强制写回
func maybeWriteBackUpgradedConfigs(cfg *InstanceConfig, botVerBefore, modelVerBefore string, forceBot, forceAll bool) error {
	if cfg == nil {
		return nil
	}
	if !forceAll && !forceBot && !configWritebackEnabled() {
		return nil
	}
	var errs []string
	if cfg.BotInner.Version != botVerBefore || forceBot {
		if err := writeUpgradedBotConfig(cfg.BotConfigPath, cfg.BotInner, cfg.Bot); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if cfg.ModelInner.Version != modelVerBefore {
		if err := writeUpgradedModelConfig(cfg.ModelConfigPath, cfg.ModelInner, cfg.Model); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("配置升级写回: %s", strings.Join(errs, "; "))
	}
	return nil
}

func writeUpgradedBotConfig(path string, inner InnerMeta, bot BotConfigFile) error {
	doc := botConfigDocument{Inner: inner, BotConfigFile: bot}
	data, err := toml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("编码 %s: %w", filepath.Base(path), err)
	}
	return writeUpgradedConfigBytes(path, normalizeAMemorixImportForWrite(data))
}

func writeUpgradedModelConfig(path string, inner InnerMeta, model ModelConfigFile) error {
	doc := modelConfigDocument{Inner: inner, ModelConfigFile: model}
	return writeUpgradedConfigFile(path, doc)
}

// writeUpgradedConfigFile 用 go-toml/v2 重写，并尽量把原文件注释与未变更节合并保留
// 写前将现有文件移到 config/old/<stem>_<timestamp>.toml，对齐 MaiBot write_config_to_file
func writeUpgradedConfigFile(path string, doc any) error {
	data, err := toml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("编码 %s: %w", filepath.Base(path), err)
	}
	return writeUpgradedConfigBytes(path, data)
}

// writeUpgradedConfigBytes 写入已编码的配置并合并原注释
func writeUpgradedConfigBytes(path string, data []byte) error {
	path = filepath.Clean(path)
	original, _ := os.ReadFile(path)
	if err := backupConfigFile(path); err != nil {
		return fmt.Errorf("备份 %s: %w", filepath.Base(path), err)
	}
	data = mergeTOMLPreservingComments(original, data)
	// 先写唯一临时文件并刷盘再改名，避免并发写或中途失败破坏正式配置
	tmpPath := path + ".tmp-" + strings.ReplaceAll(fmt.Sprint(time.Now().UnixNano()), ":", "")
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("写入 %s.tmp: %w", filepath.Base(path), err)
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("写入 %s.tmp: %w", filepath.Base(path), errors.Join(writeErr, syncErr, closeErr))
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("替换 %s: %w", filepath.Base(path), err)
	}
	return nil
}

func backupConfigFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s 是目录", path)
	}
	backupRoot := filepath.Join(filepath.Dir(path), "old")
	if err := os.MkdirAll(backupRoot, 0o755); err != nil {
		return err
	}
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	backupName := fmt.Sprintf("%s_%s.toml", stem, time.Now().Format("20060102_150405.000000"))
	backupPath := filepath.Join(backupRoot, backupName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(backupPath, raw, 0o600)
}
