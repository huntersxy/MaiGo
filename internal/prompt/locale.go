package prompt

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	localePartPattern = regexp.MustCompile(`^[A-Za-z0-9]+$`)
	localeSeparators  = regexp.MustCompile(`[-_]+`)
)

// NormalizeLocale 对齐 MaiBot normalize_locale：下划线转连字符、语言小写、地区大写、四位脚本首字母大写
func NormalizeLocale(locale string) (string, error) {
	cleaned := strings.TrimSpace(strings.ReplaceAll(locale, "_", "-"))
	if cleaned == "" {
		return "", fmt.Errorf("Locale 不能为空")
	}
	parts := make([]string, 0, 3)
	for _, part := range localeSeparators.Split(cleaned, -1) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !localePartPattern.MatchString(part) {
			return "", fmt.Errorf("Locale 非法: %s", locale)
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("Locale 非法: %s", locale)
	}

	normalized := make([]string, len(parts))
	for i, part := range parts {
		switch {
		case i == 0:
			normalized[i] = strings.ToLower(part)
		case len(part) == 2:
			normalized[i] = strings.ToUpper(part)
		case len(part) == 4:
			normalized[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
		default:
			normalized[i] = part
		}
	}
	return strings.Join(normalized, "-"), nil
}

// DiscoverLocales 返回已加载的内置语言层（排序，默认语言层优先）
func (m *Manager) DiscoverLocales() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return discoverLocales(m.byLocale)
}

func discoverLocales(byLocale map[string]map[string]string) []string {
	if len(byLocale) == 0 {
		return nil
	}
	locales := make([]string, 0, len(byLocale))
	for locale := range byLocale {
		locales = append(locales, locale)
	}
	sort.Slice(locales, func(i, j int) bool {
		if locales[i] == DefaultLocale {
			return true
		}
		if locales[j] == DefaultLocale {
			return false
		}
		return locales[i] < locales[j]
	})
	return locales
}

// LocaleFallbacks 返回语言回退链：目标语言 -> 默认语言（去重）
func LocaleFallbacks(locale string) []string {
	normalized, err := NormalizeLocale(locale)
	if err != nil || normalized == "" {
		return []string{DefaultLocale}
	}
	if normalized == DefaultLocale {
		return []string{DefaultLocale}
	}
	return []string{normalized, DefaultLocale}
}
