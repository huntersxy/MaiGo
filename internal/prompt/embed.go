package prompt

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"
)

//go:embed all:templates
var embeddedTemplates embed.FS

// embeddedTemplateSet 是内置模板索引：prompt 名称在 locale 内唯一，分类目录仅用于定位与展示
type embeddedTemplateSet struct {
	byLocale         map[string]map[string]string
	categoryByLocale map[string]map[string]string
	metadataByLocale map[string]map[string]PromptMetadata
}

var (
	embeddedSetOnce sync.Once
	embeddedSet     *embeddedTemplateSet
	embeddedSetErr  error
)

func loadEmbeddedTemplateSet() (*embeddedTemplateSet, error) {
	embeddedSetOnce.Do(func() {
		embeddedSet, embeddedSetErr = scanEmbeddedTemplates()
	})
	return embeddedSet, embeddedSetErr
}

// scanEmbeddedTemplates 递归扫描 templates/<locale>/<category>/*.prompt，按名称索引模板
func scanEmbeddedTemplates() (*embeddedTemplateSet, error) {
	set := &embeddedTemplateSet{
		byLocale:         make(map[string]map[string]string),
		categoryByLocale: make(map[string]map[string]string),
	}
	err := fs.WalkDir(embeddedTemplates, "templates", func(filePath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(filePath, ".prompt") {
			return nil
		}
		rel := strings.TrimPrefix(filePath, "templates/")
		parts := strings.Split(rel, "/")
		if len(parts) < 2 {
			return nil
		}
		locale := parts[0]
		normalized, normErr := NormalizeLocale(locale)
		if normErr != nil || normalized != locale {
			return nil
		}
		content, readErr := embeddedTemplates.ReadFile(filePath)
		if readErr != nil {
			return fmt.Errorf("读取嵌入模板 %q: %w", filePath, readErr)
		}
		name := strings.TrimSuffix(parts[len(parts)-1], ".prompt")
		category := strings.Join(parts[1:len(parts)-1], "/")
		if set.byLocale[locale] == nil {
			set.byLocale[locale] = make(map[string]string)
			set.categoryByLocale[locale] = make(map[string]string)
		}
		if _, exists := set.byLocale[locale][name]; exists {
			return fmt.Errorf("locale %q 下重复的 prompt 名称 %q", locale, name)
		}
		set.byLocale[locale][name] = string(content)
		set.categoryByLocale[locale][name] = category
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(set.byLocale[DefaultLocale]) == 0 {
		return nil, fmt.Errorf("未找到默认 locale %q 的模板", DefaultLocale)
	}
	set.metadataByLocale = make(map[string]map[string]PromptMetadata, len(set.byLocale))
	for locale, names := range set.byLocale {
		layer := make(map[string]PromptMetadata, len(names))
		for name := range names {
			dir := path.Join("templates", locale)
			if category := set.categoryByLocale[locale][name]; category != "" {
				dir = path.Join(dir, category)
			}
			layer[name] = loadPromptMetadata(name, func(fileName string) map[string]any {
				return readEmbeddedMetadataFile(path.Join(dir, fileName))
			})
		}
		set.metadataByLocale[locale] = layer
	}
	return set, nil
}

// mergeLocaleLayers 按默认语言层铺底、目标语言层覆盖，返回名称到模板的映射
func mergeLocaleLayers(byLocale map[string]map[string]string, locale string) map[string]string {
	merged := make(map[string]string, len(byLocale[DefaultLocale]))
	for name, tpl := range byLocale[DefaultLocale] {
		merged[name] = tpl
	}
	if locale != DefaultLocale {
		if layer, ok := byLocale[locale]; ok {
			for name, tpl := range layer {
				merged[name] = tpl
			}
		}
	}
	return merged
}

// promptResourceFS 兼容按 locale 平铺的只读访问：
// 真实路径优先，旧式 <locale>/<name>.prompt 路径回退到分类目录中的同名模板。
type promptResourceFS struct {
	fs.FS
	set *embeddedTemplateSet
}

func (f promptResourceFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err == nil || !errors.Is(err, fs.ErrNotExist) || f.set == nil {
		return file, err
	}
	cleaned := strings.Trim(path.Clean("/"+name), "/")
	parts := strings.Split(cleaned, "/")
	if len(parts) != 2 || !strings.HasSuffix(parts[1], ".prompt") {
		return nil, err
	}
	locale := parts[0]
	promptName := strings.TrimSuffix(parts[1], ".prompt")
	category := f.set.categoryByLocale[locale][promptName]
	if category == "" {
		return nil, err
	}
	return f.FS.Open(path.Join(locale, category, parts[1]))
}

// EmbeddedFiles 暴露只读的内置 Prompt 资源树，路径形如 "zh-CN/main/x.prompt"
func EmbeddedFiles() fs.FS {
	set, _ := loadEmbeddedTemplateSet()
	sub, err := fs.Sub(embeddedTemplates, "templates")
	if err != nil {
		return embeddedTemplates
	}
	return promptResourceFS{FS: sub, set: set}
}
