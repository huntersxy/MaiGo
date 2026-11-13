package i18n

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// TranslationValue 字符串或 plural 节点
type TranslationValue any

// Manager 轻量 i18n 管理器，对齐 I18nManager 子集
type Manager struct {
	mu            sync.RWMutex
	defaultLocale string
	catalogs      map[string]map[string]TranslationValue
}

// NewManager 创建管理器并预加载嵌入 catalog
func NewManager(defaultLocale string) (*Manager, error) {
	m := &Manager{
		defaultLocale: NormalizeLocale(defaultLocale),
		catalogs:      make(map[string]map[string]TranslationValue),
	}
	for _, loc := range SupportedLocales() {
		if err := m.loadLocale(loc); err != nil {
			return nil, fmt.Errorf("i18n: 加载 %s 失败: %w", loc, err)
		}
	}
	return m, nil
}

func (m *Manager) loadLocale(locale string) error {
	merged := make(map[string]TranslationValue)
	for _, file := range CatalogFiles() {
		raw, err := localeFS.ReadFile(catalogPath(locale, file))
		if err != nil {
			return err
		}
		var chunk map[string]TranslationValue
		if err := json.Unmarshal(raw, &chunk); err != nil {
			return err
		}
		for k, v := range chunk {
			merged[k] = v
		}
	}
	m.catalogs[NormalizeLocale(locale)] = merged
	return nil
}

// Locale 当前默认 locale
func (m *Manager) Locale() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.defaultLocale
}

// SetLocale 设置默认 locale
func (m *Manager) SetLocale(locale string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultLocale = NormalizeLocale(locale)
}

// T 在合并后的 catalog 中查找翻译；目标 locale 缺失时回退 defaultLocale；仍缺失则返回 key
func (m *Manager) T(key string, locale string, kwargs map[string]string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	loc := m.defaultLocale
	if locale != "" {
		loc = NormalizeLocale(locale)
	}
	val, _, ok := m.lookupWithLocaleLocked(key, loc)
	if !ok {
		return key
	}
	switch v := val.(type) {
	case string:
		return formatTemplate(v, kwargs)
	case map[string]any:
		// 普通 t() 对 plural 节点只取 other，缺失时回退 key
		if other, ok := v["other"].(string); ok {
			return formatTemplate(other, kwargs)
		}
		return key
	default:
		return key
	}
}

func (m *Manager) lookupLocked(key, loc string) (TranslationValue, bool) {
	if cat := m.catalogs[loc]; cat != nil {
		if val, ok := cat[key]; ok {
			return val, true
		}
	}
	if loc != m.defaultLocale {
		if cat := m.catalogs[m.defaultLocale]; cat != nil {
			if val, ok := cat[key]; ok {
				return val, true
			}
		}
	}
	return nil, false
}

func formatTemplate(template string, kwargs map[string]string) string {
	if len(kwargs) == 0 {
		return template
	}
	out := template
	for k, v := range kwargs {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}

// lookupWithLocaleLocked 返回命中的翻译值、实际命中的 locale 与是否存在
func (m *Manager) lookupWithLocaleLocked(key, loc string) (TranslationValue, string, bool) {
	if cat := m.catalogs[loc]; cat != nil {
		if val, ok := cat[key]; ok {
			return val, loc, true
		}
	}
	if loc != m.defaultLocale {
		if cat := m.catalogs[m.defaultLocale]; cat != nil {
			if val, ok := cat[key]; ok {
				return val, m.defaultLocale, true
			}
		}
	}
	return nil, loc, false
}

// Tn 对齐 I18nManager.tn：按 babel plural category 选择模板，并把 count 注入格式化参数
func (m *Manager) Tn(key string, count float64, locale string, kwargs map[string]string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	loc := m.defaultLocale
	if locale != "" {
		loc = NormalizeLocale(locale)
	}
	val, translationLocale, ok := m.lookupWithLocaleLocked(key, loc)
	if !ok {
		return key
	}
	plural, isPlural := val.(map[string]any)
	if !isPlural {
		if _, isString := val.(string); isString {
			// 非 plural 节点回退到普通 t()，并带上 count
			return m.tWithoutLock(key, loc, withCount(kwargs, count))
		}
		return key
	}
	template := ""
	if category, ok := plural[selectPluralCategory(translationLocale, count)].(string); ok {
		template = category
	}
	if template == "" {
		template, _ = plural["other"].(string)
	}
	if template == "" {
		return key
	}
	return formatTemplate(template, withCount(kwargs, count))
}

// tWithoutLock T 的免锁版本，供已持锁的 Tn 使用
func (m *Manager) tWithoutLock(key, loc string, kwargs map[string]string) string {
	val, _, ok := m.lookupWithLocaleLocked(key, loc)
	if !ok {
		return key
	}
	if template, ok := val.(string); ok {
		return formatTemplate(template, kwargs)
	}
	if plural, ok := val.(map[string]any); ok {
		if other, ok := plural["other"].(string); ok {
			return formatTemplate(other, kwargs)
		}
	}
	return key
}

func withCount(kwargs map[string]string, count float64) map[string]string {
	out := make(map[string]string, len(kwargs)+1)
	for k, v := range kwargs {
		out[k] = v
	}
	out["count"] = formatCount(count)
	return out
}
