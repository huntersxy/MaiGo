package chatsession

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"MaiGo/internal/common/database"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Manager 对齐 MaiBot ChatManager 的内存索引 + 数据库 chat_sessions
type Manager struct {
	db   *gorm.DB
	mu   sync.RWMutex
	byID map[string]*Record
}

func NewManager(db *gorm.DB) *Manager {
	return &Manager{db: db, byID: map[string]*Record{}}
}

// LoadAllFromDB 启动时加载全部 chat_sessions 到内存
func (m *Manager) LoadAllFromDB(ctx context.Context) error {
	if m == nil || m.db == nil {
		return nil
	}
	var rows []Record
	if err := m.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID = make(map[string]*Record, len(rows))
	for i := range rows {
		row := rows[i]
		m.byID[row.SessionID] = &row
	}
	return nil
}

// InboundParams 入站消息注册聊天流时的身份字段
type InboundParams struct {
	Platform         string
	ConversationKind string
	TargetID         int64
	GroupKey         string
	UserID           int64
	// ExternalTargetID/ExternalUserID 保留非 QQ 平台原始字符串身份；
	// TargetID/UserID 仅在无外部身份时参与 session_id 计算。
	ExternalTargetID string
	ExternalUserID   string
	Nickname         string
	GroupCard        string
	GroupName        string
	AccountID        string
	Scope            string
}

// EnsureFromInbound 入站正式创建/更新链路：可计算 session_id 并写入 chat_sessions
func (m *Manager) EnsureFromInbound(ctx context.Context, p InboundParams) (*Record, error) {
	if m == nil || m.db == nil {
		return nil, nil
	}
	platform := strings.TrimSpace(p.Platform)
	externalTarget := strings.TrimSpace(p.ExternalTargetID)
	externalUser := strings.TrimSpace(p.ExternalUserID)
	if platform == "" || (p.TargetID <= 0 && externalTarget == "" && externalUser == "") {
		return nil, ErrInvalidTarget
	}
	kind := ChatTypeFromKind(p.ConversationKind)
	if kind == "" {
		return nil, ErrInvalidTarget
	}
	var userID, groupID string
	if kind == memory.ConversationKindGroup {
		groupID = strings.TrimSpace(p.GroupKey)
		if groupID == "" {
			groupID = externalTarget
		}
		if groupID == "" {
			groupID = strconv.FormatInt(p.TargetID, 10)
		}
	} else {
		userID = externalUser
		if userID == "" {
			userID = externalTarget
		}
		if userID == "" {
			userID = strconv.FormatInt(p.TargetID, 10)
		}
	}
	sessionID, err := CalculateSessionID(CalculateParams{
		Platform: platform, UserID: userID, GroupID: groupID,
		AccountID: p.AccountID, Scope: p.Scope,
	})
	if err != nil {
		return nil, err
	}
	return m.getOrCreateLocked(ctx, sessionID, platform, userID, groupID, p.AccountID, p.Scope, p.Nickname, p.GroupCard, p.GroupName)
}

// GetOrCreateSession 对齐 chat_manager._get_or_create_session（仅创建链路调用）
func (m *Manager) GetOrCreateSession(ctx context.Context, platform, userID, groupID, accountID, scope string) (*Record, error) {
	if m == nil || m.db == nil {
		return nil, nil
	}
	sessionID, err := CalculateSessionID(CalculateParams{
		Platform: platform, UserID: userID, GroupID: groupID,
		AccountID: accountID, Scope: scope,
	})
	if err != nil {
		return nil, err
	}
	return m.getOrCreateLocked(ctx, sessionID, strings.TrimSpace(platform), strings.TrimSpace(userID), strings.TrimSpace(groupID), accountID, scope, "", "", "")
}

func (m *Manager) getOrCreateLocked(ctx context.Context, sessionID, platform, userID, groupID, accountID, scope, nickname, cardname, groupName string) (*Record, error) {
	now := time.Now()
	m.mu.Lock()
	if existing := m.byID[sessionID]; existing != nil {
		rec := cloneRecord(existing)
		m.mu.Unlock()
		changed := applyRouteMetadata(rec, accountID, scope)
		changed = applyInboundIdentity(rec, groupID, nickname, cardname, groupName) || changed
		rec.LastActiveTimestamp = &now
		if changed {
			if err := m.save(ctx, rec); err != nil {
				return rec, err
			}
		} else {
			_ = m.db.WithContext(ctx).Model(&Record{}).Where("session_id=?", sessionID).
				Update("last_active_timestamp", now).Error
		}
		m.mu.Lock()
		m.byID[sessionID] = rec
		m.mu.Unlock()
		return rec, nil
	}
	m.mu.Unlock()

	var row Record
	err := m.db.WithContext(ctx).Where("session_id=?", sessionID).First(&row).Error
	if err == nil {
		rec := &row
		changed := applyRouteMetadata(rec, accountID, scope)
		changed = applyInboundIdentity(rec, groupID, nickname, cardname, groupName) || changed
		rec.LastActiveTimestamp = &now
		if changed {
			if err := m.save(ctx, rec); err != nil {
				return rec, err
			}
		} else {
			_ = m.db.WithContext(ctx).Model(&Record{}).Where("session_id=?", sessionID).
				Update("last_active_timestamp", now).Error
		}
		m.mu.Lock()
		m.byID[sessionID] = rec
		m.mu.Unlock()
		return rec, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, err
	}

	rec := newRecord(sessionID, platform, userID, groupID, accountID, scope, now)
	applyInboundIdentity(rec, groupID, nickname, cardname, groupName)
	if err := m.save(ctx, rec); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.byID[sessionID] = rec
	m.mu.Unlock()
	return rec, nil
}

// ResolveSessionsByTarget 只返回已注册或已入库的真实聊天流，不计算 fallback session_id
func (m *Manager) ResolveSessionsByTarget(ctx context.Context, platform, targetID, chatType string) ([]Record, error) {
	if m == nil {
		return nil, nil
	}
	platform = strings.TrimSpace(platform)
	targetID = strings.TrimSpace(targetID)
	chatType = strings.TrimSpace(chatType)
	if platform == "" || targetID == "" {
		return nil, nil
	}
	var targetAttr string
	switch chatType {
	case memory.ConversationKindGroup:
		targetAttr = "group_id"
	case memory.ConversationKindPrivate:
		targetAttr = "user_id"
	default:
		return nil, nil
	}

	matched := map[string]Record{}
	m.mu.RLock()
	for _, rec := range m.byID {
		if sessionMatchesTarget(rec, platform, targetAttr, targetID) {
			matched[rec.SessionID] = *rec
		}
	}
	m.mu.RUnlock()

	if m.db != nil {
		var rows []Record
		if err := m.db.WithContext(ctx).Where("platform=?", platform).Find(&rows).Error; err != nil {
			return nil, err
		}
		for i := range rows {
			row := rows[i]
			if !sessionMatchesTarget(&row, platform, targetAttr, targetID) {
				continue
			}
			if _, ok := matched[row.SessionID]; ok {
				continue
			}
			matched[row.SessionID] = row
			m.mu.Lock()
			copy := row
			m.byID[row.SessionID] = &copy
			m.mu.Unlock()
		}
	}
	out := make([]Record, 0, len(matched))
	for _, rec := range matched {
		out = append(out, rec)
	}
	return out, nil
}

// ResolveSessionIDsByTarget 返回已知真实 session_id 集合
func (m *Manager) ResolveSessionIDsByTarget(ctx context.Context, platform, targetID, chatType string) ([]string, error) {
	sessions, err := m.ResolveSessionsByTarget(ctx, platform, targetID, chatType)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(sessions))
	for _, rec := range sessions {
		ids = append(ids, rec.SessionID)
	}
	return ids, nil
}

// TouchSession 刷新会话最后活跃时间，对齐 BotChatSession.update_active_time
func (m *Manager) TouchSession(ctx context.Context, sessionID string) error {
	if m == nil || m.db == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	now := time.Now()
	if err := m.db.WithContext(ctx).Model(&Record{}).Where("session_id=?", sessionID).
		Update("last_active_timestamp", now).Error; err != nil {
		return err
	}
	m.mu.Lock()
	if rec := m.byID[sessionID]; rec != nil {
		rec.LastActiveTimestamp = &now
	}
	m.mu.Unlock()
	return nil
}

// ResolveSession 解析唯一聊天流；不存在返回 ErrNotFound，多条返回 ErrAmbiguous
func (m *Manager) ResolveSession(ctx context.Context, platform, targetID, chatType string) (*Record, error) {
	sessions, err := m.ResolveSessionsByTarget(ctx, platform, targetID, chatType)
	if err != nil {
		return nil, err
	}
	switch len(sessions) {
	case 0:
		return nil, ErrNotFound
	case 1:
		rec := sessions[0]
		return &rec, nil
	default:
		return nil, ErrAmbiguous
	}
}

// ResolveSessionByConversationKey 从 group:ID / private:ID 解析 MaiBot session_id（不写入）
func (m *Manager) ResolveSessionByConversationKey(ctx context.Context, platform, conversationKey string) (*Record, error) {
	kind, targetID, err := ParseConversationKey(conversationKey)
	if err != nil {
		return nil, err
	}
	return m.ResolveSession(ctx, platform, strconv.FormatInt(targetID, 10), kind)
}

// GetExistingBySessionID 按 session_id 加载已存在记录
func (m *Manager) GetExistingBySessionID(ctx context.Context, sessionID string) (*Record, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, ErrNotFound
	}
	m.mu.RLock()
	if rec := m.byID[sessionID]; rec != nil {
		copy := *rec
		m.mu.RUnlock()
		return &copy, nil
	}
	m.mu.RUnlock()
	if m.db == nil {
		return nil, ErrNotFound
	}
	var row Record
	if err := m.db.WithContext(ctx).Where("session_id=?", sessionID).First(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrNotFound
		}
		return nil, err
	}
	m.mu.Lock()
	copy := row
	m.byID[row.SessionID] = &copy
	m.mu.Unlock()
	return &row, nil
}

// ValidatePersistableSessionID 拒绝 conversation key 与未入库的 session_id 写入资源表
func (m *Manager) ValidatePersistableSessionID(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if IsConversationKey(sessionID) {
		return ErrConversationKeyNotSessionID
	}
	_, err := m.GetExistingBySessionID(ctx, sessionID)
	return err
}

// DisplayName 对齐 chat_manager.get_session_name 的简化版
func DisplayName(rec *Record) string {
	if rec == nil {
		return ""
	}
	if rec.IsGroup() {
		if rec.GroupName != nil {
			if name := strings.TrimSpace(*rec.GroupName); name != "" {
				return name
			}
		}
		return ""
	}
	if rec.UserNickname != nil {
		if name := strings.TrimSpace(*rec.UserNickname); name != "" {
			return name + "的私聊"
		}
	}
	return ""
}

func sessionMatchesTarget(rec *Record, platform, targetAttr, targetID string) bool {
	if rec == nil {
		return false
	}
	if strings.TrimSpace(rec.Platform) != platform {
		return false
	}
	if (targetAttr == "group_id") != rec.IsGroup() {
		return false
	}
	switch targetAttr {
	case "group_id":
		if rec.GroupID == nil {
			return false
		}
		return strings.TrimSpace(*rec.GroupID) == targetID
	case "user_id":
		if rec.UserID == nil {
			return false
		}
		return strings.TrimSpace(*rec.UserID) == targetID
	default:
		return false
	}
}

func newRecord(sessionID, platform, userID, groupID, accountID, scope string, now time.Time) *Record {
	// 对齐 Python BotChatSession：新建会话 last_active_timestamp 保持 None（写 NULL），
	// 只有后续 get_or_create 命中时 update_active_time 才填当前时刻
	rec := &Record{
		SessionID:        sessionID,
		Platform:         platform,
		CreatedTimestamp: now,
	}
	if groupID != "" {
		g := groupID
		rec.GroupID = &g
	} else if userID != "" {
		u := userID
		rec.UserID = &u
	}
	if accountID = strings.TrimSpace(accountID); accountID != "" {
		a := accountID
		rec.AccountID = &a
	}
	if scope = strings.TrimSpace(scope); scope != "" {
		s := scope
		rec.Scope = &s
	}
	return rec
}

func applyRouteMetadata(rec *Record, accountID, scope string) bool {
	if rec == nil {
		return false
	}
	changed := false
	if accountID = strings.TrimSpace(accountID); accountID != "" {
		if rec.AccountID == nil || strings.TrimSpace(*rec.AccountID) == "" {
			a := accountID
			rec.AccountID = &a
			changed = true
		}
	}
	if scope = strings.TrimSpace(scope); scope != "" {
		if rec.Scope == nil || strings.TrimSpace(*rec.Scope) == "" {
			s := scope
			rec.Scope = &s
			changed = true
		}
	}
	return changed
}

func applyInboundIdentity(rec *Record, groupID, nickname, cardname, groupName string) bool {
	if rec == nil {
		return false
	}
	changed := false
	if groupID != "" {
		if groupName = strings.TrimSpace(groupName); groupName != "" {
			if rec.GroupName == nil || strings.TrimSpace(*rec.GroupName) != groupName {
				gn := groupName
				rec.GroupName = &gn
				changed = true
			}
		}
		if rec.UserID != nil {
			rec.UserID = nil
			changed = true
		}
		if rec.UserNickname != nil {
			rec.UserNickname = nil
			changed = true
		}
		if rec.UserCardname != nil {
			rec.UserCardname = nil
			changed = true
		}
		return changed
	}
	if nickname = strings.TrimSpace(nickname); nickname != "" {
		if rec.UserNickname == nil || strings.TrimSpace(*rec.UserNickname) != nickname {
			n := nickname
			rec.UserNickname = &n
			changed = true
		}
	}
	cardname = strings.TrimSpace(cardname)
	if rec.UserCardname == nil || strings.TrimSpace(ptrStr(rec.UserCardname)) != cardname {
		var c *string
		if cardname != "" {
			c = &cardname
		}
		rec.UserCardname = c
		changed = true
	}
	return changed
}

func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func cloneRecord(rec *Record) *Record {
	if rec == nil {
		return nil
	}
	copy := *rec
	return &copy
}

func (m *Manager) save(ctx context.Context, rec *Record) error {
	return m.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "session_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"last_active_timestamp", "user_id", "user_nickname", "user_cardname",
			"group_id", "group_name", "account_id", "scope",
		}),
	}).Create(rec).Error
}
