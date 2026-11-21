package prompt

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
)

// Manager 加载内置多语言 prompt 模板并提供渲染与自定义覆盖
//
// 行为对齐 MaiBot PromptManager / prompt_i18n：
//   - 内置模板来自 go:embed，按 locale 与 main/instructions 分类目录动态发现（不硬编码语言列表）
//   - 实例 data/custom_prompts 按 locale + category 覆盖内置模板（见 overlay.go）
//   - Render 递归解析同名字段 prompt；Prompt 对象支持 context 构造函数与保存
//   - 占位符语法与 Python 一致：{name} 为字段，{{ / }} 转义为字面量大括号
//   - 不支持 Python str.format 的格式说明、属性访问与 plural
type Manager struct {
	mu sync.RWMutex

	byLocale           map[string]map[string]string
	byLocaleCategory   map[string]map[string]string
	byLocaleMeta       map[string]map[string]PromptMetadata
	builtinOverride    map[string]map[string]string
	builtinCategory    map[string]map[string]string
	builtinMeta        map[string]map[string]PromptMetadata
	customByLocale     map[string]map[string]string
	customMetaByLocale map[string]map[string]PromptMetadata
	legacyCustom       map[string]string
	legacyMeta         map[string]PromptMetadata
	locale             string
	merged             map[string]string
	customSnap         modSnap

	prompts           map[string]*Prompt
	contextFunctions  map[string]ContextFunc
	customRoot        string
	cacheRevision     int64
	promptSaveLocales map[string]string
}

// ContextFunc 对应 Python add_context_construct_function 的同步构造函数
// 异步构造函数在 Go 侧统一用 context.Context 阻塞等待
type ContextFunc func(ctx context.Context) (string, error)

// NewManager 从嵌入模板构建 Manager；locale 取 MAIBOT_LOCALE，未设置时用系统默认语言
func NewManager() (*Manager, error) {
	set, err := loadEmbeddedTemplateSet()
	if err != nil {
		return nil, err
	}
	m := &Manager{
		byLocale:          set.byLocale,
		byLocaleCategory:  set.categoryByLocale,
		byLocaleMeta:      set.metadataByLocale,
		locale:            InitialLocale(),
		prompts:           make(map[string]*Prompt),
		contextFunctions:  make(map[string]ContextFunc),
		promptSaveLocales: make(map[string]string),
	}
	m.merged = mergeWithCustom(m.byLocale, m.builtinOverride, nil, nil, m.locale)
	return m, nil
}

// SetLocale 切换当前语言层（默认语言层铺底，目标 locale 覆盖；未知 locale 合法时仅保留默认层）
func (m *Manager) SetLocale(locale string) error {
	normalized, err := NormalizeLocale(locale)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.locale = normalized
	m.merged = mergeWithCustom(m.byLocale, m.builtinOverride, m.customByLocale, m.legacyCustom, normalized)
	m.applyRegisteredLocked()
	return nil
}

// Locale 返回当前 locale
func (m *Manager) Locale() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.locale
}

// Names 返回当前 locale 合并后可用的 prompt 名称（排序）
func (m *Manager) Names() []string {
	m.ensureFresh()
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.merged))
	for name := range m.merged {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Get 返回指定名称的原始模板文本
func (m *Manager) Get(name string) (string, error) {
	m.ensureFresh()
	m.mu.RLock()
	defer m.mu.RUnlock()
	tpl, ok := m.merged[name]
	if !ok {
		return "", fmt.Errorf("prompt %q 不存在", name)
	}
	return tpl, nil
}

// Render 用 vars 填充模板；缺失的键若同名 prompt 存在则递归渲染该 prompt
func (m *Manager) Render(name string, vars map[string]string) (string, error) {
	m.ensureFresh()
	return m.renderResolved(name, vars, 0)
}

// RenderLocale 按指定 locale 渲染，不改变 Manager 当前语言层；对齐 Python 的 use_locale 局部切换
func (m *Manager) RenderLocale(locale, name string, vars map[string]string) (string, error) {
	normalized, err := NormalizeLocale(locale)
	if err != nil {
		return "", err
	}
	m.ensureFresh()
	m.mu.RLock()
	merged := mergeWithCustom(m.byLocale, m.builtinOverride, m.customByLocale, m.legacyCustom, normalized)
	for promptName, registered := range m.prompts {
		merged[promptName] = registered.Template()
	}
	m.mu.RUnlock()
	return renderFromMerged(name, merged, vars, 0)
}

// RenderLocaleTransform 按 locale 取模板，先转换固定文案再填充变量
func (m *Manager) RenderLocaleTransform(locale, name string, vars map[string]string, transform func(string) string) (string, error) {
	normalized, err := NormalizeLocale(locale)
	if err != nil {
		return "", err
	}
	m.ensureFresh()
	m.mu.RLock()
	merged := mergeWithCustom(m.byLocale, m.builtinOverride, m.customByLocale, m.legacyCustom, normalized)
	for promptName, registered := range m.prompts {
		merged[promptName] = registered.Template()
	}
	m.mu.RUnlock()
	if transform != nil {
		if tpl, ok := merged[name]; ok {
			merged[name] = transform(tpl)
		}
	}
	return renderFromMerged(name, merged, vars, 0)
}

func (m *Manager) renderResolved(name string, vars map[string]string, depth int) (string, error) {
	m.mu.RLock()
	merged := m.merged
	m.mu.RUnlock()
	return renderFromMerged(name, merged, vars, depth)
}

func renderFromMerged(name string, merged map[string]string, vars map[string]string, depth int) (string, error) {
	tpl, ok := merged[name]
	if !ok {
		return "", fmt.Errorf("prompt %q 不存在", name)
	}
	if vars == nil {
		vars = map[string]string{}
	}
	return renderTemplate(tpl, func(field string) (string, bool, error) {
		if value, provided := vars[field]; provided {
			return value, true, nil
		}
		nested, exists := merged[field]
		if !exists {
			return "", false, nil
		}
		rendered, err := renderNestedTemplate(nested, merged, vars, depth+1)
		if err != nil {
			return "", false, err
		}
		return rendered, true, nil
	}, depth)
}

func renderNestedTemplate(template string, merged map[string]string, vars map[string]string, depth int) (string, error) {
	return renderTemplate(template, func(field string) (string, bool, error) {
		if value, provided := vars[field]; provided {
			return value, true, nil
		}
		nested, exists := merged[field]
		if !exists {
			return "", false, nil
		}
		rendered, err := renderNestedTemplate(nested, merged, vars, depth+1)
		if err != nil {
			return "", false, err
		}
		return rendered, true, nil
	}, depth)
}

// GetPrompt 返回指定模板的独立 Prompt 克隆（对齐 Python get_prompt）
func (m *Manager) GetPrompt(name string) (*Prompt, error) {
	m.ensureFresh()
	m.mu.RLock()
	tpl, ok := m.merged[name]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("prompt %q 不存在", name)
	}
	return clonePromptTemplate(name, tpl), nil
}

// AddPrompt 注册一个新的 Prompt（needSave 时记入待保存集合）
func (m *Manager) AddPrompt(name, template string, needSave bool, locale string) error {
	p, err := NewPrompt(name, template)
	if err != nil {
		return err
	}
	normalizedLocale := DefaultLocale
	if locale != "" {
		if normalized, normErr := NormalizeLocale(locale); normErr == nil {
			normalizedLocale = normalized
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.prompts[name]; exists {
		return fmt.Errorf("prompt %q 已存在", name)
	}
	m.prompts[name] = p
	m.merged[name] = template
	if needSave {
		m.promptSaveLocales[name] = normalizedLocale
	}
	m.bumpCacheRevisionLocked()
	return nil
}

// RemovePrompt 移除已注册的 Prompt
func (m *Manager) RemovePrompt(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.prompts[name]; !exists {
		return fmt.Errorf("prompt %q 不存在", name)
	}
	delete(m.prompts, name)
	delete(m.promptSaveLocales, name)
	delete(m.merged, name)
	m.bumpCacheRevisionLocked()
	return nil
}

// ReplacePrompt 替换已注册的 Prompt 模板，保留其 context 函数
func (m *Manager) ReplacePrompt(name, template string) error {
	next, err := NewPrompt(name, template)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists := m.prompts[name]
	if !exists {
		return fmt.Errorf("prompt %q 不存在", name)
	}
	next.contexts = current.contexts
	m.prompts[name] = next
	m.merged[name] = template
	m.bumpCacheRevisionLocked()
	return nil
}

// AddContextConstructFunction 注册全局 context 构造函数（同名重复返回错误）
func (m *Manager) AddContextConstructFunction(name string, fn ContextFunc) error {
	if name == "" || fn == nil {
		return fmt.Errorf("context 构造函数名称与实现不能为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.contextFunctions[name]; exists {
		return fmt.Errorf("context 构造函数 %q 已存在", name)
	}
	m.contextFunctions[name] = fn
	m.bumpCacheRevisionLocked()
	return nil
}

// RenderPrompt 渲染已注册（克隆）的 Prompt；未注册时返回错误（对齐 Python render_prompt）
func (m *Manager) RenderPrompt(ctx context.Context, name string, vars map[string]string) (string, error) {
	m.ensureFresh()
	m.mu.RLock()
	p, exists := m.prompts[name]
	m.mu.RUnlock()
	if !exists {
		return "", fmt.Errorf("prompt %q 未克隆，不能渲染", name)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	merged := make(map[string]string, len(vars))
	for key, value := range vars {
		merged[key] = value
	}
	return m.renderPromptFields(ctx, name, p, merged, 0)
}

func (m *Manager) renderPromptFields(ctx context.Context, name string, p *Prompt, vars map[string]string, depth int) (string, error) {
	if depth > maxRenderDepth {
		return "", fmt.Errorf("prompt 渲染递归过深（>%d 层）", maxRenderDepth)
	}
	fields := extractFieldNames(p.template)
	for _, field := range fields {
		if _, provided := vars[field]; provided {
			continue
		}
		if nested, ok := m.lookupPromptText(field); ok {
			rendered, err := m.renderResolved(field, vars, depth+1)
			if err != nil {
				return "", err
			}
			_ = nested
			vars[field] = rendered
			continue
		}
		if fn, ok := p.contexts[field]; ok {
			value, err := fn(ctx)
			if err != nil {
				return "", fmt.Errorf("prompt %q 的 context %q 构造失败: %w", name, field, err)
			}
			vars[field] = value
			continue
		}
		if fn, ok := m.lookupContextFunction(field); ok {
			value, err := fn(ctx)
			if err != nil {
				return "", fmt.Errorf("prompt %q 的全局 context %q 构造失败: %w", name, field, err)
			}
			vars[field] = value
			continue
		}
		return "", fmt.Errorf("Prompt %q 中缺少必要的内容块或构建函数: %q", name, field)
	}
	return renderTemplate(p.template, func(field string) (string, bool, error) {
		value, ok := vars[field]
		return value, ok, nil
	}, depth)
}

func (m *Manager) lookupPromptText(name string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tpl, ok := m.merged[name]
	return tpl, ok
}

func (m *Manager) lookupContextFunction(name string) (ContextFunc, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	fn, ok := m.contextFunctions[name]
	return fn, ok
}

// LoadPrompts 重新加载自定义 prompt 目录并递增缓存 revision（对齐 Python load_prompts）
func (m *Manager) LoadPrompts(customRoot string) error {
	if err := m.LoadFromDir(customRoot); err != nil {
		return err
	}
	m.mu.Lock()
	if customRoot != "" {
		m.customRoot = filepath.Clean(customRoot)
	}
	m.bumpCacheRevisionLocked()
	m.mu.Unlock()
	return nil
}

// SavePrompts 将标记需要保存的 Prompt 写入 custom_prompts/<locale>/<name>.prompt
func (m *Manager) SavePrompts(locale string) error {
	m.mu.RLock()
	root := m.customRoot
	if root == "" {
		root = filepath.Join("data", "custom_prompts")
	}
	normalizedLocale := DefaultLocale
	if locale != "" {
		if normalized, err := NormalizeLocale(locale); err == nil {
			normalizedLocale = normalized
		}
	}
	toSave := make(map[string]string)
	for name, saveLocale := range m.promptSaveLocales {
		if p, ok := m.prompts[name]; ok {
			toSave[name] = saveLocale
			_ = p
		}
	}
	prompts := make(map[string]*Prompt, len(toSave))
	for name := range toSave {
		if p, ok := m.prompts[name]; ok {
			prompts[name] = p
		}
	}
	m.mu.RUnlock()
	return savePromptFiles(root, normalizedLocale, prompts, toSave)
}

// PromptCacheRevision 返回缓存 revision（对齐 Python get_prompt_cache_revision）
func (m *Manager) PromptCacheRevision() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cacheRevision
}

// ReloadIfCacheChanged 比较 revision，变化时重载自定义目录（对齐 _reload_if_prompt_cache_changed）
func (m *Manager) ReloadIfCacheChanged(expected int64) error {
	if m.PromptCacheRevision() == expected {
		return nil
	}
	m.mu.RLock()
	root := m.customRoot
	m.mu.RUnlock()
	if root == "" {
		return nil
	}
	return m.LoadFromDir(root)
}

func (m *Manager) bumpCacheRevisionLocked() {
	m.cacheRevision++
}

// ensureFresh 对齐 MaiBot _reload_if_prompt_cache_changed：
// 自定义目录（.prompt 与 .meta.*）变化时自动重载，渲染路径无需调用方显式刷新
func (m *Manager) ensureFresh() {
	m.mu.RLock()
	root := m.customRoot
	committed := m.customSnap
	m.mu.RUnlock()
	if root == "" {
		return
	}
	snap, err := dirModSnap(root)
	if err != nil || snap == committed {
		return
	}
	if loadErr := m.LoadFromDir(root); loadErr != nil {
		return
	}
}

// applyRegisteredLocked 把运行时注册的 Prompt 重新叠回 merged，避免切换 locale 后丢失
func (m *Manager) applyRegisteredLocked() {
	for name, registered := range m.prompts {
		m.merged[name] = registered.Template()
	}
}

// setCustomRoot 记录 custom_prompts 根目录，供 SavePrompts 使用
func (m *Manager) setCustomRoot(customRoot string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.customRoot = filepath.Clean(customRoot)
}
