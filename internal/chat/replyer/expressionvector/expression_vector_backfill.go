package expressionvector

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// 表达向量 profile 的三条固定探针，与 Python EMBEDDING_PROFILE_PROBE_TEXTS 一致
var ExpressionEmbeddingProfileProbeTexts = []string{
	"MaiBot 表达检索 embedding profile probe v1：技术问题排查、报错截图、配置异常",
	"MaiBot 表达检索 embedding profile probe v1：轻松吐槽、接梗、日常群聊",
	"MaiBot 表达检索 embedding profile probe v1：情绪回应、安慰、拒绝、调侃",
}

// TextEmbeddingProbe 一条探针的嵌入结果与来源身份
type TextEmbeddingProbe struct {
	Embedding       []float32
	Model           string
	ModelIdentifier string
	APIProvider     string
	Fingerprint     map[string]any
}

// TextEmbeddingProfiler 可选接口：embedding 客户端可返回模型身份与探针向量
// 未实现时回退到 go-cosine-v1 兼容 marker
type TextEmbeddingProfiler interface {
	EmbeddingProfile(ctx context.Context, probes []string) ([]TextEmbeddingProbe, error)
}

// ExpressionBackfillOptions 历史补建循环参数，零值使用 Python 默认常量
type ExpressionBackfillOptions struct {
	BatchSize                int
	MinInterval              time.Duration
	MaxInterval              time.Duration
	IntervalSpeedRatio       float64
	EmptyScanInterval        time.Duration
	FailureRetryInterval     time.Duration
	ItemFailureRetryInterval time.Duration
	IsolationAttempts        int
	IsolationInterval        time.Duration
	ErrorMaxLength           int
	ProfileCache             time.Duration
	Now                      func() time.Time
	Wait                     func(ctx context.Context, duration time.Duration) bool
	Settings                 func() (Settings, error)
	// RouteSignature 标识当前 embedding 模型/Provider 配置；为空时只按学习配置签名去抖
	RouteSignature string
}

func (o ExpressionBackfillOptions) normalized() ExpressionBackfillOptions {
	if o.BatchSize <= 0 {
		o.BatchSize = 200
	}
	if o.MinInterval <= 0 {
		o.MinInterval = 10 * time.Second
	}
	if o.MaxInterval <= 0 {
		o.MaxInterval = 600 * time.Second
	}
	if o.MaxInterval < o.MinInterval {
		o.MaxInterval = o.MinInterval
	}
	if o.IntervalSpeedRatio < 0 {
		o.IntervalSpeedRatio = 0
	}
	if o.IntervalSpeedRatio == 0 {
		o.IntervalSpeedRatio = 1
	}
	if o.EmptyScanInterval <= 0 {
		o.EmptyScanInterval = 300 * time.Second
	}
	if o.FailureRetryInterval <= 0 {
		o.FailureRetryInterval = 60 * time.Second
	}
	if o.ItemFailureRetryInterval <= 0 {
		o.ItemFailureRetryInterval = 60 * time.Second
	}
	if o.IsolationAttempts <= 0 {
		o.IsolationAttempts = 3
	}
	if o.IsolationInterval <= 0 {
		o.IsolationInterval = 24 * time.Hour
	}
	if o.ErrorMaxLength <= 0 {
		o.ErrorMaxLength = 500
	}
	if o.ProfileCache <= 0 {
		o.ProfileCache = 600 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	// 默认配置由宿主 Port 提供，本包不感知 learners 的全局加载器
	if o.Settings == nil {
		o.Settings = func() (Settings, error) { return Settings{}, nil }
	}
	return o
}

type expressionBackfillRunner struct {
	reg  Port
	opts ExpressionBackfillOptions

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	wakeup chan struct{}

	mu                sync.Mutex
	generation        uint64
	settingsSignature string
	routeSignature    string
	cachedProfile     *ExpressionEmbeddingProfile
	cachedGeneration  uint64
	cachedAt          time.Time
	lastEmptyAt       time.Time
	lastFailureAt     time.Time
	// drift 记录 embedding 向量空间漂移的连续确认状态
	drift expressionProfileDriftState
}

// expressionBackfillSettingsSignature 生成影响补建循环的学习配置签名
// 仅覆盖 use_vector_expression 与索引路径，embedding 模型身份由 RouteSignature 负责
func expressionBackfillSettingsSignature(settings Settings) string {
	return fmt.Sprintf("use=%t|path=%s", settings.UseVectorExpression, strings.TrimSpace(settings.ExpressionVectorIndexPath))
}

func (r *expressionBackfillRunner) Notify() {
	r.mu.Lock()
	r.generation++
	r.cachedProfile = nil
	r.lastEmptyAt = time.Time{}
	r.lastFailureAt = time.Time{}
	r.mu.Unlock()
	select {
	case r.wakeup <- struct{}{}:
	default:
	}
}

func (r *expressionBackfillRunner) run() {
	defer close(r.done)
	r.reg.BeginExpressionVectorSync()
	var lastErr error
	defer func() {
		if r.ctx.Err() != nil {
			lastErr = nil
		}
		r.reg.FinishExpressionVectorSync(lastErr)
	}()
	for {
		if r.ctx.Err() != nil {
			return
		}
		settings, err := r.opts.Settings()
		if err != nil {
			lastErr = err
			r.reg.RecordExpressionVectorSyncError(err)
			if !r.wait(r.opts.FailureRetryInterval) {
				return
			}
			continue
		}
		if !settings.UseVectorExpression {
			// 对齐 Python expression_vector_index.py:3025-3027：仅停止循环并清运行期错误，已持久化的失败记录保留
			r.reg.ClearExpressionVectorSyncError()
			r.mu.Lock()
			r.cachedProfile = nil
			r.cachedGeneration = 0
			r.cachedAt = time.Time{}
			r.lastFailureAt = time.Time{}
			r.mu.Unlock()
			return
		}
		generation := r.currentGeneration()
		store, err := r.ensureStore(settings)
		if err != nil {
			lastErr = err
			r.reg.RecordExpressionVectorSyncError(err)
			if !r.wait(r.opts.FailureRetryInterval) {
				return
			}
			continue
		}
		embedder := r.reg.Embedder()
		if embedder == nil {
			lastErr = fmt.Errorf("表达向量补建缺少 embedding 客户端")
			r.reg.RecordExpressionVectorSyncError(lastErr)
			if !r.wait(r.opts.FailureRetryInterval) {
				return
			}
			continue
		}
		profile, err := r.ensureProfile(embedder, generation)
		if err != nil {
			lastErr = err
			r.reg.RecordExpressionVectorSyncError(err)
			if !r.wait(r.opts.FailureRetryInterval) {
				return
			}
			continue
		}
		if r.generationChanged(generation) {
			continue
		}
		// 用持久化基准校验本次探针结果，识别向量空间漂移
		profile = r.resolveProfileDrift(store, profile)
		if err := store.SetActiveEmbeddingProfile(profile); err != nil {
			lastErr = err
			r.reg.RecordExpressionVectorSyncError(err)
			if !r.wait(r.opts.FailureRetryInterval) {
				return
			}
			continue
		}
		batch, currentFingerprints, pending, err := r.selectBatch(store, profile)
		if err != nil {
			lastErr = err
			r.reg.RecordExpressionVectorSyncError(err)
			if !r.wait(r.opts.FailureRetryInterval) {
				return
			}
			continue
		}
		startedAt := r.now()
		if len(batch) == 0 {
			if err := store.FinalizeExpressionBackfill(profile, r.now()); err != nil {
				lastErr = err
				r.reg.RecordExpressionVectorSyncError(err)
				if !r.wait(r.opts.FailureRetryInterval) {
					return
				}
				continue
			}
			lastErr = nil
			r.reg.ClearExpressionVectorSyncError()
			r.mu.Lock()
			r.lastEmptyAt = r.now()
			r.mu.Unlock()
			if !r.wait(r.opts.EmptyScanInterval) {
				return
			}
			continue
		}
		successes, failures, err := r.embedBatch(batch, profile)
		if err != nil {
			lastErr = err
			r.reg.RecordExpressionVectorSyncError(err)
			if !r.wait(r.opts.FailureRetryInterval) {
				return
			}
			continue
		}
		policy := ExpressionBackfillFailurePolicy{
			ItemFailureRetry:  r.opts.ItemFailureRetryInterval,
			IsolationInterval: r.opts.IsolationInterval,
			IsolationAttempts: r.opts.IsolationAttempts,
			ErrorMaxLength:    r.opts.ErrorMaxLength,
		}
		isolated, err := store.ApplyExpressionBackfillBatch(profile, successes, failures, currentFingerprints, r.now(), policy)
		if err != nil {
			lastErr = err
			r.reg.RecordExpressionVectorSyncError(err)
			if !r.wait(r.opts.FailureRetryInterval) {
				return
			}
			continue
		}
		lastErr = nil
		r.reg.ClearExpressionVectorSyncError()
		elapsed := r.now().Sub(startedAt)
		interval := r.adaptiveInterval(elapsed)
		_ = isolated
		_ = pending
		if !r.wait(interval) {
			return
		}
	}
}

func (r *expressionBackfillRunner) ensureStore(settings Settings) (*PersistentExpressionVectorStore, error) {
	path := strings.TrimSpace(settings.ExpressionVectorIndexPath)
	if path == "" {
		return nil, fmt.Errorf("表达向量索引路径为空")
	}
	if store, ok := r.reg.ExpressionVectors().(*PersistentExpressionVectorStore); ok &&
		strings.TrimSpace(store.Path()) == path {
		return store, nil
	}
	return r.reg.EnablePersistentExpressionVectors(path)
}

func (r *expressionBackfillRunner) ensureProfile(embedder TextEmbedder, generation uint64) (ExpressionEmbeddingProfile, error) {
	if profile := r.cachedProfileFor(generation); profile != nil {
		return *profile, nil
	}
	var profile ExpressionEmbeddingProfile
	if profiler, ok := embedder.(TextEmbeddingProfiler); ok {
		probes, err := profiler.EmbeddingProfile(r.ctx, append([]string(nil), ExpressionEmbeddingProfileProbeTexts...))
		if err != nil {
			return ExpressionEmbeddingProfile{}, err
		}
		profile, err = buildExpressionEmbeddingProfile(probes)
		if err != nil {
			return ExpressionEmbeddingProfile{}, err
		}
	} else {
		vector, err := embedder.EmbedText(r.ctx, ExpressionEmbeddingProfileProbeTexts[0])
		if err != nil {
			return ExpressionEmbeddingProfile{}, err
		}
		if len(vector) == 0 {
			return ExpressionEmbeddingProfile{}, fmt.Errorf("表达向量 profile 探针返回空向量")
		}
		profile = ExpressionEmbeddingProfile{
			Marker:          expressionVectorProfileMarker,
			ModelName:       expressionVectorEmbeddingModel,
			Dimension:       len(vector),
			Revision:        1,
			ProbeTexts:      append([]string(nil), ExpressionEmbeddingProfileProbeTexts[:1]...),
			ProbeEmbeddings: [][]float32{append([]float32(nil), vector...)},
			Fingerprint:     map[string]any{},
		}
	}
	r.mu.Lock()
	if r.generation == generation {
		cached := profile
		r.cachedProfile = &cached
		r.cachedGeneration = generation
		r.cachedAt = r.now()
	}
	r.mu.Unlock()
	return profile, nil
}

// resolveProfileDrift 用持久化基准校验新探针结果，识别向量空间漂移
//
// 对齐 _resolve_embedding_profile_candidate：同名模型探针相似度低于阈值时
// 需连续确认三次，才把 revision+1 的新 profile 当作基准，避免一次抖动就重建全部向量。
func (r *expressionBackfillRunner) resolveProfileDrift(store *PersistentExpressionVectorStore, candidate ExpressionEmbeddingProfile) ExpressionEmbeddingProfile {
	persisted := store.persistedEmbeddingProfile()
	r.mu.Lock()
	defer r.mu.Unlock()
	resolved, switched := r.drift.resolve(persisted, candidate)
	if switched {
		r.cachedProfile = nil
		r.cachedGeneration = 0
	}
	return resolved
}

func (r *expressionBackfillRunner) cachedProfileFor(generation uint64) *ExpressionEmbeddingProfile {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cachedProfile == nil || r.cachedGeneration != generation || r.generation != generation {
		return nil
	}
	if r.opts.ProfileCache > 0 && r.now().Sub(r.cachedAt) > r.opts.ProfileCache {
		return nil
	}
	profile := *r.cachedProfile
	return &profile
}

func buildExpressionEmbeddingProfile(probes []TextEmbeddingProbe) (ExpressionEmbeddingProfile, error) {
	if len(probes) != len(ExpressionEmbeddingProfileProbeTexts) {
		return ExpressionEmbeddingProfile{}, fmt.Errorf("表达向量 profile 需要 %d 条探针结果，实际 %d", len(ExpressionEmbeddingProfileProbeTexts), len(probes))
	}
	dimension := 0
	models := make(map[string]struct{}, len(probes))
	identifiers := make(map[string]struct{}, len(probes))
	providers := make(map[string]struct{}, len(probes))
	embeddings := make([][]float32, 0, len(probes))
	fingerprint := map[string]any{}
	for index, probe := range probes {
		if len(probe.Embedding) == 0 {
			return ExpressionEmbeddingProfile{}, fmt.Errorf("表达向量 profile 第 %d 条探针为空向量", index)
		}
		if dimension == 0 {
			dimension = len(probe.Embedding)
		}
		if len(probe.Embedding) != dimension {
			return ExpressionEmbeddingProfile{}, fmt.Errorf("表达向量 profile 探针维度不一致: %d != %d", len(probe.Embedding), dimension)
		}
		model := strings.TrimSpace(probe.Model)
		identifier := strings.TrimSpace(probe.ModelIdentifier)
		provider := strings.TrimSpace(probe.APIProvider)
		if model == "" || identifier == "" || provider == "" {
			return ExpressionEmbeddingProfile{}, fmt.Errorf("表达向量 profile 探针缺少模型身份")
		}
		models[model] = struct{}{}
		identifiers[identifier] = struct{}{}
		providers[provider] = struct{}{}
		if len(fingerprint) == 0 && probe.Fingerprint != nil {
			fingerprint = probe.Fingerprint
		}
		embeddings = append(embeddings, append([]float32(nil), probe.Embedding...))
	}
	if len(models) != 1 || len(identifiers) != 1 || len(providers) != 1 {
		return ExpressionEmbeddingProfile{}, fmt.Errorf("表达向量 profile 探针模型身份不一致")
	}
	markerPayload := map[string]any{
		"version":               expressionVectorProfileVersion,
		"model_name":            onlyMapKey(models),
		"model_identifier":      onlyMapKey(identifiers),
		"api_provider":          onlyMapKey(providers),
		"dimension":             dimension,
		"revision":              1,
		"embedding_fingerprint": fingerprint,
	}
	marker, err := pythonJSONSHA256(markerPayload)
	if err != nil {
		return ExpressionEmbeddingProfile{}, err
	}
	return ExpressionEmbeddingProfile{
		Marker:          marker,
		ModelName:       onlyMapKey(models),
		ModelIdentifier: onlyMapKey(identifiers),
		APIProvider:     onlyMapKey(providers),
		Dimension:       dimension,
		Revision:        1,
		ProbeTexts:      append([]string(nil), ExpressionEmbeddingProfileProbeTexts...),
		ProbeEmbeddings: embeddings,
		Fingerprint:     fingerprint,
	}, nil
}

func onlyMapKey(values map[string]struct{}) string {
	for value := range values {
		return value
	}
	return ""
}

func (r *expressionBackfillRunner) selectBatch(store *PersistentExpressionVectorStore, profile ExpressionEmbeddingProfile) ([]persistentExpressionVector, map[uint]string, int, error) {
	indexed, failures := store.expressionBackfillIndexState()
	nowTimestamp := float64(r.now().UnixNano()) / float64(time.Second)
	const pageSize = 200
	batch := make([]persistentExpressionVector, 0, r.opts.BatchSize)
	fingerprints := make(map[uint]string)
	pending := 0
	for page := 1; ; page++ {
		rows, total, err := r.reg.ListExpressions(r.ctx, (page-1)*pageSize, pageSize)
		if err != nil {
			return nil, nil, 0, err
		}
		for _, rec := range rows {
			if rec.ID == 0 {
				continue
			}
			situation := normalizeExpressionVectorText(rec.Situation)
			style := NormalizeExpressionStyleForLearning(rec.Style)
			if situation == "" || style == "" {
				continue
			}
			fingerprint := expressionVectorFingerprint(rec.ID, situation, style)
			fingerprints[rec.ID] = fingerprint
			pending++
			if item, ok := indexed[rec.ID]; ok && !expressionNeedsHistoryBackfill(&item, rec.ID, situation, style, profile) {
				pending--
				continue
			}
			if record, ok := failures[rec.ID]; ok && expressionFailureRecordMatches(&record, rec.ID, situation, style, profile) &&
				record.RetryAfterTimestamp > nowTimestamp {
				pending--
				continue
			}
			if len(batch) >= r.opts.BatchSize {
				continue
			}
			modifiedBy := rec.ModifiedBy
			batch = append(batch, persistentExpressionVector{
				ID:          rec.ID,
				SessionID:   rec.SessionID,
				Situation:   situation,
				Style:       style,
				Count:       rec.Count,
				Checked:     rec.Checked,
				ModifiedBy:  modifiedBy,
				Fingerprint: fingerprint,
			})
		}
		if page*pageSize >= total || len(rows) == 0 {
			break
		}
	}
	return batch, fingerprints, pending, nil
}

// expressionNeedsHistoryBackfill 判断数据库表达是否缺失当前 profile 的可用向量
// 对齐 Python expression_vector_index.py:1304-1322
func expressionNeedsHistoryBackfill(indexed *persistentExpressionVector, expressionID uint, situation, style string, profile ExpressionEmbeddingProfile) bool {
	if indexed == nil {
		return true
	}
	if normalizeExpressionVectorText(indexed.EmbeddingProfileMarker) != normalizeExpressionVectorText(profile.Marker) {
		return true
	}
	if indexed.EmbeddingDimension != profile.Dimension {
		return true
	}
	return normalizeExpressionVectorText(indexed.Fingerprint) != expressionVectorFingerprint(expressionID, situation, style)
}

// expressionFailureRecordMatches 判断失败记录是否仍对当前内容和向量空间有效
// 对齐 Python expression_vector_index.py:1288-1302
func expressionFailureRecordMatches(record *persistentEmbeddingFailure, expressionID uint, situation, style string, profile ExpressionEmbeddingProfile) bool {
	if record == nil {
		return false
	}
	return record.ExpressionID == expressionID &&
		normalizeExpressionVectorText(record.Fingerprint) == expressionVectorFingerprint(expressionID, situation, style) &&
		normalizeExpressionVectorText(record.EmbeddingProfileMarker) == normalizeExpressionVectorText(profile.Marker)
}

func (r *expressionBackfillRunner) embedBatch(batch []persistentExpressionVector, profile ExpressionEmbeddingProfile) ([]ExpressionBackfillSuccess, []ExpressionBackfillFailure, error) {
	type embedResult struct {
		index  int
		vector []float32
		err    error
	}
	results := make([]embedResult, len(batch))
	jobs := make(chan int)
	var workers sync.WaitGroup
	workerCount := 3
	if len(batch) < workerCount {
		workerCount = len(batch)
	}
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				item := batch[index]
				vector, err := r.reg.Embedder().EmbedText(r.ctx, expressionEmbeddingText(item.Situation, item.Style))
				results[index] = embedResult{index: index, vector: vector, err: err}
			}
		}()
	}
	for index := range batch {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	if r.ctx.Err() != nil {
		return nil, nil, r.ctx.Err()
	}
	successes := make([]ExpressionBackfillSuccess, 0, len(batch))
	failures := make([]ExpressionBackfillFailure, 0)
	for _, result := range results {
		item := batch[result.index]
		if result.err == nil && len(result.vector) != profile.Dimension {
			result.err = fmt.Errorf("表达向量维度不匹配: got=%d want=%d", len(result.vector), profile.Dimension)
		}
		meta := ExpressionVectorMetadata{
			ID:         item.ID,
			SessionID:  item.SessionID,
			Situation:  item.Situation,
			Style:      item.Style,
			Count:      item.Count,
			Checked:    item.Checked,
			ModifiedBy: item.ModifiedBy,
		}
		if result.err != nil || len(result.vector) == 0 {
			err := result.err
			if err == nil {
				err = fmt.Errorf("embedding 返回空向量")
			}
			failures = append(failures, ExpressionBackfillFailure{Meta: meta, Err: err})
			continue
		}
		successes = append(successes, ExpressionBackfillSuccess{Meta: meta, Vector: result.vector})
	}
	if len(successes) == 0 && len(failures) > 0 {
		if _, err := r.reg.Embedder().EmbedText(r.ctx, ExpressionEmbeddingProfileProbeTexts[0]); err != nil {
			return nil, nil, fmt.Errorf("表达向量 embedding provider 不可用: %w", err)
		}
	}
	return successes, failures, nil
}

func (r *expressionBackfillRunner) adaptiveInterval(elapsed time.Duration) time.Duration {
	seconds := elapsed.Seconds() * r.opts.IntervalSpeedRatio
	interval := time.Duration(seconds * float64(time.Second))
	if interval < r.opts.MinInterval {
		interval = r.opts.MinInterval
	}
	if interval > r.opts.MaxInterval {
		interval = r.opts.MaxInterval
	}
	return interval
}

func (r *expressionBackfillRunner) wait(duration time.Duration) bool {
	if r.ctx.Err() != nil {
		return false
	}
	if duration <= 0 {
		duration = time.Millisecond
	}
	if r.opts.Wait != nil {
		select {
		case <-r.ctx.Done():
			return false
		case <-time.After(duration):
			return r.ctx.Err() == nil
		}
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-r.ctx.Done():
		return false
	case <-r.wakeup:
		return true
	case <-timer.C:
		return true
	}
}

func (r *expressionBackfillRunner) currentGeneration() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generation
}

func (r *expressionBackfillRunner) generationChanged(generation uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generation != generation
}

func (r *expressionBackfillRunner) now() time.Time {
	return r.opts.Now()
}

func sortedExpressionVectorIDs(items []persistentExpressionVector) []uint {
	ids := make([]uint, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
