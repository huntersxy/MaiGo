package i18n

import (
	"os"
	"strings"
	"sync"
)

// 包级单例，对齐 Python src/common/i18n/manager.py 的模块级 set_locale/get_locale/t/tn
var (
	defaultManagerOnce sync.Once
	defaultManager     *Manager
	defaultManagerErr  error
)

// Default 返回进程级默认管理器，首次调用时按 MAIBOT_LOCALE 初始化
func Default() (*Manager, error) {
	defaultManagerOnce.Do(func() {
		defaultManager, defaultManagerErr = NewManager(EnvLocale())
	})
	return defaultManager, defaultManagerErr
}

// EnvLocale 读取 MAIBOT_LOCALE，缺省 zh-CN，对齐 Python os.getenv("MAIBOT_LOCALE", "zh-CN")
func EnvLocale() string {
	if value := strings.TrimSpace(os.Getenv("MAIBOT_LOCALE")); value != "" {
		return value
	}
	return DefaultLocale()
}

// SetLocale 设置进程级默认 locale，对齐 Python set_locale
func SetLocale(locale string) string {
	m, err := Default()
	if err != nil || m == nil {
		return NormalizeLocale(locale)
	}
	m.SetLocale(locale)
	return m.Locale()
}

// GetLocale 返回进程级当前 locale，对齐 Python get_locale
func GetLocale() string {
	m, err := Default()
	if err != nil || m == nil {
		return EnvLocale()
	}
	return m.Locale()
}

// T 用进程级默认 locale 翻译，对齐 Python t
func T(key string, kwargs map[string]string) string {
	m, err := Default()
	if err != nil || m == nil {
		return key
	}
	return m.T(key, "", kwargs)
}

// Tn 用进程级默认 locale 按复数翻译，对齐 Python tn
func Tn(key string, count float64, kwargs map[string]string) string {
	m, err := Default()
	if err != nil || m == nil {
		return key
	}
	return m.Tn(key, count, "", kwargs)
}
