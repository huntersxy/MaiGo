package messagereceive

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"maibot-go/internal/chat/session"
	"maibot-go/internal/common/data_models/messagecomponent"
	"maibot-go/internal/core/localoperator"
	"maibot-go/internal/platform_io"

	"go.uber.org/zap"
)

// ChatBot 对齐 MaiBot ChatBot 的入站主链
type ChatBot struct {
	manager            *ChatManager
	hooks              HookInvoker
	processor          *Processor
	commands           CommandResolver
	executor           CommandExecutor
	policy             CommandPolicy
	pluginConfigs      PluginConfigProvider
	store              MessageStore
	files              FileIngestor
	heartflow          HeartflowProcessor
	messageIDs         MessageIDUpdater
	textSender         TextSender
	clear              ClearContextHandler
	filter             MessageFilter
	images             ImagePreprocessor
	lifecycle          AdapterLifecycle
	now                func() time.Time
	voiceTranscription bool
	silentPermission   func() bool
}

// NewChatBot 构造入站机器人；Manager 为空时创建仅内存管理器
func NewChatBot(options ChatBotOptions) *ChatBot {
	manager := options.Manager
	if manager == nil {
		manager = NewChatManager(nil)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	processor := &Processor{
		Images:  options.ImageDescriber,
		Emojis:  options.EmojiDescriber,
		Voice:   options.VoiceTranscriber,
		Persons: options.Persons,
		Replies: options.Replies,
		Bot:     options.BotIdentity,
	}
	return &ChatBot{
		manager:            manager,
		hooks:              options.Hooks,
		processor:          processor,
		commands:           options.Commands,
		executor:           options.Executor,
		policy:             options.Policy,
		pluginConfigs:      options.PluginConfigs,
		store:              options.Store,
		files:              options.Files,
		heartflow:          options.Heartflow,
		messageIDs:         options.MessageIDs,
		textSender:         options.Text,
		clear:              options.Clear,
		filter:             options.Filter,
		images:             options.Images,
		lifecycle:          options.Lifecycle,
		now:                now,
		voiceTranscription: options.EnableVoiceTranscription,
		silentPermission:   options.SilentPermission,
	}
}

// Manager 返回会话管理器
func (b *ChatBot) Manager() *ChatManager {
	if b == nil {
		return nil
	}
	return b.manager
}

// MessageProcessInput 是 message_process 的 Go 入参，替代 Python 原始字典
type MessageProcessInput struct {
	MessageID         string
	Timestamp         time.Time
	ReceivedAt        time.Time
	Platform          string
	AccountID         string
	Scope             string
	UserID            string
	UserNickname      string
	UserCardname      *string
	GroupID           string
	GroupName         string
	RawMessage        *messagecomponent.MessageSequence
	AdditionalConfig  map[string]any
	FileSource        map[string]any
	IsMentioned       bool
	IsAt              bool
	IsEmoji           bool
	IsPicture         bool
	IsCommand         bool
	IsNotify          bool
	PlatformMessageID *string
}

// BuildSessionMessage 将入参转成 SessionMessage
func (in MessageProcessInput) BuildSessionMessage() *SessionMessage {
	message := &SessionMessage{
		MessageID:   strings.TrimSpace(in.MessageID),
		Timestamp:   in.Timestamp,
		ReceivedAt:  in.ReceivedAt,
		Initialized: true,
		Platform:    strings.TrimSpace(in.Platform),
		MessageInfo: MessageInfo{
			UserInfo: UserInfo{
				UserID:       strings.TrimSpace(in.UserID),
				UserNickname: strings.TrimSpace(in.UserNickname),
				UserCardname: in.UserCardname,
			},
			AdditionalConfig: in.AdditionalConfig,
		},
		IsMentioned:       in.IsMentioned,
		IsAt:              in.IsAt,
		IsEmoji:           in.IsEmoji,
		IsPicture:         in.IsPicture,
		IsCommand:         in.IsCommand,
		IsNotify:          in.IsNotify,
		RawMessage:        in.RawMessage,
		FileSource:        in.FileSource,
		PlatformMessageID: in.PlatformMessageID,
	}
	if account := strings.TrimSpace(in.AccountID); account != "" {
		message.AccountID = &account
	}
	if scope := strings.TrimSpace(in.Scope); scope != "" {
		message.Scope = &scope
	}
	if groupID := strings.TrimSpace(in.GroupID); groupID != "" {
		message.MessageInfo.GroupInfo = &GroupInfo{GroupID: groupID, GroupName: strings.TrimSpace(in.GroupName)}
	}
	return message
}

// MessageProcess 对齐 bot.message_process：构造消息并进入 receive_message
func (b *ChatBot) MessageProcess(ctx context.Context, input MessageProcessInput) error {
	return b.ReceiveMessage(ctx, input.BuildSessionMessage())
}

// ReceiveMessage 对齐 bot.receive_message 的完整入站链路
func (b *ChatBot) ReceiveMessage(ctx context.Context, message *SessionMessage) error {
	if b == nil || message == nil {
		return nil
	}
	if message.ReceivedAt.IsZero() {
		message.ReceivedAt = b.now()
	}
	if message.Platform == "" {
		return fmt.Errorf("消息缺少平台信息")
	}
	b.resolveRoute(message)
	sessionID, err := chatsession.CalculateSessionID(chatsession.CalculateParams{
		Platform:  message.Platform,
		UserID:    message.UserID(),
		GroupID:   groupIDOf(message),
		AccountID: pointerText(message.AccountID),
		Scope:     pointerText(message.Scope),
	})
	if err != nil {
		return err
	}
	message.SessionID = sessionID
	if b.images != nil {
		result, err := b.images.ProcessReceivedImages(ctx, message)
		if err != nil {
			// 对齐 Python receive_message：任一步异常中止整条链，不再进入 hook/命令/心流
			zap.L().Error("预处理消息失败", zap.String("message_id", message.MessageID), zap.Error(err))
			return err
		}
		if result.Compressed > 0 || result.Discarded > 0 {
			zap.L().Info("消息 " + message.MessageID + " 入站过大图片处理完成: " + result.Details)
		}
	}

	message, aborted := b.invokeReceiveHook(ctx, "chat.receive.before_process", message)
	if aborted {
		return nil
	}
	// Python bot.py:819-820 调用 handle_notice_message 但忽略返回值
	// 通知仍需进入后续预处理、注册、命令和心流链路，不能在这里提前终止
	b.HandleNoticeMessage(message)
	if b.processor != nil {
		if err := b.processor.Process(ctx, message, ProcessOptions{
			EnableHeavyMediaAnalysis: false,
			EnableVoiceTranscription: b.voiceTranscription,
		}); err != nil {
			zap.L().Error("预处理消息失败", zap.String("message_id", message.MessageID), zap.Error(err))
			return err
		}
	}
	message, aborted = b.invokeReceiveHook(ctx, "chat.receive.after_process", message)
	if aborted {
		return nil
	}

	if !b.IsCommandCandidate(message) {
		text := message.PlainText()
		if word := b.firstBanWord(text); word != "" {
			zap.L().Info(filterPreviewLog(message, text))
			zap.L().Info("[过滤词识别]消息中含有"+word+"，filtered",
				zap.String("message_id", message.MessageID),
				zap.String("word", word))
			return nil
		}
		if pattern := b.firstBanRegex(text); pattern != "" {
			zap.L().Info(filterPreviewLog(message, text))
			zap.L().Info("[正则表达式过滤]消息匹配到"+pattern+"，filtered",
				zap.String("message_id", message.MessageID),
				zap.String("pattern", pattern))
			return nil
		}
	}

	if err := b.manager.RegisterMessage(ctx, message); err != nil {
		zap.L().Error("预处理消息失败", zap.Error(err))
		return err
	}
	if _, err := b.manager.GetOrCreateSession(ctx, message.Platform, message.UserID(), groupIDOf(message), pointerText(message.AccountID), pointerText(message.Scope)); err != nil {
		zap.L().Error("预处理消息失败", zap.Error(err))
		return err
	}
	if b.files != nil {
		if err := b.files.Ingest(ctx, message); err != nil {
			zap.L().Error("预处理消息失败", zap.Error(err))
			return err
		}
	}
	if b.ProcessAdapterLifecycleCommand(ctx, message) {
		return nil
	}
	if b.ProcessClearContextCommand(ctx, message) {
		return nil
	}
	handled, cmdResult, continueProcess := b.ProcessCommands(ctx, message)
	if handled && b.HandleCommandProcessingResult(ctx, message, cmdResult, continueProcess) {
		return nil
	}
	if b.heartflow != nil {
		if message.MessageInfo.GroupInfo == nil {
			zap.L().Debug("[私聊]检测到私聊消息，路由到 Maisaka")
		} else {
			zap.L().Debug("[群聊]检测到群聊消息，路由到 Maisaka")
		}
		if err := b.heartflow.ProcessMessage(ctx, message); err != nil {
			zap.L().Error("预处理消息失败", zap.String("message_id", message.MessageID), zap.Error(err))
			return err
		}
	}
	return nil
}

// HandleNoticeMessage 对齐 bot.handle_notice_message
func (b *ChatBot) HandleNoticeMessage(message *SessionMessage) bool {
	if message == nil || !message.IsNotify {
		return false
	}
	return message.MessageInfo.AdditionalConfig != nil
}

// EchoMessageProcess 对齐 bot.echo_message_process
func (b *ChatBot) EchoMessageProcess(ctx context.Context, raw map[string]any) error {
	if raw == nil || b.messageIDs == nil {
		return nil
	}
	content, ok := raw["content"].(map[string]any)
	if !ok {
		return nil
	}
	if value, _ := content["type"].(string); strings.TrimSpace(value) != "echo" {
		return nil
	}
	// 对齐 Python str(echo or "") / str(actual_id or "")：数字等同类型也需接受
	echoID := strings.TrimSpace(anyString(content["echo"]))
	actualID := strings.TrimSpace(anyString(content["actual_id"]))
	if echoID == "" || actualID == "" {
		return nil
	}
	if err := b.messageIDs.UpdateMessageID(ctx, echoID, actualID); err != nil {
		zap.L().Debug("更新回声消息 ID 失败", zap.Error(err))
	}
	return nil
}

// invokeReceiveHook 对齐 bot._invoke_message_hook，保留 received_at/file_source
func (b *ChatBot) invokeReceiveHook(ctx context.Context, hookName string, message *SessionMessage) (*SessionMessage, bool) {
	if b == nil || b.hooks == nil || message == nil {
		return message, false
	}
	receivedAt := message.ReceivedAt
	fileSource := cloneMap(message.FileSource)
	result := b.hooks.Invoke(ctx, hookName, map[string]any{"message": message.ToHookMap()})
	replacement := message
	if raw, ok := result.Kwargs["message"].(map[string]any); ok {
		clone := message.Clone()
		clone.ApplyHookPatch(raw)
		replacement = clone
	}
	replacement.ReceivedAt = receivedAt
	replacement.FileSource = fileSource
	for _, errMessage := range result.Errors {
		zap.L().Warn("入站消息 Hook 错误", zap.String("hook", hookName), zap.String("error", errMessage))
	}
	if result.Aborted {
		zap.L().Info("入站消息被 Hook 中止",
			zap.String("hook", hookName),
			zap.String("stopped_by", result.StoppedBy),
			zap.String("message_id", replacement.MessageID))
		return replacement, true
	}
	return replacement, false
}

func (b *ChatBot) resolveRoute(message *SessionMessage) {
	if message == nil || message.Platform == "" {
		return
	}
	key, err := (platformio.RouteKeyFactory{}).FromPlatform(
		message.Platform,
		pointerText(message.AccountID),
		pointerText(message.Scope),
		message.MessageInfo.AdditionalConfig,
	)
	if err != nil {
		return
	}
	if key.AccountID != "" {
		value := key.AccountID
		message.AccountID = &value
	}
	if key.Scope != "" {
		value := key.Scope
		message.Scope = &value
	}
}

func (b *ChatBot) firstBanWord(text string) string {
	if b == nil || b.filter == nil {
		return ""
	}
	return strings.TrimSpace(b.filter.FirstBanWord(text))
}

func (b *ChatBot) firstBanRegex(text string) string {
	if b == nil || b.filter == nil {
		return ""
	}
	return strings.TrimSpace(b.filter.FirstBanRegex(text))
}

// filterPreviewLog 对齐 Python 过滤命中前的 "[chat_name]nickname:text" 预览行
func filterPreviewLog(message *SessionMessage, text string) string {
	chatName := "私聊"
	if message != nil && message.MessageInfo.GroupInfo != nil {
		chatName = message.GroupName()
	}
	nickname := ""
	if message != nil {
		nickname = message.Nickname()
	}
	return "[" + chatName + "]" + nickname + ":" + text
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// anyString 对齐 Python str(value or "")，把数字等常见标量转成字符串
func anyString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case bool:
		return fmt.Sprintf("%v", typed)
	case int:
		return fmt.Sprintf("%d", typed)
	case int8:
		return fmt.Sprintf("%d", typed)
	case int16:
		return fmt.Sprintf("%d", typed)
	case int32:
		return fmt.Sprintf("%d", typed)
	case int64:
		return fmt.Sprintf("%d", typed)
	case uint:
		return fmt.Sprintf("%d", typed)
	case uint8:
		return fmt.Sprintf("%d", typed)
	case uint16:
		return fmt.Sprintf("%d", typed)
	case uint32:
		return fmt.Sprintf("%d", typed)
	case uint64:
		return fmt.Sprintf("%d", typed)
	case float32:
		return formatFloatLikePython(float64(typed))
	case float64:
		return formatFloatLikePython(typed)
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func formatFloatLikePython(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%.0f", value)
	}
	return fmt.Sprintf("%v", value)
}

// IsLocalOperatorMessage 暴露本地操作员判断，供上层复用
func IsLocalOperatorMessage(message *SessionMessage) bool {
	if message == nil {
		return false
	}
	return localoperator.IsLocalOperator(message.Platform, message.MessageInfo.AdditionalConfig)
}
