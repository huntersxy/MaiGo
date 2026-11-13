package replyer

import (
	"fmt"
	"strings"

	"maibot-go/internal/prompt"
)

// NewReplyerManager 创建 replyer 实例缓存；newBuilder 负责按 requestType 构造装配器
func NewReplyerManager(newBuilder func(requestType string) *PromptBuilder) *ReplyerManager {
	return &ReplyerManager{
		newBuilder: newBuilder,
		byCache:    make(map[string]*PromptBuilder),
	}
}

// GetReplyer 对齐 ReplyerManager.get_replyer：按 replyer_type + stream_id 缓存
func (m *ReplyerManager) GetReplyer(sessionID, chatID, requestType, replyerType string) (*PromptBuilder, error) {
	if m == nil || m.newBuilder == nil {
		return nil, fmt.Errorf("replyer manager 未配置构造器")
	}
	streamID := strings.TrimSpace(sessionID)
	if streamID == "" {
		streamID = strings.TrimSpace(chatID)
	}
	if streamID == "" {
		return nil, fmt.Errorf("缺少会话标识，无法创建 replyer")
	}
	// 对齐 Python：仅 replyer_type == "maisaka" 可创建；default/空/其他一律拒绝
	normalizedType := strings.TrimSpace(replyerType)
	if normalizedType != "maisaka" {
		return nil, fmt.Errorf("不支持的 replyer_type: %s", normalizedType)
	}
	normalizedRequestType := strings.TrimSpace(requestType)
	if normalizedRequestType == "" {
		normalizedRequestType = "replyer"
	}
	cacheKey := normalizedType + ":" + streamID

	m.mu.RLock()
	cached := m.byCache[cacheKey]
	m.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}

	builder := m.newBuilder(normalizedRequestType)
	if builder == nil {
		return nil, fmt.Errorf("replyer 构造器返回空实例")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.byCache[cacheKey]; existing != nil {
		return existing, nil
	}
	m.byCache[cacheKey] = builder
	return builder, nil
}

// CachedReplyers 返回当前缓存的实例数
func (m *ReplyerManager) CachedReplyers() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.byCache)
}

// Reset 清空缓存（配置热重载时使用）
func (m *ReplyerManager) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byCache = make(map[string]*PromptBuilder)
}

// NewPromptManagerLoader 将 prompt.Manager 适配为 LoadPromptFunc
// locale 为空时使用 Manager 当前语言层；渲染失败返回真实错误
func NewPromptManagerLoader(manager *prompt.Manager) LoadPromptFunc {
	return func(name string, vars map[string]string) (string, error) {
		if manager == nil {
			return "", fmt.Errorf("prompt manager 未初始化")
		}
		rendered, err := manager.Render(name, vars)
		if err != nil {
			return "", fmt.Errorf("渲染 prompt %q: %w", name, err)
		}
		return rendered, nil
	}
}
