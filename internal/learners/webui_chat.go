package learners

import (
	"context"
	"sort"
	"strings"

	"maibot-go/internal/chat/session"

	"gorm.io/gorm"
)

// ChatName 解析聊天流显示名称
//
// 对齐 MaiBot get_chat_name：会话名 → 会话群名/私聊昵称 → 会话 ID；
// Go 侧 mai_messages 不保存群名，因此不再做“最近消息”兜底，避免同名误判
func (s *gormWebUIStore) ChatName(ctx context.Context, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	if names := s.ChatNames(ctx, []string{sessionID}); names[sessionID] != "" {
		return names[sessionID]
	}
	return truncateSessionID(sessionID)
}

// ChatNames 批量解析聊天流显示名称，避免列表接口逐条查询
func (s *gormWebUIStore) ChatNames(ctx context.Context, sessionIDs []string) map[string]string {
	out := make(map[string]string, len(sessionIDs))
	unique := make([]string, 0, len(sessionIDs))
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, raw := range sessionIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return out
	}
	var rows []chatsession.Record
	if err := s.db.WithContext(ctx).Where("session_id IN ?", unique).Find(&rows).Error; err != nil {
		for _, id := range unique {
			out[id] = truncateSessionID(id)
		}
		return out
	}
	byID := make(map[string]chatsession.Record, len(rows))
	for _, row := range rows {
		byID[row.SessionID] = row
	}
	for _, id := range unique {
		if rec, ok := byID[id]; ok {
			if name := displayNameForRecord(&rec); name != "" {
				out[id] = name
				continue
			}
		}
		out[id] = truncateSessionID(id)
	}
	return out
}

// ExistingSession 判断 session_id 是否为 chat_sessions 中的真实会话
func (s *gormWebUIStore) ExistingSession(ctx context.Context, sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&chatsession.Record{}).
		Where("session_id = ?", sessionID).Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

// displayNameForRecord 对齐 chat_session_to_display_name
func displayNameForRecord(rec *chatsession.Record) string {
	if rec == nil {
		return ""
	}
	if rec.IsGroup() {
		if rec.GroupName != nil {
			if name := strings.TrimSpace(*rec.GroupName); name != "" {
				return name
			}
		}
		return "群聊" + strings.TrimSpace(*rec.GroupID)
	}
	if rec.UserID != nil {
		name := ""
		if rec.UserCardname != nil {
			name = strings.TrimSpace(*rec.UserCardname)
		}
		if name == "" && rec.UserNickname != nil {
			name = strings.TrimSpace(*rec.UserNickname)
		}
		if name == "" {
			name = "用户" + strings.TrimSpace(*rec.UserID)
		}
		return name + "的私聊"
	}
	return ""
}

func truncateSessionID(sessionID string) string {
	if len(sessionID) <= 20 {
		return sessionID
	}
	return sessionID[:20]
}

// visibleExpressionChatIDs 表达列表默认可见聊天流
//
// MaiBot 只显示属于当前配置平台账号的会话；Go 尚无 bot account 列表注入，
// 以“chat_sessions 中存在且 platform/account_id 均非空”近似，差异见 ROUTES.md
func (s *gormWebUIStore) visibleExpressionChatIDs(ctx context.Context) (map[string]struct{}, error) {
	var expressionIDs []string
	if err := s.db.WithContext(ctx).Model(&Expression{}).
		Where("session_id IS NOT NULL").Distinct().Pluck("session_id", &expressionIDs).Error; err != nil {
		return nil, err
	}
	visible := make(map[string]struct{}, len(expressionIDs))
	if len(expressionIDs) == 0 {
		return visible, nil
	}
	// 对齐 Python get_visible_expression_chat_ids：逐条按 is_current_account_session 判定，
	// 本地平台（internal/webui）恒属本实例，早期未写 account_id 的会话也能出现在列表里
	var rows []chatsession.Record
	if err := s.db.WithContext(ctx).Where("session_id IN ?", expressionIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		if isCurrentAccountSession(&rows[i]) {
			visible[rows[i].SessionID] = struct{}{}
		}
	}
	return visible, nil
}

// allExpressionChatIDs include_legacy=true 时的表达归属集合
//
// Python 会排除 session_id 非 UTF-8 的坏记录；Go 列类型为 TEXT，天然都是合法文本
func (s *gormWebUIStore) allExpressionChatIDs(ctx context.Context) (map[string]struct{}, error) {
	var ids []string
	if err := s.db.WithContext(ctx).Model(&Expression{}).
		Where("session_id IS NOT NULL").Distinct().Pluck("session_id", &ids).Error; err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			out[id] = struct{}{}
		}
	}
	return out, nil
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func scopeSessionIDs(ctx context.Context, s *gormWebUIStore, includeLegacy bool) ([]string, error) {
	if includeLegacy {
		set, err := s.allExpressionChatIDs(ctx)
		if err != nil {
			return nil, err
		}
		return sortedKeys(set), nil
	}
	set, err := s.visibleExpressionChatIDs(ctx)
	if err != nil {
		return nil, err
	}
	return sortedKeys(set), nil
}

func applySessionScope(q *gorm.DB, field string, ids []string) *gorm.DB {
	if len(ids) == 0 {
		return q.Where(field + " IS NULL")
	}
	return q.Where(field+" IS NULL OR "+field+" IN ?", ids)
}
