package utils

// A_memorix 检索调优管理器，对齐 Python core/utils/retrieval_tuning_manager.py
// 负责运行时配置读写、任务队列、参数应用与回滚；检索评估与产物目录由调用方注入

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RetrievalTuningApplyOutcome 运行时热应用结果
type RetrievalTuningApplyOutcome struct {
	RuntimeRebuilt   bool
	ValidationPassed bool
	ApplyError       string
}

// RetrievalTuningRuntime 调优依赖的插件运行时能力
type RetrievalTuningRuntime interface {
	// MissingDependencies 返回未初始化的依赖名，顺序与 Python 相同
	MissingDependencies() []string
	// RuntimeReady 返回插件运行时是否就绪
	RuntimeReady() bool
	// ApplyRetrievalTuningProfile 热应用规范化参数
	ApplyRetrievalTuningProfile(ctx context.Context, profile map[string]any, validate bool) (RetrievalTuningApplyOutcome, error)
}

// RetrievalTuningEvaluationRequest 单次参数评估请求
type RetrievalTuningEvaluationRequest struct {
	Profile             map[string]any
	Cases               []*RetrievalQueryCase
	Objective           string
	TopKEval            int
	QueryTimeoutSeconds float64
	EvaluationMode      string
}

// RetrievalTuningEvaluationResult 单次参数评估结果
type RetrievalTuningEvaluationResult struct {
	Metrics        map[string]any
	AvgElapsedMS   float64
	FailureSummary map[string]any
}

// RetrievalTuningEvaluator 按参数执行检索评估
// Python 侧由 _evaluate_profile 内部构建 search runtime 完成，Go 侧改为注入
type RetrievalTuningEvaluator interface {
	EvaluateRetrievalProfile(ctx context.Context, request RetrievalTuningEvaluationRequest) (RetrievalTuningEvaluationResult, error)
}

// RetrievalTuningTriple 调优取样使用的三元组
type RetrievalTuningTriple struct {
	Subject   string
	Predicate string
	Object    string
	Hash      string
}

// RetrievalTuningQuerySetSource 构建测试集所需的知识库读取能力
type RetrievalTuningQuerySetSource interface {
	GetAllTriples() ([]RetrievalTuningTriple, error)
	GetParagraphsByRelation(relationHash string) ([]map[string]any, error)
}

// RetrievalTuningLLM 调优使用的文本生成能力
type RetrievalTuningLLM interface {
	Generate(ctx context.Context, requestType string, prompt string) (string, error)
}

// RetrievalTuningFastLLM 可选端口：轻量任务优先使用 fast_model，未实现时沿用原任务
type RetrievalTuningFastLLM interface {
	GenerateFast(ctx context.Context, requestType string, prompt string) (string, error)
}

// RetrievalTuningManagerOptions 检索调优管理器依赖
type RetrievalTuningManagerOptions struct {
	Config             ConfigGetter
	Logger             Logger
	ArtifactsRoot      string
	ImportWriteBlocked func() bool
	Runtime            RetrievalTuningRuntime
	Evaluator          RetrievalTuningEvaluator
	QuerySet           RetrievalTuningQuerySetSource
	LLM                RetrievalTuningLLM
	Now                func() float64
	NewTaskID          func() string
	Sleep              func(ctx context.Context, duration time.Duration) error
}

// RetrievalTuningManager 检索调优任务管理器
type RetrievalTuningManager struct {
	mu sync.Mutex

	config             ConfigGetter
	logger             Logger
	artifactsRoot      string
	importWriteBlocked func() bool
	runtime            RetrievalTuningRuntime
	evaluator          RetrievalTuningEvaluator
	querySet           RetrievalTuningQuerySetSource
	llm                RetrievalTuningLLM
	nowFunc            func() float64
	newTaskIDFunc      func() string
	sleepFunc          func(ctx context.Context, duration time.Duration) error

	tasks        map[string]*RetrievalTuningTaskRecord
	taskOrder    []string
	queue        []string
	activeTaskID string
	stopping     bool

	rollbackSnapshot map[string]any

	workerCancel context.CancelFunc
	workerDone   chan struct{}
}

// NewRetrievalTuningManager 创建管理器
func NewRetrievalTuningManager(options RetrievalTuningManagerOptions) *RetrievalTuningManager {
	manager := &RetrievalTuningManager{
		config:             options.Config,
		logger:             OrLogger(options.Logger),
		artifactsRoot:      options.ArtifactsRoot,
		importWriteBlocked: options.ImportWriteBlocked,
		runtime:            options.Runtime,
		evaluator:          options.Evaluator,
		querySet:           options.QuerySet,
		llm:                options.LLM,
		nowFunc:            options.Now,
		newTaskIDFunc:      options.NewTaskID,
		sleepFunc:          options.Sleep,
		tasks:              map[string]*RetrievalTuningTaskRecord{},
	}
	if manager.nowFunc == nil {
		manager.nowFunc = func() float64 {
			return float64(time.Now().UnixNano()) / 1e9
		}
	}
	if manager.newTaskIDFunc == nil {
		manager.newTaskIDFunc = defaultRetrievalTuningTaskID
	}
	if manager.sleepFunc == nil {
		manager.sleepFunc = sleepWithContext
	}
	return manager
}

// defaultRetrievalTuningTaskID 对齐 uuid.uuid4().hex
func defaultRetrievalTuningTaskID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

// sleepWithContext 支持取消的等待
func sleepWithContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Logger 返回管理器日志器
func (m *RetrievalTuningManager) Logger() Logger {
	return m.logger
}

// cfg 对齐 _cfg
func (m *RetrievalTuningManager) cfg(key string, defaultValue any) any {
	if m.config == nil {
		return defaultValue
	}
	return m.config(key, defaultValue)
}

// isEnabled 对齐 _is_enabled
func (m *RetrievalTuningManager) isEnabled() bool {
	return isTruthy(m.cfg("web.tuning.enabled", true))
}

// queueLimit 对齐 _queue_limit
func (m *RetrievalTuningManager) queueLimit() int {
	return retrievalTuningClampInt(m.cfg("web.tuning.max_queue_size", 8), 8, 1, 100)
}

// pollIntervalSeconds 对齐 _poll_interval_s
func (m *RetrievalTuningManager) pollIntervalSeconds() float64 {
	milliseconds := retrievalTuningClampInt(m.cfg("web.tuning.poll_interval_ms", 1200), 1200, 200, 60000)
	seconds := float64(milliseconds) / 1000.0
	if seconds < 0.2 {
		return 0.2
	}
	return seconds
}

// llmRetryConfig 对齐 _llm_retry_cfg
func (m *RetrievalTuningManager) llmRetryConfig() map[string]any {
	return map[string]any{
		"max_attempts":       retrievalTuningClampInt(m.cfg("web.tuning.llm_retry.max_attempts", 3), 3, 1, 10),
		"min_wait_seconds":   retrievalTuningClampFloat(m.cfg("web.tuning.llm_retry.min_wait_seconds", 2), 2.0, 0.1, 60.0),
		"max_wait_seconds":   retrievalTuningClampFloat(m.cfg("web.tuning.llm_retry.max_wait_seconds", 20), 20.0, 0.2, 120.0),
		"backoff_multiplier": retrievalTuningClampFloat(m.cfg("web.tuning.llm_retry.backoff_multiplier", 2), 2.0, 1.0, 10.0),
	}
}

// evalQueryTimeoutSeconds 对齐 _eval_query_timeout_s
func (m *RetrievalTuningManager) evalQueryTimeoutSeconds() float64 {
	return retrievalTuningClampFloat(m.cfg("web.tuning.eval_query_timeout_seconds", 10.0), 10.0, 0.01, 120.0)
}

// GetRuntimeSettings 对齐 get_runtime_settings
func (m *RetrievalTuningManager) GetRuntimeSettings() map[string]any {
	intensity := pyStr(m.cfg("web.tuning.default_intensity", "standard"))
	if intensity == "" {
		intensity = "standard"
	}
	rounds, ok := RetrievalTuningIntensityRounds(intensity)
	if !ok {
		intensity = "standard"
		rounds = 20
	}
	objective := pyStr(m.cfg("web.tuning.default_objective", "precision_priority"))
	if !IsRetrievalTuningObjective(objective) {
		objective = RetrievalTuningObjectivePrecisionPriority
	}
	return map[string]any{
		"enabled":                    m.isEnabled(),
		"poll_interval_ms":           retrievalTuningClampInt(m.cfg("web.tuning.poll_interval_ms", 1200), 1200, 200, 60000),
		"max_queue_size":             m.queueLimit(),
		"default_objective":          objective,
		"default_intensity":          intensity,
		"default_rounds":             rounds,
		"default_top_k_eval":         retrievalTuningClampInt(m.cfg("web.tuning.default_top_k_eval", 20), 20, 5, 100),
		"default_sample_size":        retrievalTuningClampInt(m.cfg("web.tuning.default_sample_size", 24), 24, 4, 200),
		"eval_query_timeout_seconds": m.evalQueryTimeoutSeconds(),
		"llm_retry":                  m.llmRetryConfig(),
	}
}

// EnsureReady 对齐 _ensure_ready
func (m *RetrievalTuningManager) EnsureReady() error {
	if m.runtime == nil {
		return fmt.Errorf("调优依赖未初始化: metadata_store, vector_store, graph_store, embedding_manager")
	}
	if missing := m.runtime.MissingDependencies(); len(missing) > 0 {
		return fmt.Errorf("调优依赖未初始化: %s", strings.Join(missing, ", "))
	}
	if !m.runtime.RuntimeReady() {
		return fmt.Errorf("插件运行时未就绪")
	}
	if m.importWriteBlocked != nil && m.importWriteBlocked() {
		return fmt.Errorf("导入任务运行中，当前禁止启动检索调优")
	}
	return nil
}

// profileConfigKeys 调优快照读取的配置键与默认值
func retrievalTuningProfileConfigKeys() []struct {
	Key     string
	Default any
} {
	return []struct {
		Key     string
		Default any
	}{
		{"retrieval.top_k_paragraphs", 20},
		{"retrieval.top_k_relations", 10},
		{"retrieval.top_k_final", 10},
		{"retrieval.alpha", 0.5},
		{"retrieval.enable_ppr", true},
		{"retrieval.ppr_alpha", 0.85},
		{"retrieval.ppr_timeout_seconds", 1.5},
		{"retrieval.search.smart_fallback.enabled", true},
		{"retrieval.sparse.enabled", true},
		{"retrieval.sparse.mode", "auto"},
		{"retrieval.sparse.candidate_k", 80},
		{"retrieval.sparse.relation_candidate_k", 60},
		{"retrieval.fusion.method", "weighted_rrf"},
		{"retrieval.fusion.rrf_k", 60},
		{"retrieval.fusion.vector_weight", 0.7},
		{"retrieval.fusion.bm25_weight", 0.3},
		{"retrieval.vector_pools.mode", "single"},
		{"retrieval.vector_pools.paragraph_top_k", 20},
		{"retrieval.vector_pools.graph_top_k", 40},
		{"retrieval.vector_pools.graph_expand_paragraph_k", 80},
		{"retrieval.vector_pools.relation_expand_per_hit", 5},
		{"retrieval.vector_pools.entity_expand_per_hit", 8},
		{"retrieval.vector_pools.relation_evidence_weight", 1.0},
		{"retrieval.vector_pools.entity_evidence_weight", 0.55},
		{"retrieval.vector_pools.semantic_weight", 0.65},
		{"retrieval.vector_pools.sparse_weight", 0.2},
		{"retrieval.vector_pools.graph_weight", 0.15},
		{"retrieval.vector_pools.relation_intent.graph_top_k", 80},
		{"retrieval.vector_pools.relation_intent.semantic_weight", 0.45},
		{"retrieval.vector_pools.relation_intent.sparse_weight", 0.15},
		{"retrieval.vector_pools.relation_intent.graph_weight", 0.4},
		{"threshold.min_threshold", 0.29},
		{"threshold.max_threshold", 0.95},
		{"threshold.percentile", 75.0},
		{"threshold.min_results", 4},
	}
}

// GetProfileSnapshot 对齐 get_profile_snapshot，从 ConfigGetter 还原嵌套参数
func (m *RetrievalTuningManager) GetProfileSnapshot() map[string]any {
	config := make(map[string]any, 36)
	for _, item := range retrievalTuningProfileConfigKeys() {
		if m.config == nil {
			continue
		}
		value := m.config(item.Key, item.Default)
		if value == nil {
			continue
		}
		config[item.Key] = value
	}
	return RetrievalTuningProfileSnapshot(config)
}

// NormalizeProfile 对齐 _normalize_profile，缺省使用当前快照作为回退
func (m *RetrievalTuningManager) NormalizeProfile(profile map[string]any) map[string]any {
	fallback := m.GetProfileSnapshot()
	if profile == nil {
		return fallback
	}
	return NormalizeRetrievalProfile(profile, fallback)
}

// GetPersistableProfile 对齐 get_persistable_profile
func (m *RetrievalTuningManager) GetPersistableProfile(profile map[string]any) map[string]any {
	if profile == nil {
		return m.GetProfileSnapshot()
	}
	return m.NormalizeProfile(profile)
}

// ExportTOMLSnippet 对齐 export_toml_snippet
func (m *RetrievalTuningManager) ExportTOMLSnippet(profile map[string]any) string {
	if profile == nil {
		profile = m.GetProfileSnapshot()
	}
	return ExportRetrievalTOMLSnippet(m.NormalizeProfile(profile))
}

// applyProfileToRuntime 对齐 _apply_profile_to_runtime
func (m *RetrievalTuningManager) applyProfileToRuntime(ctx context.Context, normalized map[string]any, validate bool) (RetrievalTuningApplyOutcome, error) {
	if m.runtime == nil {
		return RetrievalTuningApplyOutcome{}, fmt.Errorf("插件运行时不可写")
	}
	outcome, err := m.runtime.ApplyRetrievalTuningProfile(ctx, normalized, validate)
	if err != nil {
		return RetrievalTuningApplyOutcome{}, err
	}
	return RetrievalTuningApplyOutcome{
		RuntimeRebuilt:   outcome.RuntimeRebuilt,
		ValidationPassed: outcome.ValidationPassed,
		ApplyError:       outcome.ApplyError,
	}, nil
}

// ApplyProfile 对齐 apply_profile
func (m *RetrievalTuningManager) ApplyProfile(ctx context.Context, profile map[string]any, reason string, validate bool) (map[string]any, error) {
	if reason == "" {
		reason = "manual"
	}
	normalized := m.NormalizeProfile(profile)
	current := m.GetProfileSnapshot()

	m.mu.Lock()
	m.rollbackSnapshot = current
	m.mu.Unlock()

	outcome, err := m.applyProfileToRuntime(ctx, normalized, validate)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"applied":           normalized,
		"rollback_snapshot": current,
		"reason":            reason,
		"applied_at":        m.nowFunc(),
		"persisted":         false,
		"runtime_rebuilt":   outcome.RuntimeRebuilt,
		"validation_passed": outcome.ValidationPassed,
		"apply_error":       outcome.ApplyError,
	}, nil
}

// RollbackProfile 对齐 rollback_profile
func (m *RetrievalTuningManager) RollbackProfile(ctx context.Context) (map[string]any, error) {
	m.mu.Lock()
	snapshot := m.rollbackSnapshot
	m.mu.Unlock()
	if len(snapshot) == 0 {
		return nil, fmt.Errorf("暂无可回滚的参数快照")
	}
	target := NormalizeRetrievalProfile(snapshot, snapshot)
	outcome, err := m.applyProfileToRuntime(ctx, target, true)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"rolled_back_to":    target,
		"rolled_back_at":    m.nowFunc(),
		"runtime_rebuilt":   outcome.RuntimeRebuilt,
		"validation_passed": outcome.ValidationPassed,
		"apply_error":       outcome.ApplyError,
	}, nil
}

// artifactsDir 返回任务产物目录，未配置产物根目录时返回空串
func (m *RetrievalTuningManager) artifactsDir(taskID string) string {
	if pyStrip(m.artifactsRoot) == "" {
		return ""
	}
	return filepath.Join(m.artifactsRoot, taskID)
}
