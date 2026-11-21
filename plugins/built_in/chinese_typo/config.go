package chinesetypo

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"MaiGo/internal/platform_io"
)

const PluginID = "builtin.chinese-typo"
const Directory = "chinese_typo"

//go:embed _manifest.json
var manifestJSON []byte

//go:embed config_schema.json
var schemaJSON []byte

var configSchema = sync.OnceValue(Schema)

func Manifest() map[string]any {
	manifest := decodeAsset(manifestJSON)
	manifest["dependencies"] = []any{}
	return manifest
}

func Schema() map[string]any { return decodeAsset(schemaJSON) }

func decodeAsset(raw []byte) map[string]any {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		panic(err)
	}
	return object
}

type Config struct {
	Enabled        bool
	typo           typoSettings
	reactionActive bool
	rules          []keywordRule
}

type typoSettings struct {
	enabled, quoteCorrection           bool
	mode                               string
	dictionaryChance, quoteChance      float64
	charChance, toneChance, wordChance float64
	minFrequency                       int
}

type keywordRule struct {
	keywords, patterns []string
	reaction           string
}

func ParseConfig(input map[string]any) (Config, map[string]any, error) {
	if len(input) != 0 {
		section, _ := input["plugin"].(map[string]any)
		version, ok := section["config_version"].(string)
		if !ok || strings.TrimSpace(version) == "" {
			return Config{}, nil, fmt.Errorf("插件配置文件必须提供 plugin.config_version 版本号")
		}
	}
	sections, err := platformio.NormalizeNativeConfig(input, configSchema())
	if err != nil {
		return Config{}, nil, err
	}
	plugin, typo, reactions := sections["plugin"], sections["typo"], sections["keyword_reaction"]
	config := Config{
		Enabled: plugin["enabled"].(bool),
		typo: typoSettings{
			enabled: typo["enabled"].(bool), mode: typo["mode"].(string),
			quoteCorrection: typo["enable_correction_quote"].(bool), quoteChance: typo["correction_quote_probability"].(float64),
			dictionaryChance: typo["dictionary_probability"].(float64), minFrequency: typo["min_freq"].(int),
			charChance: typo["error_rate"].(float64), toneChance: typo["tone_error_rate"].(float64), wordChance: typo["word_replace_rate"].(float64),
		},
		reactionActive: reactions["enabled"].(bool),
	}
	for _, rule := range reactions["rules"].([]map[string]any) {
		config.rules = append(config.rules, keywordRule{rule["keywords"].([]string), rule["regex"].([]string), rule["reaction"].(string)})
	}
	normalized := make(map[string]any, len(sections))
	for name, section := range sections {
		normalized[name] = section
	}
	return config, normalized, nil
}
