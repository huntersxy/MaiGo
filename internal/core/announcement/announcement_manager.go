package announcement

import (
	"sync"

	"go.uber.org/zap"
)

// Manager 按聊天保存被禁用的动作、命令、事件处理器与工具
type Manager struct {
	mu               sync.RWMutex
	disabledActions  map[string][]string
	disabledCommands map[string][]string
	disabledHandlers map[string][]string
	disabledTools    map[string][]string
}

// NewManager 创建空的禁用项管理器
func NewManager() *Manager {
	return &Manager{
		disabledActions:  make(map[string][]string),
		disabledCommands: make(map[string][]string),
		disabledHandlers: make(map[string][]string),
		disabledTools:    make(map[string][]string),
	}
}

// Default 对应 Python 的 global_announcement_manager
var Default = NewManager()

// DisableSpecificChatAction 禁用特定聊天的某个动作
func (m *Manager) DisableSpecificChatAction(chatID, actionName string) bool {
	return disableItem(m, m.disabledActionsLocked, chatID, actionName, "动作")
}

// EnableSpecificChatAction 启用特定聊天的某个动作
func (m *Manager) EnableSpecificChatAction(chatID, actionName string) bool {
	return enableItem(m, m.disabledActionsLocked, chatID, actionName, "动作")
}

// GetDisabledChatActions 获取特定聊天禁用的所有动作
func (m *Manager) GetDisabledChatActions(chatID string) []string {
	return getItems(m, m.disabledActionsLocked, chatID)
}

// DisableSpecificChatCommand 禁用特定聊天的某个命令
func (m *Manager) DisableSpecificChatCommand(chatID, commandName string) bool {
	return disableItem(m, m.disabledCommandsLocked, chatID, commandName, "命令")
}

// EnableSpecificChatCommand 启用特定聊天的某个命令
func (m *Manager) EnableSpecificChatCommand(chatID, commandName string) bool {
	return enableItem(m, m.disabledCommandsLocked, chatID, commandName, "命令")
}

// GetDisabledChatCommands 获取特定聊天禁用的所有命令
func (m *Manager) GetDisabledChatCommands(chatID string) []string {
	return getItems(m, m.disabledCommandsLocked, chatID)
}

// DisableSpecificChatEventHandler 禁用特定聊天的某个事件处理器
func (m *Manager) DisableSpecificChatEventHandler(chatID, handlerName string) bool {
	return disableItem(m, m.disabledHandlersLocked, chatID, handlerName, "事件处理器")
}

// EnableSpecificChatEventHandler 启用特定聊天的某个事件处理器
func (m *Manager) EnableSpecificChatEventHandler(chatID, handlerName string) bool {
	return enableItem(m, m.disabledHandlersLocked, chatID, handlerName, "事件处理器")
}

// GetDisabledChatEventHandlers 获取特定聊天禁用的所有事件处理器
func (m *Manager) GetDisabledChatEventHandlers(chatID string) []string {
	return getItems(m, m.disabledHandlersLocked, chatID)
}

// DisableSpecificChatTool 禁用特定聊天的某个工具
func (m *Manager) DisableSpecificChatTool(chatID, toolName string) bool {
	return disableItem(m, m.disabledToolsLocked, chatID, toolName, "工具")
}

// EnableSpecificChatTool 启用特定聊天的某个工具
func (m *Manager) EnableSpecificChatTool(chatID, toolName string) bool {
	return enableItem(m, m.disabledToolsLocked, chatID, toolName, "工具")
}

// GetDisabledChatTools 获取特定聊天禁用的所有工具
func (m *Manager) GetDisabledChatTools(chatID string) []string {
	return getItems(m, m.disabledToolsLocked, chatID)
}

func (m *Manager) disabledActionsLocked(create bool) map[string][]string {
	if create && m.disabledActions == nil {
		m.disabledActions = make(map[string][]string)
	}
	return m.disabledActions
}

func (m *Manager) disabledCommandsLocked(create bool) map[string][]string {
	if create && m.disabledCommands == nil {
		m.disabledCommands = make(map[string][]string)
	}
	return m.disabledCommands
}

func (m *Manager) disabledHandlersLocked(create bool) map[string][]string {
	if create && m.disabledHandlers == nil {
		m.disabledHandlers = make(map[string][]string)
	}
	return m.disabledHandlers
}

func (m *Manager) disabledToolsLocked(create bool) map[string][]string {
	if create && m.disabledTools == nil {
		m.disabledTools = make(map[string][]string)
	}
	return m.disabledTools
}

func disableItem(m *Manager, table func(bool) map[string][]string, chatID, name, label string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := table(true)
	for _, item := range items[chatID] {
		if item == name {
			zap.L().Warn(label+" 已经被禁用", zap.String("chat_id", chatID), zap.String("name", name))
			return false
		}
	}
	items[chatID] = append(items[chatID], name)
	return true
}

func enableItem(m *Manager, table func(bool) map[string][]string, chatID, name, label string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := table(false)
	values, ok := items[chatID]
	if !ok {
		return false
	}
	for i, item := range values {
		if item == name {
			items[chatID] = append(values[:i], values[i+1:]...)
			return true
		}
	}
	zap.L().Warn(label+" 不在禁用列表中", zap.String("chat_id", chatID), zap.String("name", name))
	return false
}

func getItems(m *Manager, table func(bool) map[string][]string, chatID string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	values := table(false)[chatID]
	if len(values) == 0 {
		return []string{}
	}
	return append([]string(nil), values...)
}
