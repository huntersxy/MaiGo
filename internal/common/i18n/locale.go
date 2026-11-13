package i18n

import (
	"fmt"
	"os"
	"strings"
)

const defaultLocale = "zh-CN"

// NormalizeLocale 将 MaiBot 风格 locale 规范为 BCP47 标签
// 对齐 loaders.normalize_locale：首段小写，两字母地区大写，四字母 script 首字母大写，其余段保持原样
func NormalizeLocale(locale string) string {
	cleaned := strings.ReplaceAll(strings.TrimSpace(locale), "_", "-")
	if cleaned == "" {
		return defaultLocale
	}
	parts := strings.Split(cleaned, "-")
	normalized := make([]string, 0, len(parts))
	for index, part := range parts {
		if part == "" {
			continue
		}
		switch {
		case index == 0:
			normalized = append(normalized, strings.ToLower(part))
		case len(part) == 2:
			normalized = append(normalized, strings.ToUpper(part))
		case len(part) == 4:
			normalized = append(normalized, strings.ToUpper(part[:1])+strings.ToLower(part[1:]))
		default:
			normalized = append(normalized, part)
		}
	}
	if len(normalized) == 0 {
		return defaultLocale
	}
	return strings.Join(normalized, "-")
}

// DefaultLocale 返回默认语言
func DefaultLocale() string {
	if v := strings.TrimSpace(os.Getenv("MAIBOT_LOCALE")); v != "" {
		return NormalizeLocale(v)
	}
	return defaultLocale
}

// SupportedLocales 当前嵌入的语言列表
func SupportedLocales() []string {
	return []string{"zh-CN", "en-US", "ja-JP", "ko"}
}

// CatalogFiles 每个 locale 嵌入的 JSON 文件名（字母序，与 MaiBot iter_locale_files 一致）
func CatalogFiles() []string {
	return []string{"config.json", "core.json", "prompts.json", "startup.json"}
}

func catalogPath(locale, file string) string {
	return fmt.Sprintf("locales/%s/%s", NormalizeLocale(locale), file)
}
