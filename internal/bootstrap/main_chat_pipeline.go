package bootstrap

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"maibot-go/internal/chat/heart_flow"
	"maibot-go/internal/chat/image_system"
	"maibot-go/internal/chat/message_receive"
	"maibot-go/internal/chat/wiring"
	maibotcfg "maibot-go/internal/config/maibot"
	"maibot-go/internal/core/localoperator"
	"maibot-go/internal/files"
	"maibot-go/internal/llm_models"
	maisaruntime "maibot-go/internal/maisaka/runtime"
	"maibot-go/internal/person_info"
	"maibot-go/internal/platform_io"
	"maibot-go/internal/platform_io/drivers/maim_bridge"
	"maibot-go/internal/platform_io/drivers/onebot"
	"maibot-go/internal/runtime/execution"
	"maibot-go/internal/runtime/execution/scheduler"
	"maibot-go/internal/services"
)

// chatRuntimePipeline 把入站图片预处理、统一接收链与 Agent 分发串成一条主链
// 心流 Manager 只负责会话运行时索引与生命周期观察，运行时启停仍归 Agent
type chatRuntimePipeline struct {
	agent     *maisaruntime.Agent
	handler   func(*onebot.ConversationMessage)
	images    *chatImagePreprocessor
	chat      *chatwiring.Runtime
	manager   *heartflow.Manager
	sessions  *chatSessionRegistry
	runtimes  *chatRuntimeProvider
	scheduler *scheduler.Scheduler
	cancel    context.CancelFunc
	inflight  sync.Map
	delivered sync.Map
}

// chatPipelineOptions 汇总 main 的宿主依赖；零值保持旧构造行为的可用子集
type chatPipelineOptions struct {
	Agent        *maisaruntime.Agent
	Images       *chatImagePreprocessor
	Platform     *platformio.Manager
	DB           *gorm.DB
	Writer       chatwiring.MessageLogWriter
	Broadcaster  messagereceive.WebUIBroadcaster
	Files        *filesvc.Service
	ImageManager *imagesystem.Manager
	ImageFiles   chatwiring.ImageFileResolver
	People       *personinfo.Registry
	Messages     *services.MessageService
	// Memory 入站记忆自动化；nil 时接收器跳过
	Memory heartflow.MemoryAutomation
	// SentHook 发送后记忆自动化；nil 时发送器跳过
	SentHook    messagereceive.SentMessageHook
	APIHub      maimbridge.Hub
	APIEnabled  func() bool
	APIRunning  func() bool
	ReplyTiming func() *maibotcfg.ChatReplyTimingConfig
	// MessageFilter 屏蔽词/正则过滤器；由宿主用当前配置构造并支持热重载
	MessageFilter messagereceive.MessageFilter
	// CommandPermissions 命令权限判定所需配置；nil 时受保护命令一律拒绝
	CommandPermissions func() ([]string, map[string]localoperator.CommandPermissionRule)
	// LocalOperator 判断用户是否为本地操作员
	LocalOperator func(platform, userID string) bool
	// AdapterLifecycle 适配器上下线；nil 时 /offline 与 /online 为空操作
	AdapterLifecycle *chatwiring.AdapterLifecyclePort
	BotNickname      string
	Now              func() time.Time

	// NativeSessionScheduler 启用按会话 FIFO 的邮箱与有界工作池。
	NativeSessionScheduler bool
	// SessionSchedulerWorkers <=0 时按 CPU 核心数取值。
	SessionSchedulerWorkers int
	// SessionSchedulerQueueSize <=0 时使用 scheduler.DefaultQueueSize。
	SessionSchedulerQueueSize int

	LLMRouter                *llm.Router
	VoiceTranscriber         llm.AudioTranscriber
	VoiceResolver            chatwiring.VoiceResolver
	VoiceMaxBytes            int64
	EnableVoiceTranscription bool
	// SilentPermission 对齐 plugin.silent_permission_denied；nil 视为 false
	SilentPermission func() bool
}

// newChatRuntimePipeline 构建旧四参主链；imag 可为空，为空时只保留 Agent 直通
func newChatRuntimePipeline(parent context.Context, maiAgent *maisaruntime.Agent, imag *chatImagePreprocessor, policy *platformio.Manager) *chatRuntimePipeline {
	return newChatRuntimePipelineWithOptions(parent, chatPipelineOptions{
		Agent:    maiAgent,
		Images:   imag,
		Platform: policy,
	})
}

// newChatRuntimePipelineWithOptions 用 chatwiring 装配统一接收链，保留 Agent 分发桥
func newChatRuntimePipelineWithOptions(parent context.Context, options chatPipelineOptions) *chatRuntimePipeline {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	sessions := newChatSessionRegistry()
	provider := &chatRuntimeProvider{
		sessions:  sessions,
		agent:     options.Agent,
		attaching: map[string]struct{}{},
	}
	pipeline := &chatRuntimePipeline{
		agent:    options.Agent,
		images:   options.Images,
		sessions: sessions,
		runtimes: provider,
		cancel:   cancel,
	}
	if options.NativeSessionScheduler {
		pipeline.scheduler = scheduler.New(ctx, scheduler.Options{
			Workers:   options.SessionSchedulerWorkers,
			QueueSize: options.SessionSchedulerQueueSize,
			OnError: func(sessionKey string, err error) {
				zap.L().Error("原生会话任务失败", zap.String("session_id", sessionKey), zap.Error(err))
			},
		})
		zap.L().Info("原生会话调度器已启动",
			zap.String("execution_mode", string(execution.ModeNative)),
			zap.Int("workers", pipeline.scheduler.Workers()),
		)
	}
	if options.Agent != nil {
		pipeline.handler = options.Agent.MessageHandler()
	}
	provider.dispatch = func(msg *onebot.ConversationMessage) {
		if pipeline.handler != nil {
			pipeline.handler(msg)
		}
	}
	provider.onDelivered = func(key string) {
		pipeline.delivered.Store(key, struct{}{})
	}
	provider.inflight = &pipeline.inflight
	provider.ctx = ctx

	imageFiles := options.ImageFiles
	if imageFiles == nil && options.ImageManager != nil {
		imageFiles = &chatImageFileResolver{manager: options.ImageManager}
	}
	voiceTranscriber := options.VoiceTranscriber
	if voiceTranscriber == nil && options.LLMRouter != nil {
		voiceTranscriber = chatwiring.NewRouterAudioTranscriber(options.LLMRouter)
	}
	runtimeAssembly := chatwiring.New(chatwiring.Options{
		DB:          options.DB,
		Writer:      options.Writer,
		Broadcaster: options.Broadcaster,
		Platform:    options.Platform,
		ImageFiles:  imageFiles,
		APIHub:      options.APIHub,
		APIEnabled:  options.APIEnabled,
		APIRunning:  options.APIRunning,
		Files:       options.Files,
		Images:      options.ImageManager,
		People:      options.People,
		Messages:    options.Messages,
		Memory:      options.Memory,
		SentHook:    options.SentHook,
		HeartflowOptions: heartflow.ManagerOptions{
			Sessions: sessions,
			Policy:   chatPlatformPolicy{manager: options.Platform},
			New: func(_ context.Context, session heartflow.ChatSession) (heartflow.Runtime, error) {
				return provider.GetOrCreate(context.Background(), session.SessionID)
			},
			// Agent 已经拥有运行时生命周期，心流 Manager 不得重复启动或停止底层循环
			StartRuntime: func(heartflow.Runtime) {},
			StopRuntime:  func(heartflow.Runtime) error { return nil },
			Now:          options.Now,
		},
		FrequencyFallback: chatFrequencyFallback(options.ReplyTiming),
		InboundFilter:     options.MessageFilter,
		CommandPolicy: &chatwiring.CommandPolicyPort{
			Settings:      options.CommandPermissions,
			LocalOperator: options.LocalOperator,
		},
		AdapterLifecycle:         options.AdapterLifecycle,
		BotNickname:              options.BotNickname,
		Now:                      options.Now,
		VoiceTranscriber:         voiceTranscriber,
		VoiceResolver:            options.VoiceResolver,
		VoiceMaxBytes:            options.VoiceMaxBytes,
		EnableVoiceTranscription: options.EnableVoiceTranscription,
		SilentPermission:         options.SilentPermission,
		SkipInboundStore:         true,
		OnHeartflowError:         pipeline.handleHeartflowError,
		StartupContext:           ctx,
	})
	pipeline.chat = runtimeAssembly
	pipeline.manager = runtimeAssembly.Heartflow
	provider.manager = runtimeAssembly.Heartflow
	return pipeline
}

// Handle 处理一条 Agent 入站消息：先补图片描述，再走统一接收链与心流校验。
//
// 开启原生会话调度后，同一会话的整条接收链按 FIFO 串行，跨会话由有界工作池并行；
// 未开启或无法解析会话时保持原有直通路径。
func (p *chatRuntimePipeline) Handle(ctx context.Context, msg *onebot.ConversationMessage) {
	if p == nil || msg == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if p.scheduler == nil || p.chat == nil || p.chat.ChatBot == nil {
		p.handleMessage(ctx, msg)
		return
	}
	session, ok := p.resolveSession(msg)
	if !ok {
		p.handleMessage(ctx, msg)
		return
	}
	if err := p.scheduler.Submit(ctx, session.SessionID, func(runCtx context.Context) error {
		p.handleSessionMessage(runCtx, msg, session)
		return nil
	}); err != nil {
		zap.L().Warn("原生会话调度拒绝入站消息",
			zap.String("session_id", session.SessionID),
			zap.String("message_id", msg.ExternalMessageID),
			zap.Error(err))
	}
}

// handleMessage 是未启用调度器时的完整入站链路，也用于无法解析会话的降级。
func (p *chatRuntimePipeline) handleMessage(ctx context.Context, msg *onebot.ConversationMessage) {
	if p.chat == nil || p.chat.ChatBot == nil {
		if p.images != nil {
			p.images.Process(ctx, msg)
		}
		p.dispatch(msg)
		return
	}
	session, ok := p.resolveSession(msg)
	if !ok {
		if p.images != nil {
			p.images.Process(ctx, msg)
		}
		p.dispatch(msg)
		return
	}
	p.handleSessionMessage(ctx, msg, session)
}

func (p *chatRuntimePipeline) resolveSession(msg *onebot.ConversationMessage) (heartflow.ChatSession, bool) {
	var selfID int64
	if p.agent != nil {
		selfID = p.agent.SelfID()
	}
	return chatSessionForMessage(msg, selfID)
}

// handleSessionMessage 执行单个会话的图片预处理与统一接收链。
func (p *chatRuntimePipeline) handleSessionMessage(ctx context.Context, msg *onebot.ConversationMessage, session heartflow.ChatSession) {
	if p.images != nil {
		p.images.Process(ctx, msg)
	}
	p.sessions.Remember(session)
	inbound := chatInboundSessionMessage(msg, session)
	key := chatPipelineMessageKey(inbound)
	p.inflight.Store(key, msg)
	defer p.inflight.Delete(key)
	defer p.delivered.Delete(key)
	err := p.chat.ChatBot.ReceiveMessage(ctx, inbound)
	if err != nil {
		zap.L().Warn("统一接收链处理失败，降级为 Agent 直通",
			zap.String("session_id", session.SessionID), zap.Error(err))
		if _, delivered := p.delivered.Load(key); !delivered {
			p.dispatch(msg)
		}
	}
}

// handleHeartflowError 只在不重复分发时降级；心流在 RegisterMessage 后失败不能再次直通
func (p *chatRuntimePipeline) handleHeartflowError(_ context.Context, message *messagereceive.SessionMessage, err error) {
	if p == nil || message == nil {
		return
	}
	key := chatPipelineMessageKey(message)
	if _, delivered := p.delivered.Load(key); delivered {
		zap.L().Warn("心流后置步骤失败，消息已分发，不再重复直通",
			zap.String("session_id", message.SessionID), zap.Error(err))
		return
	}
	source, ok := p.inflight.Load(key)
	if !ok {
		return
	}
	msg, ok := source.(*onebot.ConversationMessage)
	if !ok || msg == nil {
		return
	}
	zap.L().Warn("心流消息处理失败，降级为 Agent 直通",
		zap.String("session_id", message.SessionID), zap.Error(err))
	p.delivered.Store(key, struct{}{})
	p.dispatch(msg)
}

// chatPipelineMessageKey 用会话、消息号与时间戳区分同一条入站消息
func chatPipelineMessageKey(message *messagereceive.SessionMessage) string {
	if message == nil {
		return ""
	}
	return message.SessionID + "|" + message.MessageID + "|" + strconv.FormatInt(message.Timestamp.UnixNano(), 10)
}

func chatPipelineContextMessageKey(sessionID, messageID string, timestamp time.Time) string {
	return sessionID + "|" + messageID + "|" + strconv.FormatInt(timestamp.UnixNano(), 10)
}

// HandleMessage 供不接受 context 的 OneBot 直通链调用
func (p *chatRuntimePipeline) HandleMessage(msg *onebot.ConversationMessage) {
	p.Handle(context.Background(), msg)
}

// SessionSchedulerStats 返回原生会话调度器快照；未启用时返回 nil。
func (p *chatRuntimePipeline) SessionSchedulerStats() any {
	if p == nil || p.scheduler == nil {
		return nil
	}
	return p.scheduler.Stats()
}

// SessionTaskSubmitter 返回按会话提交任务的入口；调度器未启用时返回 nil。
//
// WebUI 聊天等非 Platform IO 入口用它把消息并入同一个原生调度器。
func (p *chatRuntimePipeline) SessionTaskSubmitter() func(context.Context, string, func(context.Context) error) error {
	if p == nil || p.scheduler == nil {
		return nil
	}
	return func(ctx context.Context, sessionKey string, task func(context.Context) error) error {
		if task == nil {
			return nil
		}
		return p.scheduler.Submit(ctx, sessionKey, scheduler.Task(task))
	}
}

// Close 先排空原生会话调度器，再停止心流管理与延迟挂接；不触碰 Agent 的运行时生命周期。
func (p *chatRuntimePipeline) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	var schedulerErr error
	if p.scheduler != nil {
		// 先排空已接收的消息，再取消会话上下文并关闭心流。
		schedulerErr = p.scheduler.Shutdown(ctx)
	}
	if p.cancel != nil {
		p.cancel()
	}
	if p.manager == nil {
		return schedulerErr
	}
	return errors.Join(schedulerErr, p.manager.Shutdown(ctx))
}

func (p *chatRuntimePipeline) dispatch(msg *onebot.ConversationMessage) {
	if p != nil && p.handler != nil {
		p.handler(msg)
	}
}
