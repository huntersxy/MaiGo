package bootstrap

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"MaiGo/internal/chat/heart_flow"
	"MaiGo/internal/chat/image_system"
	"MaiGo/internal/chat/message_receive"
	"MaiGo/internal/chat/wiring"
	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/core/localoperator"
	"MaiGo/internal/files"
	"MaiGo/internal/llm_models"
	maisaruntime "MaiGo/internal/maisaka/runtime"
	"MaiGo/internal/person_info"
	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/maim_bridge"
	"MaiGo/internal/platform_io/drivers/onebot"
	"MaiGo/internal/runtime/execution/policy"
	"MaiGo/internal/services"
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
	policies  *policy.Registry
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

	// Policies 已装配的局部执行策略；nil 时使用全 compatibility 的默认注册表。
	Policies *policy.Registry

	LLMRouter                *llm.Router
	VoiceTranscriber         llm.AudioTranscriber
	VoiceResolver            chatwiring.VoiceResolver
	VoiceMaxBytes            int64
	EnableVoiceTranscription bool
	// SilentPermission 对齐 plugin.silent_permission_denied；nil 视为 false
	SilentPermission func() bool
	// RequireSelfMention 对齐 plugin.command_require_self_mention；nil 视为 false
	RequireSelfMention func() bool
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
	policies := options.Policies
	if policies == nil {
		policies = policy.NewRegistry()
	}
	pipeline := &chatRuntimePipeline{
		agent:    options.Agent,
		images:   options.Images,
		sessions: sessions,
		runtimes: provider,
		policies: policies,
		cancel:   cancel,
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
			// StopRuntime 为空实现时仍需回收 Agent.sessions 与分发适配器缓存
			OnEvict: func(_ context.Context, session heartflow.ChatSession, _ string) {
				provider.Drop(session.SessionID)
				if options.Agent == nil {
					return
				}
				_ = options.Agent.DropChatSession(session.IsGroup, session.GroupID, session.UserID)
			},
			Now: options.Now,
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
		RequireSelfMention:       options.RequireSelfMention,
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
	sessionScheduler, ok := p.sessionScheduler()
	if !ok || p.chat == nil || p.chat.ChatBot == nil {
		p.handleMessage(ctx, msg)
		return
	}
	session, ok := p.resolveSession(msg)
	if !ok {
		p.handleMessage(ctx, msg)
		return
	}
	if err := sessionScheduler.Submit(ctx, session.SessionID, policy.SessionTask(func(runCtx context.Context) error {
		p.handleSessionMessage(runCtx, msg, session)
		return nil
	})); err != nil {
		zap.L().Warn("会话调度拒绝入站消息",
			zap.String("session_id", session.SessionID),
			zap.String("message_id", msg.ExternalMessageID),
			zap.Error(err))
	}
}

// sessionScheduler 从策略注册表读取当前会话调度实现。
func (p *chatRuntimePipeline) sessionScheduler() (policy.SessionScheduler, bool) {
	if p == nil || p.policies == nil || p.policies.SessionScheduler == nil {
		return nil, false
	}
	return p.policies.SessionScheduler.Get()
}

// handleMessage 是未启用调度器时的完整入站链路，也用于无法解析会话的降级。
func (p *chatRuntimePipeline) handleMessage(ctx context.Context, msg *onebot.ConversationMessage) {
	if p.chat == nil || p.chat.ChatBot == nil {
		if p.images != nil {
			p.images.Process(ctx, msg, "")
		}
		p.dispatch(msg)
		return
	}
	session, ok := p.resolveSession(msg)
	if !ok {
		if p.images != nil {
			p.images.Process(ctx, msg, "")
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
		p.images.Process(ctx, msg, session.SessionID)
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

// SessionSchedulerStats 返回当前会话调度器快照；compatibility 实现返回 nil。
func (p *chatRuntimePipeline) SessionSchedulerStats() any {
	if p == nil || p.policies == nil {
		return nil
	}
	return p.policies.SessionSchedulerStats()
}

// SessionTaskSubmitter 返回按会话提交任务的入口；注册表使用 compatibility 实现时也会内联执行。
func (p *chatRuntimePipeline) SessionTaskSubmitter() func(context.Context, string, func(context.Context) error) error {
	if p == nil || p.policies == nil {
		return nil
	}
	return func(ctx context.Context, sessionKey string, task func(context.Context) error) error {
		sessionScheduler, ok := p.sessionScheduler()
		if !ok {
			if task != nil {
				return task(ctx)
			}
			return nil
		}
		if task == nil {
			return nil
		}
		return sessionScheduler.Submit(ctx, sessionKey, policy.SessionTask(task))
	}
}

// Close 先关闭策略注册表（排空 native 调度器），再停止心流管理与延迟挂接。
func (p *chatRuntimePipeline) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	var schedulerErr error
	if p.policies != nil {
		schedulerErr = p.policies.Close(ctx)
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
