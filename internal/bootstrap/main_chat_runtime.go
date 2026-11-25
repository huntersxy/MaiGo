package bootstrap

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/chat/heart_flow"
	messagereceive "MaiGo/internal/chat/message_receive"
	"MaiGo/internal/chat/session"
	"MaiGo/internal/common/data_models/messagecomponent"
	"MaiGo/internal/common/database"
	"MaiGo/internal/maisaka"
	maisaruntime "MaiGo/internal/maisaka/runtime"
	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/onebot"
)

// chatSessionRegistry 保存入站消息对应的聊天流，供心流 Manager 与运行时工厂查询
type chatSessionRegistry struct {
	mu   sync.RWMutex
	byID map[string]heartflow.ChatSession
}

func newChatSessionRegistry() *chatSessionRegistry {
	return &chatSessionRegistry{byID: map[string]heartflow.ChatSession{}}
}

func (r *chatSessionRegistry) Remember(session heartflow.ChatSession) {
	if r == nil || strings.TrimSpace(session.SessionID) == "" {
		return
	}
	r.mu.Lock()
	r.byID[session.SessionID] = session
	r.mu.Unlock()
}

func (r *chatSessionRegistry) SessionByID(_ context.Context, sessionID string) (heartflow.ChatSession, bool, error) {
	if r == nil {
		return heartflow.ChatSession{}, false, nil
	}
	r.mu.RLock()
	session, ok := r.byID[strings.TrimSpace(sessionID)]
	r.mu.RUnlock()
	return session, ok, nil
}

// chatRuntimeProvider 向心流 Receiver 返回分发适配器，真实运行时由 Manager 延迟挂接
type chatRuntimeProvider struct {
	sessions    *chatSessionRegistry
	agent       *maisaruntime.Agent
	manager     *heartflow.Manager
	dispatch    func(context.Context, *onebot.ConversationMessage)
	onDelivered func(string)
	inflight    *sync.Map
	ctx         context.Context
	mu          sync.Mutex
	runtimes    map[string]*chatDispatchRuntime
	attaching   map[string]struct{}
}

func (p *chatRuntimeProvider) GetOrCreate(_ context.Context, sessionID string) (heartflow.Runtime, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id 不能为空")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.runtimes == nil {
		p.runtimes = map[string]*chatDispatchRuntime{}
	}
	if runtime := p.runtimes[sessionID]; runtime != nil {
		return runtime, nil
	}
	runtime := &chatDispatchRuntime{sessionID: sessionID, provider: p}
	p.runtimes[sessionID] = runtime
	return runtime, nil
}

// Drop 淘汰后丢掉分发适配器缓存，避免继续持有旧 delegate
func (p *chatRuntimeProvider) Drop(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if p == nil || sessionID == "" {
		return
	}
	p.mu.Lock()
	delete(p.runtimes, sessionID)
	delete(p.attaching, sessionID)
	p.mu.Unlock()
}

func (p *chatRuntimeProvider) setDelegate(sessionID string, runtime heartflow.Runtime) {
	p.mu.Lock()
	dispatcher := p.runtimes[sessionID]
	p.mu.Unlock()
	if dispatcher != nil {
		dispatcher.setDelegate(runtime)
	}
}

// scheduleAttach 在 Agent 创建运行时后把它挂到心流 Manager；只在内存态可用时调用 Manager
func (p *chatRuntimeProvider) scheduleAttach(sessionID string) {
	if p == nil || p.agent == nil || p.manager == nil {
		return
	}
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	if p.attaching == nil {
		p.attaching = map[string]struct{}{}
	}
	if _, exists := p.attaching[sessionID]; exists {
		p.mu.Unlock()
		return
	}
	p.attaching[sessionID] = struct{}{}
	p.mu.Unlock()

	go func() {
		defer func() {
			p.mu.Lock()
			delete(p.attaching, sessionID)
			p.mu.Unlock()
		}()
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := p.agent.SessionRuntime(sessionID); err == nil {
				runtime, err := p.manager.GetOrCreate(ctx, sessionID)
				if err == nil {
					p.setDelegate(sessionID, runtime)
					return
				}
				zap.L().Debug("心流运行时挂接失败，稍后重试",
					zap.String("session_id", sessionID), zap.Error(err))
			}
			if time.Now().After(deadline) {
				zap.L().Debug("心流运行时暂未创建，等待后续消息再挂接",
					zap.String("session_id", sessionID))
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	}()
}

// chatDispatchRuntime 是心流 Runtime 的宿主分发适配器
// Start/Stop 为空实现，RegisterMessage 交给 Agent，不污染真实 Maisaka 运行时缓存
type chatDispatchRuntime struct {
	sessionID string
	provider  *chatRuntimeProvider
	mu        sync.RWMutex
	delegate  heartflow.Runtime
}

func (r *chatDispatchRuntime) SessionID() string {
	if r == nil {
		return ""
	}
	return r.sessionID
}

func (r *chatDispatchRuntime) Start() {}
func (r *chatDispatchRuntime) Stop()  {}

func (r *chatDispatchRuntime) RegisterMessage(ctx context.Context, message maisaka.ContextMessage) error {
	if r == nil || r.provider == nil {
		return nil
	}
	key := chatPipelineContextMessageKey(r.sessionID, message.MessageID, message.Timestamp)
	var source *onebot.ConversationMessage
	if r.provider.inflight != nil {
		if value, ok := r.provider.inflight.Load(key); ok {
			source, _ = value.(*onebot.ConversationMessage)
		}
	}
	if source == nil {
		source, _ = message.Metadata[heartflow.MetadataHostSource].(*onebot.ConversationMessage)
	}
	if source == nil {
		return nil
	}
	if resolved, ok := message.Metadata[heartflow.MetadataHostSource].(*messagereceive.SessionMessage); ok && resolved != nil {
		syncResolvedInboundFields(source, resolved)
	}
	if r.provider.onDelivered != nil {
		r.provider.onDelivered(key)
	}
	if r.provider.dispatch != nil {
		r.provider.dispatch(ctx, source)
	}
	r.provider.scheduleAttach(r.sessionID)
	return nil
}

// syncResolvedInboundFields 将统一接收链已经解析好的引用原文和 @ 显示名同步回宿主消息，避免下游重复查找失败。
func syncResolvedInboundFields(source *onebot.ConversationMessage, resolved *messagereceive.SessionMessage) {
	if source == nil || resolved == nil || resolved.RawMessage == nil {
		return
	}
	for _, component := range resolved.RawMessage.Components {
		switch item := component.(type) {
		case messagecomponent.ReplyComponent:
			applyResolvedReplyComponent(source, item)
		case *messagecomponent.ReplyComponent:
			if item != nil {
				applyResolvedReplyComponent(source, *item)
			}
		case messagecomponent.AtComponent:
			applyResolvedAtComponent(source, item)
		case *messagecomponent.AtComponent:
			if item != nil {
				applyResolvedAtComponent(source, *item)
			}
		}
	}
}

func applyResolvedReplyComponent(source *onebot.ConversationMessage, item messagecomponent.ReplyComponent) {
	if item.TargetContent == nil || strings.TrimSpace(*item.TargetContent) == "" {
		return
	}
	if source.Reply == nil {
		externalID := strings.TrimSpace(item.TargetMessageID)
		if externalID == "" {
			return
		}
		reply := &onebot.ReplyInfo{ExternalMessageID: externalID}
		if numeric, err := strconv.ParseInt(externalID, 10, 64); err == nil && numeric > 0 {
			reply.MessageID = numeric
		} else {
			reply.MessageID = onebot.SyntheticIdentityID("reply", externalID)
		}
		source.Reply = reply
	}
	if strings.TrimSpace(source.Reply.Content) == "" {
		source.Reply.Content = strings.TrimSpace(*item.TargetContent)
	}
	if item.SenderID != nil && strings.TrimSpace(*item.SenderID) != "" {
		sender := strings.TrimSpace(*item.SenderID)
		if source.Reply.ExternalSenderID == "" {
			source.Reply.ExternalSenderID = sender
		}
		if source.Reply.SenderID == 0 {
			if numeric, err := strconv.ParseInt(sender, 10, 64); err == nil && numeric > 0 {
				source.Reply.SenderID = numeric
			}
		}
	}
	if item.SenderNickname != nil && strings.TrimSpace(*item.SenderNickname) != "" && strings.TrimSpace(source.Reply.Nickname) == "" {
		source.Reply.Nickname = strings.TrimSpace(*item.SenderNickname)
	}
	if item.SenderCardname != nil && strings.TrimSpace(*item.SenderCardname) != "" && strings.TrimSpace(source.Reply.GroupCard) == "" {
		source.Reply.GroupCard = strings.TrimSpace(*item.SenderCardname)
	}
}

func applyResolvedAtComponent(source *onebot.ConversationMessage, item messagecomponent.AtComponent) {
	targetID := strings.TrimSpace(item.TargetUserID)
	if targetID == "" {
		return
	}
	numeric, err := strconv.ParseInt(targetID, 10, 64)
	if err != nil || numeric <= 0 {
		return
	}
	display := ""
	if item.Cardname != nil && strings.TrimSpace(*item.Cardname) != "" {
		display = strings.TrimSpace(*item.Cardname)
	} else if item.Nickname != nil && strings.TrimSpace(*item.Nickname) != "" {
		display = strings.TrimSpace(*item.Nickname)
	}
	if display == "" || display == targetID {
		return
	}
	if source.AtNames == nil {
		source.AtNames = map[int64]string{}
	}
	existing := strings.TrimSpace(source.AtNames[numeric])
	if existing == "" || existing == targetID {
		source.AtNames[numeric] = display
	}
}

func (r *chatDispatchRuntime) AdjustTalkFrequency(frequency float64) {
	r.mu.RLock()
	delegate := r.delegate
	r.mu.RUnlock()
	if delegate != nil {
		delegate.AdjustTalkFrequency(frequency)
	}
}

func (r *chatDispatchRuntime) setDelegate(runtime heartflow.Runtime) {
	r.mu.Lock()
	r.delegate = runtime
	r.mu.Unlock()
}

// chatPlatformPolicy 把 Platform IO 的适配器名单校验接入心流 Manager
type chatPlatformPolicy struct {
	manager *platformio.Manager
}

func (p chatPlatformPolicy) EnsureSendPipelineReady(ctx context.Context) error {
	if p.manager == nil {
		return nil
	}
	return p.manager.EnsureSendPipelineReady(ctx)
}

func (p chatPlatformPolicy) IsTargetAllowed(_ context.Context, session heartflow.ChatSession) (bool, error) {
	if p.manager == nil {
		return true, nil
	}
	return p.manager.IsTargetAllowed(platformio.RouteKey{
		Platform:  session.Platform,
		AccountID: session.AccountID,
		Scope:     session.Scope,
	}, session.ChatType(), session.TargetID()), nil
}

// chatSessionForMessage 按 Agent ensureChatSession 的同一规则计算 session_id
func chatSessionForMessage(msg *onebot.ConversationMessage, selfID int64) (heartflow.ChatSession, bool) {
	if msg == nil {
		return heartflow.ChatSession{}, false
	}
	platform := strings.TrimSpace(msg.Platform)
	if platform == "" {
		platform = "qq"
	}
	kind := msg.ConversationKind
	if kind == "" {
		kind = memory.ConversationKindGroup
	}
	accountID := strings.TrimSpace(msg.AccountID)
	if accountID == "" && platform == "qq" && selfID > 0 {
		accountID = strconv.FormatInt(selfID, 10)
	}
	groupID := ""
	userID := ""
	if kind == memory.ConversationKindGroup {
		groupID = strings.TrimSpace(msg.GroupKey)
		if groupID == "" {
			groupID = strings.TrimSpace(msg.ExternalTargetID)
		}
		if groupID == "" && msg.TargetID > 0 {
			groupID = strconv.FormatInt(msg.TargetID, 10)
		}
	} else {
		userID = strings.TrimSpace(msg.ExternalUserID)
		if userID == "" {
			userID = strings.TrimSpace(msg.ExternalTargetID)
		}
		if userID == "" && msg.TargetID > 0 {
			userID = strconv.FormatInt(msg.TargetID, 10)
		}
	}
	if groupID == "" && userID == "" {
		return heartflow.ChatSession{}, false
	}
	sessionID, err := chatsession.CalculateSessionID(chatsession.CalculateParams{
		Platform:  platform,
		UserID:    userID,
		GroupID:   groupID,
		AccountID: accountID,
		Scope:     msg.Scope,
	})
	if err != nil {
		return heartflow.ChatSession{}, false
	}
	return heartflow.ChatSession{
		SessionID: sessionID,
		Platform:  platform,
		AccountID: accountID,
		Scope:     msg.Scope,
		UserID:    userID,
		GroupID:   groupID,
		IsGroup:   kind == memory.ConversationKindGroup,
	}, true
}
