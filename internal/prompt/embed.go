package prompt

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

//go:embed all:templates
var embeddedTemplates embed.FS

func loadEmbeddedLocales() (map[string]map[string]string, error) {
	byLocale := make(map[string]map[string]string)
	err := fs.WalkDir(embeddedTemplates, "templates", func(filePath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(filePath, ".prompt") {
			return nil
		}
		rel := strings.TrimPrefix(filePath, "templates/")
		parts := strings.Split(rel, "/")
		if len(parts) < 2 {
			return nil
		}
		locale, fileName := parts[0], parts[len(parts)-1]
		normalized, normErr := NormalizeLocale(locale)
		if normErr != nil || normalized != locale {
			return nil
		}
		content, readErr := embeddedTemplates.ReadFile(filePath)
		if readErr != nil {
			return fmt.Errorf("读取嵌入模板 %q: %w", filePath, readErr)
		}
		category := ""
		if len(parts) > 2 {
			category = strings.Join(parts[1:len(parts)-1], "/")
		}
		name := strings.TrimSuffix(fileName, path.Ext(fileName))
		key := name
		if category != "" {
			key = category + "/" + name
		}
		if byLocale[locale] == nil {
			byLocale[locale] = make(map[string]string)
		}
		if _, exists := byLocale[locale][key]; exists {
			return fmt.Errorf("locale %q 下重复的 prompt 名称 %q", locale, key)
		}
		byLocale[locale][key] = string(content)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(byLocale[DefaultLocale]) == 0 {
		return nil, fmt.Errorf("未找到默认 locale %q 的模板", DefaultLocale)
	}
	return byLocale, nil
}

// mergeLocaleLayers 按默认语言层铺底、目标语言层覆盖，返回平铺名称到模板的映射
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

// EmbeddedFiles 暴露只读的内置 Prompt 资源树，路径形如 "templates/zh-CN/x.prompt"
func EmbeddedFiles() fs.FS {
	sub, err := fs.Sub(embeddedTemplates, "templates")
	if err != nil {
		return embeddedTemplates
	}
	return sub
}
