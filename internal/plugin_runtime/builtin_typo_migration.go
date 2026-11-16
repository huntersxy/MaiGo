// 将旧主配置中的错别字设置迁入内置 chinese_typo 插件，对齐 src/config/builtin_typo_migration.py
package pluginruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	toml "github.com/pelletier/go-toml/v2"

	"MaiGo/internal/config/maibot"
	"MaiGo/internal/plugin_runtime/runner"
)

// MigrateBuiltinTypoConfig 首次迁移保留现有插件配置，并允许主配置写入失败后安全重试
func MigrateBuiltinTypoConfig(projectRoot string, legacy *maibot.ChineseTypoConfig, presentKeys map[string]struct{}) error {
	builtinRoot := runner.DefaultBuiltinPluginDir(projectRoot)
	if strings.TrimSpace(builtinRoot) == "" || legacy == nil {
		return nil
	}
	target := filepath.Join(ResolveInstanceRoot(), "config", "builtin_plugins", "chinese_typo", "config.toml")
	if pathExists(target) {
		return nil
	}
	enabled := true
	if _, exists := presentKeys["enable"]; exists {
		enabled = legacy.Enable
	}
	typo := map[string]any{}
	set := func(key string, value any) {
		if _, exists := presentKeys[key]; exists {
			typo[key] = value
		}
	}
	set("enable_correction_quote", legacy.EnableCorrectionQuote)
	set("correction_quote_probability", legacy.CorrectionQuoteProbability)
	set("error_rate", legacy.ErrorRate)
	set("min_freq", legacy.MinFreq)
	set("tone_error_rate", legacy.ToneErrorRate)
	set("word_replace_rate", legacy.WordReplaceRate)
	content, err := toml.Marshal(map[string]any{
		"plugin": map[string]any{"enabled": enabled, "config_version": "1.0.0"},
		"typo":   typo,
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(target), ".migration-"+uuid.NewString()+".toml")
	if err := os.WriteFile(temporary, content, 0o644); err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Link(temporary, target); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("发布内置错字插件配置失败: %w", err)
	}
	return nil
}
