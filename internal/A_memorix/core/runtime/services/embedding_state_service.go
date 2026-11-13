package services

// Embedding 状态服务，逐段对齐 embedding_state_service.py 的 MemoryEmbeddingStateService

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"maibot-go/internal/A_memorix/core/storage"
)

// EmbeddingStateDeps Embedding 状态服务的依赖端口
type EmbeddingStateDeps struct {
	// DualVectorPoolsConfigEnabled 双池配置是否启用
	DualVectorPoolsConfigEnabled func(ctx context.Context) bool
	// ReadDualVectorReadyManifest 读取双池 ready manifest
	ReadDualVectorReadyManifest func(ctx context.Context) map[string]any
	// VectorsRoot 当前向量根目录
	VectorsRoot func(ctx context.Context) string
	// RequestedDimension 模型请求维度
	RequestedDimension func() int
	// DefaultDimension 模型默认维度
	DefaultDimension func() int
	// EmbeddingDimension 当前 embedding 维度
	EmbeddingDimension func() int
	// SetEmbeddingDimension 写入当前 embedding 维度
	SetEmbeddingDimension func(int)
	// Fingerprint 生成 embedding 指纹
	Fingerprint func(dimension int) (map[string]any, error)
	// MetadataStore 元数据存储
	MetadataStore func() *storage.Store
	// VectorStore 单池向量存储
	VectorStore func() *storage.VectorStore
	// SetVectorStore 写入单池向量存储
	SetVectorStore func(*storage.VectorStore)
	// ParagraphVectorStore 段池向量存储
	ParagraphVectorStore func() *storage.VectorStore
	// GraphVectorStore 图池向量存储
	GraphVectorStore func() *storage.VectorStore
	// SetParagraphVectorStore 写入段池向量存储
	SetParagraphVectorStore func(*storage.VectorStore)
	// SetGraphVectorStore 写入图池向量存储
	SetGraphVectorStore func(*storage.VectorStore)
	// MakeVectorStore 创建向量存储
	MakeVectorStore func(ctx context.Context, dataDir string, dimension *int) (*storage.VectorStore, error)
	// ReloadDualVectorStoresFromDisk 从磁盘重载双池
	ReloadDualVectorStoresFromDisk func(ctx context.Context) bool
	// DualVectorPoolsEnabled 双池是否就绪
	DualVectorPoolsEnabled func(ctx context.Context) bool
	// ParagraphVectorDir 段池目录
	ParagraphVectorDir func(ctx context.Context) string
	// GraphVectorDir 图池目录
	GraphVectorDir func(ctx context.Context) string
	// V1ValidHashesForPool V1 合法 hash
	V1ValidHashesForPool func(ctx context.Context, pool string) []string
	// V1ReconciliationEvidenceRoot V1 对账证据目录
	V1ReconciliationEvidenceRoot func(ctx context.Context) string
	// LegacyVectorViewPresent 是否存在旧单池只读视图
	LegacyVectorViewPresent func() bool
	// EmbeddingDegraded 读取降级状态
	EmbeddingDegraded func() map[string]any
	// SetEmbeddingDegraded 写入降级状态
	SetEmbeddingDegraded func(active bool, reason string, checkedAt *float64)
	// SetRuntimeCapability 写入运行时能力
	SetRuntimeCapability func(name string, enabled bool)
	// RuntimeCapability 读取运行时能力
	RuntimeCapability func(name string) bool
	// SetRuntimeSparseOnly 设置检索器 sparse-only
	SetRuntimeSparseOnly func(sparseOnly bool)
	// RunSelfCheck 执行 embedding 运行时自检
	RunSelfCheck func(ctx context.Context, sampleText string, config map[string]any) (map[string]any, error)
	// BuildRuntimeConfig 构建运行时配置
	BuildRuntimeConfig func(ctx context.Context) map[string]any
	// SelfCheckReport 读取上次自检报告
	SelfCheckReport func() map[string]any
	// SetSelfCheckReport 写回自检报告
	SetSelfCheckReport func(map[string]any)
	// RefreshRelationWriteService 刷新关系写入服务
	RefreshRelationWriteService func(ctx context.Context)
	// RefreshRuntimeDependents 刷新运行时依赖
	RefreshRuntimeDependents func(ctx context.Context)
	// StartBackgroundTasks 启动后台任务
	StartBackgroundTasks func(ctx context.Context) error
	// SetVectorHealth 写入向量健康状态
	SetVectorHealth func(ctx context.Context, patch map[string]any)
	// VectorHealthSnapshot 读取向量健康快照，对齐 _vector_health_snapshot
	VectorHealthSnapshot func() map[string]any
	// VectorHealthErrorCode 读取向量健康错误码
	VectorHealthErrorCode func() string
	// DisableVectorChannel 禁用向量通道
	DisableVectorChannel func(ctx context.Context, err error)
	// RecoverKnownVectorFailure 尝试恢复已知向量故障
	RecoverKnownVectorFailure func(ctx context.Context, err error) bool
	// BuildSearchRuntime 重建检索运行时
	BuildSearchRuntime func(ctx context.Context) (SearchRuntimeBundle, error)
	// SetRetriever 写入检索器
	SetRetriever func(any)
	// SetThresholdFilter 写入阈值过滤器
	SetThresholdFilter func(any)
	// SetSparseIndex 写入稀疏索引
	SetSparseIndex func(any)
	// RebuildLock 向量重建锁
	RebuildLock func() *sync.Mutex
	// SetVectorPersistBlockedUntilRebuild 标记向量持久化阻塞
	SetVectorPersistBlockedUntilRebuild func(bool)
	// SetVectorRebuildSourceDimension 记录重建源维度
	SetVectorRebuildSourceDimension func(int)
	// SynchronizeVectorSpace 同步向量空间
	SynchronizeVectorSpace func(ctx context.Context) (map[string]any, error)
	// RunParagraphBackfillOnce 执行一次段落向量回填
	RunParagraphBackfillOnce func(ctx context.Context, limit int, maxRetry int, trigger string) (map[string]any, error)
}

// EmbeddingStateService Embedding 状态服务
type EmbeddingStateService struct {
	*Base
	deps EmbeddingStateDeps
}

// NewEmbeddingStateService 构造 Embedding 状态服务
func NewEmbeddingStateService(base *Base, deps EmbeddingStateDeps) *EmbeddingStateService {
	return &EmbeddingStateService{Base: base, deps: deps}
}

// StoredVectorDimension 对齐 _stored_vector_dimension
func (s *EmbeddingStateService) StoredVectorDimension(ctx context.Context, store *storage.VectorStore) *int {
	var readyManifest map[string]any
	if store == nil && s.deps.DualVectorPoolsConfigEnabled != nil && s.deps.DualVectorPoolsConfigEnabled(ctx) && s.deps.ReadDualVectorReadyManifest != nil {
		readyManifest = s.deps.ReadDualVectorReadyManifest(ctx)
	}
	if readyManifest != nil {
		manifestDimension := pyIntOr(readyManifest["dimension"], 0, -1<<62)
		if manifestDimension > 0 {
			return &manifestDimension
		}
	}
	vectorDir := ""
	if store != nil && store.DataDir() != "" {
		vectorDir = store.DataDir()
	} else if s.deps.VectorsRoot != nil {
		vectorDir = s.deps.VectorsRoot(ctx)
	}
	if vectorDir == "" {
		return nil
	}
	metaPath := filepath.Join(vectorDir, "vectors_metadata.json")
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return nil
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		s.Logger().Warn("读取向量元数据失败，将回退到 runtime self-check: " + err.Error())
		return nil
	}
	value := pyIntOr(meta["dimension"], 0, -1<<62)
	if value > 0 {
		return &value
	}
	return nil
}

// NormalizeEmbeddingFingerprint 对齐 _normalize_embedding_fingerprint
func (s *EmbeddingStateService) NormalizeEmbeddingFingerprint(value any) map[string]any {
	payload := asMap(value)
	if payload == nil {
		return nil
	}
	hashValue := strings.TrimSpace(asString(payload["hash"]))
	if hashValue == "" {
		return nil
	}
	normalized := make(map[string]any, len(payload))
	for key, item := range payload {
		normalized[key] = item
	}
	normalized["hash"] = hashValue
	return normalized
}

// CurrentEmbeddingStatusDimension 对齐 _current_embedding_status_dimension
func (s *EmbeddingStateService) CurrentEmbeddingStatusDimension(ctx context.Context) int {
	if s.deps.RequestedDimension != nil {
		if requested := s.deps.RequestedDimension(); requested > 0 {
			return requested
		}
	}
	if s.deps.DefaultDimension != nil {
		if fallback := s.deps.DefaultDimension(); fallback > 0 {
			return fallback
		}
	}
	dimension := 0
	if s.deps.EmbeddingDimension != nil {
		dimension = s.deps.EmbeddingDimension()
	}
	configured := pyIntOr(s.Cfg("embedding.dimension", dimension), 0, -1<<62)
	if configured <= 0 {
		configured = dimension
	}
	if configured < 1 {
		configured = 1
	}
	return configured
}

// CurrentEmbeddingFingerprint 对齐 _current_embedding_fingerprint
func (s *EmbeddingStateService) CurrentEmbeddingFingerprint(ctx context.Context, dimension *int) map[string]any {
	if s.deps.Fingerprint == nil {
		return nil
	}
	effective := 0
	if dimension != nil {
		effective = *dimension
	}
	if effective == 0 {
		effective = s.CurrentEmbeddingStatusDimension(ctx)
	}
	fingerprint, err := s.deps.Fingerprint(effective)
	if err != nil {
		s.Logger().Warn("生成 embedding 指纹失败: " + err.Error())
		return nil
	}
	return s.NormalizeEmbeddingFingerprint(fingerprint)
}

// CurrentEmbeddingFingerprintForValidation 对齐 _current_embedding_fingerprint_for_validation
func (s *EmbeddingStateService) CurrentEmbeddingFingerprintForValidation(ctx context.Context, dimension *int) map[string]any {
	fingerprint := s.CurrentEmbeddingFingerprint(ctx, dimension)
	if fingerprint == nil {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(asString(fingerprint["source"]))) != "observed" {
		return nil
	}
	return fingerprint
}

// StoredEmbeddingFingerprint 对齐 _stored_embedding_fingerprint
func (s *EmbeddingStateService) StoredEmbeddingFingerprint(ctx context.Context, store *storage.VectorStore) map[string]any {
	var readyManifest map[string]any
	if store == nil && s.deps.DualVectorPoolsConfigEnabled != nil && s.deps.DualVectorPoolsConfigEnabled(ctx) && s.deps.ReadDualVectorReadyManifest != nil {
		readyManifest = s.deps.ReadDualVectorReadyManifest(ctx)
	}
	if readyManifest != nil {
		if fingerprint := s.NormalizeEmbeddingFingerprint(readyManifest["embedding_fingerprint"]); fingerprint != nil {
			return fingerprint
		}
	}
	vectorDir := ""
	if store != nil && store.DataDir() != "" {
		vectorDir = store.DataDir()
	} else if s.deps.VectorsRoot != nil {
		vectorDir = s.deps.VectorsRoot(ctx)
	}
	if vectorDir == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(vectorDir, "vectors_metadata.json"))
	if err != nil {
		return nil
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		s.Logger().Warn("读取向量指纹元数据失败: " + err.Error())
		return nil
	}
	if meta == nil {
		return nil
	}
	return s.NormalizeEmbeddingFingerprint(meta["embedding_fingerprint"])
}

// StampMissingEmbeddingFingerprintIfDimensionMatches 对齐 _stamp_missing_embedding_fingerprint_if_dimension_matches
func (s *EmbeddingStateService) StampMissingEmbeddingFingerprintIfDimensionMatches(ctx context.Context, store *storage.VectorStore) bool {
	if store == nil {
		return false
	}
	storedDimension := s.StoredVectorDimension(ctx, store)
	currentDimension := s.CurrentEmbeddingStatusDimension(ctx)
	if storedDimension == nil || *storedDimension != currentDimension {
		return false
	}
	currentFingerprint := s.CurrentEmbeddingFingerprintForValidation(ctx, &currentDimension)
	if currentFingerprint == nil {
		return false
	}
	if s.StoredEmbeddingFingerprint(ctx, store) != nil {
		return false
	}
	if err := store.Save("", currentFingerprint); err != nil {
		s.Logger().Warn("写入旧向量库指纹失败: " + err.Error())
		return false
	}
	s.Logger().Warn("旧向量库缺少 embedding 指纹且维度匹配，已写入当前模型指纹以复用旧向量")
	stamped := s.StoredEmbeddingFingerprint(ctx, store)
	return stamped != nil && asString(stamped["hash"]) == asString(currentFingerprint["hash"])
}

// EmbeddingFingerprintStatus 对齐 _embedding_fingerprint_status
func (s *EmbeddingStateService) EmbeddingFingerprintStatus(current map[string]any, stored map[string]any, hasStoredVectors bool) string {
	if !hasStoredVectors {
		return "none"
	}
	if current == nil {
		return "unknown"
	}
	if stored == nil {
		return "missing"
	}
	if asString(current["hash"]) == asString(stored["hash"]) {
		return "matched"
	}
	return "mismatched"
}

// StoredVectorsCompatibleWithCurrentEmbedding 对齐 _stored_vectors_compatible_with_current_embedding
func (s *EmbeddingStateService) StoredVectorsCompatibleWithCurrentEmbedding(ctx context.Context, store *storage.VectorStore) bool {
	current := s.CurrentEmbeddingFingerprintForValidation(ctx, nil)
	stored := s.StoredEmbeddingFingerprint(ctx, store)
	if current == nil || stored == nil {
		return false
	}
	return asString(current["hash"]) == asString(stored["hash"])
}

// VectorMismatchError 对齐 _vector_mismatch_error
func (s *EmbeddingStateService) VectorMismatchError(ctx context.Context, storedDimension int, detectedDimension int) string {
	return fmt.Sprintf(
		"检测到现有向量库与当前 embedding 输出维度不一致：stored=%d, encoded=%d。"+
			" 当前版本不会兼容 hash 时代或其他维度的旧向量，请改回原 embedding 配置，"+
			"或执行重嵌入/重建向量。",
		storedDimension, detectedDimension)
}

// EmbeddingFallbackEnabled 对齐 _embedding_fallback_enabled
func (s *EmbeddingStateService) EmbeddingFallbackEnabled(ctx context.Context) bool {
	return pyTruthy(s.Cfg("embedding.fallback.enabled", true))
}

// AllowMetadataOnlyWrite 对齐 _allow_metadata_only_write
func (s *EmbeddingStateService) AllowMetadataOnlyWrite(ctx context.Context) bool {
	return pyTruthy(s.Cfg("embedding.fallback.allow_metadata_only_write", true))
}

// EmbeddingProbeIntervalSeconds 对齐 _embedding_probe_interval_seconds
func (s *EmbeddingStateService) EmbeddingProbeIntervalSeconds(ctx context.Context) float64 {
	return mathMax(10.0, spaceFloatAny(s.Cfg("embedding.fallback.probe_interval_seconds", 180)))
}

// ParagraphVectorBackfillEnabled 对齐 _paragraph_vector_backfill_enabled
func (s *EmbeddingStateService) ParagraphVectorBackfillEnabled(ctx context.Context) bool {
	return pyTruthy(s.Cfg("embedding.paragraph_vector_backfill.enabled", true))
}

// ParagraphVectorBackfillIntervalSeconds 对齐 _paragraph_vector_backfill_interval_seconds
func (s *EmbeddingStateService) ParagraphVectorBackfillIntervalSeconds(ctx context.Context) float64 {
	return mathMax(10.0, spaceFloatAny(s.Cfg("embedding.paragraph_vector_backfill.interval_seconds", 60)))
}

// ParagraphVectorBackfillBatchSize 对齐 _paragraph_vector_backfill_batch_size
func (s *EmbeddingStateService) ParagraphVectorBackfillBatchSize(ctx context.Context) int {
	return pyIntOr(s.Cfg("embedding.paragraph_vector_backfill.batch_size", 64), 64, 1)
}

// ParagraphVectorBackfillMaxRetry 对齐 _paragraph_vector_backfill_max_retry
func (s *EmbeddingStateService) ParagraphVectorBackfillMaxRetry(ctx context.Context) int {
	return pyIntOr(s.Cfg("embedding.paragraph_vector_backfill.max_retry", 5), 5, 1)
}

// IsEmbeddingDegraded 对齐 _is_embedding_degraded
func (s *EmbeddingStateService) IsEmbeddingDegraded(ctx context.Context) bool {
	if s.deps.EmbeddingDegraded == nil {
		return false
	}
	return pyTruthy(s.deps.EmbeddingDegraded()["active"])
}

// EmbeddingDegradedSnapshot 对齐 _embedding_degraded_snapshot
func (s *EmbeddingStateService) EmbeddingDegradedSnapshot(ctx context.Context) map[string]any {
	state := map[string]any{}
	if s.deps.EmbeddingDegraded != nil {
		state = s.deps.EmbeddingDegraded()
	}
	if state == nil {
		state = map[string]any{}
	}
	return map[string]any{
		"active":     pyTruthy(state["active"]),
		"reason":     asString(state["reason"]),
		"since":      state["since"],
		"last_check": state["last_check"],
	}
}

// SetEmbeddingDegraded 对齐 _set_embedding_degraded
func (s *EmbeddingStateService) SetEmbeddingDegraded(ctx context.Context, active bool, reason string, checkedAt *float64) {
	now := float64(time.Now().UnixNano()) / 1e9
	if checkedAt != nil {
		now = *checkedAt
	}
	previous := s.EmbeddingDegradedSnapshot(ctx)
	wasActive := pyTruthy(previous["active"])
	if s.deps.SetEmbeddingDegraded != nil {
		s.deps.SetEmbeddingDegraded(active, reason, &now)
	}
	if s.deps.SetRuntimeCapability != nil {
		vectorEnabled := !active
		if s.deps.EmbeddingDimension != nil && s.deps.EmbeddingDimension() <= 0 {
			vectorEnabled = false
		}
		s.deps.SetRuntimeCapability("embedding", vectorEnabled)
	}
	if wasActive != active {
		if active {
			if strings.TrimSpace(reason) == "正在同步当前模型向量库" {
				s.Logger().Info("embedding 进入降级态，将启用 sparse-only 与 metadata-only 写入回退: reason=" + reason)
			} else {
				s.Logger().Warn("embedding 进入降级态，将启用 sparse-only 与 metadata-only 写入回退: reason=" + reason)
			}
		} else {
			s.Logger().Info("embedding 已恢复，退出降级态")
		}
	}
	s.ApplyRuntimeSparseMode(ctx)
}

// ApplyRuntimeSparseMode 对齐 _apply_runtime_sparse_mode
func (s *EmbeddingStateService) ApplyRuntimeSparseMode(ctx context.Context) {
	if s.deps.SetRuntimeSparseOnly == nil {
		return
	}
	sparseOnly := s.IsEmbeddingDegraded(ctx)
	if s.deps.RuntimeCapability != nil && !s.deps.RuntimeCapability("vector_read") {
		sparseOnly = true
	}
	s.deps.SetRuntimeSparseOnly(sparseOnly)
}

// RefreshRuntimeSelfCheck 对齐 _refresh_runtime_self_check
func (s *EmbeddingStateService) RefreshRuntimeSelfCheck(ctx context.Context, sampleText string) (map[string]any, error) {
	if strings.TrimSpace(sampleText) == "" {
		sampleText = "A_Memorix runtime self check"
	}
	if s.deps.RunSelfCheck == nil {
		return nil, fmt.Errorf("embedding 自检端口未注入")
	}
	config := map[string]any{}
	if s.deps.BuildRuntimeConfig != nil {
		config = s.deps.BuildRuntimeConfig(ctx)
	}
	report, err := s.deps.RunSelfCheck(ctx, sampleText, config)
	if err != nil {
		return nil, err
	}
	copied := map[string]any{}
	for key, value := range report {
		copied[key] = value
	}
	if s.deps.SetSelfCheckReport != nil {
		s.deps.SetSelfCheckReport(copied)
	}
	checkedAt := spaceFloatAny(report["checked_at"])
	if checkedAt == 0 {
		checkedAt = float64(time.Now().UnixNano()) / 1e9
	}
	if s.deps.SetEmbeddingDegraded != nil {
		state := map[string]any{}
		if s.deps.EmbeddingDegraded != nil {
			state = s.deps.EmbeddingDegraded()
		}
		active := pyTruthy(state["active"])
		reason := asString(state["reason"])
		since := state["since"]
		s.deps.SetEmbeddingDegraded(active, reason, &checkedAt)
		_ = since
	}
	return report, nil
}

// MarkStartupSelfCheckDeferred 对齐 _mark_startup_self_check_deferred
func (s *EmbeddingStateService) MarkStartupSelfCheckDeferred(ctx context.Context) {
	configuredDimension := pyIntOr(s.Cfg("embedding.dimension", s.embeddingDimension()), 6, 1)
	requestedDimension := s.CurrentEmbeddingStatusDimension(ctx)
	vectorStoreDimension := 0
	if s.deps.VectorStore != nil {
		if store := s.deps.VectorStore(); store != nil {
			vectorStoreDimension = store.Dimension()
		}
	}
	degraded := s.EmbeddingDegradedSnapshot(ctx)
	isDegraded := pyTruthy(degraded["active"])
	code := "startup_self_check_deferred"
	if isDegraded {
		code = "startup_self_check_deferred_degraded"
	}
	message := strings.TrimSpace(asString(degraded["reason"]))
	if message == "" {
		message = "启动阶段已跳过真实 embedding encode 自检，将由后台探测或手动 self_check 执行"
	}
	if s.deps.SetSelfCheckReport != nil {
		s.deps.SetSelfCheckReport(map[string]any{
			"ok":                     !isDegraded,
			"code":                   code,
			"message":                message,
			"configured_dimension":   configuredDimension,
			"requested_dimension":    requestedDimension,
			"vector_store_dimension": vectorStoreDimension,
			"detected_dimension":     requestedDimension,
			"encoded_dimension":      0,
			"elapsed_ms":             0.0,
			"sample_text":            "",
			"checked_at":             nil,
		})
	}
}

// IsStartupSelfCheckDeferred 对齐 _is_startup_self_check_deferred
func (s *EmbeddingStateService) IsStartupSelfCheckDeferred(ctx context.Context) bool {
	if s.deps.SelfCheckReport == nil {
		return false
	}
	report := s.deps.SelfCheckReport()
	code := ""
	if report != nil {
		code = asString(report["code"])
	}
	return code == "startup_self_check_deferred" || code == "startup_self_check_deferred_degraded"
}

// SelfCheckEffectiveDimension 对齐 _self_check_effective_dimension
func (s *EmbeddingStateService) SelfCheckEffectiveDimension(report map[string]any) int {
	for _, key := range []string{"encoded_dimension", "detected_dimension", "requested_dimension"} {
		value := pyIntOr(report[key], 0, -1<<62)
		if value > 0 {
			return value
		}
	}
	return 0
}

// ApplySelfCheckDimensionResult 对齐 _apply_self_check_dimension_result
func (s *EmbeddingStateService) ApplySelfCheckDimensionResult(ctx context.Context, report map[string]any) string {
	detectedDimension := s.SelfCheckEffectiveDimension(report)
	if detectedDimension <= 0 {
		return ""
	}
	if s.deps.SetEmbeddingDimension != nil {
		s.deps.SetEmbeddingDimension(detectedDimension)
	}
	vectorDimension := 0
	if s.deps.VectorStore != nil {
		if store := s.deps.VectorStore(); store != nil {
			vectorDimension = store.Dimension()
		}
	}
	if vectorDimension <= 0 || vectorDimension == detectedDimension {
		return ""
	}
	storedDimension := vectorDimension
	if stored := s.StoredVectorDimension(ctx, nil); stored != nil {
		storedDimension = *stored
	}
	message := s.VectorMismatchError(ctx, storedDimension, detectedDimension)
	if s.deps.SetVectorPersistBlockedUntilRebuild != nil {
		s.deps.SetVectorPersistBlockedUntilRebuild(true)
	}
	if s.deps.SetVectorRebuildSourceDimension != nil {
		s.deps.SetVectorRebuildSourceDimension(storedDimension)
	}
	return message
}

// EnqueueParagraphVectorBackfill 对齐 _enqueue_paragraph_vector_backfill
func (s *EmbeddingStateService) EnqueueParagraphVectorBackfill(ctx context.Context, paragraphHash string, errorText string) {
	if s.deps.MetadataStore == nil {
		return
	}
	store := s.deps.MetadataStore()
	if store == nil {
		return
	}
	if err := store.EnqueueParagraphVectorBackfill(paragraphHash, nil, errorText, nil); err != nil {
		s.Logger().Warn("登记 paragraph 向量回填任务失败: " + err.Error())
	}
}

// ParagraphVectorBackfillCounts 对齐 metadata_store 的状态计数
func (s *EmbeddingStateService) ParagraphVectorBackfillCounts(ctx context.Context) map[string]int {
	if s.deps.MetadataStore == nil {
		return map[string]int{}
	}
	store := s.deps.MetadataStore()
	if store == nil {
		return map[string]int{}
	}
	counts, err := store.GetParagraphVectorBackfillStatusCounts(nil)
	if err != nil {
		s.Logger().Warn("读取 paragraph 向量回填状态失败: " + err.Error())
		return map[string]int{}
	}
	return counts
}

// RunParagraphBackfillOnce 对齐 _run_paragraph_backfill_once
func (s *EmbeddingStateService) RunParagraphBackfillOnce(ctx context.Context, limit int) (map[string]any, error) {
	if s.deps.RunParagraphBackfillOnce == nil {
		return map[string]any{}, nil
	}
	if limit <= 0 {
		limit = s.ParagraphVectorBackfillBatchSize(ctx)
	}
	return s.deps.RunParagraphBackfillOnce(ctx, limit, s.ParagraphVectorBackfillMaxRetry(ctx), "manual")
}

// LoadVectorStoresForRuntime 对齐 _load_vector_stores_for_runtime
func (s *EmbeddingStateService) LoadVectorStoresForRuntime(ctx context.Context, requireExisting bool) error {
	if s.deps.DualVectorPoolsConfigEnabled != nil && s.deps.DualVectorPoolsConfigEnabled(ctx) &&
		s.deps.ReloadDualVectorStoresFromDisk != nil && s.deps.ReloadDualVectorStoresFromDisk(ctx) {
		return nil
	}
	if s.deps.LegacyVectorViewPresent != nil && s.deps.LegacyVectorViewPresent() {
		return nil
	}
	if s.deps.VectorStore == nil {
		return fmt.Errorf("向量存储未初始化")
	}
	store := s.deps.VectorStore()
	if store == nil {
		return fmt.Errorf("向量存储未初始化")
	}
	if !store.HasData() {
		if requireExisting {
			return storage.NewVectorStoreIntegrityError("未找到可加载的向量世代：双池未就绪且旧单池元数据不存在", storage.VectorStoreIntegrityOptions{
				ErrorCode:         "vector_generation_missing",
				DimensionStatus:   "unknown",
				FingerprintStatus: "unknown",
			})
		}
		return nil
	}
	expectedFingerprint := s.CurrentEmbeddingFingerprintForValidation(ctx, nil)
	if expectedFingerprint == nil {
		return storage.NewVectorStoreIntegrityError("当前 Embedding 指纹尚未经过真实请求确认", storage.VectorStoreIntegrityOptions{
			ErrorCode:         "embedding_fingerprint_unavailable",
			DimensionStatus:   "unknown",
			FingerprintStatus: "unknown",
		})
	}
	var validHashes []string
	if s.deps.V1ValidHashesForPool != nil {
		validHashes = s.deps.V1ValidHashesForPool(ctx, "single")
	}
	evidenceRoot := ""
	if s.deps.V1ReconciliationEvidenceRoot != nil {
		evidenceRoot = s.deps.V1ReconciliationEvidenceRoot(ctx)
	}
	if err := store.Load(store.DataDir(), expectedFingerprint, validHashes, evidenceRoot); err != nil {
		return err
	}
	store.WarmupIndex(true)
	return nil
}

// RecoverEmbeddingOnce 对齐 _recover_embedding_once
func (s *EmbeddingStateService) RecoverEmbeddingOnce(ctx context.Context, sampleText string) (map[string]any, error) {
	report, err := s.RefreshRuntimeSelfCheck(ctx, sampleText)
	if err != nil {
		return nil, err
	}
	checkedAt := spaceFloatAny(report["checked_at"])
	if checkedAt == 0 {
		checkedAt = float64(time.Now().UnixNano()) / 1e9
	}
	ok := pyTruthy(report["ok"])
	if ok || asString(report["code"]) == "embedding_dimension_mismatch" {
		if s.deps.SynchronizeVectorSpace != nil {
			spaceResult, spaceErr := s.deps.SynchronizeVectorSpace(ctx)
			if spaceErr == nil && pyTruthy(spaceResult["changed"]) {
				result := map[string]any{
					"success":          true,
					"recovered":        true,
					"vector_restored":  true,
					"vector_available": true,
					"report":           s.selfCheckReportSnapshot(),
					"space":            spaceResult,
				}
				if s.deps.VectorHealthSnapshot != nil {
					result["vector_health"] = s.deps.VectorHealthSnapshot()
				}
				return result, nil
			}
		}
	}
	if mismatch := s.ApplySelfCheckDimensionResult(ctx, report); mismatch != "" {
		s.SetEmbeddingDegraded(ctx, true, mismatch, &checkedAt)
		return map[string]any{"success": false, "recovered": false, "report": report, "detail": "dimension_mismatch"}, nil
	}
	if ok {
		s.SetEmbeddingDegraded(ctx, false, "", &checkedAt)
		vectorRestored := s.RestoreVectorChannelAfterEmbeddingRecovery(ctx)
		backfill := map[string]any{}
		if s.ParagraphVectorBackfillEnabled(ctx) && s.deps.RunParagraphBackfillOnce != nil {
			if result, backfillErr := s.deps.RunParagraphBackfillOnce(ctx, s.ParagraphVectorBackfillBatchSize(ctx), s.ParagraphVectorBackfillMaxRetry(ctx), "embedding_recovered"); backfillErr == nil {
				backfill = result
			}
		}
		vectorAvailable := false
		if s.deps.RuntimeCapability != nil {
			vectorAvailable = s.deps.RuntimeCapability("vector_read") && s.deps.RuntimeCapability("vector_write")
		}
		result := map[string]any{
			"success":          true,
			"recovered":        true,
			"vector_restored":  vectorRestored,
			"vector_available": vectorAvailable,
			"report":           report,
			"backfill":         backfill,
		}
		if s.deps.VectorHealthSnapshot != nil {
			result["vector_health"] = s.deps.VectorHealthSnapshot()
		}
		return result, nil
	}
	reason := asString(report["message"])
	if reason == "" {
		reason = "runtime self-check failed"
	}
	if s.EmbeddingFallbackEnabled(ctx) {
		s.SetEmbeddingDegraded(ctx, true, reason, &checkedAt)
		return map[string]any{"success": false, "recovered": false, "report": report, "detail": "still_degraded"}, nil
	}
	return map[string]any{"success": false, "recovered": false, "report": report, "detail": "fallback_disabled"}, nil
}

// RestoreVectorChannelAfterEmbeddingRecovery 对齐 _restore_vector_channel_after_embedding_recovery
func (s *EmbeddingStateService) RestoreVectorChannelAfterEmbeddingRecovery(ctx context.Context) bool {
	errorCode := ""
	if s.deps.VectorHealthErrorCode != nil {
		errorCode = s.deps.VectorHealthErrorCode()
	}
	if errorCode != "embedding_fingerprint_unavailable" {
		return false
	}
	loadOrRecover := func() (bool, bool) {
		if s.deps.MakeVectorStore != nil {
			dimension := s.CurrentEmbeddingStatusDimension(ctx)
			if store, err := s.deps.MakeVectorStore(ctx, s.deps.VectorsRoot(ctx), &dimension); err == nil && s.deps.SetVectorStore != nil {
				s.deps.SetVectorStore(store)
			}
		}
		if err := s.LoadVectorStoresForRuntime(ctx, true); err != nil {
			if s.deps.RecoverKnownVectorFailure == nil || !s.deps.RecoverKnownVectorFailure(ctx, err) {
				if s.deps.DisableVectorChannel != nil {
					s.deps.DisableVectorChannel(ctx, err)
				}
				return false, false
			}
			dualEnabled := s.deps.DualVectorPoolsEnabled != nil && s.deps.DualVectorPoolsEnabled(ctx)
			return dualEnabled, false
		}
		if s.deps.DualVectorPoolsConfigEnabled != nil && !s.deps.DualVectorPoolsConfigEnabled(ctx) && s.deps.MakeVectorStore != nil {
			if store, err := s.deps.MakeVectorStore(ctx, s.deps.ParagraphVectorDir(ctx), nil); err == nil && s.deps.SetParagraphVectorStore != nil {
				s.deps.SetParagraphVectorStore(store)
			}
			if store, err := s.deps.MakeVectorStore(ctx, s.deps.GraphVectorDir(ctx), nil); err == nil && s.deps.SetGraphVectorStore != nil {
				s.deps.SetGraphVectorStore(store)
			}
		}
		return true, true
	}
	var lock *sync.Mutex
	if s.deps.RebuildLock != nil {
		lock = s.deps.RebuildLock()
	}
	if lock != nil {
		lock.Lock()
		defer lock.Unlock()
	}
	if s.deps.VectorHealthErrorCode != nil && s.deps.VectorHealthErrorCode() != "embedding_fingerprint_unavailable" {
		return false
	}
	vectorAvailable, loadedDirectly := loadOrRecover()
	if !vectorAvailable {
		return false
	}
	if s.deps.BuildSearchRuntime == nil {
		return false
	}
	bundle, err := s.deps.BuildSearchRuntime(ctx)
	if err != nil {
		s.Logger().Warn(err.Error())
		return false
	}
	if !bundle.Ready {
		message := bundle.Error
		if message == "" {
			message = "Embedding 恢复后检索运行时重建失败"
		}
		s.Logger().Warn(message)
		return false
	}
	if s.deps.SetRetriever != nil {
		s.deps.SetRetriever(bundle.Retriever)
	}
	if s.deps.SetThresholdFilter != nil {
		s.deps.SetThresholdFilter(bundle.ThresholdFilter)
	}
	if bundle.SparseIndex != nil && s.deps.SetSparseIndex != nil {
		s.deps.SetSparseIndex(bundle.SparseIndex)
	}
	if s.deps.SetRuntimeCapability != nil {
		s.deps.SetRuntimeCapability("vector_read", true)
		s.deps.SetRuntimeCapability("vector_write", true)
	}
	if loadedDirectly && s.deps.SetVectorHealth != nil {
		s.deps.SetVectorHealth(ctx, map[string]any{
			"state":            "healthy",
			"error_code":       "",
			"reason":           "",
			"trusted_coverage": 1.0,
			"recovery_stage":   "idle",
			"operation_id":     "",
			"copy_progress":    map[string]any{},
		})
	}
	if s.deps.RefreshRelationWriteService != nil {
		s.deps.RefreshRelationWriteService(ctx)
	}
	if s.deps.RefreshRuntimeDependents != nil {
		s.deps.RefreshRuntimeDependents(ctx)
	}
	s.ApplyRuntimeSparseMode(ctx)
	if s.deps.LegacyVectorViewPresent != nil && s.deps.LegacyVectorViewPresent() && s.deps.StartBackgroundTasks != nil {
		_ = s.deps.StartBackgroundTasks(ctx)
	}
	s.Logger().Info("Embedding 指纹恢复后已重新启用向量通道")
	return true
}

func (s *EmbeddingStateService) embeddingDimension() int {
	if s.deps.EmbeddingDimension == nil {
		return 0
	}
	return s.deps.EmbeddingDimension()
}

func (s *EmbeddingStateService) selfCheckReportSnapshot() map[string]any {
	if s.deps.SelfCheckReport == nil {
		return map[string]any{}
	}
	return s.deps.SelfCheckReport()
}

func spaceFloatAny(value any) float64 {
	parsed, _ := asFloat(value)
	return parsed
}

func mathMax(a float64, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
