package personinfo

// 记忆自动化协调器：把入站身份观察与发送后人物事实/摘要写回接到生产链路
// 对应 Python MemoryAutomationService 的 on_incoming_message / on_message_sent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"maibot-go/internal/chat/heart_flow"
	"maibot-go/internal/chat/message_receive"
	"maibot-go/internal/common/database"
	"maibot-go/internal/platform_io/drivers/onebot"
	botservices "maibot-go/internal/services"
)

// IdentityObserver 身份观察端口；生产注入 Registry
type IdentityObserver interface {
	ObserveIdentity(ctx context.Context, observation IdentityObservation) error
}

// SentMessage 是发送成功后交给记忆自动化的中性事件
type SentMessage struct {
	Platform         string
	ConversationKind string
	SessionID        string
	TargetID         string
	MessageID        string
	UserID           string
	GroupID          string
	Nickname         string
	CardName         string
	Content          string
	FinalContent     string
	ReplyMessageID   string
	Timestamp        time.Time
	// AgentMessage 非空时人物事实写回直接复用它，避免重建丢失图片等字段
	AgentMessage *onebot.ConversationMessage
}

// IncomingMessage 是入站消息交给图片写回的中性事件
type IncomingMessage struct {
	Platform  string
	SessionID string
	MessageID string
	UserID    string
	GroupID   string
	Nickname  string
	CardName  string
	Text      string
	Timestamp time.Time
	Source    any
}

// ImageIncomingEnqueueFunc 入站图片写回入队端口；task-250 注入真实实现
type ImageIncomingEnqueueFunc func(ctx context.Context, message IncomingMessage) error

// ChatSummaryEnqueueFunc 发送后聊天摘要写回入队端口；task-250 注入真实实现
type ChatSummaryEnqueueFunc func(ctx context.Context, message SentMessage) error

// MemoryAutomationOptions 记忆自动化依赖
type MemoryAutomationOptions struct {
	Registry        *Registry
	Identity        IdentityObserver
	Enabled         func() bool
	FactEnabled     func() bool
	SummaryEnabled  func() bool
	FactHandler     func(ctx context.Context, message *onebot.ConversationMessage) error
	ImageIncoming   ImageIncomingEnqueueFunc
	SummaryEnqueue  ChatSummaryEnqueueFunc
	Reclassifier    *HistoricalReclassifyRunner
	QueueCapacity   int
	UnavailableWait time.Duration
}

// MemoryAutomation 生产记忆自动化协调器
type MemoryAutomation struct {
	opts MemoryAutomationOptions

	mu      sync.Mutex
	started bool
	stopped bool
	queue   chan factJob
	wg      sync.WaitGroup

	dedupeMu sync.Mutex
	dedupe   map[string]struct{}
}

type registryIdentityObserver struct{ registry *Registry }

func (o registryIdentityObserver) ObserveIdentity(ctx context.Context, observation IdentityObservation) error {
	_, err := o.registry.ObserveIdentity(ctx, observation)
	return err
}

type factJob struct {
	ctx context.Context
	msg SentMessage
}

// NewMemoryAutomation 创建协调器；未注入的开关按启用处理，端口缺失时跳过对应能力
func NewMemoryAutomation(opts MemoryAutomationOptions) *MemoryAutomation {
	if opts.QueueCapacity <= 0 {
		opts.QueueCapacity = 256
	}
	if opts.UnavailableWait <= 0 {
		opts.UnavailableWait = 30 * time.Second
	}
	if opts.Identity == nil && opts.Registry != nil {
		opts.Identity = registryIdentityObserver{registry: opts.Registry}
	}
	return &MemoryAutomation{opts: opts, dedupe: make(map[string]struct{})}
}

// Start 启动人物事实队列与历史重分类循环
func (a *MemoryAutomation) Start(ctx context.Context) error {
	if a == nil {
		return errors.New("记忆自动化未初始化")
	}
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return nil
	}
	if a.stopped {
		a.mu.Unlock()
		return errors.New("记忆自动化已停止，不能重新启动")
	}
	a.queue = make(chan factJob, a.opts.QueueCapacity)
	a.started = true
	queue := a.queue
	a.mu.Unlock()

	a.wg.Add(1)
	go a.factWorker(ctx, queue)
	a.startReclassifier(ctx)
	return nil
}

// Stop 停止接收新任务并等待在途任务退出
func (a *MemoryAutomation) Stop(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return nil
	}
	a.stopped = true
	queue := a.queue
	a.mu.Unlock()
	if queue != nil {
		close(queue)
	}
	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()
	var waitErr error
	select {
	case <-done:
	case <-ctx.Done():
		waitErr = ctx.Err()
	}
	if a.opts.Reclassifier != nil {
		if err := a.opts.Reclassifier.Stop(ctx); err != nil {
			waitErr = errors.Join(waitErr, err)
		}
	}
	return waitErr
}

func (a *MemoryAutomation) startReclassifier(ctx context.Context) {
	runner := a.opts.Reclassifier
	if runner == nil && a.opts.Registry != nil {
		runner = a.opts.Registry.HistoricalReclassifier()
	}
	if runner == nil {
		return
	}
	runner.Available = func() bool {
		return a.factEnabled() && runner.MetadataAvailable()
	}
	runner.UnavailableDelay = a.opts.UnavailableWait
	if !runner.MetadataAvailable() && runner.StatePath == "" {
		zap.L().Debug("历史人物事实重分类跳过：Memorix metadata 不可用")
		return
	}
	if err := runner.Start(ctx); err != nil {
		zap.L().Warn("历史人物事实重分类后台循环启动失败", zap.Error(err))
		return
	}
	a.opts.Reclassifier = runner
}

func (a *MemoryAutomation) pluginEnabled() bool {
	if a == nil {
		return false
	}
	return a.opts.Enabled == nil || a.opts.Enabled()
}

func (a *MemoryAutomation) factEnabled() bool {
	return a.pluginEnabled() && (a.opts.FactEnabled == nil || a.opts.FactEnabled())
}

func (a *MemoryAutomation) summaryEnabled() bool {
	return a.pluginEnabled() && (a.opts.SummaryEnabled == nil || a.opts.SummaryEnabled())
}

// OnIncomingMessage 入站：真实登记发送者身份，并把图片写回交给注入端口
func (a *MemoryAutomation) OnIncomingMessage(ctx context.Context, message *heartflow.SessionMessage) error {
	if a == nil || message == nil || !a.pluginEnabled() {
		return nil
	}
	var errs []error
	userID, nickname, cardname, groupID := "", "", "", ""
	if message.MessageInfo.User != nil {
		userID = message.MessageInfo.User.UserID
		nickname = message.MessageInfo.User.Nickname
		cardname = message.MessageInfo.User.Cardname
	}
	if message.MessageInfo.Group != nil {
		groupID = message.MessageInfo.Group.GroupID
	}
	if err := a.observeSenderIdentity(ctx, message.Platform, userID, nickname, cardname, groupID); err != nil {
		errs = append(errs, err)
	}
	if a.opts.ImageIncoming != nil {
		err := a.opts.ImageIncoming(ctx, IncomingMessage{
			Platform: message.Platform, SessionID: message.SessionID, MessageID: message.MessageID,
			UserID: userID, GroupID: groupID, Nickname: nickname, CardName: cardname,
			Text: message.ProcessedPlainText, Timestamp: message.Timestamp, Source: message.Source,
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("图片写回入队失败: %w", err))
		}
	}
	return joinAutomationErrors(errs)
}

// OnMessageSent 发送完成：身份观察 + 人物事实入队 + 摘要入队
func (a *MemoryAutomation) OnMessageSent(ctx context.Context, message *messagereceive.SessionMessage) error {
	if a == nil || message == nil || !a.pluginEnabled() {
		return nil
	}
	cardname := ""
	if message.MessageInfo.UserInfo.UserCardname != nil {
		cardname = *message.MessageInfo.UserInfo.UserCardname
	}
	replyID := ""
	if message.ReplyTo != nil {
		replyID = *message.ReplyTo
	}
	content := ""
	if message.ProcessedPlainText != nil {
		content = *message.ProcessedPlainText
	}
	groupID := ""
	if message.MessageInfo.GroupInfo != nil {
		groupID = message.MessageInfo.GroupInfo.GroupID
	}
	return a.processSent(ctx, SentMessage{
		Platform: message.Platform, ConversationKind: message.ChatType(), SessionID: message.SessionID,
		TargetID: message.TargetID(), MessageID: message.MessageID, UserID: message.UserID(),
		GroupID: groupID, Nickname: message.MessageInfo.UserInfo.UserNickname, CardName: cardname,
		Content: content, ReplyMessageID: replyID, Timestamp: message.Timestamp,
	})
}

// OnAgentMessageSent 供 Agent 直接通知已成功发送的消息
func (a *MemoryAutomation) OnAgentMessageSent(ctx context.Context, message *onebot.ConversationMessage) error {
	if a == nil || message == nil || !a.pluginEnabled() {
		return nil
	}
	groupID := ""
	if message.ConversationKind == memory.ConversationKindGroup {
		groupID = strconv.FormatInt(message.TargetID, 10)
	}
	return a.processSent(ctx, SentMessage{
		Platform: message.Platform, ConversationKind: message.ConversationKind,
		TargetID: strconv.FormatInt(message.TargetID, 10), MessageID: strconv.FormatInt(message.MessageID, 10),
		UserID: strconv.FormatInt(message.UserID, 10), GroupID: groupID,
		Nickname: message.Nickname, CardName: message.GroupCard,
		Content: message.Content, FinalContent: message.FinalContent,
		Timestamp: message.Time, AgentMessage: message,
	})
}

// OnServiceMessageSent 供 services.SendService 成功发送后通知
func (a *MemoryAutomation) OnServiceMessageSent(ctx context.Context, message botservices.ServiceSentMessage) error {
	if a == nil || !a.pluginEnabled() {
		return nil
	}
	kind := memory.ConversationKindPrivate
	groupID := ""
	if message.ChatType == "group" {
		kind = memory.ConversationKindGroup
		groupID = message.TargetID
	}
	return a.processSent(ctx, SentMessage{
		Platform: message.Platform, ConversationKind: kind, SessionID: message.SessionID,
		TargetID: message.TargetID, MessageID: message.MessageID, UserID: message.AccountID,
		GroupID: groupID, Content: message.Text, ReplyMessageID: message.ReplyToMessageID,
		Timestamp: message.Timestamp,
	})
}

func (a *MemoryAutomation) processSent(ctx context.Context, message SentMessage) error {
	var errs []error
	if err := a.observeSenderIdentity(ctx, message.Platform, message.UserID, message.Nickname, message.CardName, message.GroupID); err != nil {
		errs = append(errs, err)
	}
	if a.factEnabled() {
		if err := a.enqueueFact(ctx, message); err != nil {
			errs = append(errs, err)
		}
	}
	if a.summaryEnabled() && a.opts.SummaryEnqueue != nil {
		if err := a.opts.SummaryEnqueue(ctx, message); err != nil {
			errs = append(errs, fmt.Errorf("聊天摘要写回入队失败: %w", err))
		}
	}
	return joinAutomationErrors(errs)
}

func joinAutomationErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("记忆身份登记或写回入队失败: %w", errors.Join(errs...))
}

func (a *MemoryAutomation) observeSenderIdentity(ctx context.Context, platform, userID, nickname, cardname, groupID string) error {
	platform = strings.TrimSpace(platform)
	userID = strings.TrimSpace(userID)
	if platform == "" || userID == "" {
		return errors.New("消息缺少真实的发送者 platform/user_id")
	}
	if a.opts.Identity == nil {
		return errors.New("身份观察端口未接线")
	}
	aliases := make([]string, 0, 2)
	if nickname = strings.TrimSpace(nickname); nickname != "" {
		aliases = append(aliases, nickname)
	}
	if cardname = strings.TrimSpace(cardname); cardname != "" {
		aliases = append(aliases, cardname)
	}
	if err := a.opts.Identity.ObserveIdentity(ctx, IdentityObservation{
		Platform: platform, UserID: userID, Aliases: aliases, GroupID: strings.TrimSpace(groupID),
	}); err != nil {
		return fmt.Errorf("登记发送者身份失败: %w", err)
	}
	return nil
}

func (a *MemoryAutomation) enqueueFact(ctx context.Context, message SentMessage) error {
	if a.opts.FactHandler == nil {
		return errors.New("人物事实写回处理器未接线")
	}
	messageID := strings.TrimSpace(message.MessageID)
	if messageID == "" {
		// Python 的 fact enqueue 不校验 message_id；Go 发送链路需要真实 ID 才能定位落库消息，缺省显式跳过
		zap.L().Warn("人物事实写回跳过：消息缺少真实 message_id",
			zap.String("platform", message.Platform), zap.String("target_id", message.TargetID))
		return nil
	}
	key := strings.Join([]string{message.Platform, message.TargetID, messageID}, "|")
	a.dedupeMu.Lock()
	if _, exists := a.dedupe[key]; exists {
		a.dedupeMu.Unlock()
		return nil
	}
	if len(a.dedupe) >= 4096 {
		a.dedupe = make(map[string]struct{})
	}
	a.dedupe[key] = struct{}{}
	a.dedupeMu.Unlock()

	// 持锁投递避免与 Stop 关闭队列竞态；队列满时立即释放并按 Python 仅告警
	a.mu.Lock()
	queue := a.queue
	started := a.started && !a.stopped
	if !started || queue == nil {
		a.mu.Unlock()
		zap.L().Warn("记忆自动化未启动，跳过人物事实写回", zap.String("message_id", messageID))
		return nil
	}
	job := factJob{ctx: context.WithoutCancel(ctx), msg: message}
	select {
	case queue <- job:
		a.mu.Unlock()
		return nil
	default:
		a.mu.Unlock()
		a.dedupeMu.Lock()
		delete(a.dedupe, key)
		a.dedupeMu.Unlock()
		zap.L().Warn("人物事实写回队列已满，跳过本次发送", zap.String("message_id", messageID))
		return nil
	}
}

func (a *MemoryAutomation) factWorker(_ context.Context, queue <-chan factJob) {
	defer a.wg.Done()
	for job := range queue {
		agentMessage := job.msg.AgentMessage
		if agentMessage == nil {
			agentMessage = conversationMessageFromSent(job.msg)
		}
		if agentMessage == nil || agentMessage.MessageID == 0 {
			continue
		}
		if err := a.opts.FactHandler(job.ctx, agentMessage); err != nil {
			zap.L().Warn("人物事实写回失败", zap.String("message_id", job.msg.MessageID), zap.Error(err))
		}
	}
}

func conversationMessageFromSent(message SentMessage) *onebot.ConversationMessage {
	messageID := parseIntOrSynthetic(message.MessageID, "message", message.Platform, message.TargetID, message.MessageID)
	if messageID == 0 {
		return nil
	}
	targetID := parseIntOrSynthetic(message.TargetID, "target", message.Platform, message.TargetID)
	userID := parseIntOrSynthetic(message.UserID, "user", message.Platform, message.UserID)
	kind := message.ConversationKind
	if kind == "" {
		if message.GroupID != "" {
			kind = memory.ConversationKindGroup
		} else {
			kind = memory.ConversationKindPrivate
		}
	}
	content := strings.TrimSpace(message.Content)
	finalContent := strings.TrimSpace(message.FinalContent)
	if content == "" {
		content = finalContent
	}
	return &onebot.ConversationMessage{
		Platform: message.Platform, MessageID: messageID, ConversationKind: kind,
		TargetID: targetID, UserID: userID, Nickname: message.Nickname, GroupCard: message.CardName,
		Content: content, FinalContent: finalContent, Time: message.Timestamp,
	}
}

func parseIntOrSynthetic(value, scope, platform string, parts ...string) int64 {
	if numeric, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && numeric != 0 {
		return numeric
	}
	if strings.TrimSpace(value) == "" {
		return 0
	}
	return onebot.SyntheticIdentityID(append([]string{scope, platform}, parts...)...)
}
