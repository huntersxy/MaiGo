package maibot

import (
	_ "embed"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

// 由 scripts/gen_maibot_config_types.py 的同类思路从 MaiBot 1.3.19
// src/config/config.py + official_configs.py 提取的 Pydantic 默认值
// 仅包含显式声明了 default/default_factory 的字段，required 字段保持缺省
//
//go:embed default_bot_config.toml
var defaultBotConfigTOML []byte

var (
	defaultBotConfigOnce sync.Once
	defaultBotConfig     BotConfigFile
	defaultBotConfigErr  error
)

// applyBotConfigDefaults 按 Python Config.from_dict 的语义补齐缺失键
// 只填充原始 TOML 中不存在的键；显式空列表/空表不会被默认值覆盖
func applyBotConfigDefaults(bot *BotConfigFile, raw map[string]any) error {
	if bot == nil || raw == nil {
		return nil
	}
	defaultBotConfigOnce.Do(func() {
		defaultBotConfigErr = toml.Unmarshal(defaultBotConfigTOML, &defaultBotConfig)
	})
	if defaultBotConfigErr != nil {
		return fmt.Errorf("解析内置 bot 配置默认值: %w", defaultBotConfigErr)
	}
	fillStructDefaults(reflect.ValueOf(bot).Elem(), reflect.ValueOf(defaultBotConfig), raw)
	return nil
}

func botConfigKeyTree(raw []byte) map[string]any {
	var data map[string]any
	if err := toml.Unmarshal(raw, &data); err != nil {
		return nil
	}
	delete(data, "inner")
	return data
}

func fillStructDefaults(dst, def reflect.Value, raw map[string]any) {
	if dst.Kind() != reflect.Struct || def.Kind() != reflect.Struct {
		return
	}
	typ := dst.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := tomlFieldName(field.Tag.Get("toml"))
		if name == "" {
			continue
		}
		dstField := dst.Field(i)
		defField := def.Field(i)
		if !dstField.CanSet() {
			continue
		}
		rawValue, present := raw[name]
		if dstField.Kind() == reflect.Struct {
			sub, _ := rawValue.(map[string]any)
			fillStructDefaults(dstField, defField, sub)
			continue
		}
		if present {
			continue
		}
		// 升级钩子可能把旧键搬到新位置，此时原始 TOML 里没有新键但字段已非零；
		// 只填充仍为零值的字段，避免默认值覆盖升级结果
		if !dstField.IsZero() {
			continue
		}
		if defField.IsValid() && defField.Type().AssignableTo(dstField.Type()) {
			dstField.Set(defField)
		}
	}
}

func tomlFieldName(tag string) string {
	if tag == "" || tag == "-" {
		return ""
	}
	if comma := strings.IndexByte(tag, ','); comma >= 0 {
		tag = tag[:comma]
	}
	return tag
}
