package focus

import (
	"context"
	"fmt"
	"sync"

	"maibot-go/internal/maisaka"
)

// SessionLookup 会话投影查询，用于把 session_id 解析为 Focus 判定所需的字段
type SessionLookup interface {
	// SessionInfo 返回会话投影；不存在时 ok=false
	SessionInfo(sessionID string) (SessionInfo, bool)
}

// ManagerOptions Focus 共享组管理器的装配参数
type ManagerOptions struct {
	// Settings 返回当前 Focus 配置快照，允许热重载
	Settings func() Settings
	// Lookup 会话投影查询
	Lookup SessionLookup
	// NewGroup 构造共享组运行时，nil 时用默认装配
	NewGroup func(scopeID string) *GroupRuntime
	// OpenSession 按需恢复检查点里的其他真实会话，nil 表示不做恢复
	OpenSession func(sessionID string) (context.Context, Member, bool)
}

// FocusModeManager 同一 bot 循环内维护每个共享组唯一的 Planner
type FocusModeManager struct {
	mu      sync.Mutex
	options ManagerOptions
	groups  map[string]*GroupRuntime
}

// NewFocusModeManager 构造管理器
func NewFocusModeManager(options ManagerOptions) *FocusModeManager {
	return &FocusModeManager{options: options, groups: map[string]*GroupRuntime{}}
}

// IsEnabled 全局 Focus 实验开关
func (m *FocusModeManager) IsEnabled() bool {
	if m == nil || m.options.Settings == nil {
		return false
	}
	return IsEnabled(m.options.Settings())
}

// IsEnabledForChat 群聊默认启用；私聊需 focus_on_private
func (m *FocusModeManager) IsEnabledForChat(isGroupChat *bool) bool {
	if m == nil || m.options.Settings == nil {
		return false
	}
	settings := m.options.Settings()
	if !IsEnabled(settings) {
		return false
	}
	if isGroupChat != nil && !*isGroupChat {
		return settings.FocusOnPrivate
	}
	return true
}

// IsEnabledForSession 会话是否应进入 Focus 共享 Planner
func (m *FocusModeManager) IsEnabledForSession(sessionID string, isGroupChat *bool) bool {
	if m == nil || sessionID == "" || m.options.Settings == nil {
		return false
	}
	settings := m.options.Settings()
	session, ok := m.sessionInfo(sessionID)
	if !ok {
		return false
	}
	group := session.IsGroup
	if isGroupChat != nil {
		group = *isGroupChat
	}
	session.IsGroup = group
	return IsEnabledForSession(settings, session)
}

// GetFocusScopeKey 共享组身份；组身份基于规则内容，不因配置列表重新排序而改变
func (m *FocusModeManager) GetFocusScopeKey(sessionID string) (string, error) {
	if m == nil || sessionID == "" {
		return "", fmt.Errorf("Focus 需要非空 session_id")
	}
	if m.options.Settings == nil {
		return "", fmt.Errorf("Focus 配置未初始化")
	}
	session, ok := m.sessionInfo(sessionID)
	if !ok {
		return "", fmt.Errorf("Focus 聊天流不存在：%s", sessionID)
	}
	return GetFocusScopeKey(m.options.Settings(), session)
}

func (m *FocusModeManager) sessionInfo(sessionID string) (SessionInfo, bool) {
	if m.options.Lookup != nil {
		return m.options.Lookup.SessionInfo(sessionID)
	}
	return SessionInfo{SessionID: sessionID}, true
}

// Attach 把会话运行时挂到唯一共享组，必要时创建并恢复
func (m *FocusModeManager) Attach(ctx context.Context, member Member) error {
	if m == nil || member == nil {
		return nil
	}
	scopeID, err := m.GetFocusScopeKey(member.SessionID())
	if err != nil {
		return err
	}
	m.mu.Lock()
	group := m.groups[scopeID]
	created := false
	if group == nil {
		if m.options.NewGroup != nil {
			group = m.options.NewGroup(scopeID)
		} else {
			group = NewGroupRuntime(scopeID, GroupOptions{})
		}
		group.hydrating = true
		if err := group.Restore(ctx); err != nil {
			m.mu.Unlock()
			return err
		}
		m.groups[scopeID] = group
		created = true
	}
	group.AddMember(member)
	member.SetFocusGroup(group)
	m.mu.Unlock()
	if created {
		m.hydrateRestoredSessions(ctx, group)
		group.hydrating = false
		group.EnsureWorker()
	}
	return nil
}

// hydrateRestoredSessions 新版检查点里的其他真实会话按需恢复服务
func (m *FocusModeManager) hydrateRestoredSessions(ctx context.Context, group *GroupRuntime) {
	if m.options.OpenSession == nil {
		return
	}
	var keep []*Entry
	for _, entry := range group.entries {
		drop := false
		for sessionID := range entry.SessionIDs {
			if _, ok := group.members[sessionID]; ok {
				continue
			}
			session, ok := m.sessionInfo(sessionID)
			if !ok || !m.IsEnabledForSession(sessionID, &session.IsGroup) {
				drop = true
				continue
			}
			memberCtx, member, ok := m.options.OpenSession(sessionID)
			if !ok {
				drop = true
				continue
			}
			_ = memberCtx
			group.AddMember(member)
		}
		if !drop {
			keep = append(keep, entry)
		}
	}
	group.entries = keep
}

// Detach 把会话从共享组摘除
func (m *FocusModeManager) Detach(ctx context.Context, member Member, clearContext bool) error {
	if m == nil || member == nil || member.FocusGroup() == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.detachMemberLocked(ctx, member, clearContext)
}

func (m *FocusModeManager) detachMemberLocked(ctx context.Context, member Member, clearContext bool) error {
	group := member.FocusGroup()
	if group == nil {
		return nil
	}
	member.SetFocusGroup(nil)
	if err := group.RemoveMember(ctx, member.SessionID(), clearContext); err != nil {
		return err
	}
	if len(group.Members()) == 0 {
		group.Stop()
		delete(m.groups, group.ScopeID())
	}
	return nil
}

// ClearContext 清理会话在共享组与检查点中的全部材料
func (m *FocusModeManager) ClearContext(ctx context.Context, sessionID string) error {
	if m == nil || sessionID == "" {
		return nil
	}
	m.mu.Lock()
	groups := make([]*GroupRuntime, 0, len(m.groups))
	for _, group := range m.groups {
		groups = append(groups, group)
	}
	m.mu.Unlock()
	excluded := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if member, ok := group.Members()[sessionID]; ok {
			if err := m.Detach(ctx, member, true); err != nil {
				return err
			}
		} else if group.hasSession(sessionID) {
			if err := group.RemoveMember(ctx, sessionID, true); err != nil {
				return err
			}
		}
		if group.Store() != nil {
			excluded[group.Store().Path()] = struct{}{}
		}
	}
	root := DefaultCheckpointRoot
	if len(groups) > 0 && groups[0].Store() != nil {
		root = groups[0].checkpointRoot
	}
	return PurgeSession(sessionID, excluded, root)
}

// Reconcile 在入站与配置刷新边界交接归属，避免普通循环与共享循环同时处理同一聊天
func (m *FocusModeManager) Reconcile(ctx context.Context, member Member) error {
	if m == nil || member == nil {
		return nil
	}
	enabled := m.IsEnabledForSession(member.SessionID(), boolPtr(member.IsGroupSession()))
	group := member.FocusGroup()
	var pending []maisaka.ContextMessage
	if group != nil {
		scopeID, err := m.GetFocusScopeKey(member.SessionID())
		if err != nil {
			return err
		}
		if !enabled || group.ScopeID() != scopeID {
			pending, err = group.UnprocessedMessages(member.SessionID())
			if err != nil {
				return err
			}
			if err := m.Detach(ctx, member, true); err != nil {
				return err
			}
		}
	}
	if enabled && member.FocusGroup() == nil {
		if err := m.Attach(ctx, member); err != nil {
			return err
		}
		pending = append(pending, member.CollectPendingMessages()...)
		seen := map[string]struct{}{}
		for _, message := range pending {
			if message.MessageID != "" {
				if _, ok := seen[message.MessageID]; ok {
					continue
				}
				seen[message.MessageID] = struct{}{}
			}
			if err := member.FocusGroup().RegisterMessage(ctx, member, message); err != nil {
				return err
			}
		}
	}
	return nil
}

// Groups 当前存活的共享组，调试与测试用
func (m *FocusModeManager) Groups() map[string]*GroupRuntime {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]*GroupRuntime, len(m.groups))
	for key, group := range m.groups {
		out[key] = group
	}
	return out
}

func boolPtr(value bool) *bool { return &value }
