package messagereceive

import (
	"context"
	"time"
)

// HookInvoker 入站/命令 Hook 触发器端口
type HookInvoker interface {
	Invoke(ctx context.Context, hookName string, kwargs map[string]any) HookResult
}

// HookResult 对齐 hooks.DispatchResult 的最小投影
type HookResult struct {
	Kwargs    map[string]any
	Aborted   bool
	StoppedBy string
	Errors    []string
}

// CommandMatch 命令匹配结果
type CommandMatch struct {
	Name             string
	PluginID         string
	Permission       string
	SilentWhenDenied bool
	MatchedGroups    map[string]any
}

// CommandExecution 命令执行结果
type CommandExecution struct {
	Success   bool
	Response  string
	Intercept int
	Continue  bool
}

// CommandResolver 命令查询端口
type CommandResolver interface {
	FindCommandByText(text string) *CommandMatch
	IsCommandDisabled(commandID string) bool
	IsCommandDisabledInSession(sessionID, commandName string) bool
	IsCoreCommandDisabled(name string) bool
}

// CommandExecutor 命令执行端口
type CommandExecutor interface {
	Execute(ctx context.Context, message *SessionMessage, match *CommandMatch, pluginConfig map[string]any) (CommandExecution, error)
}

// CommandPolicy 命令权限端口
type CommandPolicy interface {
	HasCommandPermission(ctx context.Context, commandID string, message *SessionMessage) bool
	HasPluginManagementPermission(ctx context.Context, message *SessionMessage) bool
}

// PluginConfigProvider 读取插件配置
type PluginConfigProvider interface {
	PluginConfig(ctx context.Context, pluginID string) (map[string]any, error)
}

// MessageStore 消息持久化端口
type MessageStore interface {
	StoreMessage(ctx context.Context, message *SessionMessage) error
}

// FileIngestor 文件服务摄入端口
type FileIngestor interface {
	Ingest(ctx context.Context, message *SessionMessage) error
}

// HeartflowProcessor 心流消息处理端口
type HeartflowProcessor interface {
	ProcessMessage(ctx context.Context, message *SessionMessage) error
}

// MessageIDUpdater 回声消息 ID 更新端口
type MessageIDUpdater interface {
	UpdateMessageID(ctx context.Context, oldMessageID, newMessageID string) error
}

// TextSender 命令/管理回复发送端口
type TextSender interface {
	SendText(ctx context.Context, message *SessionMessage, text string) error
}

// ClearContextHandler 清理指定聊天流 Maisaka 上下文
type ClearContextHandler interface {
	ClearChatHistoryContext(ctx context.Context, sessionID string) error
}

// MessageFilter 屏蔽词/正则过滤端口
type MessageFilter interface {
	FirstBanWord(text string) string
	FirstBanRegex(text string) string
}

// ImagePreprocessResult 入站图片预处理统计
type ImagePreprocessResult struct {
	Compressed int
	Discarded  int
	Details    string
}

// ImagePreprocessor 入站图片压缩/丢弃端口
type ImagePreprocessor interface {
	ProcessReceivedImages(ctx context.Context, message *SessionMessage) (ImagePreprocessResult, error)
}

// AdapterLifecycleResult 适配器上下线结果
type AdapterLifecycleResult struct {
	Changed []string
	Failed  map[string]string
	Pending []string
}

// AdapterLifecycle 适配器插件生命周期端口
type AdapterLifecycle interface {
	Remote() bool
	TakeAdaptersOffline(ctx context.Context) (AdapterLifecycleResult, error)
	BringAdaptersOnline(ctx context.Context) (AdapterLifecycleResult, error)
}

// ChatBotOptions ChatBot 依赖注入
type ChatBotOptions struct {
	Manager *ChatManager
	Hooks   HookInvoker

	Commands      CommandResolver
	Executor      CommandExecutor
	Policy        CommandPolicy
	PluginConfigs PluginConfigProvider
	Store         MessageStore
	Files         FileIngestor
	Heartflow     HeartflowProcessor
	MessageIDs    MessageIDUpdater
	Text          TextSender
	Clear         ClearContextHandler
	Filter        MessageFilter
	Images        ImagePreprocessor
	Lifecycle     AdapterLifecycle

	ImageDescriber   ImageDescriber
	EmojiDescriber   EmojiDescriber
	VoiceTranscriber VoiceTranscriber
	Persons          PersonResolver
	Replies          ReplyLookup
	BotIdentity      BotIdentity

	EnableVoiceTranscription bool
	// SilentPermission 对齐 global_config.plugin.silent_permission_denied；nil 视为 false
	SilentPermission func() bool
	Now              func() time.Time
}
