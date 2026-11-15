package hostports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"MaiGo/internal/chat/session"
	"MaiGo/internal/platform_io"
	"MaiGo/internal/plugin_runtime"

	"gorm.io/gorm"
)

// Sessions 插件会话端口，基于 chat_sessions 表
type Sessions struct {
	db      *gorm.DB
	manager *chatsession.Manager
	avatar  *platformio.AvatarService
}

// SessionsOption 会话端口可选依赖
type SessionsOption func(*Sessions)

// WithAvatarService 注入头像服务；未注入时 chat.get_avatar 返回真实的 unsupported 状态
func WithAvatarService(service *platformio.AvatarService) SessionsOption {
	return func(s *Sessions) {
		s.avatar = service
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

// OpenSession 打开或创建会话
func (s *Sessions) OpenSession(ctx context.Context, req pluginruntime.PluginOpenSession) (map[string]any, error) {
	if s == nil || s.manager == nil {
		return nil, fmt.Errorf("会话存储未初始化")
	}
	platform := firstNonEmpty(req.Platform, "qq")
	scope := strings.TrimSpace(req.Scope)
	accountID := strings.TrimSpace(req.AccountID)
	var record *chatsession.Record
	var err error
	if req.ChatType == "group" {
		record, err = s.manager.GetOrCreateSession(ctx, platform, "", req.GroupID, accountID, scope)
	} else {
		record, err = s.manager.GetOrCreateSession(ctx, platform, req.UserID, "", accountID, scope)
	}
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, fmt.Errorf("会话创建失败")
	}
	return map[string]any{"success": true, "created": true, "stream": serializeSession(record)}, nil
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
