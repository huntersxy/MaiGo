//go:build !windows

package prompt

import (
	"os"
	"strings"
)

// systemLocale 按 POSIX 约定读取 LC_ALL / LC_MESSAGES / LANG
func systemLocale() string {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		if index := strings.IndexAny(raw, ".@"); index >= 0 {
			raw = raw[:index]
		}
		return raw
	}
	return ""
}
