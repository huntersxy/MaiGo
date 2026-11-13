package learners

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// ChatLearningConfig 对齐 ExpressionConfigUtils / JargonConfigUtils 的 per-chat 开关
//
// 显式 Set 覆盖优先；未显式设置时按实例 TOML 的 learning_list 规则解析。
// 未命中规则或配置读取失败时回落 (true, true)，与 Python get_*_config_for_chat 一致
type ChatLearningConfig struct {
	mu sync.RWMutex

	useExpression        map[string]bool
	enableExprLearning   map[string]bool
	useJargon            map[string]bool
	enableJargonLearning map[string]bool

	registry        atomic.Pointer[Registry]
	lastSettingsErr string
	scopeCache      map[string]chatLearningScopeCacheEntry
}

// chatLearningScopeCacheEntry 缓存 session_id 到平台目标的解析，避免每条消息都查 chat_sessions
type chatLearningScopeCacheEntry struct {
	scope   LearningSessionScope
	ok      bool
	err     error
	expires time.Time
}

type chatLearningKind int

const (
	chatLearningExpression chatLearningKind = iota
	chatLearningJargon
)

func NewDefaultChatLearningConfig() ChatLearningConfig {
	return ChatLearningConfig{
		useExpression:        map[string]bool{},
		enableExprLearning:   map[string]bool{},
		useJargon:            map[string]bool{},
		enableJargonLearning: map[string]bool{},
	}
}

// bindRegistry 由 Registry.ChatConfig 绑定，供规则解析读取实例配置与会话目录
func (c *ChatLearningConfig) bindRegistry(reg *Registry) {
	if c == nil || reg == nil {
		return
	}
	c.registry.Store(reg)
}

func (c *ChatLearningConfig) GetExpressionConfig(sessionID string) (useExpression, enableLearning bool) {
	return c.get(chatLearningExpression, sessionID)
}

func (c *ChatLearningConfig) SetExpressionEnabled(sessionID string, enabled bool) {
	c.mu.Lock()
	c.ensureMapsLocked()
	c.useExpression[sessionID] = enabled
	c.mu.Unlock()
}

func (c *ChatLearningConfig) SetExpressionLearningEnabled(sessionID string, enabled bool) {
	c.mu.Lock()
	c.ensureMapsLocked()
	c.enableExprLearning[sessionID] = enabled
	c.mu.Unlock()
}

func (c *ChatLearningConfig) GetJargonConfig(sessionID string) (useJargon, enableLearning bool) {
	return c.get(chatLearningJargon, sessionID)
}

func (c *ChatLearningConfig) SetJargonEnabled(sessionID string, enabled bool) {
	c.mu.Lock()
	c.ensureMapsLocked()
	c.useJargon[sessionID] = enabled
	c.mu.Unlock()
}

func (c *ChatLearningConfig) SetJargonLearningEnabled(sessionID string, enabled bool) {
	c.mu.Lock()
	c.ensureMapsLocked()
	c.enableJargonLearning[sessionID] = enabled
	c.mu.Unlock()
}

func (c *ChatLearningConfig) ensureMapsLocked() {
	if c.useExpression == nil {
		c.useExpression = map[string]bool{}
	}
	if c.enableExprLearning == nil {
		c.enableExprLearning = map[string]bool{}
	}
	if c.useJargon == nil {
		c.useJargon = map[string]bool{}
	}
	if c.enableJargonLearning == nil {
		c.enableJargonLearning = map[string]bool{}
	}
}

func (k chatLearningKind) String() string {
	if k == chatLearningExpression {
		return "expression"
	}
	return "jargon"
}

func (c *ChatLearningConfig) get(kind chatLearningKind, sessionID string) (use, learn bool) {
	sessionID = strings.TrimSpace(sessionID)
	c.mu.RLock()
	var useOverrides, learnOverrides map[string]bool
	switch kind {
	case chatLearningExpression:
		useOverrides, learnOverrides = c.useExpression, c.enableExprLearning
	case chatLearningJargon:
		useOverrides, learnOverrides = c.useJargon, c.enableJargonLearning
	}
	use, hasUse := useOverrides[sessionID]
	learn, hasLearn := learnOverrides[sessionID]
	c.mu.RUnlock()
	if hasUse && hasLearn {
		if !learn {
			zap.L().Debug("学习开关已关闭", zap.String("module", kind.String()), zap.String("session_id", sessionID))
		}
		return use, learn
	}

	rule, found, err := c.resolveRule(kind, sessionID)
	if err != nil {
		// 对齐 Python：无独立读失败路径，未命中规则回落 (True, True)
		c.recordSettingsError(err)
		rule = ChatLearningRule{Use: true, Learn: true}
	} else {
		c.clearSettingsError()
		if !found {
			rule = ChatLearningRule{Use: true, Learn: true}
		}
	}
	if !hasUse {
		use = rule.Use
	}
	if !hasLearn {
		learn = rule.Learn
	}
	if !learn {
		zap.L().Debug("学习开关已关闭", zap.String("module", kind.String()), zap.String("session_id", sessionID))
	}
	return use, learn
}

func (c *ChatLearningConfig) resolveRule(kind chatLearningKind, sessionID string) (ChatLearningRule, bool, error) {
	defaultRule := ChatLearningRule{Use: true, Learn: true}
	reg := c.registry.Load()
	if reg == nil {
		return defaultRule, false, nil
	}
	settings, err := reg.LearningSettingsWithError()
	if err != nil {
		return ChatLearningRule{}, false, fmt.Errorf("加载学习开关配置: %w", err)
	}
	var rules []ChatLearningRule
	switch kind {
	case chatLearningExpression:
		rules = settings.ExpressionLearningRules
	case chatLearningJargon:
		rules = settings.JargonLearningRules
	}
	directory := reg.sessionDirectory()
	if sessionID == "" || directory == nil {
		rule, found := resolveGlobalChatLearningRule(rules)
		return rule, found, nil
	}
	scope, ok, err := c.lookupScope(directory, sessionID)
	if err != nil || !ok {
		// 对齐 Python：_get_existing_chat_stream 异常/空时退到全局默认规则
		rule, found := resolveGlobalChatLearningRule(rules)
		return rule, found, nil
	}
	rule, found := ResolveChatLearningRule(rules, scope, true)
	return rule, found, nil
}

// resolveGlobalChatLearningRule 会话不可用时只按全局默认（platform/item_id 为空）兜底
func resolveGlobalChatLearningRule(rules []ChatLearningRule) (ChatLearningRule, bool) {
	return ResolveChatLearningRule(rules, LearningSessionScope{}, false)
}

func (c *ChatLearningConfig) lookupScope(directory LearningSessionDirectory, sessionID string) (LearningSessionScope, bool, error) {
	now := time.Now()
	c.mu.RLock()
	entry, cached := c.scopeCache[sessionID]
	c.mu.RUnlock()
	if cached && now.Before(entry.expires) {
		return entry.scope, entry.ok, entry.err
	}
	scope, ok, err := directory.LearningSessionScope(context.Background(), sessionID)
	ttl := 5 * time.Second
	if err != nil {
		ttl = time.Second
	}
	c.mu.Lock()
	if c.scopeCache == nil {
		c.scopeCache = map[string]chatLearningScopeCacheEntry{}
	}
	if len(c.scopeCache) >= 2048 {
		c.scopeCache = map[string]chatLearningScopeCacheEntry{}
	}
	c.scopeCache[sessionID] = chatLearningScopeCacheEntry{scope: scope, ok: ok, err: err, expires: now.Add(ttl)}
	c.mu.Unlock()
	return scope, ok, err
}

func (c *ChatLearningConfig) recordSettingsError(err error) {
	if err == nil {
		return
	}
	message := err.Error()
	c.mu.Lock()
	if c.lastSettingsErr == message {
		c.mu.Unlock()
		return
	}
	c.lastSettingsErr = message
	c.mu.Unlock()
	zap.L().Error("读取学习开关配置失败，已按默认开启处理", zap.Error(err))
}

func (c *ChatLearningConfig) clearSettingsError() {
	c.mu.Lock()
	c.lastSettingsErr = ""
	c.mu.Unlock()
}

// ResolveRelatedSessionIDs 黑话组共享范围，对齐 JargonConfigUtils.resolve_jargon_group_scope
func ResolveRelatedSessionIDs(sessionID string) []string {
	return DefaultRegistry().ResolveJargonRelatedSessionIDs(context.Background(), sessionID)
}

// ResolveExpressionRelatedSessionIDs 表达组共享范围，对齐 _resolve_expression_group_scope
func ResolveExpressionRelatedSessionIDs(sessionID string) []string {
	return DefaultRegistry().ResolveExpressionRelatedSessionIDs(context.Background(), sessionID)
}
