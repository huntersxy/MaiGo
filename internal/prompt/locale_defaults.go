package prompt

import (
	"os"
	"strings"
)

// FallbackLocale 对齐 MaiBot loaders 的兜底语言；系统语言不可用或模板缺失时使用
const FallbackLocale = "zh-CN"

// DefaultLocale 对齐 MaiBot loaders.DEFAULT_LOCALE：
// 取操作系统语言，只有嵌入模板中存在该语言层时才采用，否则回退 FallbackLocale
var DefaultLocale = detectDefaultLocale()

// InitialLocale 对齐 MaiBot i18n.get_locale：
// 显式配置的 MAIBOT_LOCALE 优先，其次才是系统默认语言
func InitialLocale() string {
	if raw := strings.TrimSpace(os.Getenv("MAIBOT_LOCALE")); raw != "" {
		if normalized, err := NormalizeLocale(raw); err == nil {
			return normalized
		}
	}
	return DefaultLocale
}

func detectDefaultLocale() string {
	candidate := strings.TrimSpace(systemLocale())
	if candidate == "" {
		return FallbackLocale
	}
	normalized, err := NormalizeLocale(candidate)
	if err != nil {
		return FallbackLocale
	}
	if !embeddedLocaleAvailable(normalized) {
		return FallbackLocale
	}
	return normalized
}

// embeddedLocaleAvailable 判断嵌入模板目录里是否存在该 locale 层
func embeddedLocaleAvailable(locale string) bool {
	entries, err := embeddedTemplates.ReadDir("templates")
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() == locale {
			return true
		}
	}
	return false
}
