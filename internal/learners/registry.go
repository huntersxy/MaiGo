package learners

import (
	"context"
	"sync"

	"maibot-go/internal/chat/replyer/expressionvector"
	"maibot-go/internal/llm_models"
)

// Registry 进程内 learners 依赖入口，agent/Maisaka 后续从此取 Store 与聊天级开关
type Registry struct {
	mu                sync.RWMutex
	store             Store
	chat              ChatLearningConfig
	textEmbedder      TextEmbedder
	expressionVectors ExpressionVectorStore
	jargonRouter      *llm.Router
	expressionRouter  *llm.Router
	webuiStore        WebUIStore
	settingsProvider  func() LearningSettings

	learningSessionChecker      LearningSessionChecker
	learningSessionDirectory    LearningSessionDirectory
	learningSessionNameResolver LearningSessionNameResolver

	// 表达向量索引同步任务的运行期状态，对齐 Python 后台补建任务与错误字段
	expressionSyncRunning   bool
	expressionSyncLastError string

	// 表达向量历史补建后台循环
	expressionBackfill *expressionvector.BackfillRunner
}

var defaultRegistry = &Registry{
	store:             NoopStore{},
	chat:              NewDefaultChatLearningConfig(),
	expressionVectors: NewInMemoryExpressionVectorStore(),
}

// DefaultRegistry 返回全局 Registry
func DefaultRegistry() *Registry {
	return defaultRegistry
}

// SetStore 注入持久化实现（通常在数据库初始化后调用）
func (r *Registry) SetStore(store Store) {
	if store == nil {
		store = NoopStore{}
	}
	r.mu.Lock()
	r.store = store
	r.mu.Unlock()
}

// Store 返回当前 Store
func (r *Registry) Store() Store {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.store
}

// ChatConfig 返回聊天级学习/注入开关
func (r *Registry) ChatConfig() *ChatLearningConfig {
	r.chat.bindRegistry(r)
	return &r.chat
}

// SetTextEmbedder 注入表达/query 嵌入客户端；nil 表示未配置 embedding 任务
func (r *Registry) SetTextEmbedder(embedder TextEmbedder) {
	r.mu.Lock()
	r.textEmbedder = embedder
	r.mu.Unlock()
}

// TextEmbedder 返回当前嵌入客户端
func (r *Registry) TextEmbedder() TextEmbedder {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.textEmbedder
}

// SetExpressionVectorStore 替换表达向量索引（测试或后续持久化实现）
func (r *Registry) SetExpressionVectorStore(store ExpressionVectorStore) {
	if store == nil {
		store = expressionvector.NoopStore{}
	}
	r.mu.Lock()
	r.expressionVectors = store
	r.mu.Unlock()
}

// ExpressionVectors 返回当前表达向量索引
func (r *Registry) ExpressionVectors() ExpressionVectorStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.expressionVectors == nil {
		return expressionvector.NoopStore{}
	}
	return r.expressionVectors
}

// SetJargonRouter 注入黑话 LLM 推断用的任务路由器；nil 表示关闭推断
func (r *Registry) SetJargonRouter(router *llm.Router) {
	r.mu.Lock()
	r.jargonRouter = router
	r.mu.Unlock()
}

// JargonRouter 返回当前黑话 LLM 路由器
func (r *Registry) JargonRouter() *llm.Router {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.jargonRouter
}

// SetExpressionRouter 注入表达学习 LLM 批次用的任务路由器；nil 表示关闭批次
func (r *Registry) SetExpressionRouter(router *llm.Router) {
	r.mu.Lock()
	r.expressionRouter = router
	r.mu.Unlock()
}

// ExpressionRouter 返回当前表达学习 LLM 路由器
func (r *Registry) ExpressionRouter() *llm.Router {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.expressionRouter
}

// SetLearningSettingsProvider 注入学习配置提供者；nil 时从实例 TOML 读取默认值
func (r *Registry) SetLearningSettingsProvider(provider func() LearningSettings) {
	r.mu.Lock()
	r.settingsProvider = provider
	r.mu.Unlock()
}

// LearningSettings 返回当前学习配置，对齐 global_config.expression/jargon
func (r *Registry) LearningSettings() LearningSettings {
	settings, err := r.LearningSettingsWithError()
	if err != nil {
		return NormalizeLearningSettings(DefaultLearningSettings())
	}
	return settings
}

// LearningSettingsWithError 返回学习配置并保留加载错误，供 per-chat 开关按 Python 默认开启处理
func (r *Registry) LearningSettingsWithError() (LearningSettings, error) {
	r.mu.RLock()
	provider := r.settingsProvider
	r.mu.RUnlock()
	if provider != nil {
		return NormalizeLearningSettings(provider()), nil
	}
	settings, err := LoadLearningSettingsWithError()
	if err != nil {
		return LearningSettings{}, err
	}
	return NormalizeLearningSettings(settings), nil
}

// SetWebUIStore 注入 Dashboard 查询实现；nil 表示回退到 GORMStore 的默认实现
func (r *Registry) SetWebUIStore(store WebUIStore) {
	r.mu.Lock()
	r.webuiStore = store
	r.mu.Unlock()
}

// WebUIStore 返回 Dashboard 查询实现
//
// 优先使用显式注入（测试或自定义实现），否则在 GORMStore 已就绪时按库连接构造，
// 避免要求 main 额外接线
func (r *Registry) WebUIStore() WebUIStore {
	r.mu.RLock()
	custom := r.webuiStore
	store := r.store
	r.mu.RUnlock()
	if custom != nil {
		return custom
	}
	if gormStore, ok := store.(*GORMStore); ok {
		if db := gormStore.DB(); db != nil {
			return NewGORMWebUIStore(db)
		}
	}
	return nil
}

// —— expressionvector.Port 实现 ——
// 向量索引包通过该端口取用宿主能力，避免其反向依赖 learners。

// Embedder 返回文本嵌入客户端
func (r *Registry) Embedder() expressionvector.TextEmbedder {
	embedder := r.TextEmbedder()
	if embedder == nil {
		return nil
	}
	return embedder
}

// Settings 返回向量索引相关配置
func (r *Registry) Settings() expressionvector.Settings {
	settings := r.LearningSettings()
	return expressionvector.Settings{
		UseVectorExpression:       settings.UseVectorExpression,
		ExpressionVectorIndexPath: settings.ExpressionVectorIndexPath,
		VectorCandidatePoolSize:   settings.VectorCandidatePoolSize,
	}
}

// ListExpressions 分页读取表达方式，供历史回填使用
func (r *Registry) ListExpressions(ctx context.Context, offset, limit int) ([]expressionvector.Record, int, error) {
	page := offset/limit + 1
	rows, total, err := r.Store().ListExpressions(ctx, ExpressionFilter{Page: page, PageSize: limit})
	if err != nil {
		return nil, 0, err
	}
	out := make([]expressionvector.Record, 0, len(rows))
	for _, item := range rows {
		modifiedBy := ""
		if item.ModifiedBy != nil {
			modifiedBy = string(*item.ModifiedBy)
		}
		out = append(out, expressionvector.Record{
			ID: item.ID, Situation: item.Situation, Style: item.Style,
			Count: item.Count, Checked: item.Checked, SessionID: item.SessionID,
			ModifiedBy: modifiedBy,
		})
	}
	return out, total, nil
}

// BeginExpressionVectorSync 标记同步开始
func (r *Registry) BeginExpressionVectorSync() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.expressionSyncRunning = true
	r.mu.Unlock()
}

// FinishExpressionVectorSync 记录同步结果：成功清空错误，失败保留异常文本
func (r *Registry) FinishExpressionVectorSync(err error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.expressionSyncRunning = false
	if err != nil {
		r.expressionSyncLastError = err.Error()
	} else {
		r.expressionSyncLastError = ""
	}
	r.mu.Unlock()
}

// RecordExpressionVectorSyncError 记录尚未进入补建阶段就失败的错误
func (r *Registry) RecordExpressionVectorSyncError(err error) {
	if r == nil || err == nil {
		return
	}
	r.mu.Lock()
	r.expressionSyncLastError = err.Error()
	r.mu.Unlock()
}

// ClearExpressionVectorSyncError 只清空错误，不改变运行中标记
func (r *Registry) ClearExpressionVectorSyncError() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.expressionSyncLastError = ""
	r.mu.Unlock()
}

// EnablePersistentExpressionVectors 切换到持久化索引并载入已有文件
func (r *Registry) EnablePersistentExpressionVectors(path string) (*expressionvector.PersistentExpressionVectorStore, error) {
	store, err := expressionvector.NewPersistentExpressionVectorStore(path)
	if err != nil {
		return nil, err
	}
	r.SetExpressionVectorStore(store)
	return store, nil
}
