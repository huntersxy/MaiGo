package maisaka

import "strings"

// BuiltinToolStage 内置工具所属阶段
type BuiltinToolStage string

const (
	// BuiltinStageAction 只属于 Action Loop
	BuiltinStageAction BuiltinToolStage = "action"
	// BuiltinStageBoth Reasoning 与 Action 两个阶段都可见
	BuiltinStageBoth BuiltinToolStage = "both"
)

// BuiltinToolVisibility 内置工具默认可见性
type BuiltinToolVisibility string

const (
	// BuiltinVisible 默认暴露给模型
	BuiltinVisible BuiltinToolVisibility = "visible"
	// BuiltinDeferred 需要 tool_search 发现后暴露
	BuiltinDeferred BuiltinToolVisibility = "deferred"
	// BuiltinHidden 不在工具列表里出现
	BuiltinHidden BuiltinToolVisibility = "hidden"
)

// BuiltinToolChatScope 内置工具的聊天适用范围
type BuiltinToolChatScope string

const (
	BuiltinScopeAll     BuiltinToolChatScope = "all"
	BuiltinScopeGroup   BuiltinToolChatScope = "group"
	BuiltinScopePrivate BuiltinToolChatScope = "private"
)

// BuiltinToolEntry 内置工具目录项，集中声明阶段与默认可见性
type BuiltinToolEntry struct {
	Name       BuiltinToolName
	Stage      BuiltinToolStage
	Visibility BuiltinToolVisibility
	ChatScope  BuiltinToolChatScope
}

// BuiltinToolEntries 对齐 Python BUILTIN_TOOL_ENTRIES 的目录
var BuiltinToolEntries = []BuiltinToolEntry{
	{Name: ToolWait, Stage: BuiltinStageBoth, Visibility: BuiltinVisible, ChatScope: BuiltinScopeAll},
	{Name: ToolStop, Stage: BuiltinStageAction, Visibility: BuiltinVisible, ChatScope: BuiltinScopeAll},
	{Name: ToolReply, Stage: BuiltinStageAction, Visibility: BuiltinVisible, ChatScope: BuiltinScopeAll},
	{Name: ToolViewForwardMessage, Stage: BuiltinStageAction, Visibility: BuiltinDeferred, ChatScope: BuiltinScopeAll},
	{Name: ToolQueryMemory, Stage: BuiltinStageAction, Visibility: BuiltinVisible, ChatScope: BuiltinScopeAll},
	{Name: ToolQueryImageMemory, Stage: BuiltinStageAction, Visibility: BuiltinVisible, ChatScope: BuiltinScopeAll},
	{Name: ToolQueryPersonProfile, Stage: BuiltinStageAction, Visibility: BuiltinVisible, ChatScope: BuiltinScopeAll},
	{Name: ToolSendEmoji, Stage: BuiltinStageAction, Visibility: BuiltinVisible, ChatScope: BuiltinScopeAll},
	{Name: ToolShowEmojiList, Stage: BuiltinStageAction, Visibility: BuiltinVisible, ChatScope: BuiltinScopeAll},
	{Name: ToolToolSearch, Stage: BuiltinStageAction, Visibility: BuiltinVisible, ChatScope: BuiltinScopeAll},
	{Name: ToolSubagent, Stage: BuiltinStageAction, Visibility: BuiltinVisible, ChatScope: BuiltinScopeAll},
	{Name: ToolSubagentStatus, Stage: BuiltinStageAction, Visibility: BuiltinVisible, ChatScope: BuiltinScopeAll},
	{Name: ToolSubagentStop, Stage: BuiltinStageAction, Visibility: BuiltinVisible, ChatScope: BuiltinScopeAll},
	{Name: ToolReadToolResult, Stage: BuiltinStageAction, Visibility: BuiltinVisible, ChatScope: BuiltinScopeAll},
}

// BuiltinProviderName 内置工具的 provider 名
const BuiltinProviderName = "maisaka_builtin"

// LookupBuiltinToolEntry 按工具名查目录项
func LookupBuiltinToolEntry(name string) (BuiltinToolEntry, bool) {
	for _, entry := range BuiltinToolEntries {
		if string(entry.Name) == name {
			return entry, true
		}
	}
	return BuiltinToolEntry{}, false
}

// AnnotateBuiltinToolSpec 写入统一可见性元数据
func AnnotateBuiltinToolSpec(spec ToolSpec) ToolSpec {
	entry, ok := LookupBuiltinToolEntry(spec.Name)
	if !ok {
		return spec
	}
	if spec.Metadata == nil {
		spec.Metadata = map[string]any{}
	}
	spec.Metadata["builtin_stage"] = string(entry.Stage)
	spec.Metadata["visibility"] = string(entry.Visibility)
	spec.Provider = BuiltinProviderName
	return spec
}

// GetBuiltinToolVisibility 读取工具声明里的可见性
func GetBuiltinToolVisibility(spec ToolSpec) BuiltinToolVisibility {
	raw := ""
	if spec.Metadata != nil {
		if value, ok := spec.Metadata["visibility"].(string); ok {
			raw = strings.TrimSpace(value)
		}
	}
	switch raw {
	case string(BuiltinDeferred):
		return BuiltinDeferred
	case string(BuiltinHidden):
		return BuiltinHidden
	default:
		return BuiltinVisible
	}
}

// IsBuiltinToolInActionStage 内置工具是否属于 Action Loop 阶段；被动式单独使用 stop
func IsBuiltinToolInActionStage(spec ToolSpec, passiveReplyer bool) bool {
	if (spec.Name == "stop" || spec.Name == "wait") && !passiveReplyer {
		return false
	}
	if spec.Name == "reply" && passiveReplyer {
		return false
	}
	raw := ""
	if spec.Metadata != nil {
		if value, ok := spec.Metadata["builtin_stage"].(string); ok {
			raw = strings.TrimSpace(value)
		}
	}
	return raw == string(BuiltinStageAction) || raw == string(BuiltinStageBoth)
}

// BuiltinToolConfig 内置工具按配置启停所需的开关
type BuiltinToolConfig struct {
	UseNewSendLogic      bool
	PlannerSubagent      bool
	ToolResultCondense   bool
	WebSearchEnabled     bool
	MemoryQueryEnabled   bool
	PersonProfileEnabled bool
	ImageMemoryEnabled   bool
}

// BuiltinToolEnabledByConfig 根据全局配置判断内置工具是否应暴露
func BuiltinToolEnabledByConfig(name string, cfg BuiltinToolConfig) bool {
	switch name {
	case string(ToolShowEmojiList):
		return cfg.UseNewSendLogic
	case string(ToolSendEmoji):
		return !cfg.UseNewSendLogic
	case string(ToolSubagent), string(ToolSubagentStatus), string(ToolSubagentStop):
		return cfg.PlannerSubagent
	case string(ToolReadToolResult):
		return cfg.ToolResultCondense || cfg.WebSearchEnabled
	case string(ToolQueryMemory):
		return cfg.MemoryQueryEnabled
	case string(ToolQueryImageMemory):
		return cfg.ImageMemoryEnabled
	case string(ToolQueryPersonProfile):
		return cfg.PersonProfileEnabled
	default:
		return true
	}
}

// BuiltinToolAvailableForChat 判断内置工具是否适用于当前聊天
func BuiltinToolAvailableForChat(entry BuiltinToolEntry, isGroupChat bool) bool {
	switch entry.ChatScope {
	case BuiltinScopeGroup:
		return isGroupChat
	case BuiltinScopePrivate:
		return !isGroupChat
	default:
		return true
	}
}
