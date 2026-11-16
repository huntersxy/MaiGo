package messagereceive

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"MaiGo/internal/chat/session"

	"go.uber.org/zap"
)

// SessionContext 对齐 MaiBot SessionContext
type SessionContext struct {
	Message      *SessionMessage
	TemplateName string
}

// UpdateTemplate 更新模板名
func (c *SessionContext) UpdateTemplate(name string) {
	if c == nil {
		return
	}
	c.TemplateName = name
}

// BotChatSession 对齐 MaiBot BotChatSession，并保留 chat_sessions 持久化记录
type BotChatSession struct {
	Record       chatsession.Record
	Context      *SessionContext
	AcceptFormat []string
}

// SessionID 返回会话 ID
func (s *BotChatSession) SessionID() string {
	if s == nil {
		return ""
	}
	return s.Record.SessionID
}

// IsGroup 是否为群聊
func (s *BotChatSession) IsGroup() bool {
	return s != nil && s.Record.IsGroup()
}

// UpdateActiveTime 刷新活跃时间
func (s *BotChatSession) UpdateActiveTime(now time.Time) {
	if s == nil {
		return
	}
	value := now
	s.Record.LastActiveTimestamp = &value
}

// CheckTypes 判断 accept_format 是否覆盖全部类型
func (s *BotChatSession) CheckTypes(types []string) bool {
	if s == nil {
		return false
	}
	if len(types) == 0 {
		return true
	}
	allowed := make(map[string]struct{}, len(s.AcceptFormat))
	for _, item := range s.AcceptFormat {
		allowed[strings.TrimSpace(item)] = struct{}{}
	}
	for _, item := range types {
		if _, ok := allowed[strings.TrimSpace(item)]; !ok {
			return false
		}
	}
	return true
}

// SetContext 绑定最近一条消息
func (s *BotChatSession) SetContext(message *SessionMessage) {
	if s == nil {
		return
	}
	if s.Context == nil {
		s.Context = &SessionContext{}
	}
	s.Context.Message = message
}

// ChatManager 对齐 MaiBot ChatManager 的内存索引与 chat_sessions 持久化
type ChatManager struct {
	mu           sync.RWMutex
	repo         SessionRepository
	sessions     map[string]*BotChatSession
	lastMessages map[string]*SessionMessage
	now          func() time.Time
}

// NewChatManager 构造聊天流管理器；repo 可为 nil，此时只维护内存
func NewChatManager(repo SessionRepository) *ChatManager {
	return &ChatManager{
		repo:         repo,
		sessions:     map[string]*BotChatSession{},
		lastMessages: map[string]*SessionMessage{},
		now:          time.Now,
	}
}

// SetNow 注入时钟，测试用
func (m *ChatManager) SetNow(now func() time.Time) {
	if m == nil || now == nil {
		return
	}
	m.now = now
}

// LoadAllFromDB 清空并加载全部聊天流，异常时清空并返回错误
func (m *ChatManager) LoadAllFromDB(ctx context.Context) error {
	if m == nil {
		return nil
	}
	// 对齐 Python load_all_sessions_from_db：只清 sessions，保留 last_messages
	m.mu.Lock()
	m.sessions = map[string]*BotChatSession{}
	m.mu.Unlock()
	if m.repo == nil {
		return nil
	}
	rows, err := m.repo.LoadAll(ctx)
	if err != nil {
		zap.L().Error("从数据库加载会话记录时发生错误", zap.Error(err))
		m.mu.Lock()
		m.sessions = map[string]*BotChatSession{}
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := range rows {
		row := rows[index]
		m.sessions[row.SessionID] = sessionFromRecord(row)
	}
	return nil
}

// RegisterMessage 对齐 chat_manager.register_message：计算 session_id 并记录最近消息
func (m *ChatManager) RegisterMessage(ctx context.Context, message *SessionMessage) error {
	if m == nil || message == nil {
		return nil
	}
	platform := strings.TrimSpace(message.Platform)
	if platform == "" {
		return fmt.Errorf("消息缺少平台信息")
	}
	accountID, scope := pointerText(message.AccountID), pointerText(message.Scope)
	sessionID, err := chatsession.CalculateSessionID(chatsession.CalculateParams{
		Platform:  platform,
		UserID:    message.UserID(),
		GroupID:   groupIDOf(message),
		AccountID: accountID,
		Scope:     scope,
	})
	if err != nil {
		return err
	}
	message.SessionID = sessionID
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastMessages[sessionID] = message
	if session := m.sessions[sessionID]; session != nil {
		changed := updateSessionIdentity(session, message)
		if changed {
			return m.saveLocked(ctx, session)
		}
	}
	return nil
}

// GetOrCreateSession 对齐 chat_manager.get_or_create_session
func (m *ChatManager) GetOrCreateSession(ctx context.Context, platform, userID, groupID, accountID, scope string) (*BotChatSession, error) {
	if m == nil {
		return nil, nil
	}
	sessionID, err := chatsession.CalculateSessionID(chatsession.CalculateParams{
		Platform: platform, UserID: userID, GroupID: groupID,
		AccountID: accountID, Scope: scope,
	})
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if session := m.sessions[sessionID]; session != nil {
		changed := applyRouteMetadata(&session.Record, accountID, scope)
		session.UpdateActiveTime(now)
		if last := m.lastMessages[sessionID]; last != nil {
			changed = updateSessionIdentity(session, last) || changed
			session.SetContext(last)
		}
		if changed {
			if err := m.saveLocked(ctx, session); err != nil {
				return session, err
			}
		}
		return session, nil
	}

	if m.repo != nil {
		row, err := m.repo.Get(ctx, sessionID)
		if err != nil {
			zap.L().Error("从数据库获取会话时发生错误", zap.Error(err))
			return nil, err
		}
		if row != nil {
			session := sessionFromRecord(*row)
			changed := applyRouteMetadata(&session.Record, accountID, scope)
			session.UpdateActiveTime(now)
			if last := m.lastMessages[sessionID]; last != nil {
				changed = updateSessionIdentity(session, last) || changed
				session.SetContext(last)
			}
			if changed {
				if err := m.saveLocked(ctx, session); err != nil {
					return session, err
				}
			}
			m.sessions[sessionID] = session
			return session, nil
		}
	}

	session := &BotChatSession{Record: chatsession.Record{
		SessionID:        sessionID,
		Platform:         strings.TrimSpace(platform),
		CreatedTimestamp: now,
	}}
	if groupID = strings.TrimSpace(groupID); groupID != "" {
		value := groupID
		session.Record.GroupID = &value
	} else if userID = strings.TrimSpace(userID); userID != "" {
		value := userID
		session.Record.UserID = &value
	}
	applyRouteMetadata(&session.Record, accountID, scope)
	if last := m.lastMessages[sessionID]; last != nil {
		updateSessionIdentity(session, last)
		session.SetContext(last)
	}
	if err := m.saveLocked(ctx, session); err != nil {
		return session, err
	}
	m.sessions[sessionID] = session
	return session, nil
}

// Initialize 对齐 chat_manager.initialize：加载失败只记录，不阻断启动
func (m *ChatManager) Initialize(ctx context.Context) {
	if m == nil {
		return
	}
	if err := m.LoadAllFromDB(ctx); err != nil {
		zap.L().Error("初始化聊天管理器出现错误", zap.Error(err))
	}
}

// GetSessionByInfo 根据平台、用户、群与路由元数据查找已注册会话
func (m *ChatManager) GetSessionByInfo(ctx context.Context, platform, userID, groupID, accountID, scope string) (*BotChatSession, bool) {
	if m == nil {
		return nil, false
	}
	sessionID, err := chatsession.CalculateSessionID(chatsession.CalculateParams{
		Platform: platform, UserID: userID, GroupID: groupID, AccountID: accountID, Scope: scope,
	})
	if err != nil {
		return nil, false
	}
	if session, ok := m.GetSessionBySessionID(sessionID); ok {
		return session, true
	}
	return m.GetExistingSessionBySessionID(ctx, sessionID)
}

// GetSessionBySessionID 从内存取会话，并用最近消息更新上下文
func (m *ChatManager) GetSessionBySessionID(sessionID string) (*BotChatSession, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[strings.TrimSpace(sessionID)]
	if session == nil {
		return nil, false
	}
	if last := m.lastMessages[session.SessionID()]; last != nil {
		session.SetContext(last)
	}
	return session, true
}

// GetExistingSessionBySessionID 内存优先、数据库兜底；不存在返回 nil,false
func (m *ChatManager) GetExistingSessionBySessionID(ctx context.Context, sessionID string) (*BotChatSession, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || m == nil {
		return nil, false
	}
	if session, ok := m.GetSessionBySessionID(sessionID); ok {
		return session, true
	}
	if m.repo == nil {
		return nil, false
	}
	row, err := m.repo.Get(ctx, sessionID)
	if err != nil || row == nil {
		return nil, false
	}
	session := sessionFromRecord(*row)
	m.mu.Lock()
	m.sessions[sessionID] = session
	if last := m.lastMessages[sessionID]; last != nil {
		session.SetContext(last)
	}
	m.mu.Unlock()
	return session, true
}

// AllSessions 返回已登记会话快照，对齐 chat_manager.sessions
func (m *ChatManager) AllSessions() []*BotChatSession {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*BotChatSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		out = append(out, session)
	}
	return out
}

// GetSessionName 对齐 chat_manager.get_session_name
func (m *ChatManager) GetSessionName(session *BotChatSession) string {
	if session == nil {
		return ""
	}
	if session.IsGroup() {
		if name := pointerText(session.Record.GroupName); name != "" {
			return name
		}
		if session.Context != nil && session.Context.Message != nil {
			return session.Context.Message.GroupName()
		}
		return ""
	}
	if name := pointerText(session.Record.UserNickname); name != "" {
		return name + "的私聊"
	}
	if session.Context != nil && session.Context.Message != nil {
		if name := session.Context.Message.Nickname(); name != "" {
			return name + "的私聊"
		}
	}
	return ""
}

// NamedSessionOption 对齐 get_named_session_options 的标签与 session_id 对
type NamedSessionOption struct {
	Label     string
	SessionID string
}

// NamedSessionOptions 生成 /clear 补全用的聊天名选项
func (m *ChatManager) NamedSessionOptions(excludedPlatforms map[string]struct{}) []NamedSessionOption {
	if m == nil {
		return nil
	}
	exclude := make(map[string]struct{}, len(excludedPlatforms))
	for platform := range excludedPlatforms {
		if normalized := strings.TrimSpace(platform); normalized != "" {
			exclude[normalized] = struct{}{}
		}
	}
	m.mu.RLock()
	sessions := make([]*BotChatSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.RUnlock()
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].Record.Platform != sessions[j].Record.Platform {
			return sessions[i].Record.Platform < sessions[j].Record.Platform
		}
		return sessions[i].SessionID() < sessions[j].SessionID()
	})
	type named struct {
		name      string
		platform  string
		targetID  string
		sessionID string
	}
	items := make([]named, 0, len(sessions))
	nameCount := map[string]int{}
	for _, session := range sessions {
		if _, skip := exclude[session.Record.Platform]; skip {
			continue
		}
		name := strings.TrimSpace(m.GetSessionName(session))
		if name == "" {
			continue
		}
		items = append(items, named{name: name, platform: session.Record.Platform, targetID: sessionTargetID(session), sessionID: session.SessionID()})
		nameCount[name]++
	}
	// 对齐 Python：先按 name/platform/session_id 排序；重名统一加 [platform:target]；
	// 仅当最终 label 已占用时，给后出现的条目追加 [session_id]（第一个保留原样）。
	sort.Slice(items, func(i, j int) bool {
		if items[i].name != items[j].name {
			return items[i].name < items[j].name
		}
		if items[i].platform != items[j].platform {
			return items[i].platform < items[j].platform
		}
		return items[i].sessionID < items[j].sessionID
	})
	out := make([]NamedSessionOption, 0, len(items))
	used := map[string]struct{}{}
	for _, item := range items {
		label := item.name
		if nameCount[item.name] > 1 {
			label = item.name + " [" + item.platform + ":" + item.targetID + "]"
		}
		if _, exists := used[label]; exists {
			label = label + " [" + item.sessionID + "]"
		}
		used[label] = struct{}{}
		out = append(out, NamedSessionOption{Label: label, SessionID: item.sessionID})
	}
	return out
}

// ResolveSessionsByTarget 对齐 chat_manager.resolve_sessions_by_target
func (m *ChatManager) ResolveSessionsByTarget(ctx context.Context, platform, targetID, chatType string) ([]*BotChatSession, error) {
	platform = strings.TrimSpace(platform)
	targetID = strings.TrimSpace(targetID)
	chatType = strings.TrimSpace(chatType)
	if platform == "" || targetID == "" || m == nil {
		return nil, nil
	}
	if chatType != "group" && chatType != "private" {
		return nil, nil
	}
	matched := map[string]*BotChatSession{}
	m.mu.RLock()
	for _, session := range m.sessions {
		if sessionMatchesTarget(session, platform, targetID, chatType) {
			matched[session.SessionID()] = session
		}
	}
	m.mu.RUnlock()
	if m.repo != nil {
		rows, err := m.repo.LoadAll(ctx)
		if err != nil {
			// Python 在按目标解析时吞掉数据库异常，保留内存命中结果
			zap.L().Error("按目标解析聊天流失败",
				zap.String("platform", platform),
				zap.String("target_id", targetID),
				zap.String("chat_type", chatType),
				zap.Error(err))
			rows = nil
		}
		for index := range rows {
			row := rows[index]
			if strings.TrimSpace(row.Platform) != platform || !recordMatchesTarget(&row, targetID, chatType) {
				continue
			}
			if _, ok := matched[row.SessionID]; ok {
				continue
			}
			session := sessionFromRecord(row)
			m.mu.Lock()
			m.sessions[session.SessionID()] = session
			if last := m.lastMessages[session.SessionID()]; last != nil {
				session.SetContext(last)
			}
			m.mu.Unlock()
			matched[session.SessionID()] = session
		}
	}
	out := make([]*BotChatSession, 0, len(matched))
	for _, session := range matched {
		out = append(out, session)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID() < out[j].SessionID() })
	return out, nil
}

// ResolveSessionIDsByTarget 返回目标对应的 session_id 集合
func (m *ChatManager) ResolveSessionIDsByTarget(ctx context.Context, platform, targetID, chatType string) ([]string, error) {
	sessions, err := m.ResolveSessionsByTarget(ctx, platform, targetID, chatType)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, session.SessionID())
	}
	return out, nil
}

// SaveAllSessions 保存全部内存会话
func (m *ChatManager) SaveAllSessions(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	sessions := make([]*BotChatSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.RUnlock()
	for _, session := range sessions {
		m.mu.Lock()
		err := m.saveLocked(ctx, session)
		m.mu.Unlock()
		if err != nil {
			zap.L().Error("保存会话记录到数据库时发生错误", zap.Error(err))
			return err
		}
	}
	return nil
}

// StartPeriodicSave 对齐 regularly_save_sessions；ctx 取消后退出
func (m *ChatManager) StartPeriodicSave(ctx context.Context, interval time.Duration) {
	if m == nil {
		return
	}
	if interval <= 0 {
		interval = 300 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := m.SaveAllSessions(ctx); err != nil {
					zap.L().Error("定期保存会话记录时发生错误", zap.Error(err))
				}
			}
		}
	}()
}

func (m *ChatManager) saveLocked(ctx context.Context, session *BotChatSession) error {
	if m.repo == nil || session == nil {
		return nil
	}
	record := session.Record
	return m.repo.Save(ctx, &record)
}
