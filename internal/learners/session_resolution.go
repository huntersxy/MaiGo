package learners

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"MaiGo/internal/chat/session"
)

// LearningSessionChecker 判断会话是否已注册，供学习模块解析真实聊天流
type LearningSessionChecker interface {
	HasLearningSession(ctx context.Context, sessionID string) (bool, error)
}

// LearningSessionCheckerFunc 允许用函数实现 LearningSessionChecker
type LearningSessionCheckerFunc func(ctx context.Context, sessionID string) (bool, error)

func (f LearningSessionCheckerFunc) HasLearningSession(ctx context.Context, sessionID string) (bool, error) {
	return f(ctx, sessionID)
}

// ErrLearningSessionNotFound 表示候选会话与回退会话都未注册
var ErrLearningSessionNotFound = errors.New("无法解析到有效聊天流")

// SetLearningSessionChecker 注入会话注册表检查器；nil 时使用 GORM chat_sessions 表
func (r *Registry) SetLearningSessionChecker(checker LearningSessionChecker) {
	r.mu.Lock()
	r.learningSessionChecker = checker
	r.mu.Unlock()
}

// HasLearningSession 判断会话是否已注册；未配置 Store 时按未注册处理
func (r *Registry) HasLearningSession(ctx context.Context, sessionID string) (bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, nil
	}
	r.mu.RLock()
	checker := r.learningSessionChecker
	store := r.store
	r.mu.RUnlock()
	if checker != nil {
		return checker.HasLearningSession(ctx, sessionID)
	}
	gormStore, ok := store.(*GORMStore)
	if !ok || gormStore == nil {
		return false, nil
	}
	db := gormStore.DB()
	if db == nil {
		return false, nil
	}
	var count int64
	if err := db.WithContext(ctx).Model(&chatsession.Record{}).
		Where("session_id = ?", sessionID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ResolveLearningSessionID 对齐 Python _resolve_learning_session_id：
// 按真实消息中的 session_id 频次选择已注册聊天流，全部未注册时再校验回退会话
func (r *Registry) ResolveLearningSessionID(ctx context.Context, candidates []string, fallback string) (string, error) {
	counts := map[string]int{}
	order := make([]string, 0, len(candidates))
	for _, raw := range candidates {
		sessionID := strings.TrimSpace(raw)
		if sessionID == "" {
			continue
		}
		if _, ok := counts[sessionID]; !ok {
			order = append(order, sessionID)
		}
		counts[sessionID]++
	}
	sort.SliceStable(order, func(i, j int) bool {
		return counts[order[i]] > counts[order[j]]
	})
	for _, sessionID := range order {
		exists, err := r.HasLearningSession(ctx, sessionID)
		if err != nil {
			return "", fmt.Errorf("检查聊天流 %s 失败: %w", sessionID, err)
		}
		if exists {
			return sessionID, nil
		}
	}
	fallback = strings.TrimSpace(fallback)
	if fallback != "" {
		exists, err := r.HasLearningSession(ctx, fallback)
		if err != nil {
			return "", fmt.Errorf("检查回退聊天流 %s 失败: %w", fallback, err)
		}
		if exists {
			return fallback, nil
		}
	}
	return "", ErrLearningSessionNotFound
}

// ResolveLearningSessionID 使用默认 Registry 解析学习会话
func ResolveLearningSessionID(ctx context.Context, candidates []string, fallback string) (string, error) {
	return DefaultRegistry().ResolveLearningSessionID(ctx, candidates, fallback)
}
