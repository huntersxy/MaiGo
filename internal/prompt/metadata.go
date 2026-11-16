package prompt

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"go.uber.org/zap"
)

// PromptMetadata 对齐 MaiBot PromptMetadata；WebUI catalog 使用
type PromptMetadata struct {
	DisplayName string
	Advanced    bool
	Description string
}

// TemplateInfo 对齐 MaiBot PromptTemplateInfo
type TemplateInfo struct {
	Name       string
	Category   string
	Locale     string
	Path       string
	Customized bool
	Metadata   PromptMetadata
}

// ResolvedTemplate 对齐 MaiBot ResolvedPromptTemplate
type ResolvedTemplate struct {
	Name       string
	Category   string
	Locale     string
	Path       string
	Template   string
	Customized bool
}

func loadEmbeddedMetadata() map[string]map[string]PromptMetadata {
	out := make(map[string]map[string]PromptMetadata)
	root := "templates"
	entries, err := fs.ReadDir(embeddedTemplates, root)
	if err != nil {
		return out
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		locale := entry.Name()
		if _, normErr := NormalizeLocale(locale); normErr != nil {
			continue
		}
		layer := scanEmbeddedMetadata(locale)
		if len(layer) > 0 {
			out[locale] = layer
		}
	}
	return out
}

func scanEmbeddedMetadata(locale string) map[string]PromptMetadata {
	layer := make(map[string]PromptMetadata)
	base := rootJoin("templates", locale)
	// 嵌入文件系统遍历失败不应静默：返回已收集部分并记录告警
	if walkErr := fs.WalkDir(embeddedTemplates, base, func(filePath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if !strings.HasSuffix(filePath, ".prompt") {
			return nil
		}
		rel := strings.TrimPrefix(filePath, base+"/")
		name := strings.TrimSuffix(path.Base(rel), ".prompt")
		category := ""
		if index := strings.LastIndex(rel, "/"); index >= 0 {
			category = rel[:index]
		}
		key := name
		if category != "" {
			key = category + "/" + name
		}
		dir := path.Dir(filePath)
		layer[key] = loadPromptMetadata(name, func(fileName string) map[string]any {
			return readEmbeddedMetadataFile(rootJoin(dir, fileName))
		})
		if category != "" {
			// 兼容按扁平名称查询带 category 的模板元信息
			if _, exists := layer[name]; !exists {
				layer[name] = layer[key]
			}
		}
		return nil
	}); walkErr != nil {
		zap.L().Warn("扫描嵌入 Prompt 元信息失败，返回已收集部分",
			zap.String("locale", locale), zap.Error(walkErr))
	}
	return layer
}

func rootJoin(parts ...string) string {
	return strings.Join(parts, "/")
}

// loadPromptMetadata 对齐 MaiBot _load_prompt_metadata 的合并顺序：
// 父目录 .meta.json -> 父目录 .meta.toml -> 模板同名 .meta.json -> 模板同名 .meta.toml
func loadPromptMetadata(promptName string, read func(fileName string) map[string]any) PromptMetadata {
	return mergePromptMetadata(
		extractTemplateMetadata(read(".meta.json"), promptName),
		extractTemplateMetadata(read(".meta.toml"), promptName),
		extractTemplateMetadata(read(promptName+".meta.json"), promptName),
		extractTemplateMetadata(read(promptName+".meta.toml"), promptName),
	)
}

func readEmbeddedMetadataFile(filePath string) map[string]any {
	raw, err := embeddedTemplates.ReadFile(filePath)
	if err != nil {
		return nil
	}
	return parsePromptMetadata(raw, strings.ToLower(path.Ext(filePath)))
}

// readPromptMetadataFile 读取实例目录下的 .meta.toml/.meta.json
func readPromptMetadataFile(filePath string) map[string]any {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	return parsePromptMetadata(raw, strings.ToLower(filepath.Ext(filePath)))
}

func parsePromptMetadata(raw []byte, extension string) map[string]any {
	doc := map[string]any{}
	switch extension {
	case ".json":
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil
		}
	default:
		if err := toml.Unmarshal(raw, &doc); err != nil {
			return nil
		}
	}
	if len(doc) == 0 {
		return nil
	}
	return doc
}

func extractTemplateMetadata(metadata map[string]any, promptName string) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	if templates, ok := metadata["templates"].(map[string]any); ok {
		if entry, ok := templates[promptName].(map[string]any); ok {
			return entry
		}
	}
	if entry, ok := metadata[promptName].(map[string]any); ok {
		return entry
	}
	if _, ok := metadata["display_name"]; ok {
		return metadata
	}
	if _, ok := metadata["advanced"]; ok {
		return metadata
	}
	if _, ok := metadata["description"]; ok {
		return metadata
	}
	return nil
}

func mergePromptMetadata(layers ...map[string]any) PromptMetadata {
	var out PromptMetadata
	for _, layer := range layers {
		if value, exists := layer["display_name"]; exists {
			if display, ok := value.(string); ok {
				out.DisplayName = display
			}
		}
		if value, exists := layer["advanced"]; exists {
			if advanced, ok := value.(bool); ok {
				out.Advanced = advanced
			}
		}
		if value, exists := layer["description"]; exists {
			if description, ok := value.(string); ok {
				out.Description = description
			}
		}
	}
	return out
}

func coercePromptMetadata(entry PromptMetadata) PromptMetadata {
	return entry
}

// ListTemplates 返回当前语言层及自定义层的模板目录（按名称排序）
func (m *Manager) ListTemplates(locale string, includeCustom bool) []TemplateInfo {
	m.ensureFresh()
	requested := DefaultLocale
	if normalized, err := NormalizeLocale(locale); err == nil && normalized != "" {
		requested = normalized
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	seen := make(map[string]TemplateInfo)
	for _, layerLocale := range LocaleFallbacks(requested) {
		for key := range m.byLocale[layerLocale] {
			if _, exists := seen[key]; exists {
				continue
			}
			meta := PromptMetadata{}
			if layer := m.byLocaleMeta[layerLocale]; layer != nil {
				meta = layer[key]
			}
			name, category := splitTemplateKey(key)
			seen[key] = TemplateInfo{
				Name: name, Category: category, Locale: layerLocale,
				Path:     embeddedPromptPath(layerLocale, category, name),
				Metadata: meta,
			}
		}
	}
	if includeCustom {
		for key := range m.customByLocale[requested] {
			name, category := splitTemplateKey(key)
			info := TemplateInfo{
				Name: name, Category: category, Locale: requested,
				Path: "custom_prompts/" + requested + "/" + key + ".prompt", Customized: true,
			}
			if layer := m.customMetaByLocale[requested]; layer != nil {
				info.Metadata = layer[key]
			}
			seen[key] = info
		}
		for key := range m.legacyCustom {
			info := TemplateInfo{
				Name: key, Path: "custom_prompts/" + key + ".prompt", Customized: true,
			}
			if layer := m.legacyMeta; layer != nil {
				info.Metadata = layer[key]
			}
			seen[key] = info
		}
	}
	out := make([]TemplateInfo, 0, len(seen))
	for _, info := range seen {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Locale < out[j].Locale
	})
	return out
}

func embeddedPromptPath(locale, category, name string) string {
	file := name + ".prompt"
	if category == "" {
		return "prompts/" + locale + "/" + file
	}
	return "prompts/" + locale + "/" + category + "/" + file
}

func splitTemplateKey(key string) (name, category string) {
	if index := strings.LastIndex(key, "/"); index >= 0 {
		return key[index+1:], key[:index]
	}
	return key, ""
}

// GetMetadata 返回指定模板的元信息；自定义层优先，其次目标语言层与默认层
func (m *Manager) GetMetadata(name, locale string) PromptMetadata {
	m.ensureFresh()
	requested := DefaultLocale
	if normalized, err := NormalizeLocale(locale); err == nil && normalized != "" {
		requested = normalized
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, layerLocale := range LocaleFallbacks(requested) {
		if layer := m.customMetaByLocale[layerLocale]; layer != nil {
			if meta, ok := layer[name]; ok {
				return coercePromptMetadata(meta)
			}
		}
		if meta, ok := m.legacyMeta[name]; ok {
			return coercePromptMetadata(meta)
		}
		if layer := m.byLocaleMeta[layerLocale]; layer != nil {
			if meta, ok := layer[name]; ok {
				return coercePromptMetadata(meta)
			}
		}
	}
	return PromptMetadata{}
}

// ResolveTemplate 按语言回退链解析模板，顺序对齐 MaiBot resolve_prompt_path：
// 自定义(locale) -> 遗留自定义(root 平铺) -> 内置(locale) -> 内置(默认层)
func (m *Manager) ResolveTemplate(name, locale, category string, includeCustom bool) (ResolvedTemplate, error) {
	m.ensureFresh()
	normalizedName, err := NormalizePromptName(name)
	if err != nil {
		return ResolvedTemplate{}, err
	}
	// 空分类对齐 Python category=None：不校验、按根目录平铺模板解析。
	// 显式传入空白/非法段仍走 NormalizePromptCategory 报错。
	normalizedCategory := ""
	if strings.TrimSpace(category) != "" {
		normalizedCategory, err = NormalizePromptCategory(category)
		if err != nil {
			return ResolvedTemplate{}, err
		}
	}
	requested := DefaultLocale
	if normalized, normErr := NormalizeLocale(locale); normErr == nil && normalized != "" {
		requested = normalized
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	customLayer := m.customByLocale
	legacy := m.legacyCustom
	for _, candidateLocale := range LocaleFallbacks(requested) {
		if includeCustom {
			if layer := customLayer[candidateLocale]; layer != nil {
				if key := templateKey(normalizedCategory, normalizedName); key != "" {
					if tpl, ok := layer[key]; ok {
						return ResolvedTemplate{
							Name: normalizedName, Category: normalizedCategory, Locale: candidateLocale,
							Path:     "custom_prompts/" + candidateLocale + "/" + key + ".prompt",
							Template: tpl, Customized: true,
						}, nil
					}
				}
			}
			if tpl, ok := legacy[normalizedName]; ok {
				return ResolvedTemplate{
					Name: normalizedName, Path: "custom_prompts/" + normalizedName + ".prompt",
					Template: tpl, Customized: true,
				}, nil
			}
		}
		if layer := m.byLocale[candidateLocale]; layer != nil {
			if key := templateKey(normalizedCategory, normalizedName); key != "" {
				if tpl, ok := layer[key]; ok {
					return ResolvedTemplate{
						Name: normalizedName, Category: normalizedCategory, Locale: candidateLocale,
						Path:     embeddedPromptPath(candidateLocale, normalizedCategory, normalizedName),
						Template: tpl,
					}, nil
				}
			}
		}
	}
	return ResolvedTemplate{}, fmt.Errorf("prompt 模板不存在: %q (locale=%q category=%q)", normalizedName, requested, normalizedCategory)
}

func templateKey(category, name string) string {
	category = strings.Trim(category, "/")
	if category == "" {
		return name
	}
	return category + "/" + name
}

// NormalizePromptName 对齐 MaiBot normalize_prompt_name：去 .prompt 后缀并校验安全字符
func NormalizePromptName(name string) (string, error) {
	candidate := strings.TrimSpace(name)
	candidate = strings.TrimSuffix(candidate, ".prompt")
	// 对齐 Python："." 与 ".." 也视为非法
	if candidate == "." || candidate == ".." || !safePromptName(candidate) {
		return "", fmt.Errorf("Prompt 名称 '%s' 非法", name)
	}
	return CanonicalPromptName(candidate), nil
}

// NormalizePromptCategory 校验 category 路径（可多级，/ 分隔）
func NormalizePromptCategory(category string) (string, error) {
	trimmed := strings.TrimSpace(category)
	parts := make([]string, 0, 3)
	for _, part := range strings.Split(trimmed, "/") {
		if part == "" {
			continue
		}
		if part == "." || part == ".." || !safePromptName(part) {
			return "", fmt.Errorf("Prompt 分类 '%s' 非法", category)
		}
		parts = append(parts, part)
	}
	// 对齐 Python：非 None 的空分类（"" / 全空段）报错；None 由调用方用空串表示并跳过本函数
	if len(parts) == 0 {
		return "", fmt.Errorf("Prompt 分类 '%s' 非法", category)
	}
	return strings.Join(parts, "/"), nil
}
