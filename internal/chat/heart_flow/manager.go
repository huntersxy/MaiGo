package heartflow

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"maibot-go/internal/core/localoperator"
	"maibot-go/internal/maisaka"
)

const (
	// ActiveRetention 对齐 HEARTFLOW_ACTIVE_RETENTION_SECONDS，24 小时内活跃的会话不被淘汰
	ActiveRetention = 24 * time.Hour
	// MaxActiveChats 对齐 HEARTFLOW_MAX_ACTIVE_CHATS
	MaxActiveChats = 100
)

var (
	// ErrManagerStopping 心流管理器正在关停
	ErrManagerStopping = errors.New("心流管理器正在关停，不能创建聊天运行实例")
)

// ChatSession 是 PolicyChecker 与 RuntimeFactory 需要的最小会话信息
// 对应 Python BotChatSession 的 platform/account_id/scope/group_id/user_id
type ChatSession struct {
	SessionID string
	Platform  string
	AccountID string
	Scope     string
	UserID    string
	GroupID   string
	// IsGroup 群聊会话
	IsGroup bool
}

// TargetID 返回适配器名单校验使用的目标编号
func (s ChatSession) TargetID() string {
	if s.IsGroup {
		return s.GroupID
	}
	return s.UserID
}

// ChatType 返回适配器名单校验使用的会话类型
func (s ChatSession) ChatType() string {
	if s.IsGroup {
		return "group"
	}
	return "private"
}

// PolicyDeniedError 对齐 HeartflowPolicyDeniedError，适配器名单拒绝创建运行实例
type PolicyDeniedError struct {
	SessionID string
}

func (e *PolicyDeniedError) Error() string {
	return "适配器名单禁止创建聊天运行实例: " + e.SessionID
}

// Runtime 是本包需要的最小运行时端口，*maisaka.Runtime 满足该接口
type Runtime interface {
	SessionID() string
	Start()
	Stop()
	RegisterMessage(ctx context.Context, message maisaka.ContextMessage) error
	AdjustTalkFrequency(frequency float64)
}

// RuntimeFactory 按会话信息创建运行时，通常返回 *maisaka.Runtime
type RuntimeFactory func(ctx context.Context, session ChatSession) (Runtime, error)

// SessionProvider 按 session_id 查询聊天流，对应 chat_manager.get_session_by_session_id
type SessionProvider interface {
	SessionByID(ctx context.Context, sessionID string) (ChatSession, bool, error)
}

// PolicyChecker 适配器名单校验，对应 platform_io_manager.is_target_allowed
type PolicyChecker interface {
	IsTargetAllowed(ctx context.Context, session ChatSession) (bool, error)
}

// ContextClearer 对应 focus_mode_manager.clear_context
type ContextClearer interface {
	ClearContext(ctx context.Context, sessionID string) error
}

// PlannerResetter 对应 agent.planner_state.reset_planner_session
type PlannerResetter interface {
	ResetPlannerSession(ctx context.Context, sessionID string) error
}

// ManagerOptions 心流管理器依赖
type ManagerOptions struct {
	Sessions SessionProvider
	Policy   PolicyChecker
	New      RuntimeFactory
	Focus    ContextClearer
	Planner  PlannerResetter
	// StartRuntime 可覆盖运行时启动，默认调用 Runtime.Start
	// 宿主已持有运行时生命周期时可注入空实现，避免重复启动内部循环
	StartRuntime func(Runtime)
	// StopRuntime 可覆盖运行时停止，默认调用 Runtime.Stop
	// 宿主负责运行时生命周期时可注入空实现，避免淘汰或关停时停止仍在使用的运行时
	// 返回 error 对齐 Python chat.stop() 可抛异常；Shutdown 会聚合失败
	StopRuntime func(Runtime) error
	// Now 可注入时钟，测试用
	Now func() time.Time
	// MaxActiveChats 为 0 时使用 100
	MaxActiveChats int
	// ActiveRetention 为 0 时使用 24 小时
	ActiveRetention time.Duration
	// BindLoop 绑定宿主事件循环，对应 focus_mode_manager.bind_loop；Go 无事件循环时可为空
	BindLoop func()
}

type chatEntry struct {
	runtime    Runtime
	session    ChatSession
	lastActive time.Time
}

// Manager 管理 session 级别的 Maisaka 心流实例
type Manager struct {
	opts ManagerOptions

	mu          sync.Mutex
	chats       map[string]*chatEntry
	order       []string
	createLocks map[string]*sync.Mutex
	lastActive  map[string]time.Time
	stopping    bool
}

// NewManager 创建心流管理器
func NewManager(opts ManagerOptions) *Manager {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxActiveChats <= 0 {
		opts.MaxActiveChats = MaxActiveChats
	}
	if opts.ActiveRetention <= 0 {
		opts.ActiveRetention = ActiveRetention
	}
	return &Manager{
		opts:        opts,
		chats:       map[string]*chatEntry{},
		createLocks: map[string]*sync.Mutex{},
		lastActive:  map[string]time.Time{},
	}
}

// GetOrCreate 获取或创建指定会话对应的运行时
func (m *Manager) GetOrCreate(ctx context.Context, sessionID string) (Runtime, error) {
	if m.opts.BindLoop != nil {
		m.opts.BindLoop()
	}
	if err := m.ensureRunning(); err != nil {
		return nil, err
	}
	if runtime, ok := m.lookupAndTouch(sessionID); ok {
		return runtime, nil
	}

	lock := m.createLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	if runtime, ok := m.lookupAndTouch(sessionID); ok {
		return runtime, nil
	}
	if err := m.ensureRunning(); err != nil {
		return nil, err
	}
	if m.opts.Sessions == nil {
		err := fmt.Errorf("未找到 session_id=%s 对应的聊天流", sessionID)
		zap.L().Error(fmt.Sprintf("创建心流聊天 %s 失败: %v", sessionID, err), zap.Error(err))
		return nil, err
	}
	session, ok, err := m.opts.Sessions.SessionByID(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("查询 session_id=%s 对应的聊天流失败: %w", sessionID, err)
	}
	if !ok {
		err := fmt.Errorf("未找到 session_id=%s 对应的聊天流", sessionID)
		zap.L().Error(fmt.Sprintf("创建心流聊天 %s 失败: %v", sessionID, err), zap.Error(err))
		return nil, err
	}
	allowed, err := m.isSessionAllowed(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("校验聊天流 %s 的适配器名单失败: %w", sessionID, err)
	}
	if !allowed {
		err := &PolicyDeniedError{SessionID: sessionID}
		zap.L().Warn(err.Error())
		return nil, err
	}
	if err := m.ensureRunning(); err != nil {
		return nil, err
	}
	if m.opts.New == nil {
		return nil, fmt.Errorf("创建心流聊天 %s 失败: 未配置运行时工厂", sessionID)
	}

	runtime, err := m.opts.New(ctx, session)
	if err != nil {
		wrapped := fmt.Errorf("创建心流聊天 %s 失败: %w", sessionID, err)
		zap.L().Error(wrapped.Error(), zap.Error(err))
		return nil, wrapped
	}
	m.startRuntime(runtime)

	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		_ = m.stopRuntime(runtime)
		return nil, ErrManagerStopping
	}
	if entry, exists := m.chats[sessionID]; exists {
		m.mu.Unlock()
		_ = m.stopRuntime(runtime)
		m.touch(sessionID)
		return entry.runtime, nil
	}
	entry := &chatEntry{runtime: runtime, session: session, lastActive: m.opts.Now()}
	m.chats[sessionID] = entry
	m.lastActive[sessionID] = entry.lastActive
	m.moveToEndLocked(sessionID)
	m.mu.Unlock()

	// 对齐 Python _evict_over_limit_chats：逐次重查上限后淘汰，避免并发创建时过量淘汰
	for {
		m.mu.Lock()
		candidate := m.nextEvictableLocked(sessionID)
		m.mu.Unlock()
		if candidate == "" {
			break
		}
		m.evictChat(ctx, candidate, "cache_limit")
	}
	return runtime, nil
}

// Get 查询已存在的运行时
func (m *Manager) Get(sessionID string) (Runtime, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.chats[sessionID]
	if !ok {
		return nil, false
	}
	return entry.runtime, true
}

// SessionIDs 按 LRU 顺序返回当前会话编号快照
func (m *Manager) SessionIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.order))
	copy(out, m.order)
	return out
}

// Len 当前运行实例数量
func (m *Manager) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.chats)
}

// Shutdown 关闭创建入口，等待在途创建完成，再停止所有会话
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.stopping = true
	locks := make([]*sync.Mutex, 0, len(m.createLocks))
	for _, lock := range m.createLocks {
		locks = append(locks, lock)
	}
	m.mu.Unlock()

	// 等待在途创建结束，避免关闭时仍向列表写入新实例
	for _, lock := range locks {
		lock.Lock()
		lock.Unlock()
	}

	m.mu.Lock()
	entries := make([]*chatEntry, 0, len(m.chats))
	for _, entry := range m.chats {
		entries = append(entries, entry)
	}
	m.mu.Unlock()

	type stopResult struct{ err error }
	results := make(chan stopResult, len(entries))
	var waitGroup sync.WaitGroup
	for _, entry := range entries {
		if entry.runtime == nil {
			continue
		}
		waitGroup.Add(1)
		go func(runtime Runtime) {
			defer waitGroup.Done()
			results <- stopResult{err: m.stopRuntime(runtime)}
		}(entry.runtime)
	}
	go func() {
		waitGroup.Wait()
		close(results)
	}()

	var failures []error
drain:
	for {
		select {
		case result, ok := <-results:
			if !ok {
				break drain
			}
			if result.err != nil {
				failures = append(failures, result.err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d 个聊天运行实例未能停止: %w", len(failures), failures[0])
	}

	m.mu.Lock()
	m.chats = map[string]*chatEntry{}
	m.order = nil
	m.lastActive = map[string]time.Time{}
	m.createLocks = map[string]*sync.Mutex{}
	m.mu.Unlock()
	return nil
}

// ClearChatHistoryContext 停止并移除当前会话运行时，使其短期历史上下文立即失效
func (m *Manager) ClearChatHistoryContext(ctx context.Context, sessionID string) (bool, error) {
	m.mu.Lock()
	_, exists := m.chats[sessionID]
	m.mu.Unlock()
	if !exists {
		if m.opts.Focus != nil {
			if err := m.opts.Focus.ClearContext(ctx, sessionID); err != nil {
				return false, err
			}
		}
		if m.opts.Planner != nil {
			if err := m.opts.Planner.ResetPlannerSession(ctx, sessionID); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	m.evictChat(ctx, sessionID, "clear_context_command")
	if m.opts.Planner != nil {
		if err := m.opts.Planner.ResetPlannerSession(ctx, sessionID); err != nil {
			return true, err
		}
	}
	return true, nil
}

// ClosePolicyBlockedChats 策略变更后停止并注销被禁用的运行实例
func (m *Manager) ClosePolicyBlockedChats(ctx context.Context) error {
	m.mu.Lock()
	entries := make([]*chatEntry, 0, len(m.chats))
	for _, entry := range m.chats {
		entries = append(entries, entry)
	}
	m.mu.Unlock()

	for _, entry := range entries {
		allowed, err := m.isSessionAllowed(ctx, entry.session)
		if err != nil {
			return err
		}
		if !allowed {
			m.evictChat(ctx, entry.session.SessionID, "adapter_policy_block")
		}
	}
	return nil
}

// AdjustTalkFrequency 调整指定聊天流的说话频率
func (m *Manager) AdjustTalkFrequency(sessionID string, frequency float64) bool {
	m.mu.Lock()
	entry, ok := m.chats[sessionID]
	if ok {
		entry.lastActive = m.opts.Now()
		m.lastActive[sessionID] = entry.lastActive
		m.moveToEndLocked(sessionID)
	}
	m.mu.Unlock()
	if !ok {
		zap.L().Warn("无法调整频率，未找到 session_id=" + sessionID + " 的聊天流")
		return false
	}
	entry.runtime.AdjustTalkFrequency(frequency)
	zap.L().Info("已调整聊天说话频率", zap.String("session_id", sessionID), zap.Float64("frequency", frequency))
	return true
}

// startRuntime 执行运行时启动钩子，未配置时沿用默认生命周期
func (m *Manager) startRuntime(runtime Runtime) {
	if runtime == nil {
		return
	}
	if m.opts.StartRuntime != nil {
		m.opts.StartRuntime(runtime)
		return
	}
	runtime.Start()
}

// stopRuntime 执行运行时停止钩子，未配置时沿用默认生命周期；捕获 panic 对齐 Python stop 异常
func (m *Manager) stopRuntime(runtime Runtime) (err error) {
	if runtime == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("stop runtime panic: %v", recovered)
		}
	}()
	if m.opts.StopRuntime != nil {
		return m.opts.StopRuntime(runtime)
	}
	runtime.Stop()
	return nil
}

func (m *Manager) ensureRunning() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping {
		return ErrManagerStopping
	}
	return nil
}

func (m *Manager) createLock(sessionID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, ok := m.createLocks[sessionID]
	if !ok {
		lock = &sync.Mutex{}
		m.createLocks[sessionID] = lock
	}
	return lock
}

func (m *Manager) lookupAndTouch(sessionID string) (Runtime, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.chats[sessionID]
	if !ok {
		return nil, false
	}
	entry.lastActive = m.opts.Now()
	m.lastActive[sessionID] = entry.lastActive
	m.moveToEndLocked(sessionID)
	return entry.runtime, true
}

func (m *Manager) touch(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.chats[sessionID]; !ok {
		return
	}
	m.lastActive[sessionID] = m.opts.Now()
	m.moveToEndLocked(sessionID)
}

func (m *Manager) moveToEndLocked(sessionID string) {
	for i, id := range m.order {
		if id == sessionID {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	m.order = append(m.order, sessionID)
}

// nextEvictableLocked 按 LRU 返回一个超过活跃保护窗口的候选会话；未超上限返回空
func (m *Manager) nextEvictableLocked(protected string) string {
	if len(m.chats) <= m.opts.MaxActiveChats {
		return ""
	}
	now := m.opts.Now()
	expireBefore := now.Add(-m.opts.ActiveRetention)
	for _, sessionID := range m.order {
		if sessionID == protected {
			continue
		}
		if _, exists := m.chats[sessionID]; !exists {
			continue
		}
		if m.lastActive[sessionID].After(expireBefore) {
			continue
		}
		return sessionID
	}
	return ""
}

func (m *Manager) evictChat(ctx context.Context, sessionID, reason string) {
	m.mu.Lock()
	entry, ok := m.chats[sessionID]
	if ok {
		delete(m.chats, sessionID)
		delete(m.lastActive, sessionID)
		if lock, exists := m.createLocks[sessionID]; exists && lock.TryLock() {
			lock.Unlock()
			delete(m.createLocks, sessionID)
		}
		for i, id := range m.order {
			if id == sessionID {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
	}
	m.mu.Unlock()
	if !ok || entry == nil {
		return
	}
	// 对齐 Python _evict_chat：clear_context 与 stop 同在 try；前者失败则不再 stop，整体 warning
	if reason == "clear_context_command" || reason == "adapter_policy_block" {
		if m.opts.Focus != nil {
			if err := m.opts.Focus.ClearContext(ctx, sessionID); err != nil {
				zap.L().Warn("淘汰心流聊天失败", zap.String("session_id", sessionID), zap.Error(err))
				return
			}
		}
	}
	if entry.runtime != nil {
		if err := m.stopRuntime(entry.runtime); err != nil {
			zap.L().Warn("淘汰心流聊天失败", zap.String("session_id", sessionID), zap.Error(err))
			return
		}
	}
	zap.L().Info("已淘汰心流聊天", zap.String("session_id", sessionID), zap.String("reason", reason))
}

func (m *Manager) isSessionAllowed(ctx context.Context, session ChatSession) (bool, error) {
	if _, ok := localoperator.LocalPlatformBotIDs[session.Platform]; ok {
		return true, nil
	}
	if session.Platform == "internal" || session.Platform == "webui" {
		return true, nil
	}
	if m.opts.Policy == nil {
		return true, nil
	}
	if ready, ok := m.opts.Policy.(interface {
		EnsureSendPipelineReady(context.Context) error
	}); ok {
		if err := ready.EnsureSendPipelineReady(ctx); err != nil {
			return false, err
		}
	}
	return m.opts.Policy.IsTargetAllowed(ctx, session)
}
