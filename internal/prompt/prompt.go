package prompt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Prompt 对齐 MaiBot Prompt：可克隆、可挂载 context 构造函数、可渲染
type Prompt struct {
	mu       sync.RWMutex
	name     string
	template string
	contexts map[string]ContextFunc
	cloned   bool
	locale   string
}

// NewPrompt 创建 Prompt；名称与模板不能为空，且不允许未命名占位符 {}
func NewPrompt(name, template string) (*Prompt, error) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return nil, fmt.Errorf("prompt 名称不能为空")
	}
	if strings.TrimSpace(template) == "" {
		return nil, fmt.Errorf("prompt %q 模板不能为空", trimmedName)
	}
	escaped := escapeLiteralBraces(template)
	if strings.Contains(escaped, "{}") {
		return nil, fmt.Errorf("prompt %q 不允许未命名占位符 {}", trimmedName)
	}
	return &Prompt{
		name:     trimmedName,
		template: template,
		contexts: make(map[string]ContextFunc),
		locale:   DefaultLocale,
	}, nil
}

func clonePromptTemplate(name, template string) *Prompt {
	p, err := NewPrompt(name, template)
	if err != nil {
		// 内置模板均通过校验；异常模板保留原始内容，渲染时再报缺失字段
		p = &Prompt{name: name, template: template, contexts: make(map[string]ContextFunc), locale: DefaultLocale}
	}
	p.cloned = true
	return p
}

// Name 返回 prompt 名称
func (p *Prompt) Name() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.name
}

// Template 返回原始模板
func (p *Prompt) Template() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.template
}

// SetTemplate 替换模板内容
func (p *Prompt) SetTemplate(template string) error {
	if strings.TrimSpace(template) == "" {
		return fmt.Errorf("prompt %q 模板不能为空", p.Name())
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.template = template
	return nil
}

// AddContext 为字段挂载 context 构造函数；同名重复返回错误（对齐 Python KeyError）
func (p *Prompt) AddContext(field string, fn ContextFunc) error {
	if strings.TrimSpace(field) == "" {
		return fmt.Errorf("context 字段名不能为空")
	}
	if fn == nil {
		return fmt.Errorf("context %q 构造函数不能为空", field)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.contexts[field]; exists {
		return fmt.Errorf("prompt %q 的 context %q 已存在", p.name, field)
	}
	if p.contexts == nil {
		p.contexts = make(map[string]ContextFunc)
	}
	p.contexts[field] = fn
	return nil
}

// AddContextText 挂载固定文本 context
func (p *Prompt) AddContextText(field, value string) error {
	return p.AddContext(field, func(context.Context) (string, error) {
		return value, nil
	})
}

// MarkAsCloned 标记为克隆（对齐 Python mark_as_cloned）
func (p *Prompt) MarkAsCloned() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cloned = true
}

// IsCloned 返回是否可渲染
func (p *Prompt) IsCloned() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cloned
}

// Clone 返回独立的 Prompt 副本（带 context 函数与 clone 标记）
func (p *Prompt) Clone() *Prompt {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	contexts := make(map[string]ContextFunc, len(p.contexts))
	for key, fn := range p.contexts {
		contexts[key] = fn
	}
	return &Prompt{
		name:     p.name,
		template: p.template,
		contexts: contexts,
		cloned:   true,
		locale:   p.locale,
	}
}

// Render 按本 Prompt 的 context 与 vars 渲染；缺失字段返回错误
func (p *Prompt) Render(ctx context.Context, vars map[string]string) (string, error) {
	if p == nil {
		return "", fmt.Errorf("prompt 为空")
	}
	if !p.IsCloned() {
		return "", fmt.Errorf("prompt %q 未克隆，不能渲染", p.Name())
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.RLock()
	template := p.template
	contexts := make(map[string]ContextFunc, len(p.contexts))
	for key, fn := range p.contexts {
		contexts[key] = fn
	}
	name := p.name
	p.mu.RUnlock()

	merged := make(map[string]string, len(vars))
	for key, value := range vars {
		merged[key] = value
	}
	for _, field := range extractFieldNames(template) {
		if _, ok := merged[field]; ok {
			continue
		}
		if fn, ok := contexts[field]; ok {
			value, err := fn(ctx)
			if err != nil {
				return "", fmt.Errorf("prompt %q 的 context %q 构造失败: %w", name, field, err)
			}
			merged[field] = value
			continue
		}
		return "", fmt.Errorf("Prompt %q 中缺少必要的内容块或构建函数: %q", name, field)
	}
	return renderTemplate(template, func(field string) (string, bool, error) {
		value, ok := merged[field]
		return value, ok, nil
	}, 0)
}

// savePromptFiles 将待保存 Prompt 写入 custom_prompts/<locale>/<name>.prompt
// 对齐 Python save_prompts：先清空 active custom prompt 目录中的 *.prompt，再写回当前 Prompt
func savePromptFiles(root, locale string, prompts map[string]*Prompt, saveLocales map[string]string) error {
	if len(prompts) == 0 {
		return nil
	}
	dirs := []string{root, filepath.Join(root, DefaultLocale)}
	if locale != "" && locale != DefaultLocale {
		dirs = append(dirs, filepath.Join(root, locale))
	}
	for _, dir := range dirs {
		if err := clearPromptFiles(dir); err != nil {
			return err
		}
	}
	for name, p := range prompts {
		promptLocale := saveLocales[name]
		if promptLocale == "" {
			promptLocale = locale
		}
		if promptLocale == "" {
			promptLocale = DefaultLocale
		}
		targetDir := filepath.Join(root, promptLocale)
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return fmt.Errorf("创建 custom prompt 目录 %q: %w", targetDir, err)
		}
		filePath := filepath.Join(targetDir, CustomPromptStorageName(name)+".prompt")
		if err := os.WriteFile(filePath, []byte(p.Template()), 0o644); err != nil {
			return fmt.Errorf("写入 custom prompt %q: %w", filePath, err)
		}
	}
	return nil
}

func clearPromptFiles(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取 custom prompt 目录 %q: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".prompt") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return fmt.Errorf("清理 custom prompt %q: %w", entry.Name(), err)
		}
	}
	return nil
}
