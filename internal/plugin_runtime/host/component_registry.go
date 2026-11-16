package host

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.uber.org/zap"

	"MaiGo/internal/plugin_runtime/protocol"
)

// 组件聊天类型适用范围，对齐 component_registry.ComponentChatScope
const (
	ComponentChatScopeAll     = "all"
	ComponentChatScopeGroup   = "group"
	ComponentChatScopePrivate = "private"
)

// ComponentEntry Host 侧通用组件条目，对齐 component_registry.ComponentEntry。
type ComponentEntry struct {
	Name            string
	FullName        string
	ComponentType   string
	PluginID        string
	Metadata        map[string]any
	Enabled         bool
	TimeoutMS       int64
	ChatScope       string
	AllowedSession  []string
	disabledSession map[string]bool

	// TOOL
	Platforms     []string
	Description   string
	Parameters    []any
	ParametersRaw any
	InvokeMethod  string

	// EVENT_HANDLER
	EventType string
	Weight    int
	Intercept bool

	// HOOK_HANDLER
	HookName        string
	HookMode        string
	HookOrder       string
	HookErrorPolicy string

	// COMMAND
	Aliases               []string
	CommandPattern        string
	Permission            string
	InterceptMessageLevel int
	pattern               *regexp.Regexp

	// MESSAGE_GATEWAY
	RouteType string
	Platform  string
	Protocol  string
	AccountID string
	Scope     string

	// HOME_CARD
	Title string
	Width string
	Order int

	// REPLY_EXTENSION
	ParametersSchema map[string]any
	Priority         int

	// localHandler 宿主内置命令的本地执行器，非插件注册组件为空
	localHandler func(ctx context.Context, execCtx CommandExecutionContext) CommandExecutionResult
}

// ComponentFilter 组件查询的会话上下文，对齐 check_component_enabled 的可选参数。
type ComponentFilter struct {
	SessionID   string
	IsGroupChat *bool
	GroupID     string
	Platform    string
}

// ComponentStatus 组件注册统计，对齐 component_registry.StatusDict。
type ComponentStatus struct {
	Total          int
	Command        int
	Tool           int
	EventHandler   int
	HookHandler    int
	MessageGateway int
	ReplyExtension int
	Plugins        int
}

// LLMToolInfo 供 LLM 使用的工具描述，对齐 get_tools_for_llm 的字典结构。
type LLMToolInfo struct {
	Name          string
	Description   string
	Parameters    any
	ParametersRaw any
	Enabled       bool
	PluginID      string
}

// ComponentRegistry 按类型/插件索引的通用组件注册表。
//
// 索引承载全部八类组件声明，与 Python ComponentRegistry 一致；EVENT_HANDLER 与
// HOOK_HANDLER 的实际分发仍由 Host.eventHandlers / hooks.Registry 负责，本索引保存其声明
// 元数据、启用状态与注册顺序，供组件清单、统计和启停查询使用。
type ComponentRegistry struct {
	mu         sync.RWMutex
	components map[string]*ComponentEntry
	byType     map[string]map[string]*ComponentEntry
	byPlugin   map[string][]*ComponentEntry
	// order 保留全部组件的注册顺序，对齐 Python dict 的插入顺序
	order []string
}

// NewComponentRegistry 构造空组件注册表
func NewComponentRegistry() *ComponentRegistry {
	registry := &ComponentRegistry{
		components: make(map[string]*ComponentEntry),
		byType:     make(map[string]map[string]*ComponentEntry),
		byPlugin:   make(map[string][]*ComponentEntry),
	}
	for _, componentType := range []string{
		ComponentTypeCommand,
		ComponentTypeTool,
		ComponentTypeEventHandler,
		ComponentTypeHookHandler,
		ComponentTypeMessageGateway,
		ComponentTypeHomeCard,
		ComponentTypeReplyExtension,
	} {
		registry.byType[componentType] = make(map[string]*ComponentEntry)
	}
	return registry
}

// buildAuxiliaryComponents 校验并构造全部组件声明，跳过 API（API 有独立注册表）
//
// EVENT_HANDLER / HOOK_HANDLER 同时写入本索引，执行层仍由 Host.eventHandlers 与 hooks.Registry 负责，
// 与 Python 统一索引保持组件清单、统计与顺序一致。
func buildAuxiliaryComponents(rec PluginRecord, comps []protocol.ComponentDeclaration) ([]*ComponentEntry, error) {
	pluginID := strings.TrimSpace(rec.PluginID)
	if pluginID == "" {
		return nil, fmt.Errorf("plugin_id 不能为空")
	}
	entries := make([]*ComponentEntry, 0, len(comps))
	seen := make(map[string]*ComponentEntry, len(comps))
	for _, comp := range comps {
		componentType, err := normalizeComponentType(comp.ComponentType)
		if err != nil {
			return nil, err
		}
		if componentType == ComponentTypeAPI {
			continue
		}
		entry, err := buildComponentEntry(pluginID, componentType, comp)
		if err != nil {
			return nil, err
		}
		if entry == nil {
			continue
		}
		if previous, exists := seen[entry.FullName]; exists {
			if entry.ComponentType == ComponentTypeReplyExtension || previous.ComponentType == ComponentTypeReplyExtension {
				return nil, fmt.Errorf("回复扩展组件名称重复：%s", entry.FullName)
			}
		}
		seen[entry.FullName] = entry
		entries = append(entries, entry)
	}
	return entries, nil
}

// buildComponentEntry 构造单个通用组件条目，对齐 _build_component_entry
func buildComponentEntry(pluginID, componentType string, comp protocol.ComponentDeclaration) (*ComponentEntry, error) {
	metadata := cloneComponentMetadata(comp.Metadata)
	chatScope := normalizeComponentChatScope(comp.ChatScope)
	allowedSession := normalizeComponentAllowedSession(comp.AllowedSession)
	timeoutMS, err := normalizeComponentTimeoutMS(metadata["timeout_ms"])
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(comp.Name)
	entry := &ComponentEntry{
		Name:            name,
		FullName:        pluginID + "." + comp.Name,
		ComponentType:   componentType,
		PluginID:        pluginID,
		Metadata:        metadata,
		Enabled:         metadataEnabled(metadata),
		TimeoutMS:       timeoutMS,
		ChatScope:       chatScope,
		AllowedSession:  allowedSession,
		disabledSession: make(map[string]bool),
	}

	switch componentType {
	case ComponentTypeTool:
		platforms, err := normalizeComponentPlatforms(metadata["platforms"], "Tool")
		if err != nil {
			return nil, err
		}
		entry.Platforms = platforms
		entry.Description = firstNonEmpty(
			metadataText(metadata, "description"),
			metadataText(metadata, "brief_description"),
			"工具 "+name,
		)
		if raw, ok := metadata["parameters"]; ok {
			if list, ok := raw.([]any); ok {
				entry.Parameters = list
			}
		}
		if raw, exists := metadata["parameters_raw"]; exists {
			entry.ParametersRaw = raw
		} else {
			// 对齐 ToolEntry.parameters_raw 默认空对象
			entry.ParametersRaw = map[string]any{}
		}
		entry.InvokeMethod = firstNonEmpty(metadataText(metadata, "invoke_method"), "plugin.invoke_tool")
		return entry, nil
	case ComponentTypeCommand:
		entry.Aliases = normalizeCommandAliases(metadata["aliases"])
		entry.CommandPattern = metadataText(metadata, "command_pattern")
		entry.Description = metadataText(metadata, "description")
		entry.Permission = firstNonEmpty(metadataText(metadata, "permission"), "public")
		if level, err := normalizeComponentTimeoutMS(metadata["intercept_message_level"]); err == nil {
			entry.InterceptMessageLevel = int(level)
		}
		if entry.CommandPattern != "" {
			pattern, err := compileCommandPattern(entry.CommandPattern)
			if err != nil {
				zap.L().Warn("命令正则编译失败", zap.String("command", entry.FullName), zap.Error(err))
			} else {
				entry.pattern = pattern
			}
		}
		return entry, nil
	case ComponentTypeMessageGateway:
		routeType, err := normalizeMessageGatewayRouteType(metadata["route_type"])
		if err != nil {
			return nil, err
		}
		entry.RouteType = routeType
		entry.Platform = metadataText(metadata, "platform")
		entry.Protocol = metadataText(metadata, "protocol")
		entry.AccountID = metadataText(metadata, "account_id")
		entry.Scope = metadataText(metadata, "scope")
		return entry, nil
	case ComponentTypeHomeCard:
		entry.Title = firstNonEmpty(metadataText(metadata, "title"), name)
		entry.Description = metadataText(metadata, "description")
		entry.Width = normalizeHomeCardWidth(metadata["width"])
		entry.Order = normalizeHomeCardOrder(metadata["order"])
		return entry, nil
	case ComponentTypeEventHandler:
		entry.EventType = metadataEventType(metadata)
		entry.Weight = metadataInt(metadata, "weight")
		entry.Intercept = metadataTruthy(metadata, "intercept_message")
		return entry, nil
	case ComponentTypeHookHandler:
		hookName := metadataText(metadata, "hook")
		if hookName == "" {
			return nil, fmt.Errorf("HookHandler 的 hook 名称不能为空")
		}
		mode, err := normalizeHookHandlerMode(metadata)
		if err != nil {
			return nil, err
		}
		order, err := normalizeHookHandlerOrder(metadata)
		if err != nil {
			return nil, err
		}
		errorPolicy, err := normalizeHookHandlerErrorPolicy(metadata)
		if err != nil {
			return nil, err
		}
		entry.HookName = hookName
		entry.HookMode = string(mode)
		entry.HookOrder = string(order)
		entry.HookErrorPolicy = errorPolicy
		return entry, nil
	case ComponentTypeReplyExtension:
		if name == "" || !isValidChatScope(comp.ChatScope) {
			return nil, fmt.Errorf("回复扩展名称不能为空，chat_scope 必须是 all/group/private")
		}
		platforms, err := normalizeComponentPlatforms(metadata["platforms"], "ReplyExtension")
		if err != nil {
			return nil, err
		}
		entry.Platforms = platforms
		schema, err := validateReplyParametersSchema(metadata["parameters_schema"])
		if err != nil {
			return nil, err
		}
		entry.ParametersSchema = schema
		description, ok := metadata["description"].(string)
		if !ok && metadata["description"] != nil {
			return nil, fmt.Errorf("回复扩展 description 必须是字符串，priority 必须是整数")
		}
		priority := 0
		if rawPriority, hasPriority := metadata["priority"]; hasPriority {
			if rawPriority == nil {
				return nil, fmt.Errorf("回复扩展 description 必须是字符串，priority 必须是整数")
			}
			var ok bool
			priority, ok = strictInt(rawPriority, 0)
			if !ok {
				return nil, fmt.Errorf("回复扩展 description 必须是字符串，priority 必须是整数")
			}
		}
		entry.Description = description
		entry.Priority = priority
		return entry, nil
	default:
		return nil, fmt.Errorf("组件类型 %s 不存在", comp.ComponentType)
	}
}

// ReplacePluginComponents 整体替换一个插件的通用组件集合
func (r *ComponentRegistry) ReplacePluginComponents(pluginID string, entries []*ComponentEntry) {
	pluginID = strings.TrimSpace(pluginID)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removeLocked(pluginID)
	for _, entry := range entries {
		r.addLocked(entry)
	}
}

// ValidatePluginComponents 在不改动既有状态的前提下校验命名空间冲突
func (r *ComponentRegistry) ValidatePluginComponents(pluginID string, entries []*ComponentEntry) error {
	pluginID = strings.TrimSpace(pluginID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, entry := range entries {
		existing := r.components[entry.FullName]
		if existing == nil || existing.PluginID == pluginID {
			continue
		}
		if entry.ComponentType == ComponentTypeReplyExtension || existing.ComponentType == ComponentTypeReplyExtension {
			return fmt.Errorf("回复扩展命名空间与另一插件冲突：%s", existing.FullName)
		}
	}
	return nil
}

// RemoveComponentsByPlugin 移除某插件的全部通用组件，返回移除数量
func (r *ComponentRegistry) RemoveComponentsByPlugin(pluginID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.removeLocked(strings.TrimSpace(pluginID))
}

func (r *ComponentRegistry) removeLocked(pluginID string) int {
	// detachEntryLocked 会原地压缩 byPlugin 切片，先取快照避免遍历时跳过条目
	comps := append([]*ComponentEntry(nil), r.byPlugin[pluginID]...)
	for _, comp := range comps {
		r.removeEntryLocked(comp.FullName)
	}
	delete(r.byPlugin, pluginID)
	return len(comps)
}

func (r *ComponentRegistry) addLocked(entry *ComponentEntry) {
	existing := r.components[entry.FullName]
	if existing != nil {
		r.detachEntryLocked(existing)
	}
	r.components[entry.FullName] = entry
	if typeDict := r.byType[entry.ComponentType]; typeDict != nil {
		typeDict[entry.FullName] = entry
	}
	r.byPlugin[entry.PluginID] = append(r.byPlugin[entry.PluginID], entry)
	if existing == nil {
		r.order = append(r.order, entry.FullName)
	}
}

// removeEntryLocked 从全部索引移除组件（含注册顺序），用于插件整体注销
func (r *ComponentRegistry) removeEntryLocked(fullName string) {
	existing := r.components[fullName]
	if existing == nil {
		return
	}
	r.detachEntryLocked(existing)
	delete(r.components, fullName)
	r.removeOrderLocked(fullName)
}

// detachEntryLocked 只摘除类型/插件索引，保留注册顺序，对齐 Python 同名覆盖
func (r *ComponentRegistry) detachEntryLocked(existing *ComponentEntry) {
	list := r.byPlugin[existing.PluginID]
	for index, comp := range list {
		if comp == existing {
			list = append(list[:index], list[index+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(r.byPlugin, existing.PluginID)
	} else {
		r.byPlugin[existing.PluginID] = list
	}
	if typeDict := r.byType[existing.ComponentType]; typeDict != nil {
		delete(typeDict, existing.FullName)
	}
}

func (r *ComponentRegistry) removeOrderLocked(fullName string) {
	for index, name := range r.order {
		if name == fullName {
			r.order = append(r.order[:index], r.order[index+1:]...)
			return
		}
	}
}

// RegisterLocalComponent 注册宿主内置命令组件，与插件组件走同一套查询与匹配链路
func (r *ComponentRegistry) RegisterLocalComponent(entry *ComponentEntry) {
	if r == nil || entry == nil || entry.FullName == "" {
		return
	}
	if entry.disabledSession == nil {
		entry.disabledSession = make(map[string]bool)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addLocked(entry)
}

// GetComponent 按全名查询组件
func (r *ComponentRegistry) GetComponent(fullName string) *ComponentEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.components[strings.TrimSpace(fullName)]
}

// ComponentEnabled 查询组件在指定会话下是否启用，found=false 表示组件未登记
func (r *ComponentRegistry) ComponentEnabled(fullName, sessionID string) (enabled bool, found bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry := r.components[strings.TrimSpace(fullName)]
	if entry == nil {
		return false, false
	}
	return checkComponentEnabled(entry, ComponentFilter{SessionID: strings.TrimSpace(sessionID)}), true
}

// ComponentsByType 按类型查询组件
func (r *ComponentRegistry) ComponentsByType(componentType string, enabledOnly bool, filter ComponentFilter) ([]*ComponentEntry, error) {
	normalized, err := normalizeComponentType(componentType)
	if err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*ComponentEntry, 0, len(r.order))
	for _, fullName := range r.order {
		entry := r.components[fullName]
		if entry == nil {
			continue
		}
		if entry.ComponentType != normalized {
			continue
		}
		if enabledOnly && !checkComponentEnabled(entry, filter) {
			continue
		}
		out = append(out, entry)
	}
	return out, nil
}

// ComponentsByPlugin 按插件查询组件
func (r *ComponentRegistry) ComponentsByPlugin(pluginID string, enabledOnly bool, sessionID string) []*ComponentEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	comps := r.byPlugin[strings.TrimSpace(pluginID)]
	out := make([]*ComponentEntry, 0, len(comps))
	for _, entry := range comps {
		if enabledOnly && !checkComponentEnabled(entry, ComponentFilter{SessionID: sessionID}) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// HasCommands 是否注册了任何命令组件
func (r *ComponentRegistry) HasCommands() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, fullName := range r.order {
		if entry := r.components[fullName]; entry != nil && entry.ComponentType == ComponentTypeCommand {
			return true
		}
	}
	return false
}

// HasSessionScopedCommands 是否存在依赖会话白名单的命令
func (r *ComponentRegistry) HasSessionScopedCommands() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, fullName := range r.order {
		entry := r.components[fullName]
		if entry != nil && entry.ComponentType == ComponentTypeCommand && len(entry.AllowedSession) > 0 {
			return true
		}
	}
	return false
}

// FindCommandByText 通过命令正则或别名匹配文本，返回组件与命名捕获组
func (r *ComponentRegistry) FindCommandByText(text, sessionID string) (*ComponentEntry, map[string]string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, fullName := range r.order {
		entry := r.components[fullName]
		if entry == nil || entry.ComponentType != ComponentTypeCommand || !checkComponentEnabled(entry, ComponentFilter{SessionID: sessionID}) {
			continue
		}
		if entry.pattern != nil {
			if match := entry.pattern.FindStringSubmatch(text); match != nil {
				return entry, commandNamedGroups(entry.pattern, match)
			}
		}
		for _, alias := range entry.Aliases {
			if strings.HasPrefix(text, alias) {
				return entry, map[string]string{}
			}
		}
	}
	return nil, nil
}

// Tools 查询全部 Tool（含 legacy Action）
func (r *ComponentRegistry) Tools(enabledOnly bool, sessionID string) []*ComponentEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*ComponentEntry, 0, len(r.order))
	for _, fullName := range r.order {
		entry := r.components[fullName]
		if entry == nil || entry.ComponentType != ComponentTypeTool {
			continue
		}
		if enabledOnly && !checkComponentEnabled(entry, ComponentFilter{SessionID: sessionID}) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// ToolsForLLM 返回可供 LLM 使用的工具描述
func (r *ComponentRegistry) ToolsForLLM(enabledOnly bool, sessionID string) []LLMToolInfo {
	tools := r.Tools(enabledOnly, sessionID)
	out := make([]LLMToolInfo, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.ParametersRaw
		if rawMap, ok := tool.ParametersRaw.(map[string]any); !ok || len(rawMap) == 0 {
			if schema := tool.BuildParametersSchema(); schema != nil {
				parameters = schema
			} else {
				parameters = map[string]any{}
			}
		}
		out = append(out, LLMToolInfo{
			Name:          tool.FullName,
			Description:   tool.Description,
			Parameters:    parameters,
			ParametersRaw: tool.ParametersRaw,
			Enabled:       tool.Enabled,
			PluginID:      tool.PluginID,
		})
	}
	return out
}

// BuildParametersSchema 归一化参数声明为对象级 JSON Schema
func (e *ComponentEntry) BuildParametersSchema() map[string]any {
	if raw, ok := e.ParametersRaw.(map[string]any); ok && len(raw) > 0 {
		_, hasProperties := raw["properties"]
		if raw["type"] == "object" || hasProperties {
			return cloneAnyMap(raw)
		}
		properties := make(map[string]any)
		required := make([]string, 0)
		for propertyName, propertySchema := range raw {
			schema, ok := propertySchema.(map[string]any)
			if !ok {
				continue
			}
			copySchema := cloneAnyMap(schema)
			if boolValue(copySchema["required"]) {
				required = append(required, propertyName)
			}
			delete(copySchema, "required")
			properties[propertyName] = copySchema
		}
		result := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			result["required"] = required
		}
		return result
	}
	if len(e.Parameters) > 0 {
		properties := make(map[string]any)
		required := make([]string, 0)
		for _, rawParameter := range e.Parameters {
			parameter, ok := rawParameter.(map[string]any)
			if !ok || !metadataTruthy(parameter, "name") {
				continue
			}
			name := metadataText(parameter, "name")
			if name == "" {
				continue
			}
			if boolValue(parameter["required"]) {
				required = append(required, name)
			}
			property := make(map[string]any, len(parameter))
			for key, value := range parameter {
				if key == "name" || key == "required" || key == "param_type" {
					continue
				}
				property[key] = value
			}
			typeName := ""
			if metadataTruthy(parameter, "type") {
				typeName = metadataText(parameter, "type")
			} else if _, exists := parameter["type"]; !exists {
				typeName = metadataText(parameter, "param_type")
			}
			if typeName == "" {
				typeName = "string"
			}
			property["type"] = typeName
			properties[name] = property
		}
		result := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			result["required"] = required
		}
		return result
	}
	return nil
}

// MessageGateway 按插件与名称查询单个消息网关
func (r *ComponentRegistry) MessageGateway(pluginID, name string, enabledOnly bool, sessionID string) *ComponentEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry := r.components[strings.TrimSpace(pluginID)+"."+name]
	if entry == nil || entry.ComponentType != ComponentTypeMessageGateway {
		return nil
	}
	if enabledOnly && !checkComponentEnabled(entry, ComponentFilter{SessionID: sessionID}) {
		return nil
	}
	return entry
}

// MessageGateways 查询消息网关列表
func (r *ComponentRegistry) MessageGateways(pluginID, platform, routeType string, enabledOnly bool, sessionID string) []*ComponentEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	normalizedPlatform := strings.TrimSpace(platform)
	normalizedRouteType := strings.ToLower(strings.TrimSpace(routeType))
	out := make([]*ComponentEntry, 0)
	for _, fullName := range r.order {
		entry := r.components[fullName]
		if entry == nil || entry.ComponentType != ComponentTypeMessageGateway {
			continue
		}
		if pluginID != "" && entry.PluginID != pluginID {
			continue
		}
		if enabledOnly && !checkComponentEnabled(entry, ComponentFilter{SessionID: sessionID}) {
			continue
		}
		if normalizedPlatform != "" && entry.Platform != normalizedPlatform {
			continue
		}
		if normalizedRouteType != "" && entry.RouteType != normalizedRouteType {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// SetCommandAliases 更新宿主内置命令的可匹配别名，供插件管理命令动态别名使用
func (r *ComponentRegistry) SetCommandAliases(fullName string, aliases []string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.components[strings.TrimSpace(fullName)]
	if entry == nil || entry.ComponentType != ComponentTypeCommand {
		return false
	}
	entry.Aliases = append([]string(nil), aliases...)
	return true
}

// SetComponentEnabled 设置组件启用状态；sessionID 非空时仅影响该会话
func (r *ComponentRegistry) SetComponentEnabled(fullName string, enabled bool, sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.components[strings.TrimSpace(fullName)]
	if entry == nil {
		return false
	}
	if sessionID != "" {
		if enabled {
			delete(entry.disabledSession, sessionID)
		} else {
			entry.disabledSession[sessionID] = true
		}
	} else {
		entry.Enabled = enabled
	}
	return true
}

// SetPluginEnabled 批量设置某插件组件的启用状态
func (r *ComponentRegistry) SetPluginEnabled(pluginID string, enabled bool, sessionID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	comps := r.byPlugin[strings.TrimSpace(pluginID)]
	for _, entry := range comps {
		if sessionID != "" {
			if enabled {
				delete(entry.disabledSession, sessionID)
			} else {
				entry.disabledSession[sessionID] = true
			}
		} else {
			entry.Enabled = enabled
		}
	}
	return len(comps)
}

// Status 返回组件统计
func (r *ComponentRegistry) Status() ComponentStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	status := ComponentStatus{
		Total:          len(r.components),
		Command:        len(r.byType[ComponentTypeCommand]),
		EventHandler:   len(r.byType[ComponentTypeEventHandler]),
		HookHandler:    len(r.byType[ComponentTypeHookHandler]),
		MessageGateway: len(r.byType[ComponentTypeMessageGateway]),
		ReplyExtension: len(r.byType[ComponentTypeReplyExtension]),
		Plugins:        len(r.byPlugin),
	}
	for _, entry := range r.components {
		if entry.ComponentType == ComponentTypeTool {
			status.Tool++
		}
	}
	return status
}

// checkComponentEnabled 对齐 check_component_enabled
func checkComponentEnabled(entry *ComponentEntry, filter ComponentFilter) bool {
	if entry == nil {
		return false
	}
	if filter.Platform != "" && len(entry.Platforms) > 0 {
		if !slices.Contains(entry.Platforms, strings.ToLower(strings.TrimSpace(filter.Platform))) {
			return false
		}
	}
	if filter.SessionID != "" && entry.disabledSession[filter.SessionID] {
		return false
	}
	if filter.IsGroupChat != nil {
		isGroup := *filter.IsGroupChat
		if entry.ChatScope == ComponentChatScopeGroup && !isGroup {
			return false
		}
		if entry.ChatScope == ComponentChatScopePrivate && isGroup {
			return false
		}
	}
	if len(entry.AllowedSession) > 0 {
		candidates := map[string]bool{}
		if session := strings.TrimSpace(filter.SessionID); session != "" {
			candidates[session] = true
		}
		if group := strings.TrimSpace(filter.GroupID); group != "" {
			candidates[group] = true
		}
		if filter.Platform != "" && filter.GroupID != "" {
			candidates[filter.Platform+":"+filter.GroupID] = true
		}
		matched := false
		for _, allowed := range entry.AllowedSession {
			if candidates[allowed] {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return entry.Enabled
}

func normalizeComponentChatScope(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ComponentChatScopeGroup:
		return ComponentChatScopeGroup
	case ComponentChatScopePrivate:
		return ComponentChatScopePrivate
	default:
		return ComponentChatScopeAll
	}
}

func isValidChatScope(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ComponentChatScopeAll, ComponentChatScopeGroup, ComponentChatScopePrivate:
		return true
	default:
		return false
	}
}

func normalizeComponentAllowedSession(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if session := strings.TrimSpace(item); session != "" {
			out = append(out, session)
		}
	}
	return out
}

func normalizeComponentTimeoutMS(raw any) (int64, error) {
	if raw == nil {
		return 0, nil
	}
	var timeout int64
	switch typed := raw.(type) {
	case int:
		timeout = int64(typed)
	case int8:
		timeout = int64(typed)
	case int16:
		timeout = int64(typed)
	case int32:
		timeout = int64(typed)
	case int64:
		timeout = typed
	case uint:
		timeout = int64(typed)
	case uint8:
		timeout = int64(typed)
	case uint16:
		timeout = int64(typed)
	case uint32:
		timeout = int64(typed)
	case uint64:
		if typed > uint64(^uint64(0)>>1) {
			return 0, fmt.Errorf("component timeout_ms is invalid: %v", raw)
		}
		timeout = int64(typed)
	case float32:
		timeout = int64(typed)
	case float64:
		timeout = int64(typed)
	case bool:
		if typed {
			timeout = 1
		}
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("component timeout_ms is invalid: %v", raw)
		}
		timeout = parsed
	default:
		return 0, fmt.Errorf("component timeout_ms is invalid: %v", raw)
	}
	if timeout < 0 {
		return 0, fmt.Errorf("component timeout_ms cannot be negative: %v", raw)
	}
	return timeout, nil
}

func normalizeComponentPlatforms(raw any, componentName string) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	var items []any
	switch typed := raw.(type) {
	case string:
		items = []any{typed}
	case []string:
		items = make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
	case []any:
		items = typed
	default:
		return nil, fmt.Errorf("%s platforms 必须是非空平台名称字符串或字符串列表；省略、None 或 [] 表示不限平台", componentName)
	}
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("%s platforms 必须是非空平台名称字符串或字符串列表；省略、None 或 [] 表示不限平台", componentName)
		}
		normalized := strings.ToLower(strings.TrimSpace(text))
		if !seen[normalized] {
			seen[normalized] = true
			out = append(out, normalized)
		}
	}
	return out, nil
}

func normalizeMessageGatewayRouteType(raw any) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(fmt.Sprint(raw)))
	switch normalized {
	case "send":
		return "send", nil
	case "receive", "recv", "recive":
		return "receive", nil
	case "duplex":
		return "duplex", nil
	default:
		return "", fmt.Errorf("MessageGateway 路由类型不合法: %v", raw)
	}
}

func normalizeHomeCardWidth(raw any) string {
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(raw))) {
	case "small", "medium", "large", "wide", "full":
		return strings.ToLower(strings.TrimSpace(fmt.Sprint(raw)))
	default:
		return "medium"
	}
}

// normalizeHomeCardOrder 对齐 HomeCardEntry._normalize_order 的 int(raw) 语义
//
// bool 转 0/1，float 向零截断，数字字符串解析为整数，其余失败回落 1000。
func normalizeHomeCardOrder(raw any) int {
	if value, ok := pythonInt(raw); ok {
		return value
	}
	return 1000
}

func pythonInt(raw any) (int, bool) {
	switch typed := raw.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	case float32:
		return floatToInt(float64(typed))
	case float64:
		return floatToInt(typed)
	case string:
		value, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return value, true
	default:
		return 0, false
	}
}

func floatToInt(value float64) (int, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return int(value), true
}

func normalizeCommandAliases(raw any) []string {
	var items []any
	switch typed := raw.(type) {
	case []any:
		items = typed
	case []string:
		items = make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
	case nil:
		return nil
	default:
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && text != "" {
			out = append(out, text)
		}
	}
	return out
}

func compileCommandPattern(pattern string) (*regexp.Regexp, error) {
	// Python 命名组 (?P<name>...) 在 RE2 中写作 (?<name>...)
	pattern = strings.ReplaceAll(pattern, "(?P<", "(?<")
	return regexp.Compile(pattern)
}

func commandNamedGroups(pattern *regexp.Regexp, match []string) map[string]string {
	groups := make(map[string]string)
	for index, name := range pattern.SubexpNames() {
		if index == 0 || name == "" || index >= len(match) {
			continue
		}
		groups[name] = match[index]
	}
	return groups
}

func validateReplyParametersSchema(raw any) (map[string]any, error) {
	schema, ok := raw.(map[string]any)
	if !ok || schema["type"] != "object" {
		return nil, fmt.Errorf("回复扩展 parameters_schema 必须是 object JSON Schema")
	}
	cloned := cloneAnyMap(schema)
	if _, exists := cloned["additionalProperties"]; !exists {
		cloned["additionalProperties"] = false
	}
	if err := checkSchemaNodes(cloned); err != nil {
		return nil, err
	}
	// 对齐 Python：default 本身必须满足其所在节点的 schema
	if err := checkSchemaDefaults(cloned); err != nil {
		return nil, err
	}
	if _, err := json.Marshal(cloned); err != nil {
		return nil, fmt.Errorf("回复扩展 parameters_schema 必须是 object JSON Schema")
	}
	return cloned, nil
}

// checkSchemaDefaults 校验每个节点声明的 default 是否满足该节点 schema
func checkSchemaDefaults(node any) error {
	switch typed := node.(type) {
	case map[string]any:
		if defaultValue, exists := typed["default"]; exists {
			if err := validateSchemaValue(typed, defaultValue); err != nil {
				return fmt.Errorf("回复扩展参数默认值不符合声明: %w", err)
			}
		}
		for _, key := range []string{"properties", "patternProperties", "$defs", "dependentSchemas"} {
			if nested, ok := typed[key].(map[string]any); ok {
				for _, value := range nested {
					if err := checkSchemaDefaults(value); err != nil {
						return err
					}
				}
			}
		}
		for _, key := range []string{
			"items", "additionalProperties", "contains", "propertyNames", "if", "then", "else", "not",
			"unevaluatedItems", "unevaluatedProperties", "allOf", "anyOf", "oneOf", "prefixItems",
		} {
			if nested, ok := typed[key]; ok {
				if err := checkSchemaDefaults(nested); err != nil {
					return err
				}
			}
		}
	case []any:
		for _, value := range typed {
			if err := checkSchemaDefaults(value); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateSchemaValue 用节点自身作为 schema 校验一个值
func validateSchemaValue(schema map[string]any, value any) error {
	raw, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("inline://schema-default.json", strings.NewReader(string(raw))); err != nil {
		return err
	}
	compiled, err := compiler.Compile("inline://schema-default.json")
	if err != nil {
		return err
	}
	return compiled.Validate(value)
}

// fillSchemaDefaults 按 properties 递归补齐缺失的 default，对齐 Python _fill_defaults
func fillSchemaDefaults(value any, schema map[string]any) {
	object, ok := value.(map[string]any)
	if !ok {
		if list, isList := value.([]any); isList {
			items, _ := schema["items"].(map[string]any)
			if items == nil {
				return
			}
			for _, item := range list {
				fillSchemaDefaults(item, items)
			}
		}
		return
	}
	properties, _ := schema["properties"].(map[string]any)
	for name, rawChild := range properties {
		child, ok := rawChild.(map[string]any)
		if !ok {
			continue
		}
		if _, exists := object[name]; !exists {
			if defaultValue, hasDefault := child["default"]; hasDefault {
				object[name] = cloneAnyValue(defaultValue)
			}
		}
		if current, exists := object[name]; exists {
			fillSchemaDefaults(current, child)
		}
	}
}

func checkSchemaNodes(node any) error {
	switch typed := node.(type) {
	case map[string]any:
		if _, exists := typed["$ref"]; exists {
			return fmt.Errorf("回复扩展参数不支持 $ref/$dynamicRef，请内联声明，避免调用时访问外部资源")
		}
		if _, exists := typed["$dynamicRef"]; exists {
			return fmt.Errorf("回复扩展参数不支持 $ref/$dynamicRef，请内联声明，避免调用时访问外部资源")
		}
		for _, key := range []string{"properties", "patternProperties", "$defs", "dependentSchemas"} {
			if nested, ok := typed[key].(map[string]any); ok {
				for _, value := range nested {
					if err := checkSchemaNodes(value); err != nil {
						return err
					}
				}
			}
		}
		for _, key := range []string{
			"items", "additionalProperties", "contains", "propertyNames", "if", "then", "else", "not",
			"unevaluatedItems", "unevaluatedProperties", "allOf", "anyOf", "oneOf", "prefixItems",
		} {
			if nested, ok := typed[key]; ok {
				if err := checkSchemaNodes(nested); err != nil {
					return err
				}
			}
		}
	case []any:
		for _, value := range typed {
			if err := checkSchemaNodes(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func strictInt(raw any, fallback int) (int, bool) {
	if raw == nil {
		return fallback, true
	}
	switch typed := raw.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	default:
		return fallback, false
	}
}

func cloneComponentMetadata(raw map[string]any) map[string]any {
	if raw == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(raw))
	for key, value := range raw {
		out[key] = value
	}
	return out
}

func cloneAnyMap(raw map[string]any) map[string]any {
	if raw == nil {
		return nil
	}
	out := make(map[string]any, len(raw))
	for key, value := range raw {
		out[key] = cloneAnyValue(value)
	}
	return out
}

// cloneAnyValue 深拷贝 JSON 兼容值，供参数补默认值时不污染调用方
func cloneAnyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAnyMap(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = cloneAnyValue(item)
		}
		return out
	default:
		return value
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func boolValue(raw any) bool {
	value, ok := raw.(bool)
	return ok && value
}
