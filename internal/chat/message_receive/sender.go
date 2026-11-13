package messagereceive

import (
	"context"
	"fmt"
	"strings"
	"time"

	"maibot-go/internal/common/data_models/messagecomponent"
	"maibot-go/internal/core/internal_chat"

	"go.uber.org/zap"
)

// WebUIBroadcaster WebUI 群广播端口
type WebUIBroadcaster interface {
	BroadcastToGroup(ctx context.Context, groupID string, payload map[string]any, userID string) error
}

// LegacySender 旧版平台发送端口，返回是否成功
type LegacySender interface {
	SendMessage(ctx context.Context, message *SessionMessage) (bool, error)
}

// APIServerFallback API Server 兜底发送端口
type APIServerFallback interface {
	Enabled() bool
	Running() bool
	HasPlatform(platform string) bool
	SendMessage(ctx context.Context, message *SessionMessage) (map[string]bool, error)
}

// SentMessageHook 发送完成后的记忆自动化端口
type SentMessageHook interface {
	OnMessageSent(ctx context.Context, message *SessionMessage) error
}

// ReplyFrequencyFiller 发送前补全引用频率
type ReplyFrequencyFiller interface {
	FillReplyFrequency(ctx context.Context, message *SessionMessage) error
}

// ReplyTargetInfo 被引用消息的展示信息
type ReplyTargetInfo struct {
	Content  string
	SenderID string
	Nickname string
	Cardname string
}

// ReplyTargetResolver 按消息 ID 反查被引用消息，供 WebUI 实时推送补全引用卡片
type ReplyTargetResolver interface {
	LookupReplyTarget(ctx context.Context, sessionID, messageID string) (ReplyTargetInfo, bool)
}

// SenderConfig 统一发送配置
type SenderConfig struct {
	BotNickname         string
	WebUIPlatforms      map[string]struct{}
	VirtualGroupPrefix  string
	DefaultWebUIGroupID string
	TypingSpeed         float64
	Typing              TypingConfig
	Now                 func() time.Time
	Sleep               func(time.Duration)
}

// DefaultSenderConfig 对齐 Python 默认值
func DefaultSenderConfig() SenderConfig {
	return SenderConfig{
		BotNickname:        "MaiBot",
		WebUIPlatforms:     map[string]struct{}{"webui": {}},
		VirtualGroupPrefix: "webui_virtual_group_",
		TypingSpeed:        1,
		Now:                time.Now,
		Sleep:              time.Sleep,
	}
}

// SenderOptions 统一发送链依赖
type SenderOptions struct {
	Processor      *Processor
	Broadcaster    WebUIBroadcaster
	Legacy         LegacySender
	Fallback       APIServerFallback
	Store          MessageStore
	Hook           SentMessageHook
	ReplyFrequency ReplyFrequencyFiller
	ReplyTargets   ReplyTargetResolver
	Config         SenderConfig
}

// Sender 对齐 MaiBot UniversalMessageSender
type Sender struct {
	processor      *Processor
	broadcaster    WebUIBroadcaster
	legacy         LegacySender
	fallback       APIServerFallback
	store          MessageStore
	hook           SentMessageHook
	replyFrequency ReplyFrequencyFiller
	replyTargets   ReplyTargetResolver
	config         SenderConfig
}

// NewSender 构造统一发送器
func NewSender(options SenderOptions) *Sender {
	config := options.Config
	defaults := DefaultSenderConfig()
	if strings.TrimSpace(config.BotNickname) == "" {
		config.BotNickname = defaults.BotNickname
	}
	if config.WebUIPlatforms == nil {
		config.WebUIPlatforms = defaults.WebUIPlatforms
	}
	if strings.TrimSpace(config.VirtualGroupPrefix) == "" {
		config.VirtualGroupPrefix = defaults.VirtualGroupPrefix
	}
	if config.TypingSpeed == 0 {
		config.TypingSpeed = defaults.TypingSpeed
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Sleep == nil {
		config.Sleep = time.Sleep
	}
	processor := options.Processor
	if processor == nil {
		processor = &Processor{}
	}
	return &Sender{
		processor:      processor,
		broadcaster:    options.Broadcaster,
		legacy:         options.Legacy,
		fallback:       options.Fallback,
		store:          options.Store,
		hook:           options.Hook,
		replyFrequency: options.ReplyFrequency,
		replyTargets:   options.ReplyTargets,
		config:         config,
	}
}

// SendOptions 对齐 UniversalMessageSender.send_message 参数
type SendOptions struct {
	Typing         bool
	SetReply       bool
	ReplyMessageID string
	StorageMessage bool
	ShowLog        bool
}

// DefaultSendOptions 返回 Python 默认的发送参数
func DefaultSendOptions() SendOptions {
	return SendOptions{StorageMessage: true, ShowLog: true}
}

// SendMessage 对齐 UniversalMessageSender.send_message
func (s *Sender) SendMessage(ctx context.Context, message *SessionMessage, options SendOptions) (bool, error) {
	if s == nil || message == nil {
		return false, fmt.Errorf("消息为空")
	}
	if strings.TrimSpace(message.MessageID) == "" {
		return false, fmt.Errorf("消息缺少 message_id，无法发送")
	}
	if options.SetReply {
		replyID := strings.TrimSpace(options.ReplyMessageID)
		if replyID == "" {
			return false, fmt.Errorf("set_reply=True 时必须提供 reply_message_id")
		}
		if message.RawMessage == nil {
			message.RawMessage = messagecomponent.NewMessageSequence()
		}
		reply := messagecomponent.ReplyComponent{TargetMessageID: replyID}
		components := make([]messagecomponent.Component, 0, len(message.RawMessage.Components)+1)
		components = append(components, reply)
		components = append(components, message.RawMessage.Components...)
		message.RawMessage.Components = components
	}
	if err := s.processor.Process(ctx, message, ProcessOptions{EnableHeavyMediaAnalysis: true, EnableVoiceTranscription: true}); err != nil {
		return false, err
	}
	if options.Typing {
		seconds := CalculateTypingTime(message.PlainText(), TypingConfig{
			ChineseTimeSec: s.config.Typing.ChineseTimeSec,
			EnglishTimeSec: s.config.Typing.EnglishTimeSec,
			IsEmoji:        message.IsEmoji,
		}, s.config.TypingSpeed)
		if delay := floorDuration(seconds); delay > 0 {
			s.config.Sleep(delay)
		}
	}
	sent, err := s.sendPreparedMessageToPlatform(ctx, message, options.ShowLog)
	if err != nil {
		return false, err
	}
	if !sent {
		return false, nil
	}
	if options.StorageMessage {
		if s.replyFrequency != nil {
			if err := s.replyFrequency.FillReplyFrequency(ctx, message); err != nil {
				zap.L().Error("["+message.SessionID+"] 处理或存储消息 "+message.MessageID+" 时出错",
					zap.Error(err))
				return false, err
			}
		}
		if s.store != nil {
			if err := s.store.StoreMessage(ctx, message); err != nil {
				return true, err
			}
		}
	}
	if s.hook != nil {
		if err := s.hook.OnMessageSent(ctx, message); err != nil {
			zap.L().Warn("发送后记忆自动化失败", zap.Error(err))
		}
	}
	return true, nil
}

// SendPreparedMessageToPlatform 只发送不落库，对齐 send_prepared_message_to_platform
func (s *Sender) SendPreparedMessageToPlatform(ctx context.Context, message *SessionMessage, showLog bool) (bool, error) {
	if s == nil || message == nil {
		return false, fmt.Errorf("消息为空")
	}
	return s.sendPreparedMessageToPlatform(ctx, message, showLog)
}

func (s *Sender) sendPreparedMessageToPlatform(ctx context.Context, message *SessionMessage, showLog bool) (bool, error) {
	if s.broadcaster != nil && s.isWebUIMessage(message) {
		return s.sendWebUI(ctx, message, showLog)
	}
	var legacyErr error
	if s.legacy != nil {
		sent, err := s.legacy.SendMessage(ctx, message)
		if err == nil && sent {
			return true, nil
		}
		legacyErr = err
	}
	if s.fallback != nil && s.fallback.Enabled() && s.fallback.Running() && s.fallback.HasPlatform(message.Platform) {
		results, err := s.fallback.SendMessage(ctx, message)
		if err == nil {
			if anyResultTrue(results) {
				return true, nil
			}
			if legacyErr != nil {
				return false, legacyErr
			}
			return false, nil
		}
		if legacyErr != nil {
			return false, legacyErr
		}
		return false, err
	}
	if legacyErr != nil {
		return false, legacyErr
	}
	if s.legacy == nil && s.fallback == nil {
		return false, fmt.Errorf("未配置平台发送端口")
	}
	return false, nil
}

// fillReplyTargets 给 WebUI 实时推送的 reply 段补上被引用消息的发送者与正文
//
// 发送时构造的 reply 段只有 target_message_id，前端会退化成「未知发送者 /
// 原消息内容不可用」。历史回读路径有 enrichChatHistoryReplySegments 兜底，
// 实时推送路径必须在这里补同样字段，两条路径才能显示一致。
func (s *Sender) fillReplyTargets(ctx context.Context, message *SessionMessage, segments any) {
	if s.replyTargets == nil || message == nil {
		return
	}
	rows, ok := segments.([]map[string]any)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(message.SessionID)
	for _, row := range rows {
		if row == nil {
			continue
		}
		if kind, _ := row["type"].(string); kind != "reply" {
			continue
		}
		data, ok := row["data"].(map[string]any)
		if !ok {
			continue
		}
		targetID, _ := data["target_message_id"].(string)
		targetID = strings.TrimSpace(targetID)
		if targetID == "" {
			continue
		}
		info, found := s.replyTargets.LookupReplyTarget(ctx, sessionID, targetID)
		if !found {
			continue
		}
		if content, _ := data["target_message_content"].(string); strings.TrimSpace(content) == "" {
			data["target_message_content"] = info.Content
		}
		if value, _ := data["target_message_sender_id"].(string); strings.TrimSpace(value) == "" {
			data["target_message_sender_id"] = info.SenderID
		}
		if value, _ := data["target_message_sender_nickname"].(string); strings.TrimSpace(value) == "" {
			data["target_message_sender_nickname"] = info.Nickname
		}
		if value, _ := data["target_message_sender_cardname"].(string); strings.TrimSpace(value) == "" {
			data["target_message_sender_cardname"] = info.Cardname
		}
	}
}

func (s *Sender) sendWebUI(ctx context.Context, message *SessionMessage, showLog bool) (bool, error) {
	if s.broadcaster == nil {
		return false, fmt.Errorf("未配置 WebUI 广播端口")
	}
	content := message.PlainText()
	messageType := "rich"
	var segments any
	if _, ok := singleTextContent(message.RawMessage); ok {
		messageType = "text"
		segments = nil
	} else {
		segments = internalchat.SerializeSequence(message.RawMessage)
	}
	s.fillReplyTargets(ctx, message, segments)
	payloadGroupID := ""
	if message.IsGroup() {
		payloadGroupID = message.TargetID()
	}
	groupID := payloadGroupID
	if groupID == "" {
		groupID = s.config.DefaultWebUIGroupID
	}
	targetUserID := ""
	if value, ok := message.MessageInfo.AdditionalConfig["platform_io_target_user_id"]; ok {
		targetUserID = strings.TrimSpace(stringValue(value))
	}
	now := s.config.Now()
	payload := map[string]any{
		"type":         "bot_message",
		"content":      content,
		"message_type": messageType,
		"segments":     segments,
		"timestamp":    float64(now.UnixNano()) / 1e9,
		"group_id":     payloadGroupID,
		// 前端按顶层 is_bot 决定气泡左右，只放 sender 里会被忽略
		"is_bot": true,
		"sender": map[string]any{
			"name":   s.config.BotNickname,
			"avatar": nil,
			"is_bot": true,
		},
	}
	if err := s.broadcaster.BroadcastToGroup(ctx, groupID, payload, targetUserID); err != nil {
		return false, err
	}
	if showLog {
		kind := "聊天室"
		if isVirtualGroupID(groupID, s.config.VirtualGroupPrefix) {
			kind = "虚拟群"
		}
		zap.L().Info("已通过 WebUI 发送消息",
			zap.String("kind", kind),
			zap.String("group_id", groupID),
			zap.String("preview", TruncateMessage(content, 200)))
	}
	return true, nil
}

func (s *Sender) isWebUIMessage(message *SessionMessage) bool {
	if message == nil {
		return false
	}
	platform := strings.TrimSpace(message.Platform)
	if platform == "webui" {
		return true
	}
	if _, ok := s.config.WebUIPlatforms[platform]; ok {
		return true
	}
	if message.IsGroup() {
		return isVirtualGroupID(message.TargetID(), s.config.VirtualGroupPrefix)
	}
	return false
}

func isVirtualGroupID(groupID, prefix string) bool {
	groupID = strings.TrimSpace(groupID)
	prefix = strings.TrimSpace(prefix)
	return prefix != "" && strings.HasPrefix(groupID, prefix)
}

func singleTextContent(sequence *messagecomponent.MessageSequence) (string, bool) {
	if sequence == nil || len(sequence.Components) != 1 {
		return "", false
	}
	switch item := sequence.Components[0].(type) {
	case messagecomponent.TextComponent:
		return item.Text, true
	case *messagecomponent.TextComponent:
		return item.Text, true
	default:
		return "", false
	}
}

func anyResultTrue(results map[string]bool) bool {
	for _, value := range results {
		if value {
			return true
		}
	}
	return false
}
