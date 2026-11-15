package learners

import (
	"context"
	"errors"
	"sort"
	"strings"

	"MaiGo/internal/chat/session"

	"gorm.io/gorm"
)

const (
	learningChatTypeGroup   = "group"
	learningChatTypePrivate = "private"
)

// LearningSessionScope 描述 session_id 对应的平台与聊天目标
type LearningSessionScope struct {
	Platform    string
	TargetID    string
	ChatType    string
	DisplayName string
}

// LearningSessionDirectory 提供 session_id 与平台目标的互相解析
type LearningSessionDirectory interface {
	LearningSessionScope(ctx context.Context, sessionID string) (LearningSessionScope, bool, error)
	ResolveLearningSessionIDs(ctx context.Context, platform, targetID, chatType string) ([]string, error)
}

// LearningSessionNameHint 消息上下文提供的展示名回退信息
type LearningSessionNameHint struct {
	GroupName string
	Nickname  string
}

// LearningSessionNameResolver 可选的消息上下文解析器，返回 false 表示继续回退
type LearningSessionNameResolver func(ctx context.Context, sessionID string, hint LearningSessionNameHint) (string, bool)

// SetLearningSessionDirectory 注入会话目录；nil 时回退到 GORMStore 的 chat_sessions 查询
func (r *Registry) SetLearningSessionDirectory(directory LearningSessionDirectory) {
	r.mu.Lock()
	r.learningSessionDirectory = directory
	r.mu.Unlock()
}

// SetLearningSessionNameResolver 注入消息上下文展示名解析器；nil 时只使用目录与消息 hint
func (r *Registry) SetLearningSessionNameResolver(resolver LearningSessionNameResolver) {
	r.mu.Lock()
	r.learningSessionNameResolver = resolver
	r.mu.Unlock()
}

func (r *Registry) sessionNameResolver() LearningSessionNameResolver {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.learningSessionNameResolver
}

func (r *Registry) sessionDirectory() LearningSessionDirectory {
	r.mu.RLock()
	custom := r.learningSessionDirectory
	store := r.store
	r.mu.RUnlock()
	if custom != nil {
		return custom
	}
	if gormStore, ok := store.(*GORMStore); ok && gormStore != nil {
		return gormStore
	}
	return nil
}

// LearningSessionNameHintFromContexts 从同会话消息提取群名与昵称回退
func LearningSessionNameHintFromContexts(messages []ContextMessage, sessionID string) LearningSessionNameHint {
	sessionID = strings.TrimSpace(sessionID)
	hint := LearningSessionNameHint{}
	for _, message := range messages {
		messageSessionID := strings.TrimSpace(message.SessionID)
		if sessionID != "" && messageSessionID != "" && messageSessionID != sessionID {
			continue
		}
		if hint.GroupName == "" {
			hint.GroupName = strings.TrimSpace(message.GroupName)
		}
		if hint.Nickname == "" {
			hint.Nickname = strings.TrimSpace(message.Name)
		}
	}
	return hint
}

func (h LearningSessionNameHint) fallbackName(chatType string) string {
	groupName := strings.TrimSpace(h.GroupName)
	nickname := strings.TrimSpace(h.Nickname)
	switch chatType {
	case learningChatTypeGroup:
		return groupName
	case learningChatTypePrivate:
		if nickname != "" {
			return nickname + "的私聊"
		}
		return ""
	default:
		if groupName != "" {
			return groupName
		}
		if nickname != "" {
			return nickname + "的私聊"
		}
		return ""
	}
}

// LearningSessionDisplayName 解析会话展示名，无法解析时回退 session_id
func (r *Registry) LearningSessionDisplayName(ctx context.Context, sessionID string, hints ...LearningSessionNameHint) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	hint := LearningSessionNameHint{}
	if len(hints) > 0 {
		hint = hints[0]
	}
	if resolver := r.sessionNameResolver(); resolver != nil {
		if name, ok := resolver(ctx, sessionID, hint); ok {
			if name = strings.TrimSpace(name); name != "" {
				return name
			}
		}
	}
	if directory := r.sessionDirectory(); directory != nil {
		scope, ok, err := directory.LearningSessionScope(ctx, sessionID)
		if err == nil && ok {
			if name := strings.TrimSpace(scope.DisplayName); name != "" {
				return name
			}
			if name := hint.fallbackName(scope.ChatType); name != "" {
				return name
			}
			return sessionID
		}
	}
	if name := hint.fallbackName(""); name != "" {
		return name
	}
	return sessionID
}

// ResolveJargonRelatedSessionIDs 对齐 JargonConfigUtils.resolve_jargon_group_scope
func (r *Registry) ResolveJargonRelatedSessionIDs(ctx context.Context, sessionID string) []string {
	return r.resolveRelatedSessionIDs(ctx, sessionID, r.LearningSettings().JargonGroups)
}

// ResolveExpressionRelatedSessionIDs 对齐 _resolve_expression_group_scope
func (r *Registry) ResolveExpressionRelatedSessionIDs(ctx context.Context, sessionID string) []string {
	return r.resolveRelatedSessionIDs(ctx, sessionID, r.LearningSettings().ExpressionGroups)
}

func (r *Registry) resolveRelatedSessionIDs(ctx context.Context, sessionID string, groups []LearningGroup) []string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	related := map[string]struct{}{sessionID: {}}
	if len(groups) == 0 {
		return []string{sessionID}
	}
	directory := r.sessionDirectory()
	if directory == nil {
		return []string{sessionID}
	}
	scope, hasScope, err := directory.LearningSessionScope(ctx, sessionID)
	if err != nil {
		return []string{sessionID}
	}
	for _, group := range groups {
		groupSessionIDs := map[string]struct{}{}
		containsCurrent := false
		for _, target := range group.Targets {
			platform := strings.TrimSpace(target.Platform)
			itemID := strings.TrimSpace(target.ItemID)
			ruleType := strings.ToLower(strings.TrimSpace(target.RuleType))
			if platform == "" || itemID == "" {
				continue
			}
			ids, err := directory.ResolveLearningSessionIDs(ctx, platform, itemID, ruleType)
			if err != nil {
				continue
			}
			for _, id := range ids {
				id = strings.TrimSpace(id)
				if id != "" {
					groupSessionIDs[id] = struct{}{}
				}
			}
			if hasScope && learningTargetMatchesScope(scope, platform, itemID, ruleType) {
				containsCurrent = true
			}
			if !hasScope && platform != "*" && itemID != "*" {
				if _, ok := groupSessionIDs[sessionID]; ok {
					containsCurrent = true
				}
			}
		}
		if containsCurrent {
			for id := range groupSessionIDs {
				related[id] = struct{}{}
			}
		}
	}
	rest := make([]string, 0, len(related)-1)
	for id := range related {
		if id != sessionID {
			rest = append(rest, id)
		}
	}
	sort.Strings(rest)
	return append([]string{sessionID}, rest...)
}

func learningTargetMatchesScope(scope LearningSessionScope, platform, itemID, ruleType string) bool {
	if scope.Platform == "" || scope.TargetID == "" || scope.ChatType == "" {
		return false
	}
	if ruleType != scope.ChatType {
		return false
	}
	if platform != "*" && platform != scope.Platform {
		return false
	}
	if itemID != "*" && itemID != scope.TargetID {
		return false
	}
	return true
}

// LearningSessionScope 按 session_id 解析平台目标
func (s *GORMStore) LearningSessionScope(ctx context.Context, sessionID string) (LearningSessionScope, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || s.db == nil || sessionID == "" {
		return LearningSessionScope{}, false, nil
	}
	var record chatsession.Record
	err := s.db.WithContext(ctx).Where("session_id = ?", sessionID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return LearningSessionScope{}, false, nil
	}
	if err != nil {
		return LearningSessionScope{}, false, err
	}
	platform := strings.TrimSpace(record.Platform)
	if platform == "" {
		return LearningSessionScope{}, false, nil
	}
	if record.IsGroup() {
		targetID := strings.TrimSpace(*record.GroupID)
		if targetID == "" {
			return LearningSessionScope{}, false, nil
		}
		return LearningSessionScope{Platform: platform, TargetID: targetID, ChatType: learningChatTypeGroup, DisplayName: chatsession.DisplayName(&record)}, true, nil
	}
	if record.UserID != nil {
		targetID := strings.TrimSpace(*record.UserID)
		if targetID != "" {
			return LearningSessionScope{Platform: platform, TargetID: targetID, ChatType: learningChatTypePrivate, DisplayName: chatsession.DisplayName(&record)}, true, nil
		}
	}
	return LearningSessionScope{}, false, nil
}

// ResolveLearningSessionIDs 按平台目标查询真实 session_id，支持 * 通配
func (s *GORMStore) ResolveLearningSessionIDs(ctx context.Context, platform, targetID, chatType string) ([]string, error) {
	platform = strings.TrimSpace(platform)
	targetID = strings.TrimSpace(targetID)
	chatType = strings.ToLower(strings.TrimSpace(chatType))
	if s == nil || s.db == nil || platform == "" || targetID == "" {
		return nil, nil
	}
	var column string
	switch chatType {
	case learningChatTypeGroup:
		column = "group_id"
	case learningChatTypePrivate:
		column = "user_id"
	default:
		return nil, nil
	}
	query := s.db.WithContext(ctx).Model(&chatsession.Record{})
	if platform != "*" {
		query = query.Where("platform = ?", platform)
	}
	if targetID == "*" {
		query = query.Where(column + " IS NOT NULL").Where(column + " <> ''")
	} else {
		query = query.Where(column+" = ?", targetID)
	}
	ids := make([]string, 0)
	if err := query.Order("session_id ASC").Pluck("session_id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}
