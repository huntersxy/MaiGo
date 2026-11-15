package native

// 聊天摘要提示词提供者，按实例 prompts 目录读取并渲染模板

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"MaiGo/internal/A_memorix/core/utils"
	"MaiGo/internal/prompt"
)

// summaryPromptProvider 从 prompts/<locale> 读取 .prompt 模板
type summaryPromptProvider struct {
	mu        sync.Mutex
	roots     []string
	locale    string
	templates map[string]string
}

// newSummaryPromptProvider 构造提示词提供者
func newSummaryPromptProvider() *summaryPromptProvider {
	roots := make([]string, 0, 4)
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		roots = append(roots, path)
	}
	add(os.Getenv("A_MEMORIX_PROMPTS_ROOT"))
	if instance := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")); instance != "" {
		add(filepath.Join(instance, "prompts"))
	}
	if wd, err := os.Getwd(); err == nil {
		add(filepath.Join(wd, "prompts"))
	}
	if executable, err := os.Executable(); err == nil {
		add(filepath.Join(filepath.Dir(executable), "prompts"))
	}
	locale := strings.TrimSpace(os.Getenv("A_MEMORIX_PROMPT_LOCALE"))
	if locale == "" {
		locale = strings.TrimSpace(os.Getenv("MAIBOT_LOCALE"))
	}
	if locale == "" {
		locale = "zh-CN"
	}
	return &summaryPromptProvider{roots: roots, locale: locale, templates: map[string]string{}}
}

// Template 读取原始模板
func (p *summaryPromptProvider) Template(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("提示词名称不能为空")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if template, ok := p.templates[name]; ok {
		return template, nil
	}
	fileNames := []string{name + ".prompt", name}
	for _, root := range p.roots {
		for _, fileName := range fileNames {
			for _, locale := range []string{p.locale, "zh-CN", "en-US"} {
				candidate := filepath.Join(root, locale, fileName)
				raw, err := os.ReadFile(candidate)
				if err != nil {
					continue
				}
				template := string(raw)
				p.templates[name] = template
				return template, nil
			}
		}
	}
	// 部署目录未随附 prompts/ 时回退到编译期内嵌模板
	embedded := prompt.EmbeddedFiles()
	for _, fileName := range fileNames {
		for _, locale := range []string{p.locale, "zh-CN", "en-US"} {
			raw, err := fs.ReadFile(embedded, path.Join(locale, fileName))
			if err != nil {
				continue
			}
			template := string(raw)
			p.templates[name] = template
			return template, nil
		}
	}
	return "", fmt.Errorf("未找到提示词模板: %s", name)
}

// Render 渲染模板，支持 {{ 转义与 {name} 占位符
func (p *summaryPromptProvider) Render(name string, variables map[string]any) (string, error) {
	template, err := p.Template(name)
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	builder.Grow(len(template) + 256)
	for index := 0; index < len(template); {
		if strings.HasPrefix(template[index:], "{{") {
			builder.WriteByte('{')
			index += 2
			continue
		}
		if strings.HasPrefix(template[index:], "}}") {
			builder.WriteByte('}')
			index += 2
			continue
		}
		if template[index] != '{' {
			builder.WriteByte(template[index])
			index++
			continue
		}
		end := strings.IndexByte(template[index+1:], '}')
		if end < 0 {
			builder.WriteByte('{')
			index++
			continue
		}
		key := strings.TrimSpace(template[index+1 : index+1+end])
		value, exists := variables[key]
		if !exists {
			builder.WriteString(template[index : index+2+end])
			index += 2 + end
			continue
		}
		builder.WriteString(utils.PyStr(value))
		index += 2 + end
	}
	return builder.String(), nil
}
