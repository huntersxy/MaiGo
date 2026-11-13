package replyer

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// GeneratorFactory 按会话构造 replyer 生成器，由主程序注入 LLM 客户端与提示装配器
type GeneratorFactory func(sessionID, requestType, replyerType string) (*ReplyGenerator, error)

// GeneratorManager 对齐 replyer_manager.ReplyerManager 的生成器实例缓存
// 键为 "replyer_type:stream_id"；与仅缓存 PromptBuilder 的 ReplyerManager 并存
type GeneratorManager struct {
	factory GeneratorFactory

	mu      sync.RWMutex
	byCache map[string]*ReplyGenerator
}

// NewGeneratorManager 创建生成器管理器
func NewGeneratorManager(factory GeneratorFactory) *GeneratorManager {
	return &GeneratorManager{factory: factory, byCache: make(map[string]*ReplyGenerator)}
}

// GetReplyer 按会话和 replyer 类型获取实例；仅支持 maisaka，对齐 Python
func (m *GeneratorManager) GetReplyer(sessionID, chatID, requestType, replyerType string) (*ReplyGenerator, error) {
	streamID := strings.TrimSpace(sessionID)
	if streamID == "" {
		streamID = strings.TrimSpace(chatID)
	}
	if streamID == "" {
		zap.L().Warn("ReplyerManager 缺少 stream_id，无法获取 replyer")
		return nil, errors.New("缺少会话标识，无法获取回复生成器")
	}
	if strings.TrimSpace(requestType) == "" {
		requestType = "replyer"
	}
	if strings.TrimSpace(replyerType) == "" {
		replyerType = "default"
	}
	cacheKey := replyerType + ":" + streamID
	m.mu.RLock()
	cached := m.byCache[cacheKey]
	m.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}
	if replyerType != "maisaka" {
		zap.L().Warn("ReplyerManager 不支持的 replyer_type", zap.String("replyer_type", replyerType))
		return nil, fmt.Errorf("不支持的 replyer_type=%s", replyerType)
	}
	if m.factory == nil {
		return nil, errors.New("回复生成器工厂尚未配置")
	}
	generator, err := m.factory(streamID, requestType, replyerType)
	if err != nil {
		return nil, err
	}
	if generator == nil {
		return nil, errors.New("回复生成器工厂返回空实例")
	}
	m.mu.Lock()
	if existing := m.byCache[cacheKey]; existing != nil {
		m.mu.Unlock()
		return existing, nil
	}
	m.byCache[cacheKey] = generator
	m.mu.Unlock()
	return generator, nil
}

// Cached 返回当前缓存的生成器，便于诊断
func (m *GeneratorManager) Cached() map[string]*ReplyGenerator {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]*ReplyGenerator, len(m.byCache))
	for key, generator := range m.byCache {
		out[key] = generator
	}
	return out
}

// Reset 清空缓存；生成器无状态资源时由主程序在配置重载后调用
func (m *GeneratorManager) Reset() {
	m.mu.Lock()
	m.byCache = make(map[string]*ReplyGenerator)
	m.mu.Unlock()
}
