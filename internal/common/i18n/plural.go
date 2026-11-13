package i18n

import (
	"strconv"
	"strings"
)

// pluralCategories 对齐 babel Locale.plural_form 在本仓库支持语言上的取值：
// zh/ja/ko 只有 other；en 为 one/other
func pluralCategories(locale string) []string {
	base := strings.ToLower(strings.SplitN(NormalizeLocale(locale), "-", 2)[0])
	switch base {
	case "en":
		return []string{"one", "other"}
	default:
		return []string{"other"}
	}
}

// selectPluralCategory 对齐 babel Locale.plural_form
func selectPluralCategory(locale string, count float64) string {
	base := strings.ToLower(strings.SplitN(NormalizeLocale(locale), "-", 2)[0])
	if base == "en" {
		// babel en：i = 1 and v = 0，即数值等于 1（含 1.0）
		if count == 1 {
			return "one"
		}
		return "other"
	}
	return "other"
}

// formatCount 对齐 Python str(count)
func formatCount(count float64) string {
	return strconv.FormatFloat(count, 'f', -1, 64)
}
