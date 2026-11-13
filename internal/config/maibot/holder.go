package maibot

import (
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

// ReloadScope 表示本次热重载触发的配置文件范围（对齐 MaiBot ConfigManager changed_scopes）
type ReloadScope string

const (
	ReloadScopeBot   ReloadScope = "bot"
	ReloadScopeModel ReloadScope = "model"
)

// ReloadCallback 在 TOML 热重载成功并写入 Holder 后调用
type ReloadCallback func(cfg *InstanceConfig, scopes []ReloadScope)

// reloadRegistration 一条热重载回调注册项；sections 为空表示订阅全部变更
type reloadRegistration struct {
	callback ReloadCallback
	sections []string
}

// InstanceHolder 持有当前实例 TOML 快照，供热重载与回调分发
//
// 与 internal/config.Store（数据库动态配置）并存，互不覆盖
type InstanceHolder struct {
	dir string

	mu       sync.RWMutex
	reloadMu sync.Mutex
	cfg      *InstanceConfig

	cbMu      sync.RWMutex
	callbacks []reloadRegistration

	watchMu    sync.RWMutex
	watchStats *configWatcherStats
}

// NewInstanceHolder 从实例目录做首次 LoadFromInstanceDir
func NewInstanceHolder(instanceRoot string) (*InstanceHolder, error) {
	dir := filepath.Clean(instanceRoot)
	cfg, err := LoadFromInstanceDir(dir)
	if err != nil {
		return nil, err
	}
	return &InstanceHolder{dir: dir, cfg: cfg}, nil
}

// Dir 返回实例根目录
func (h *InstanceHolder) Dir() string {
	return h.dir
}

// Config 返回当前配置快照指针，只读使用，勿修改字段
func (h *InstanceHolder) Config() *InstanceConfig {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cfg
}

// RegisterReloadCallback 注册热重载成功后的回调，订阅全部配置节
func (h *InstanceHolder) RegisterReloadCallback(cb ReloadCallback) {
	h.RegisterReloadCallbackForSections(cb)
}

// RegisterReloadCallbackForSections 注册只关心指定 bot 配置节的回调
//
// 对齐 Python register_reload_callback(sections=...)：节名按点号分段做前缀匹配，
// 未声明节（空列表）时订阅全部变更；model 范围的重载不会通知声明了节的回调。
func (h *InstanceHolder) RegisterReloadCallbackForSections(cb ReloadCallback, sections ...string) {
	if cb == nil {
		return
	}
	h.cbMu.Lock()
	h.callbacks = append(h.callbacks, reloadRegistration{
		callback: cb,
		sections: normalizeReloadSections(sections),
	})
	h.cbMu.Unlock()
}

// UnregisterReloadCallback 注销指定回调，返回是否命中
func (h *InstanceHolder) UnregisterReloadCallback(cb ReloadCallback) bool {
	if cb == nil {
		return false
	}
	h.cbMu.Lock()
	defer h.cbMu.Unlock()
	removed := false
	kept := make([]reloadRegistration, 0, len(h.callbacks))
	for _, registration := range h.callbacks {
		if !removed && reflect.ValueOf(registration.callback).Pointer() == reflect.ValueOf(cb).Pointer() {
			removed = true
			continue
		}
		kept = append(kept, registration)
	}
	h.callbacks = kept
	return removed
}

// normalizeReloadSections 规范化为小写点号路径并去重
func normalizeReloadSections(sections []string) []string {
	normalized := make([]string, 0, len(sections))
	seen := map[string]struct{}{}
	for _, section := range sections {
		parts := make([]string, 0)
		for _, part := range strings.Split(strings.ToLower(strings.TrimSpace(section)), ".") {
			if part = strings.TrimSpace(part); part != "" {
				parts = append(parts, part)
			}
		}
		if len(parts) == 0 {
			continue
		}
		dotted := strings.Join(parts, ".")
		if _, exists := seen[dotted]; exists {
			continue
		}
		seen[dotted] = struct{}{}
		normalized = append(normalized, dotted)
	}
	return normalized
}

// reloadSectionMatches 判断声明的节是否命中变更节：按点号分段取共同前缀比较
func reloadSectionMatches(declared, changed string) bool {
	declaredParts := strings.Split(declared, ".")
	changedParts := strings.Split(changed, ".")
	common := len(declaredParts)
	if len(changedParts) < common {
		common = len(changedParts)
	}
	for index := 0; index < common; index++ {
		if declaredParts[index] != changedParts[index] {
			return false
		}
	}
	return true
}

// changedBotSections 对比新旧 bot 配置，返回变化的顶层节名
//
// 对比失败返回 nil，调用方按「变更节未知」保守通知。
func changedBotSections(oldConfig, newConfig *InstanceConfig) []string {
	if oldConfig == nil || newConfig == nil {
		return nil
	}
	oldData, err := botSectionSnapshot(oldConfig.Bot)
	if err != nil {
		return nil
	}
	newData, err := botSectionSnapshot(newConfig.Bot)
	if err != nil {
		return nil
	}
	changed := make([]string, 0, len(newData))
	for _, name := range botSectionNames() {
		if !reflect.DeepEqual(oldData[name], newData[name]) {
			changed = append(changed, name)
		}
	}
	return changed
}

func botSectionSnapshot(cfg BotConfigFile) (map[string]any, error) {
	raw, err := toml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := toml.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

// botSectionNames 返回 BotConfigFile 的顶层 TOML 节名
func botSectionNames() []string {
	fields := reflect.TypeOf(BotConfigFile{})
	names := make([]string, 0, fields.NumField())
	for index := 0; index < fields.NumField(); index++ {
		tag := fields.Field(index).Tag.Get("toml")
		name, _, _ := strings.Cut(tag, ",")
		if strings.TrimSpace(name) == "" {
			continue
		}
		names = append(names, name)
	}
	return names
}

// setConfigWatcherStats 记录最近一次启动的配置监听计数；同一 Holder 多次启动时后者替换前者
func (h *InstanceHolder) setConfigWatcherStats(stats *configWatcherStats) {
	h.watchMu.Lock()
	h.watchStats = stats
	h.watchMu.Unlock()
}

// ConfigWatcherStats 返回配置监听累计计数；未启动时返回零值
func (h *InstanceHolder) ConfigWatcherStats() ConfigWatcherStats {
	h.watchMu.RLock()
	stats := h.watchStats
	h.watchMu.RUnlock()
	return stats.snapshot()
}

// ReloadFromDisk 按 scopes 只重新加载变更的 TOML；失败时保留旧快照
func (h *InstanceHolder) ReloadFromDisk(scopes []ReloadScope) error {
	if len(scopes) == 0 {
		return nil
	}
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	h.mu.RLock()
	base := h.cfg
	h.mu.RUnlock()
	cfg, err := LoadFromInstanceDirScoped(h.dir, base, scopes, LoadOptions{})
	if err != nil {
		return err
	}
	h.mu.Lock()
	previous := h.cfg
	h.cfg = cfg
	h.mu.Unlock()
	h.dispatchReload(previous, cfg, scopes)
	return nil
}

// dispatchReload 按注册节过滤后通知回调
//
// 未声明节的回调始终通知；声明了节的回调只在 bot 范围内且节命中时通知，
// 变更节无法比较时保守通知，与 Python _is_registration_interested 一致。
func (h *InstanceHolder) dispatchReload(previous, cfg *InstanceConfig, scopes []ReloadScope) {
	h.cbMu.RLock()
	registrations := append([]reloadRegistration(nil), h.callbacks...)
	h.cbMu.RUnlock()

	scopeCopy := append([]ReloadScope(nil), scopes...)
	botChanged := false
	for _, scope := range scopeCopy {
		if scope == ReloadScopeBot {
			botChanged = true
			break
		}
	}
	var changedSections []string
	if botChanged {
		changedSections = changedBotSections(previous, cfg)
	}
	for _, registration := range registrations {
		if len(registration.sections) > 0 {
			if !botChanged {
				continue
			}
			if changedSections != nil {
				interested := false
				for _, declared := range registration.sections {
					for _, changed := range changedSections {
						if reloadSectionMatches(declared, changed) {
							interested = true
							break
						}
					}
					if interested {
						break
					}
				}
				if !interested {
					continue
				}
			}
		}
		cb := registration.callback
		func() {
			defer func() { _ = recover() }()
			cb(cfg, scopeCopy)
		}()
	}
}

func (h *InstanceHolder) botConfigPath() string {
	return filepath.Join(h.dir, configSubdir, botConfigFileName)
}

func (h *InstanceHolder) modelConfigPath() string {
	return filepath.Join(h.dir, configSubdir, modelConfigFileName)
}
