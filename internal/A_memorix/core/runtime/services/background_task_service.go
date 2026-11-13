package services

// 后台任务服务，逐段对齐 background_task_service.py 的 MemoryBackgroundTaskService

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"maibot-go/internal/A_memorix/core/storage"
)

// BackgroundTaskDeps 后台任务依赖端口
type BackgroundTaskDeps struct {
	Cfg     func(key string, fallback any) any
	Persist func()
	Sleep   func(ctx context.Context, seconds float64) error

	HasImageRuntime                 func() bool
	ProcessImageJobsOnce            func(ctx context.Context) error
	HasLegacyVectorView             func() bool
	CopyLegacyVectorsOnce           func(ctx context.Context, batchSize int) (map[string]any, error)
	CleanupVectorQuarantine         func()
	MaintenanceReconcile            func()
	ProcessStorageCleanup           func(ctx context.Context, limit int) (map[string]any, error)
	MaintenanceLoop                 func(ctx context.Context) error
	FeedbackCorrectionLoop          func(ctx context.Context) error
	FeedbackCorrectionReconcileLoop func(ctx context.Context) error

	DualVectorPoolsEnabled             func(ctx context.Context) bool
	ShouldStartDualVectorAutoMigration func(ctx context.Context) bool
	RebuildAllVectors                  func(ctx context.Context) (map[string]any, error)
	VectorRebuildLocked                func() bool
	NormalizeAutoMigrationProgress     func(ctx context.Context, progress any) map[string]any
	UpdateAutoMigrationStage           func(ctx context.Context, stage string, progress map[string]any)
	AutoMigrationStatus                func() map[string]any
	SetAutoMigrationStatus             func(status map[string]any)
	DualVectorMigrationInitialDelay    func() float64
	DualVectorMigrationLockRetries     func() []float64

	VectorStores                     func(ctx context.Context) map[string]*storage.VectorStore
	SaveVectorStore                  func(ctx context.Context, store *storage.VectorStore)
	VectorPersistBlockedUntilRebuild func() bool
	// LockVectorRebuild 获取向量重建锁（可能等待），ok=false 表示无法获取
	LockVectorRebuild func() (unlock func(), ok bool)

	EmbeddingManagerPresent          func() bool
	RecoverEmbeddingOnce             func(ctx context.Context)
	EmbeddingProbeInterval           func() float64
	ParagraphBackfillEnabled         func() bool
	IsEmbeddingDegraded              func() bool
	RunParagraphBackfillOnce         func(ctx context.Context, limit int, maxRetry int, trigger string)
	ParagraphBackfillIntervalSeconds func() float64
	ParagraphBackfillBatchSize       func() int
	ParagraphBackfillMaxRetry        func() int

	ProcessEpisodeSourceRebuildBatch func(ctx context.Context, params map[string]any) (map[string]any, error)

	ActivePersonTimestamps           func() map[string]float64
	HasPendingPersonProfileRefresh   func(personID string) bool
	RefreshPersonProfile             func(ctx context.Context, personID string, limit int) (map[string]any, error)
	ProcessPersonProfileRefreshQueue func(ctx context.Context, limit int) (map[string]any, error)
	ProfileRefreshQueueInterval      func() float64
	ProfileRefreshQueueBatchSize     func() int

	BackgroundStopping    func() bool
	SetBackgroundStopping func(bool)
}

// BackgroundTaskService 后台任务服务
type BackgroundTaskService struct {
	*Base
	deps BackgroundTaskDeps

	lock  sync.Mutex
	tasks map[string]context.CancelFunc
	wg    sync.WaitGroup
}

// NewBackgroundTaskService 构造后台任务服务
func NewBackgroundTaskService(base *Base, deps BackgroundTaskDeps) *BackgroundTaskService {
	return &BackgroundTaskService{Base: base, deps: deps, tasks: map[string]context.CancelFunc{}}
}

func (s *BackgroundTaskService) cfg(key string, fallback any) any {
	if s.deps.Cfg != nil {
		return s.deps.Cfg(key, fallback)
	}
	return s.Cfg(key, fallback)
}

func (s *BackgroundTaskService) stopping() bool {
	if s.deps.BackgroundStopping != nil {
		return s.deps.BackgroundStopping()
	}
	return false
}

func (s *BackgroundTaskService) setStopping(value bool) {
	if s.deps.SetBackgroundStopping != nil {
		s.deps.SetBackgroundStopping(value)
	}
}

// EnsureBackgroundTask 对齐 _ensure_background_task
func (s *BackgroundTaskService) EnsureBackgroundTask(ctx context.Context, name string, factory func(ctx context.Context) error) error {
	if factory == nil {
		return fmt.Errorf("后台任务工厂不能为空")
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	if _, ok := s.tasks[name]; ok {
		return nil
	}
	child, cancel := context.WithCancel(context.Background())
	s.tasks[name] = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.lock.Lock()
			delete(s.tasks, name)
			s.lock.Unlock()
			if recovered := recover(); recovered != nil {
				s.Logger().Error(fmt.Sprintf("后台任务 panic: name=%s error=%v", name, recovered))
			}
		}()
		if err := factory(child); err != nil && !isContextCanceled(child) {
			s.LoggerWarn("后台任务退出异常: name=%s error=%v", name, err)
		}
	}()
	return nil
}

// SleepBackground 对齐 _sleep_background
func (s *BackgroundTaskService) SleepBackground(ctx context.Context, seconds float64) error {
	if s.deps.Sleep != nil {
		return s.deps.Sleep(ctx, seconds)
	}
	if seconds <= 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// StartBackgroundTasks 对齐 _start_background_tasks
func (s *BackgroundTaskService) StartBackgroundTasks(ctx context.Context) error {
	s.lock.Lock()
	s.setStopping(false)
	s.lock.Unlock()
	loops := []struct {
		name    string
		factory func(ctx context.Context) error
	}{
		{"auto_save", s.AutoSaveLoop},
		{"episode_materialization", s.EpisodeMaterializationLoop},
		{"embedding_probe", s.EmbeddingProbeLoop},
		{"paragraph_vector_backfill", s.ParagraphVectorBackfillLoop},
		{"vector_index_training", s.VectorIndexTrainingLoop},
		{"memory_maintenance", s.maintenanceLoop},
		{"storage_cleanup", s.storageCleanupLoop},
		{"person_profile_refresh", s.PersonProfileRefreshLoop},
		{"person_profile_refresh_queue", s.PersonProfileRefreshQueueLoop},
	}
	if s.deps.FeedbackCorrectionLoop != nil {
		loops = append(loops, struct {
			name    string
			factory func(ctx context.Context) error
		}{"feedback_correction", s.deps.FeedbackCorrectionLoop})
	}
	if s.deps.FeedbackCorrectionReconcileLoop != nil {
		loops = append(loops, struct {
			name    string
			factory func(ctx context.Context) error
		}{"feedback_correction_reconcile", s.deps.FeedbackCorrectionReconcileLoop})
	}
	if s.deps.HasImageRuntime != nil && s.deps.HasImageRuntime() {
		loops = append(loops, struct {
			name    string
			factory func(ctx context.Context) error
		}{"image_embedding", s.imageEmbeddingLoop})
	}
	if s.deps.HasLegacyVectorView != nil && s.deps.HasLegacyVectorView() {
		loops = append(loops, struct {
			name    string
			factory func(ctx context.Context) error
		}{"legacy_vector_copy", s.legacyVectorCopyLoop})
	}
	if s.deps.ShouldStartDualVectorAutoMigration != nil && s.deps.ShouldStartDualVectorAutoMigration(ctx) {
		loops = append(loops, struct {
			name    string
			factory func(ctx context.Context) error
		}{"dual_vector_auto_migration", s.DualVectorAutoMigrationLoop})
	}
	for _, item := range loops {
		factory := item.factory
		if err := s.EnsureBackgroundTask(ctx, item.name, factory); err != nil {
			return err
		}
	}
	return nil
}

// StopBackgroundTasks 对齐 _stop_background_tasks
func (s *BackgroundTaskService) StopBackgroundTasks(ctx context.Context) error {
	s.lock.Lock()
	s.setStopping(true)
	cancels := make([]context.CancelFunc, 0, len(s.tasks))
	for _, cancel := range s.tasks {
		cancels = append(cancels, cancel)
	}
	s.lock.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.lock.Lock()
	s.tasks = map[string]context.CancelFunc{}
	s.lock.Unlock()
	return nil
}

// CancelAllBackgroundTasks 强制取消全部后台循环，供初始化失败路径兜底
// 与 StopBackgroundTasks 不同，这里不等待调用方上下文，最多等 5 秒后返回
func (s *BackgroundTaskService) CancelAllBackgroundTasks() {
	s.lock.Lock()
	s.setStopping(true)
	cancels := make([]context.CancelFunc, 0, len(s.tasks))
	for _, cancel := range s.tasks {
		cancels = append(cancels, cancel)
	}
	s.lock.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		s.LoggerWarn("取消后台任务超时，剩余循环将在退出时回收")
	}
	s.lock.Lock()
	s.tasks = map[string]context.CancelFunc{}
	s.lock.Unlock()
}

// AutoSaveLoop 对齐 _auto_save_loop
func (s *BackgroundTaskService) AutoSaveLoop(ctx context.Context) error {
	for !s.stopping() {
		intervalMinutes := mathMax(1.0, pyFloatOr(s.cfg("advanced.auto_save_interval_minutes", 5), 5))
		if err := s.SleepBackground(ctx, intervalMinutes*60.0); err != nil {
			return err
		}
		if s.stopping() {
			break
		}
		if pyTruthy(s.cfg("advanced.enable_auto_save", true)) && s.deps.Persist != nil {
			s.deps.Persist()
		}
	}
	return nil
}

// imageEmbeddingLoop 对齐 _image_embedding_loop
func (s *BackgroundTaskService) imageEmbeddingLoop(ctx context.Context) error {
	for !s.stopping() && s.deps.HasImageRuntime != nil && s.deps.HasImageRuntime() {
		if s.deps.ProcessImageJobsOnce != nil {
			if err := s.deps.ProcessImageJobsOnce(ctx); err != nil && !isContextCanceled(ctx) {
				s.LoggerWarn("图片嵌入任务异常，将在下一轮重试: %v", err)
			}
		}
		interval := pyFloatOr(s.cfg("image_memory.job_poll_interval_seconds", 2.0), 2.0)
		if err := s.SleepBackground(ctx, mathMax(0.1, interval)); err != nil {
			return err
		}
	}
	return nil
}

// storageCleanupLoop 对齐 _storage_cleanup_loop
func (s *BackgroundTaskService) storageCleanupLoop(ctx context.Context) error {
	for !s.stopping() {
		if err := s.SleepBackground(ctx, 10.0); err != nil {
			return err
		}
		if s.stopping() {
			break
		}
		if s.deps.MaintenanceReconcile != nil {
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						s.LoggerWarn("relation graph projection loop panic: %v", recovered)
					}
				}()
				s.deps.MaintenanceReconcile()
			}()
		}
		if s.deps.ProcessStorageCleanup != nil {
			result, err := s.deps.ProcessStorageCleanup(ctx, 100)
			if err != nil {
				s.LoggerWarn("storage cleanup loop 异常，将在下一轮重试: %v", err)
			} else if intOr(result["failed"], 0, math.MinInt) > 0 {
				s.LoggerWarn("storage cleanup 本轮失败任务: %v", result["failed"])
			}
		}
	}
	return nil
}

// TrainRuntimeVectorIndexesOnce 对齐 _train_runtime_vector_indexes_once
func (s *BackgroundTaskService) TrainRuntimeVectorIndexesOnce(ctx context.Context) (map[string]map[string]any, error) {
	thresholdValue := s.cfg("embedding.runtime_train_threshold", 256)
	runtimeThreshold := intOr(thresholdValue, 256, 1)
	summaries := map[string]map[string]any{}
	if s.stopping() || (s.deps.VectorPersistBlockedUntilRebuild != nil && s.deps.VectorPersistBlockedUntilRebuild()) {
		return summaries, nil
	}
	if s.deps.VectorRebuildLocked != nil && s.deps.VectorRebuildLocked() {
		return summaries, nil
	}
	if s.deps.LockVectorRebuild != nil {
		unlock, ok := s.deps.LockVectorRebuild()
		if !ok {
			return summaries, nil
		}
		if unlock != nil {
			defer unlock()
		}
	}
	if s.deps.VectorStores == nil {
		return summaries, nil
	}
	for name, store := range s.deps.VectorStores(ctx) {
		if s.stopping() {
			break
		}
		if store == nil {
			continue
		}
		if !store.NeedsTraining(runtimeThreshold) {
			continue
		}
		summary := store.WarmupIndex(true)
		summaries[name] = summary
		if pyTruthy(summary["trained"]) {
			if s.deps.SaveVectorStore != nil {
				s.deps.SaveVectorStore(ctx, store)
			}
			s.Logger().Info(fmt.Sprintf("运行期向量索引训练完成: pool=%s, vectors=%v, duration_ms=%.2f", name, summary["bin_count"], spaceFloatAny(summary["duration_ms"])))
		} else {
			s.LoggerWarn("运行期向量索引训练未完成: pool=%s, ok=%v, error=%v", name, summary["ok"], firstNonEmptyString(asString(summary["error"]), "index_not_trained"))
		}
	}
	return summaries, nil
}

// VectorIndexTrainingLoop 对齐 _vector_index_training_loop
func (s *BackgroundTaskService) VectorIndexTrainingLoop(ctx context.Context) error {
	for !s.stopping() {
		if err := s.SleepBackground(ctx, 60.0); err != nil {
			return err
		}
		if s.stopping() {
			break
		}
		if _, err := s.TrainRuntimeVectorIndexesOnce(ctx); err != nil {
			s.LoggerWarn("运行期向量索引训练异常，将在下一周期重试: %v", err)
		}
	}
	return nil
}

// EpisodeMaterializationLoop 对齐 _episode_materialization_loop
func (s *BackgroundTaskService) EpisodeMaterializationLoop(ctx context.Context) error {
	for !s.stopping() {
		interval := mathMax(0.1, pyFloatOr(s.cfg("episode.source_poll_interval_seconds", 1.0), 1.0))
		if err := s.SleepBackground(ctx, interval); err != nil {
			return err
		}
		if s.stopping() {
			break
		}
		if !pyTruthy(s.cfg("episode.enabled", true)) || !pyTruthy(s.cfg("episode.generation_enabled", true)) {
			continue
		}
		if s.deps.ProcessEpisodeSourceRebuildBatch == nil {
			continue
		}
		if _, err := s.deps.ProcessEpisodeSourceRebuildBatch(ctx, map[string]any{
			"limit":            maxInt(1, pyIntOr(s.cfg("episode.source_batch_size", 20), 20, 1)),
			"max_retry":        intOr(s.cfg("episode.source_max_retry", 3), 3, math.MinInt),
			"lease_seconds":    mathMax(1.0, pyFloatOr(s.cfg("episode.source_lease_seconds", 1800.0), 1800.0)),
			"max_wait_seconds": mathMax(0.0, pyFloatOr(s.cfg("episode.source_max_wait_seconds", 60.0), 0.0)),
		}); err != nil {
			s.LoggerWarn("Episode 来源物化循环异常，将在下一周期重试: %v", err)
		}
	}
	return nil
}

// EmbeddingProbeLoop 对齐 _embedding_probe_loop
func (s *BackgroundTaskService) EmbeddingProbeLoop(ctx context.Context) error {
	for !s.stopping() {
		shouldProbe := s.deps.EmbeddingManagerPresent == nil || s.deps.EmbeddingManagerPresent()
		if shouldProbe && s.deps.RecoverEmbeddingOnce != nil {
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						s.LoggerWarn("embedding 恢复探测 panic: %v", recovered)
					}
				}()
				s.deps.RecoverEmbeddingOnce(ctx)
			}()
		}
		if s.stopping() {
			break
		}
		interval := 180.0
		if s.deps.EmbeddingProbeInterval != nil {
			interval = s.deps.EmbeddingProbeInterval()
		}
		if err := s.SleepBackground(ctx, interval); err != nil {
			return err
		}
	}
	return nil
}

// ParagraphVectorBackfillLoop 对齐 _paragraph_vector_backfill_loop
func (s *BackgroundTaskService) ParagraphVectorBackfillLoop(ctx context.Context) error {
	for !s.stopping() {
		interval := 60.0
		if s.deps.ParagraphBackfillIntervalSeconds != nil {
			interval = s.deps.ParagraphBackfillIntervalSeconds()
		}
		if err := s.SleepBackground(ctx, interval); err != nil {
			return err
		}
		if s.stopping() {
			break
		}
		if s.deps.ParagraphBackfillEnabled != nil && !s.deps.ParagraphBackfillEnabled() {
			continue
		}
		if s.deps.IsEmbeddingDegraded != nil && s.deps.IsEmbeddingDegraded() {
			continue
		}
		if s.deps.RunParagraphBackfillOnce == nil {
			continue
		}
		limit := 64
		if s.deps.ParagraphBackfillBatchSize != nil {
			limit = s.deps.ParagraphBackfillBatchSize()
		}
		maxRetry := 5
		if s.deps.ParagraphBackfillMaxRetry != nil {
			maxRetry = s.deps.ParagraphBackfillMaxRetry()
		}
		s.deps.RunParagraphBackfillOnce(ctx, limit, maxRetry, "loop")
	}
	return nil
}

// legacyVectorCopyLoop 对齐 _legacy_vector_copy_loop
func (s *BackgroundTaskService) legacyVectorCopyLoop(ctx context.Context) error {
	for !s.stopping() && s.deps.HasLegacyVectorView != nil && s.deps.HasLegacyVectorView() {
		if s.deps.CopyLegacyVectorsOnce == nil {
			return nil
		}
		// 对齐 Python async with self._vector_rebuild_lock
		var unlock func()
		if s.deps.LockVectorRebuild != nil {
			var ok bool
			unlock, ok = s.deps.LockVectorRebuild()
			if !ok {
				if err := s.SleepBackground(ctx, 0.1); err != nil {
					return err
				}
				continue
			}
		}
		result, err := s.deps.CopyLegacyVectorsOnce(ctx, 256)
		if unlock != nil {
			unlock()
		}
		if err != nil {
			s.LoggerWarn("可信旧向量后台复制失败，将在下次启动续做: %v", err)
			return nil
		}
		if pyTruthy(result["done"]) {
			if s.deps.CleanupVectorQuarantine != nil {
				s.deps.CleanupVectorQuarantine()
			}
			return nil
		}
		if err := s.SleepBackground(ctx, 0.1); err != nil {
			return err
		}
	}
	return nil
}

// PersonProfileRefreshLoop 对齐 _person_profile_refresh_loop
func (s *BackgroundTaskService) PersonProfileRefreshLoop(ctx context.Context) error {
	for !s.stopping() {
		intervalMinutes := mathMax(1.0, pyFloatOr(s.cfg("person_profile.refresh_interval_minutes", 30), 30))
		if err := s.SleepBackground(ctx, mathMax(60.0, intervalMinutes*60.0)); err != nil {
			return err
		}
		if s.stopping() {
			break
		}
		if !pyTruthy(s.cfg("person_profile.enabled", true)) {
			continue
		}
		activeWindowHours := mathMax(1.0, pyFloatOr(s.cfg("person_profile.active_window_hours", 72.0), 72.0))
		maxRefresh := maxInt(1, pyIntOr(s.cfg("person_profile.max_refresh_per_cycle", 50), 50, 1))
		cutoff := float64(time.Now().UnixNano())/1e9 - activeWindowHours*3600.0
		candidates := make([]string, 0)
		if s.deps.ActivePersonTimestamps != nil {
			type entry struct {
				id     string
				seenAt float64
			}
			entries := make([]entry, 0)
			for personID, seenAt := range s.deps.ActivePersonTimestamps() {
				if seenAt >= cutoff {
					entries = append(entries, entry{id: personID, seenAt: seenAt})
				}
			}
			for i := 0; i < len(entries); i++ {
				for j := i + 1; j < len(entries); j++ {
					if entries[j].seenAt > entries[i].seenAt {
						entries[i], entries[j] = entries[j], entries[i]
					}
				}
			}
			for _, item := range entries {
				if len(candidates) >= maxRefresh {
					break
				}
				candidates = append(candidates, item.id)
			}
		}
		for _, personID := range candidates {
			if s.deps.HasPendingPersonProfileRefresh != nil && s.deps.HasPendingPersonProfileRefresh(personID) {
				continue
			}
			if s.deps.RefreshPersonProfile != nil {
				if _, err := s.deps.RefreshPersonProfile(ctx, personID, maxInt(4, pyIntOr(s.cfg("person_profile.top_k_evidence", 12), 12, 1))); err != nil {
					s.LoggerWarn("刷新人物画像失败: %v", err)
				}
			}
		}
	}
	return nil
}

// PersonProfileRefreshQueueLoop 对齐 _person_profile_refresh_queue_loop
func (s *BackgroundTaskService) PersonProfileRefreshQueueLoop(ctx context.Context) error {
	for !s.stopping() {
		interval := 60.0
		if s.deps.ProfileRefreshQueueInterval != nil {
			interval = s.deps.ProfileRefreshQueueInterval()
		}
		if err := s.SleepBackground(ctx, interval); err != nil {
			return err
		}
		if s.stopping() {
			break
		}
		if !pyTruthy(s.cfg("person_profile.enabled", true)) {
			continue
		}
		if s.deps.ProcessPersonProfileRefreshQueue == nil {
			continue
		}
		limit := 10
		if s.deps.ProfileRefreshQueueBatchSize != nil {
			limit = s.deps.ProfileRefreshQueueBatchSize()
		}
		if _, err := s.deps.ProcessPersonProfileRefreshQueue(ctx, limit); err != nil {
			s.LoggerWarn("person_profile_refresh_queue loop 异常: %v", err)
		}
	}
	return nil
}

// maintenanceLoop 对齐 memory_maintenance_loop 的委托
func (s *BackgroundTaskService) maintenanceLoop(ctx context.Context) error {
	if s.deps.MaintenanceLoop == nil {
		return nil
	}
	return s.deps.MaintenanceLoop(ctx)
}

// DualVectorAutoMigrationLoop 对齐 _dual_vector_auto_migration_loop
func (s *BackgroundTaskService) DualVectorAutoMigrationLoop(ctx context.Context) error {
	if s.deps.ShouldStartDualVectorAutoMigration == nil || !s.deps.ShouldStartDualVectorAutoMigration(ctx) {
		return nil
	}
	now := float64(time.Now().UnixNano()) / 1e9
	s.updateStatus(map[string]any{
		"running": true, "attempted": true, "success": false, "stage": "initial_delay",
		"progress":   s.normalizeProgress(ctx, map[string]any{"total": 0, "processed": 0}, now, true, false),
		"last_error": "", "started_at": now, "finished_at": nil, "updated_at": now,
	})
	delay := 0.0
	if s.deps.DualVectorMigrationInitialDelay != nil {
		delay = s.deps.DualVectorMigrationInitialDelay()
	}
	if err := s.SleepBackground(ctx, delay); err != nil {
		s.finishStatus(ctx, "cancelled", "cancelled", false)
		return err
	}
	poolsEnabled := s.deps.DualVectorPoolsEnabled != nil && s.deps.DualVectorPoolsEnabled(ctx)
	if s.stopping() || poolsEnabled {
		s.finishStatus(ctx, "skipped", "", poolsEnabled)
		return nil
	}
	retryDelays := []float64{0.0}
	if s.deps.DualVectorMigrationLockRetries != nil {
		retryDelays = append(retryDelays, s.deps.DualVectorMigrationLockRetries()...)
	}
	result := map[string]any{}
	for index, wait := range retryDelays {
		poolsNow := s.deps.DualVectorPoolsEnabled != nil && s.deps.DualVectorPoolsEnabled(ctx)
		if s.stopping() || poolsNow {
			break
		}
		if wait > 0 {
			s.stage(ctx, "retry_delay", index, wait)
			if err := s.SleepBackground(ctx, wait); err != nil {
				return err
			}
		}
		if s.deps.VectorRebuildLocked != nil && s.deps.VectorRebuildLocked() {
			s.stage(ctx, "waiting_rebuild_lock", index, 0)
			if index == len(retryDelays)-1 {
				result = map[string]any{"success": false, "error": "vector_rebuild_running", "detail": "已有向量重建任务正在运行"}
			}
			continue
		}
		s.stage(ctx, "rebuild_start", index, 0)
		if s.deps.RebuildAllVectors != nil {
			value, err := s.deps.RebuildAllVectors(ctx)
			if err != nil {
				// 对齐 Python except Exception：写 stage=exception 后继续使用单池
				s.LoggerWarn("双池后台自动迁移异常，继续使用单池: %v", err)
				finished := float64(time.Now().UnixNano()) / 1e9
				progress := map[string]any{}
				if s.deps.AutoMigrationStatus != nil {
					progress = asMap(s.deps.AutoMigrationStatus()["progress"])
				}
				lastError := err.Error()
				if len([]rune(lastError)) > 500 {
					lastError = string([]rune(lastError)[:500])
				}
				s.updateStatus(map[string]any{
					"running": false, "success": false, "stage": "exception",
					"progress":   s.normalizeProgress(ctx, progress, finished, true, false),
					"last_error": lastError, "finished_at": finished, "updated_at": finished,
				})
				return nil
			}
			result = value
		}
		if asString(result["error"]) != "vector_rebuild_running" {
			break
		}
	}
	success := pyTruthy(result["success"]) || (s.deps.DualVectorPoolsEnabled != nil && s.deps.DualVectorPoolsEnabled(ctx))
	lastError := ""
	if !success {
		errorsList := asList(result["errors"])
		if len(errorsList) > 0 {
			parts := make([]string, 0)
			for index, item := range errorsList {
				if index >= 5 {
					break
				}
				parts = append(parts, asString(item))
			}
			lastError = strings.Join(parts, "; ")
		} else {
			lastError = firstNonEmptyString(asString(result["detail"]), asString(result["error"]), "dual_vector_auto_migration_failed")
		}
		s.LoggerWarn("双池后台自动迁移未完成，继续使用单池: %s", lastError)
	} else {
		s.Logger().Info("双池后台自动迁移完成，已切换到双池检索")
	}
	finished := float64(time.Now().UnixNano()) / 1e9
	progress := map[string]any{}
	if s.deps.AutoMigrationStatus != nil {
		for key, value := range asMap(s.deps.AutoMigrationStatus()["progress"]) {
			progress[key] = value
		}
	}
	progress["result"] = result
	stage := "failed"
	if success {
		stage = "completed"
	}
	if len([]rune(lastError)) > 500 {
		lastError = string([]rune(lastError)[:500])
	}
	s.updateStatus(map[string]any{
		"running": false, "success": success, "stage": stage,
		"progress":   s.normalizeProgress(ctx, progress, finished, true, success),
		"last_error": lastError, "finished_at": finished, "updated_at": finished,
	})
	return nil
}

func (s *BackgroundTaskService) stage(ctx context.Context, name string, retryIndex int, delaySeconds float64) {
	if s.deps.UpdateAutoMigrationStage != nil {
		s.deps.UpdateAutoMigrationStage(ctx, name, map[string]any{"retry_index": retryIndex, "delay_seconds": delaySeconds})
	}
}

func (s *BackgroundTaskService) normalizeProgress(ctx context.Context, progress any, now float64, explicitProcessed bool, completed bool) map[string]any {
	if s.deps.NormalizeAutoMigrationProgress == nil {
		return asMap(progress)
	}
	_ = explicitProcessed
	_ = completed
	return s.deps.NormalizeAutoMigrationProgress(ctx, progress)
}

func (s *BackgroundTaskService) updateStatus(update map[string]any) {
	status := map[string]any{}
	if s.deps.AutoMigrationStatus != nil {
		for key, value := range s.deps.AutoMigrationStatus() {
			status[key] = value
		}
	}
	for key, value := range update {
		status[key] = value
	}
	if s.deps.SetAutoMigrationStatus != nil {
		s.deps.SetAutoMigrationStatus(status)
	}
}

func (s *BackgroundTaskService) finishStatus(ctx context.Context, stage string, lastError string, success bool) {
	finished := float64(time.Now().UnixNano()) / 1e9
	progress := map[string]any{}
	if s.deps.AutoMigrationStatus != nil {
		progress = asMap(s.deps.AutoMigrationStatus()["progress"])
	}
	s.updateStatus(map[string]any{
		"running": false, "success": success, "stage": stage,
		"progress":    s.normalizeProgress(ctx, progress, finished, false, true),
		"finished_at": finished, "updated_at": finished, "last_error": lastError,
	})
}

func isContextCanceled(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}
