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

// MigrateBuiltinKeywordReactionConfig 把旧主配置关键词反应写入内置插件；已有配置不覆盖
func MigrateBuiltinKeywordReactionConfig(projectRoot string, legacy *maibot.KeywordReactionConfig) error {
	builtinRoot := runner.DefaultBuiltinPluginDir(projectRoot)
	if strings.TrimSpace(builtinRoot) == "" || legacy == nil {
		return nil
	}
	rules := make([]any, 0, len(legacy.KeywordRules)+len(legacy.RegexRules))
	appendRule := func(item maibot.KeywordRuleConfig) {
		reaction := strings.TrimSpace(item.Reaction)
		keywords := nonEmptyStrings(item.Keywords)
		patterns := nonEmptyStrings(item.Regex)
		if reaction == "" || (len(keywords) == 0 && len(patterns) == 0) {
			return
		}
		rules = append(rules, map[string]any{
			"keywords": keywords, "regex": patterns, "reaction": reaction,
		})
	}
	for _, item := range legacy.KeywordRules {
		appendRule(item)
	}
	for _, item := range legacy.RegexRules {
		appendRule(item)
	}
	if len(rules) == 0 {
		return nil
	}
	target := filepath.Join(ResolveInstanceRoot(), "config", "builtin_plugins", "chinese_typo", "config.toml")
	document := map[string]any{}
	if raw, err := os.ReadFile(target); err == nil {
		if err := toml.Unmarshal(raw, &document); err != nil {
			return fmt.Errorf("解析内置错字插件配置失败: %w", err)
		}
		if _, exists := document["keyword_reaction"]; exists {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, exists := document["plugin"]; !exists {
		document["plugin"] = map[string]any{"enabled": true, "config_version": "1.0.0"}
	}
	document["keyword_reaction"] = map[string]any{"enabled": true, "rules": rules}
	content, err := toml.Marshal(document)
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
	return os.Rename(temporary, target)
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

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
