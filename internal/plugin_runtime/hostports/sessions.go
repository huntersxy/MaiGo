package hostports

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"MaiGo/internal/chat/session"
	"MaiGo/internal/core/localoperator"
	"MaiGo/internal/platform_io"
	"MaiGo/internal/plugin_runtime"

	"gorm.io/gorm"
)

// Sessions 插件会话端口，基于 chat_sessions 表
type Sessions struct {
	db      *gorm.DB
	manager *chatsession.Manager
	avatar  *platformio.AvatarService
	routes  SessionRouteResolver
	openMu  sync.Mutex
}

// SessionRouteResolver 解析新会话的 Bot 账号与适配器作用域
type SessionRouteResolver interface {
	ResolveNewSessionRoute(ctx context.Context, platform, accountID, scope string) (string, string, error)
}

// SessionsOption 会话端口可选依赖
type SessionsOption func(*Sessions)

// WithAvatarService 注入头像服务；未注入时 chat.get_avatar 返回真实的 unsupported 状态
func WithAvatarService(service *platformio.AvatarService) SessionsOption {
	return func(s *Sessions) {
		s.avatar = service
	}
}

// WithSessionRouteResolver 注入新会话路由解析器
func WithSessionRouteResolver(resolver SessionRouteResolver) SessionsOption {
	return func(s *Sessions) {
		s.routes = resolver
	}
}

// NewSessions 构造会话端口并同步加载 chat_sessions
func NewSessions(db *gorm.DB, opts ...SessionsOption) *Sessions {
	manager := chatsession.NewManager(db)
	if db != nil {
		_ = manager.LoadAllFromDB(context.Background())
	}
	s := &Sessions{db: db, manager: manager}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// SetAvatarService 在构造后注入或替换头像服务
func (s *Sessions) SetAvatarService(service *platformio.AvatarService) {
	if s != nil {
		s.avatar = service
	}
}

// Record 按 session_id 或 conversation key 解析会话
func (s *Sessions) Record(ctx context.Context, sessionID string) (*chatsession.Record, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("stream_id 不能为空")
	}
	if s == nil || s.manager == nil {
		return nil, fmt.Errorf("会话存储未初始化")
	}
	if rec, err := s.manager.GetExistingBySessionID(ctx, sessionID); err == nil && rec != nil {
		return rec, nil
	}
	if rec, err := s.manager.ResolveSessionByConversationKey(ctx, "qq", sessionID); err == nil && rec != nil {
		return rec, nil
	}
	return nil, fmt.Errorf("未找到会话: %s", sessionID)
}

// ListStreams 实现 pluginruntime.PluginSessionPort
func (s *Sessions) ListStreams(ctx context.Context, filter pluginruntime.PluginSessionFilter) ([]map[string]any, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("会话存储未初始化")
	}
	query := s.db.WithContext(ctx).Model(&chatsession.Record{})
	if !filter.AllPlatforms && strings.TrimSpace(filter.Platform) != "" {
		query = query.Where("platform = ?", strings.TrimSpace(filter.Platform))
	}
	if filter.IsGroup != nil {
		if *filter.IsGroup {
			query = query.Where("group_id IS NOT NULL AND group_id <> ''")
		} else {
			query = query.Where("group_id IS NULL OR group_id = ''")
		}
	}
	var rows []chatsession.Record
	if err := query.Order("last_active_timestamp DESC NULLS LAST").Limit(500).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for i := range rows {
		out = append(out, serializeSession(&rows[i]))
	}
	return out, nil
}

// OpenSession 按目标打开真实会话；未指定归属时只接受唯一确定的账号与作用域
func (s *Sessions) OpenSession(ctx context.Context, req pluginruntime.PluginOpenSession) (map[string]any, error) {
	if s == nil || s.manager == nil {
		return nil, fmt.Errorf("会话存储未初始化")
	}
	s.openMu.Lock()
	defer s.openMu.Unlock()
	platform := strings.ToLower(firstNonEmpty(req.Platform, "qq"))
	scope := strings.TrimSpace(req.Scope)
	accountID := strings.TrimSpace(req.AccountID)
	chatType := "private"
	targetID := strings.TrimSpace(req.UserID)
	userID, groupID := strings.TrimSpace(req.UserID), ""
	if req.ChatType == "group" {
		chatType = "group"
		groupID = strings.TrimSpace(req.GroupID)
		targetID = groupID
		userID = ""
	}
	localPlatform := platform == "internal" || platform == "webui"
	if _, ok := localoperator.LocalPlatformBotIDs[platform]; ok {
		localPlatform = true
	}
	existing, err := s.manager.ResolveSessionsByTarget(ctx, platform, targetID, chatType)
	if err != nil {
		return nil, err
	}
	candidates := make([]chatsession.Record, 0, len(existing))
	for _, record := range existing {
		if !localPlatform && recordAccountID(&record) == "" {
			continue
		}
		if accountID != "" && recordAccountID(&record) != accountID {
			continue
		}
		if scope != "" && recordScope(&record) != scope {
			continue
		}
		candidates = append(candidates, record)
	}
	if len(candidates) > 1 {
		return nil, fmt.Errorf("目标存在多个聊天流，请明确指定 account_id 和 scope")
	}
	if len(candidates) == 1 {
		record := candidates[0]
		if err := s.manager.TouchSession(ctx, record.SessionID); err != nil {
			return nil, err
		}
		return sessionOpenResult(&record, false), nil
	}

	resolvedAccount, resolvedScope := accountID, scope
	if !localPlatform && s.routes != nil {
		resolvedAccount, resolvedScope, err = s.routes.ResolveNewSessionRoute(ctx, platform, accountID, scope)
		if err != nil {
			return nil, err
		}
	}
	record, err := s.manager.GetOrCreateSession(ctx, platform, userID, groupID, resolvedAccount, resolvedScope)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, fmt.Errorf("会话创建失败")
	}
	created := true
	for _, previous := range existing {
		if previous.SessionID == record.SessionID {
			created = false
			break
		}
	}
	return sessionOpenResult(record, created), nil
}

func sessionOpenResult(record *chatsession.Record, created bool) map[string]any {
	stream := serializeSession(record)
	out := map[string]any{"success": true, "created": created, "stream": stream}
	for key, value := range stream {
		out[key] = value
	}
	return out
}

func recordAccountID(record *chatsession.Record) string {
	if record == nil || record.AccountID == nil {
		return ""
	}
	return strings.TrimSpace(*record.AccountID)
}

func recordScope(record *chatsession.Record) string {
	if record == nil || record.Scope == nil {
		return ""
	}
	return strings.TrimSpace(*record.Scope)
}

// StreamByGroupID 按群号查找会话
func (s *Sessions) StreamByGroupID(ctx context.Context, platform, groupID, accountID, scope string) (map[string]any, error) {
	record, err := s.manager.ResolveSession(ctx, firstNonEmpty(platform, "qq"), groupID, "group")
	if err != nil {
		return nil, nil
	}
	if record == nil || !matchesRoute(record, accountID, scope) {
		return nil, nil
	}
	return serializeSession(record), nil
}

// StreamByUserID 按用户查找私聊会话
func (s *Sessions) StreamByUserID(ctx context.Context, platform, userID, accountID, scope string) (map[string]any, error) {
	record, err := s.manager.ResolveSession(ctx, firstNonEmpty(platform, "qq"), userID, "private")
	if err != nil {
		return nil, nil
	}
	if record == nil || !matchesRoute(record, accountID, scope) {
		return nil, nil
	}
	return serializeSession(record), nil
}

// Avatar 调用头像服务；服务未注入时返回与 Python 一致的 unsupported 状态
func (s *Sessions) Avatar(ctx context.Context, req pluginruntime.PluginAvatarRequest) (map[string]any, error) {
	if s != nil && s.avatar != nil {
		result, err := s.avatar.GetAvatar(ctx, req.Platform, req.TargetID, req.TargetType, req.AccountID, req.Scope, req.ForceRefresh)
		if err != nil {
			return nil, err
		}
		return result.ToMap(), nil
	}
	return platformio.AvatarResult{
		Status:    platformio.AvatarStatusUnsupported,
		ExpiresAt: float64(time.Now().Add(5 * time.Minute).Unix()),
	}.ToMap(), nil
}

func matchesRoute(record *chatsession.Record, accountID, scope string) bool {
	if record == nil {
		return false
	}
	if accountID != "" {
		if record.AccountID == nil || strings.TrimSpace(*record.AccountID) != strings.TrimSpace(accountID) {
			return false
		}
	}
	if scope != "" {
		if record.Scope == nil || strings.TrimSpace(*record.Scope) != strings.TrimSpace(scope) {
			return false
		}
	}
	return true
}

func serializeSession(record *chatsession.Record) map[string]any {
	if record == nil {
		return nil
	}
	isGroup := record.IsGroup()
	chatType := "private"
	if isGroup {
		chatType = "group"
	}
	out := map[string]any{
		"session_id":       record.SessionID,
		"stream_id":        record.SessionID,
		"platform":         record.Platform,
		"chat_type":        chatType,
		"is_group_session": isGroup,
		"scope":            derefString(record.Scope),
		"account_id":       derefString(record.AccountID),
		"created_at":       record.CreatedTimestamp.Unix(),
	}
	if record.LastActiveTimestamp != nil {
		out["last_active_at"] = record.LastActiveTimestamp.Unix()
	}
	if record.UserID != nil {
		out["user_id"] = *record.UserID
	}
	if record.UserNickname != nil {
		out["user_nickname"] = *record.UserNickname
	}
	if record.UserCardname != nil {
		out["user_cardname"] = *record.UserCardname
	}
	if record.GroupID != nil {
		out["group_id"] = *record.GroupID
	}
	if record.GroupName != nil {
		out["group_name"] = *record.GroupName
	}
	return out
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
