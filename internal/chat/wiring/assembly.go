package chatwiring

import (
	"context"
	"time"

	"gorm.io/gorm"

	"maibot-go/internal/chat/heart_flow"
	"maibot-go/internal/chat/image_system"
	"maibot-go/internal/chat/message_receive"
	maibotcfg "maibot-go/internal/config/maibot"
	"maibot-go/internal/emoji_system"
	"maibot-go/internal/files"
	"maibot-go/internal/llm_models"
	"maibot-go/internal/person_info"
	"maibot-go/internal/platform_io"
	"maibot-go/internal/platform_io/drivers/maim_bridge"
	"maibot-go/internal/services"
)

// Options 宿主装配依赖；缺失能力在调用时返回真实错误，不用空实现伪造成功
type Options struct {
	DB          *gorm.DB
	Writer      MessageLogWriter
	ChatManager *messagereceive.ChatManager

	Broadcaster messagereceive.WebUIBroadcaster
	Platform    *platformio.Manager
	ImageFiles  ImageFileResolver

	APIHub     maimbridge.Hub
	APIEnabled func() bool
	APIRunning func() bool

	Files    *filesvc.Service
	Images   *imagesystem.Manager
	People   *personinfo.Registry
	Messages *services.MessageService

	// CommandPolicy 命令权限判定端口；nil 时受保护命令一律拒绝
	CommandPolicy messagereceive.CommandPolicy
	// AdapterLifecycle 适配器上下线端口；nil 时 /offline 与 /online 为空操作
	AdapterLifecycle messagereceive.AdapterLifecycle

	// Memory 入站记忆自动化端口；nil 时心流接收器跳过
	Memory heartflow.MemoryAutomation
	// SentHook 发送完成后的记忆自动化端口；nil 时发送器跳过
	SentHook messagereceive.SentMessageHook

	Heartflow        *heartflow.Manager
	HeartflowOptions heartflow.ManagerOptions

	FrequencyFallback func(message *messagereceive.SessionMessage) (float64, bool)
	ReplyTiming       *maibotcfg.ChatReplyTimingConfig
	// InboundFilter 屏蔽词/正则过滤器；nil 时不做过滤
	InboundFilter messagereceive.MessageFilter
	BotNickname   string
	SenderConfig  messagereceive.SenderConfig
	Now           func() time.Time

	// VoiceTranscriber 语音转写模型；nil 时 VoicePort 返回真实“未配置”错误
	VoiceTranscriber llm.AudioTranscriber
	// VoiceResolver 语音字节解析器；nil 时使用 data URI/base64/file/URL/本地路径的默认实现
	VoiceResolver VoiceResolver
	// VoiceMaxBytes 语音大小上限；<=0 使用 DefaultVoiceMaxBytes
	VoiceMaxBytes int64
	// EnableVoiceTranscription 对齐 MaiBot global_config.voice.enable_asr；默认关闭
	EnableVoiceTranscription bool
	// SilentPermission 对齐 global_config.plugin.silent_permission_denied；nil 视为 false
	SilentPermission func() bool

	// SkipInboundStore 跳过心流接收器落库；宿主运行时已自行持久化消息时使用
	SkipInboundStore bool
	// OnHeartflowError 心流接收失败回调；宿主可据此降级分发
	OnHeartflowError func(context.Context, *messagereceive.SessionMessage, error)

	// StartupContext 聊天流持久化生命周期；非空时启动初始化与周期保存，取消后停止
	StartupContext context.Context
	// ChatSaveInterval 聊天流周期保存间隔；<=0 时使用 300s
	ChatSaveInterval time.Duration
}

// Runtime 暴露装配后的入站、发送与心流组件
type Runtime struct {
	ChatManager *messagereceive.ChatManager
	ChatBot     *messagereceive.ChatBot
	Sender      *messagereceive.Sender
	Heartflow   *heartflow.Manager
	Frequency   *FrequencyFiller
	Store       *MessageStore
	FileIngest  *FileIngestor
}

// New 构造可注入的消息接收链，不依赖 main 或全局单例
func New(options Options) *Runtime {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	manager := options.ChatManager
	createdManager := false
	if manager == nil {
		var repository messagereceive.SessionRepository
		if options.DB != nil {
			repository = messagereceive.NewGormSessionRepository(options.DB)
		}
		manager = messagereceive.NewChatManager(repository)
		createdManager = true
	}
	// 对齐 chat_manager.initialize 与 regularly_save_sessions：有持久化或宿主显式注入的生命周期时才启动
	if options.StartupContext != nil && (options.DB != nil || options.ChatManager != nil) {
		if createdManager && options.DB != nil {
			manager.Initialize(options.StartupContext)
		}
		manager.StartPeriodicSave(options.StartupContext, options.ChatSaveInterval)
	}
	heartflowManager := options.Heartflow
	if heartflowManager == nil {
		heartflowOptions := options.HeartflowOptions
		if heartflowOptions.Sessions == nil {
			heartflowOptions.Sessions = &HeartflowChatProvider{Manager: manager}
		}
		if heartflowOptions.Now == nil {
			heartflowOptions.Now = now
		}
		heartflowManager = heartflow.NewManager(heartflowOptions)
	}
	frequencyFallback := options.FrequencyFallback
	if frequencyFallback == nil {
		frequencyFallback = ConfigFrequencyFallback(options.ReplyTiming)
	}
	frequency := &FrequencyFiller{
		Runtime:  heartflowManager,
		Fallback: frequencyFallback,
	}
	store := &MessageStore{
		DB:        options.DB,
		Writer:    options.Writer,
		Frequency: frequency,
		Now:       now,
	}
	var people heartflow.PersonRegistrar
	if options.People != nil {
		people = &HeartflowPeople{Registry: options.People}
	}
	receiverOptions := heartflow.ReceiverOptions{
		Chats:  heartflowManager,
		People: people,
		Now:    now,
		Memory: options.Memory,
	}
	if !options.SkipInboundStore {
		receiverOptions.Store = &HeartflowStore{Messages: store}
	}
	receiver := heartflow.NewReceiver(receiverOptions)
	processor := &HeartflowProcessor{Receiver: receiver, OnError: options.OnHeartflowError}

	var ingestor *FileIngestor
	if options.Files != nil {
		ingestor = &FileIngestor{Service: options.Files}
	}

	var fallback messagereceive.APIServerFallback
	if options.APIHub != nil {
		fallback = &APIServerFallback{
			Hub:         options.APIHub,
			EnabledFunc: options.APIEnabled,
			RunningFunc: options.APIRunning,
		}
	}
	senderConfig := options.SenderConfig
	if senderConfig.BotNickname == "" {
		senderConfig.BotNickname = options.BotNickname
	}
	if senderConfig.Now == nil {
		senderConfig.Now = now
	}
	sender := messagereceive.NewSender(messagereceive.SenderOptions{
		Hook:           options.SentHook,
		Broadcaster:    options.Broadcaster,
		Legacy:         &PlatformSender{Manager: options.Platform, Images: options.ImageFiles},
		Fallback:       fallback,
		Store:          store,
		ReplyFrequency: frequency,
		ReplyTargets:   NewReplyTargetResolver(options.DB),
		Config:         senderConfig,
	})
	voicePort := &VoicePort{
		Transcriber: options.VoiceTranscriber,
		Resolver:    options.VoiceResolver,
		MaxBytes:    options.VoiceMaxBytes,
	}
	chatBot := messagereceive.NewChatBot(messagereceive.ChatBotOptions{
		Manager:                  manager,
		Store:                    store,
		Files:                    optionalFileIngestor(ingestor),
		Heartflow:                processor,
		ImageDescriber:           &ImagePort{Manager: options.Images},
		EmojiDescriber:           &EmojiPort{Registry: emojisystem.DefaultRegistry(), Images: options.Images},
		VoiceTranscriber:         voicePort,
		EnableVoiceTranscription: options.EnableVoiceTranscription,
		SilentPermission:         options.SilentPermission,
		Persons:                  &PersonPort{Registry: options.People},
		Replies:                  &ReplyPort{Messages: options.Messages},
		Filter:                   options.InboundFilter,
		MessageIDs:               newMessageIDPort(options.DB),
		Policy:                   options.CommandPolicy,
		Lifecycle:                options.AdapterLifecycle,
		BotIdentity:              &BotPort{Registry: options.People, Nickname: options.BotNickname},
		Text:                     &SenderText{Sender: sender},
		Now:                      now,
	})

	return &Runtime{
		ChatManager: manager,
		ChatBot:     chatBot,
		Sender:      sender,
		Heartflow:   heartflowManager,
		Frequency:   frequency,
		Store:       store,
		FileIngest:  ingestor,
	}
}

func optionalFileIngestor(ingestor *FileIngestor) messagereceive.FileIngestor {
	if ingestor == nil {
		return nil
	}
	return ingestor
}
