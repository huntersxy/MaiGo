package host

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"maibot-go/internal/plugin_runtime/protocol"

	"go.uber.org/zap"
)

// APIEntry API 组件条目，对齐 host/api_registry.py APIEntry
type APIEntry struct {
	Name            string
	PluginID        string
	Description     string
	Version         string
	Public          bool
	Metadata        map[string]any
	Enabled         bool
	HandlerName     string
	Dynamic         bool
	OfflineReason   string
	DisabledSession map[string]struct{}
	TimeoutMS       int64
	FullName        string
	RegistryKey     string
}

// Clone 返回条目的浅拷贝，外部修改元数据不会绕过注册表锁
func (e *APIEntry) Clone() *APIEntry {
	if e == nil {
		return nil
	}
	out := *e
	out.Metadata = cloneAnyMap(e.Metadata)
	out.DisabledSession = make(map[string]struct{}, len(e.DisabledSession))
	for key := range e.DisabledSession {
		out.DisabledSession[key] = struct{}{}
	}
	return &out
}

func newAPIEntry(name, pluginID string, metadata map[string]any) *APIEntry {
	name = strings.TrimSpace(name)
	pluginID = strings.TrimSpace(pluginID)
	entry := &APIEntry{
		Name:            name,
		PluginID:        pluginID,
		Version:         firstNonEmptyText(metadataText(metadata, "version"), "1"),
		Description:     metadataText(metadata, "description"),
		Public:          metadataBool(metadata, "public", false),
		Metadata:        cloneAnyMap(metadata),
		Enabled:         metadataBool(metadata, "enabled", true),
		HandlerName:     firstNonEmptyText(metadataText(metadata, "handler_name"), name),
		Dynamic:         metadataBool(metadata, "dynamic", false),
		OfflineReason:   metadataText(metadata, "offline_reason"),
		DisabledSession: map[string]struct{}{},
	}
	if entry.Metadata == nil {
		entry.Metadata = map[string]any{}
	}
	timeoutMS, _ := NormalizeComponentTimeoutMS(entry.Metadata["timeout_ms"])
	entry.TimeoutMS = timeoutMS
	entry.FullName = pluginID + "." + name
	entry.RegistryKey = BuildAPIRegistryKey(pluginID, name, entry.Version)
	return entry
}

// BuildAPIRegistryKey 构造 API 注册唯一键，对齐 APIRegistry.build_registry_key
func BuildAPIRegistryKey(pluginID, name, version string) string {
	normalizedVersion := strings.TrimSpace(version)
	if normalizedVersion == "" {
		normalizedVersion = "1"
	}
	return strings.TrimSpace(pluginID) + "." + strings.TrimSpace(name) + "@" + normalizedVersion
}

// APIRegistry Host 侧插件 API 动态注册表，对齐 host/api_registry.py
type APIRegistry struct {
	mu   sync.RWMutex
	apis map[string]*APIEntry
}

// NewAPIRegistry 构造空注册表
func NewAPIRegistry() *APIRegistry {
	return &APIRegistry{apis: make(map[string]*APIEntry)}
}

// Clear 清空注册表
func (r *APIRegistry) Clear() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.apis = make(map[string]*APIEntry)
	r.mu.Unlock()
}

// IsAPIComponent 判断组件声明类型是否为 API
func IsAPIComponent(componentType string) bool {
	return strings.EqualFold(strings.TrimSpace(componentType), "API")
}

// RegisterAPI 注册单个 API 条目，返回是否写入
func (r *APIRegistry) RegisterAPI(name, pluginID string, metadata map[string]any) bool {
	if r == nil {
		return false
	}
	entry := newAPIEntry(name, pluginID, metadata)
	if entry.Name == "" {
		zap.L().Warn("插件存在空 API 名称声明，已忽略", zap.String("plugin_id", pluginID))
		return false
	}
	if entry.PluginID == "" {
		return false
	}
	r.mu.Lock()
	if existing := r.apis[entry.RegistryKey]; existing != nil {
		delete(r.apis, entry.RegistryKey)
	}
	r.apis[entry.RegistryKey] = entry
	r.mu.Unlock()
	return true
}

// RegisterPluginAPIs 批量注册插件声明的全部 API，返回注册数量
func (r *APIRegistry) RegisterPluginAPIs(pluginID string, comps []protocol.ComponentDeclaration) int {
	if r == nil {
		return 0
	}
	count := 0
	for _, comp := range comps {
		if !IsAPIComponent(comp.ComponentType) {
			continue
		}
		if r.RegisterAPI(comp.Name, pluginID, comp.Metadata) {
			count++
		}
	}
	return count
}

// ReplacePluginDynamicAPIs 替换插件动态 API 集合，返回注册数与下线数
func (r *APIRegistry) ReplacePluginDynamicAPIs(pluginID string, comps []protocol.ComponentDeclaration, offlineReason string) (int, int) {
	if r == nil {
		return 0, 0
	}
	if strings.TrimSpace(offlineReason) == "" {
		offlineReason = "动态 API 已下线"
	}
	desired := make(map[string]struct{}, len(comps))
	registered := 0
	for _, comp := range comps {
		if !IsAPIComponent(comp.ComponentType) {
			continue
		}
		metadata := cloneAnyMap(comp.Metadata)
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata["dynamic"] = true
		delete(metadata, "offline_reason")
		entry := newAPIEntry(comp.Name, pluginID, metadata)
		if entry.Name == "" {
			continue
		}
		desired[entry.RegistryKey] = struct{}{}
		if r.RegisterAPI(entry.Name, pluginID, metadata) {
			registered++
		}
	}

	offlined := 0
	r.mu.Lock()
	for _, entry := range r.apis {
		if entry.PluginID != strings.TrimSpace(pluginID) || !entry.Dynamic {
			continue
		}
		if _, ok := desired[entry.RegistryKey]; ok {
			continue
		}
		entry.Enabled = false
		entry.OfflineReason = offlineReason
		entry.Metadata["offline_reason"] = offlineReason
		offlined++
	}
	r.mu.Unlock()
	return registered, offlined
}

// RemoveAPIsByPlugin 移除插件的全部 API，返回移除数量
func (r *APIRegistry) RemoveAPIsByPlugin(pluginID string) int {
	if r == nil {
		return 0
	}
	pluginID = strings.TrimSpace(pluginID)
	r.mu.Lock()
	removed := 0
	for key, entry := range r.apis {
		if entry.PluginID == pluginID {
			delete(r.apis, key)
			removed++
		}
	}
	r.mu.Unlock()
	return removed
}

// CheckAPIEnabled 判断条目在指定会话下是否可用
func CheckAPIEnabled(entry *APIEntry, sessionID string) bool {
	if entry == nil {
		return false
	}
	if sessionID != "" {
		if _, disabled := entry.DisabledSession[sessionID]; disabled {
			return false
		}
	}
	return entry.Enabled
}

func splitAPIReference(reference, version string) (string, string) {
	reference = strings.TrimSpace(reference)
	version = strings.TrimSpace(version)
	if reference != "" && version == "" {
		if index := strings.LastIndex(reference, "@"); index > 0 {
			candidateName := strings.TrimSpace(reference[:index])
			candidateVersion := strings.TrimSpace(reference[index+1:])
			if candidateName != "" && candidateVersion != "" {
				return candidateName, candidateVersion
			}
		}
	}
	return reference, version
}

// GetAPIByFullName 按完整名查询 API，对齐 get_api_by_full_name
func (r *APIRegistry) GetAPIByFullName(fullName, version string, enabledOnly bool, sessionID string) *APIEntry {
	if r == nil {
		return nil
	}
	fullName, version = splitAPIReference(fullName, version)
	if fullName == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if version != "" {
		entry := r.apis[fullName+"@"+version]
		if entry == nil || (enabledOnly && !CheckAPIEnabled(entry, sessionID)) {
			return nil
		}
		return entry.Clone()
	}
	var matches []*APIEntry
	for _, entry := range r.apis {
		if entry.FullName != fullName {
			continue
		}
		if enabledOnly && !CheckAPIEnabled(entry, sessionID) {
			continue
		}
		matches = append(matches, entry)
	}
	if len(matches) != 1 {
		return nil
	}
	return matches[0].Clone()
}

// GetAPI 按插件 ID、短名与版本查询
func (r *APIRegistry) GetAPI(pluginID, name, version string, enabledOnly bool) *APIEntry {
	return r.GetAPIByFullName(strings.TrimSpace(pluginID)+"."+strings.TrimSpace(name), version, enabledOnly, "")
}

// APIFilter API 列表过滤条件
type APIFilter struct {
	PluginID    string
	Name        string
	Version     string
	EnabledOnly bool
	SessionID   string
}

// List 查询 API 列表，按 plugin_id/name/version 排序
func (r *APIRegistry) List(filter APIFilter) []APIEntry {
	if r == nil {
		return nil
	}
	name, version := splitAPIReference(filter.Name, filter.Version)
	r.mu.RLock()
	out := make([]APIEntry, 0, len(r.apis))
	for _, entry := range r.apis {
		if filter.PluginID != "" && entry.PluginID != filter.PluginID {
			continue
		}
		if name != "" && entry.Name != name {
			continue
		}
		if version != "" && entry.Version != version {
			continue
		}
		if filter.EnabledOnly && !CheckAPIEnabled(entry, filter.SessionID) {
			continue
		}
		out = append(out, *entry.Clone())
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].PluginID != out[j].PluginID {
			return out[i].PluginID < out[j].PluginID
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version < out[j].Version
	})
	return out
}

// ToggleAPIStatus 设置指定 API 的启用状态；sessionID 非空时只影响该会话
func (r *APIRegistry) ToggleAPIStatus(fullName, version string, enabled bool, sessionID string) bool {
	if r == nil {
		return false
	}
	fullName, version = splitAPIReference(fullName, version)
	r.mu.Lock()
	defer r.mu.Unlock()
	var target *APIEntry
	if version != "" {
		target = r.apis[fullName+"@"+version]
	} else {
		var matches []*APIEntry
		for _, entry := range r.apis {
			if entry.FullName == fullName {
				matches = append(matches, entry)
			}
		}
		if len(matches) == 1 {
			target = matches[0]
		}
	}
	if target == nil {
		return false
	}
	if sessionID != "" {
		if enabled {
			delete(target.DisabledSession, sessionID)
		} else {
			target.DisabledSession[sessionID] = struct{}{}
		}
		return true
	}
	target.Enabled = enabled
	if enabled {
		target.OfflineReason = ""
		delete(target.Metadata, "offline_reason")
	}
	return true
}

// CountByPlugin 返回指定插件注册的 API 数量
func (r *APIRegistry) CountByPlugin(pluginID string) int {
	if r == nil {
		return 0
	}
	pluginID = strings.TrimSpace(pluginID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, entry := range r.apis {
		if entry.PluginID == pluginID {
			count++
		}
	}
	return count
}

// Count 返回当前注册的 API 数量
func (r *APIRegistry) Count() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.apis)
}

// IsAPIVisibleToPlugin 判断 API 是否对调用方可见
func IsAPIVisibleToPlugin(entry *APIEntry, callerPluginID string) bool {
	return entry != nil && (entry.PluginID == callerPluginID || entry.Public)
}

// ResolveAPI 解析 API 引用到唯一可调用条目，对齐 _resolve_api_target
func (r *APIRegistry) ResolveAPI(callerPluginID, apiName, version string) (*APIEntry, string) {
	name, version := splitAPIReference(apiName, version)
	if name == "" {
		return nil, "缺少必要参数 api_name"
	}
	all := r.List(APIFilter{Name: name, Version: version})
	var enabled, disabled []*APIEntry
	hidden := false
	for i := range all {
		entry := &all[i]
		if IsAPIVisibleToPlugin(entry, callerPluginID) {
			if entry.Enabled {
				enabled = append(enabled, entry)
			} else {
				disabled = append(disabled, entry)
			}
		} else {
			hidden = true
		}
	}
	if len(enabled) == 1 {
		return enabled[0], ""
	}
	if len(enabled) > 1 {
		return nil, fmt.Sprintf("API 名称不唯一: %s，请显式指定 version", name)
	}
	if len(disabled) > 0 {
		if len(disabled) == 1 {
			return nil, apiUnavailableError(disabled[0])
		}
		return nil, fmt.Sprintf("API %s 存在多个已下线版本，请显式指定 version", name)
	}
	if hidden {
		return nil, fmt.Sprintf("API %s 未公开，禁止跨插件调用", name)
	}

	if strings.Contains(name, ".") {
		index := strings.LastIndex(name, ".")
		pluginID, shortName := name[:index], name[index+1:]
		entry := r.GetAPI(pluginID, shortName, version, false)
		if entry == nil {
			if version != "" {
				return nil, fmt.Sprintf("未找到版本为 %s 的 API: %s", version, name)
			}
			return nil, fmt.Sprintf("未找到 API: %s", name)
		}
		if !IsAPIVisibleToPlugin(entry, callerPluginID) {
			return nil, fmt.Sprintf("API %s 未公开，禁止跨插件调用", name)
		}
		if !entry.Enabled {
			return nil, apiUnavailableError(entry)
		}
		return entry, ""
	}
	if version != "" {
		return nil, fmt.Sprintf("未找到版本为 %s 的 API: %s", version, name)
	}
	return nil, fmt.Sprintf("未找到 API: %s", name)
}

func apiUnavailableError(entry *APIEntry) string {
	if entry == nil {
		return "API 当前不可用"
	}
	if entry.OfflineReason != "" {
		return entry.OfflineReason
	}
	return fmt.Sprintf("API %s 当前不可用", entry.RegistryKey)
}

// ResolveAPIToggle 解析需要启用或禁用的 API，对齐 _resolve_api_toggle_target
func (r *APIRegistry) ResolveAPIToggle(name, version string) (*APIEntry, string) {
	name, version = splitAPIReference(name, version)
	if name == "" {
		return nil, "缺少必要参数 name"
	}
	if strings.Contains(name, ".") {
		index := strings.LastIndex(name, ".")
		pluginID, shortName := name[:index], name[index+1:]
		entry := r.GetAPI(pluginID, shortName, version, false)
		if entry != nil {
			return entry, ""
		}
		return nil, fmt.Sprintf("未找到 API: %s", name)
	}
	matches := r.List(APIFilter{Name: name, Version: version})
	if len(matches) == 1 {
		return &matches[0], ""
	}
	if len(matches) > 1 {
		return nil, fmt.Sprintf("API 名称不唯一: %s，请使用 plugin_id.api_name 或显式指定 version", name)
	}
	return nil, fmt.Sprintf("未找到 API: %s", name)
}

// SerializeAPIEntry API 元信息能力返回值
func SerializeAPIEntry(entry *APIEntry) map[string]any {
	if entry == nil {
		return nil
	}
	return map[string]any{
		"name":           entry.Name,
		"full_name":      entry.FullName,
		"plugin_id":      entry.PluginID,
		"description":    entry.Description,
		"version":        entry.Version,
		"public":         entry.Public,
		"enabled":        entry.Enabled,
		"dynamic":        entry.Dynamic,
		"offline_reason": entry.OfflineReason,
		"metadata":       cloneAnyMap(entry.Metadata),
	}
}

// SerializeAPIComponentEntry API 条目的通用组件视图
func SerializeAPIComponentEntry(entry *APIEntry) map[string]any {
	if entry == nil {
		return nil
	}
	return map[string]any{
		"name":      entry.Name,
		"full_name": entry.FullName,
		"type":      "API",
		"enabled":   entry.Enabled,
		"metadata":  cloneAnyMap(entry.Metadata),
	}
}

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func metadataBool(metadata map[string]any, key string, fallback bool) bool {
	if metadata == nil {
		return fallback
	}
	if raw, ok := metadata[key].(bool); ok {
		return raw
	}
	return fallback
}
