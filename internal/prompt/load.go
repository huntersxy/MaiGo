package prompt

import (
	"fmt"
	"strings"
)

// LoadPrompt 对齐 MaiBot prompt_i18n.load_prompt：
// 自定义模板在新版字段集下渲染失败时回退到内置模板；内置模板失败直接返回错误
func (m *Manager) LoadPrompt(name, locale string, vars map[string]string) (string, error) {
	m.ensureFresh()
	normalizedName, err := NormalizePromptName(name)
	if err != nil {
		return "", err
	}
	resolved, err := m.ResolveTemplate(normalizedName, locale, "", true)
	if err != nil {
		return "", err
	}
	rendered, renderErr := renderTemplate(resolved.Template, func(field string) (string, bool, error) {
		value, ok := vars[field]
		return value, ok, nil
	}, 0)
	if renderErr == nil {
		return rendered, nil
	}
	if !resolved.Customized {
		return "", fmt.Errorf("渲染 prompt %q 失败: %w", normalizedName, renderErr)
	}
	builtin, builtinErr := m.ResolveTemplate(normalizedName, locale, "", false)
	if builtinErr != nil {
		return "", fmt.Errorf("自定义 prompt %q 不可用且内置模板缺失: %w", normalizedName, renderErr)
	}
	rendered, builtinRenderErr := renderTemplate(builtin.Template, func(field string) (string, bool, error) {
		value, ok := vars[field]
		return value, ok, nil
	}, 0)
	if builtinRenderErr != nil {
		return "", fmt.Errorf("自定义 prompt %q 已过期，回退内置模板仍失败: %w", normalizedName, builtinRenderErr)
	}
	return rendered, nil
}

// ResolvePromptPath 返回模板解析路径（供 WebUI/catalog 展示）
func (m *Manager) ResolvePromptPath(name, locale, category string) (string, bool, error) {
	resolved, err := m.ResolveTemplate(name, locale, category, true)
	if err != nil {
		return "", false, err
	}
	return resolved.Path, resolved.Customized, nil
}

// LookupTemplate 按名称与 locale 返回原始模板
func (m *Manager) LookupTemplate(name, locale string) (string, bool) {
	resolved, err := m.ResolveTemplate(name, locale, "", false)
	if err != nil {
		return "", false
	}
	return resolved.Template, true
}

// IsStrictPromptI18NMode 对齐 MaiBot 严格模式判断；Go 侧通过显式参数决定是否启用
func IsStrictPromptI18NMode(values map[string]string) bool {
	for _, key := range []string{"MAIBOT_PROMPT_I18N_STRICT", "MAIBOT_I18N_STRICT"} {
		switch strings.ToLower(strings.TrimSpace(values[key])) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}
