package memruntime

// 内核对外运行时入口，对齐 sdk_memory_kernel.py 第 874-1095 行的服务转发

import (
	"context"
	"fmt"
	"strings"

	"maibot-go/internal/A_memorix/core/utils"
)

// Initialize 对齐 initialize
func (k *Kernel) Initialize(ctx context.Context) error {
	if k.services.RuntimeLifecycle == nil {
		return fmt.Errorf("runtime_lifecycle_service 未注入")
	}
	return k.services.RuntimeLifecycle.Initialize(ctx)
}

// Shutdown 对齐 shutdown
func (k *Kernel) Shutdown(ctx context.Context) error {
	if k.services.RuntimeLifecycle == nil {
		return fmt.Errorf("runtime_lifecycle_service 未注入")
	}
	return k.services.RuntimeLifecycle.Shutdown(ctx)
}

// Close 对齐 close
func (k *Kernel) Close() {
	if k.services.RuntimeLifecycle == nil {
		return
	}
	k.services.RuntimeLifecycle.Close()
}

// ExecuteRequestWithDedup 对齐 execute_request_with_dedup
func (k *Kernel) ExecuteRequestWithDedup(ctx context.Context, requestKey string, executor func() (map[string]any, error)) (bool, map[string]any, error) {
	if k.services.RequestDedup == nil {
		result, err := executor()
		return false, result, err
	}
	return k.services.RequestDedup.ExecuteRequestWithDedup(ctx, requestKey, executor)
}

// SummarizeChatStream 对齐 summarize_chat_stream
func (k *Kernel) SummarizeChatStream(ctx context.Context, params SummarizeChatStreamParams) (map[string]any, error) {
	if k.services.Summary == nil {
		return map[string]any{}, nil
	}
	return k.services.Summary.SummarizeChatStream(ctx, params)
}

// IngestSummary 对齐 ingest_summary
func (k *Kernel) IngestSummary(ctx context.Context, params IngestSummaryParams) (map[string]any, error) {
	if k.services.Ingest == nil {
		return map[string]any{}, nil
	}
	params.RespectFilter = boolPtrValue(respectFilterOrDefault(params.RespectFilter))
	return k.services.Ingest.IngestSummary(ctx, params)
}

// IngestText 对齐 ingest_text
func (k *Kernel) IngestText(ctx context.Context, params IngestTextParams) (map[string]any, error) {
	if k.services.Ingest == nil {
		return map[string]any{}, nil
	}
	params.RespectFilter = boolPtrValue(respectFilterOrDefault(params.RespectFilter))
	return k.services.Ingest.IngestText(ctx, params)
}

// ProcessEpisodeSourceRebuildBatch 对齐 process_episode_source_rebuild_batch
func (k *Kernel) ProcessEpisodeSourceRebuildBatch(ctx context.Context, params map[string]any) (map[string]any, error) {
	if k.services.Ingest == nil {
		return map[string]any{}, nil
	}
	return k.services.Ingest.ProcessEpisodeSourceRebuildBatch(ctx, params)
}

// WriteParagraphVectorOrEnqueue 对齐 write_paragraph_vector_or_enqueue
func (k *Kernel) WriteParagraphVectorOrEnqueue(ctx context.Context, paragraphHash string, content string, contextText string) (map[string]any, error) {
	if k.services.Ingest == nil {
		return map[string]any{}, nil
	}
	return k.services.Ingest.WriteParagraphVectorOrEnqueue(ctx, paragraphHash, content, contextText)
}

// SearchMemory 对齐 search_memory
func (k *Kernel) SearchMemory(ctx context.Context, request KernelSearchRequest) (map[string]any, error) {
	if k.services.Search == nil {
		return map[string]any{}, nil
	}
	request.RespectFilter = boolPtrValue(request.RespectFilterEnabled())
	return k.services.Search.SearchMemory(ctx, request)
}

// EmptyPersonProfileResponse 对齐 _empty_person_profile_response
func EmptyPersonProfileResponse(personID string, personName string) map[string]any {
	return map[string]any{
		"summary":             "",
		"traits":              []any{},
		"evidence":            []any{},
		"person_id":           strings.TrimSpace(personID),
		"person_name":         strings.TrimSpace(personName),
		"profile_source":      "",
		"has_manual_override": false,
	}
}

// QueryPersonProfileWithFeedbackRefresh 对齐 _query_person_profile_with_feedback_refresh
func (k *Kernel) QueryPersonProfileWithFeedbackRefresh(ctx context.Context, personID string, chatID string, limit int) (map[string]any, error) {
	if k.services.ProfileAdmin == nil {
		return map[string]any{}, nil
	}
	return k.services.ProfileAdmin.QueryPersonProfileWithFeedbackRefresh(ctx, personID, chatID, limit)
}

// BuildPersonProfileResponse 对齐 _build_person_profile_response
func (k *Kernel) BuildPersonProfileResponse(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if k.services.ProfileAdmin == nil {
		return map[string]any{}, nil
	}
	return k.services.ProfileAdmin.BuildPersonProfileResponse(ctx, payload)
}

// GetPersonProfile 对齐 get_person_profile
func (k *Kernel) GetPersonProfile(ctx context.Context, personID string, chatID string, limit int) (map[string]any, error) {
	if k.services.ProfileAdmin == nil {
		return EmptyPersonProfileResponse(personID, ""), nil
	}
	return k.services.ProfileAdmin.GetPersonProfile(ctx, personID, chatID, limit)
}

// RefreshPersonProfile 对齐 refresh_person_profile
// limit<=0 时按 Python 默认 10 处理
func (k *Kernel) RefreshPersonProfile(ctx context.Context, personID string, limit int, markActive bool) (map[string]any, error) {
	if k.services.ProfileAdmin == nil {
		return EmptyPersonProfileResponse(personID, ""), nil
	}
	if limit <= 0 {
		limit = 10
	}
	payload := map[string]any{"person_id": personID, "limit": limit, "mark_active": markActive}
	return k.services.ProfileAdmin.RefreshPersonProfile(ctx, payload)
}

// MaintainMemory 对齐 maintain_memory
func (k *Kernel) MaintainMemory(ctx context.Context, action string, target string, hours *float64, reason string, limit int) (map[string]any, error) {
	if k.services.V5Admin == nil {
		return map[string]any{}, nil
	}
	return k.services.V5Admin.MaintainMemory(ctx, action, target, hours, reason, limit)
}

// RebuildEpisodesForSources 对齐 rebuild_episodes_for_sources
func (k *Kernel) RebuildEpisodesForSources(ctx context.Context, sources []string) (map[string]any, error) {
	if k.services.EpisodeAdmin == nil {
		return map[string]any{}, nil
	}
	return k.services.EpisodeAdmin.RebuildEpisodesForSources(ctx, sources)
}

// MemoryStats 对齐 memory_stats
func (k *Kernel) MemoryStats() (map[string]any, error) {
	if k.services.Stats == nil {
		return map[string]any{}, nil
	}
	return k.services.Stats.MemoryStats(k.context())
}

// StartBackgroundTasks 对齐 start_background_tasks
func (k *Kernel) StartBackgroundTasks(ctx context.Context) error {
	if k.services.BackgroundTask == nil {
		return nil
	}
	return k.services.BackgroundTask.StartBackgroundTasks(ctx)
}

// EnsureBackgroundTask 对齐 _ensure_background_task
func (k *Kernel) EnsureBackgroundTask(ctx context.Context, name string, factory func(ctx context.Context) error) error {
	if k.services.BackgroundTask == nil {
		return nil
	}
	return k.services.BackgroundTask.EnsureBackgroundTask(ctx, name, factory)
}

// StopBackgroundTasks 对齐 stop_background_tasks
func (k *Kernel) StopBackgroundTasks(ctx context.Context) error {
	if k.services.BackgroundTask == nil {
		return nil
	}
	return k.services.BackgroundTask.StopBackgroundTasks(ctx)
}

// CancelAllBackgroundTasks 强制取消全部后台任务，供初始化失败兜底
func (k *Kernel) CancelAllBackgroundTasks() {
	if k.services.BackgroundTask == nil {
		return
	}
	k.services.BackgroundTask.CancelAllBackgroundTasks()
}

// SleepBackground 对齐 _sleep_background
func (k *Kernel) SleepBackground(ctx context.Context, seconds float64) error {
	if k.services.BackgroundTask == nil {
		return nil
	}
	return k.services.BackgroundTask.SleepBackground(ctx, seconds)
}

// DualVectorAutoMigrationLoop 对齐 _dual_vector_auto_migration_loop
func (k *Kernel) DualVectorAutoMigrationLoop(ctx context.Context) error {
	if k.services.BackgroundTask == nil {
		return nil
	}
	return k.services.BackgroundTask.DualVectorAutoMigrationLoop(ctx)
}

// AutoSaveLoop 对齐 _auto_save_loop
func (k *Kernel) AutoSaveLoop(ctx context.Context) error {
	if k.services.BackgroundTask == nil {
		return nil
	}
	return k.services.BackgroundTask.AutoSaveLoop(ctx)
}

// VectorIndexTrainingLoop 对齐 _vector_index_training_loop
func (k *Kernel) VectorIndexTrainingLoop(ctx context.Context) error {
	if k.services.BackgroundTask == nil {
		return nil
	}
	return k.services.BackgroundTask.VectorIndexTrainingLoop(ctx)
}

// TrainRuntimeVectorIndexesOnce 对齐 _train_runtime_vector_indexes_once
func (k *Kernel) TrainRuntimeVectorIndexesOnce(ctx context.Context) (map[string]map[string]any, error) {
	if k.services.BackgroundTask == nil {
		return map[string]map[string]any{}, nil
	}
	return k.services.BackgroundTask.TrainRuntimeVectorIndexesOnce(ctx)
}

// EpisodeMaterializationLoop 对齐 _episode_materialization_loop
func (k *Kernel) EpisodeMaterializationLoop(ctx context.Context) error {
	if k.services.BackgroundTask == nil {
		return nil
	}
	return k.services.BackgroundTask.EpisodeMaterializationLoop(ctx)
}

// EmbeddingProbeLoop 对齐 _embedding_probe_loop
func (k *Kernel) EmbeddingProbeLoop(ctx context.Context) error {
	if k.services.BackgroundTask == nil {
		return nil
	}
	return k.services.BackgroundTask.EmbeddingProbeLoop(ctx)
}

// ParagraphVectorBackfillLoop 对齐 _paragraph_vector_backfill_loop
func (k *Kernel) ParagraphVectorBackfillLoop(ctx context.Context) error {
	if k.services.BackgroundTask == nil {
		return nil
	}
	return k.services.BackgroundTask.ParagraphVectorBackfillLoop(ctx)
}

// PersonProfileRefreshLoop 对齐 _person_profile_refresh_loop
func (k *Kernel) PersonProfileRefreshLoop(ctx context.Context) error {
	if k.services.BackgroundTask == nil {
		return nil
	}
	return k.services.BackgroundTask.PersonProfileRefreshLoop(ctx)
}

// PersonProfileRefreshQueueLoop 对齐 _person_profile_refresh_queue_loop
func (k *Kernel) PersonProfileRefreshQueueLoop(ctx context.Context) error {
	if k.services.BackgroundTask == nil {
		return nil
	}
	return k.services.BackgroundTask.PersonProfileRefreshQueueLoop(ctx)
}

// MemoryMaintenanceLoop 对齐 _memory_maintenance_loop
func (k *Kernel) MemoryMaintenanceLoop(ctx context.Context) error {
	if k.services.Maintenance == nil {
		return nil
	}
	return k.services.Maintenance.MemoryMaintenanceLoop(ctx)
}

// RunMemoryMaintenanceCycle 对齐 _run_memory_maintenance_cycle
func (k *Kernel) RunMemoryMaintenanceCycle(ctx context.Context, intervalHours float64) error {
	if k.services.Maintenance == nil {
		return nil
	}
	return k.services.Maintenance.RunMemoryMaintenanceCycle(ctx, intervalHours)
}

// ProcessFreezeAndPrune 对齐 process_freeze_and_prune
func (k *Kernel) ProcessFreezeAndPrune(ctx context.Context) error {
	if k.services.Maintenance == nil {
		return nil
	}
	return k.services.Maintenance.ProcessFreezeAndPrune(ctx)
}

// OrphanGCPhase 对齐 _orphan_gc_phase
func (k *Kernel) OrphanGCPhase(ctx context.Context) error {
	if k.services.Maintenance == nil {
		return nil
	}
	return k.services.Maintenance.OrphanGCPhase(ctx)
}

// ApplyRelationLifecycleEvent 对齐 apply_relation_lifecycle_event
func (k *Kernel) ApplyRelationLifecycleEvent(ctx context.Context, hashes []string, event utils.RelationLifecycleEvent) error {
	if k.services.Maintenance == nil {
		return nil
	}
	return k.services.Maintenance.ApplyRelationLifecycleEvent(ctx, hashes, event)
}

// RefreshRelationWriteService 对齐 refresh_relation_write_service
func (k *Kernel) RefreshRelationWriteService(ctx context.Context) {
	if k.services.RuntimeDependency == nil {
		return
	}
	k.services.RuntimeDependency.RefreshRelationWriteService(ctx)
}

// Persist 对齐 persist
func (k *Kernel) Persist(ctx context.Context, forceVectors bool) error {
	if k.services.RuntimeDependency == nil {
		return nil
	}
	return k.services.RuntimeDependency.Persist(ctx, forceVectors)
}

// IsChatEnabled 对齐 is_chat_enabled
func (k *Kernel) IsChatEnabled(ctx context.Context, streamID string, groupID string, userID string) bool {
	if k.services.ChatFilter == nil {
		return false
	}
	return k.services.ChatFilter.IsChatEnabled(ctx, streamID, groupID, userID)
}

// ChatFilterConfigAllows 对齐 _chat_filter_config_allows
func (k *Kernel) ChatFilterConfigAllows(ctx context.Context, filterConfig map[string]any, streamID string, groupID string, userID string, defaultWhenEmpty bool) bool {
	if k.services.ChatFilter == nil {
		return defaultWhenEmpty
	}
	return k.services.ChatFilter.ChatFilterConfigAllows(ctx, filterConfig, streamID, groupID, userID, defaultWhenEmpty)
}

// IsChatFiltered 对齐 _is_chat_filtered
func (k *Kernel) IsChatFiltered(ctx context.Context, respectFilter bool, streamID string, groupID string, userID string) bool {
	if k.services.ChatFilter == nil {
		return false
	}
	return k.services.ChatFilter.IsChatFiltered(ctx, respectFilter, streamID, groupID, userID)
}

// BuildRuntimeConfig 对齐 _build_runtime_config
func (k *Kernel) BuildRuntimeConfig(ctx context.Context, baseConfig map[string]any) (map[string]any, error) {
	if k.services.RuntimeConfig == nil {
		return map[string]any{}, nil
	}
	return k.services.RuntimeConfig.BuildRuntimeConfig(ctx, baseConfig)
}

// ApplyRetrievalTuningProfile 对齐 apply_retrieval_tuning_profile
func (k *Kernel) ApplyRetrievalTuningProfile(ctx context.Context, profile map[string]any, validate bool) (map[string]any, error) {
	if k.services.RuntimeConfig == nil {
		return map[string]any{}, nil
	}
	return k.services.RuntimeConfig.ApplyRetrievalTuningProfile(ctx, profile, validate)
}

// MarkPersonActive 对齐 _mark_person_active
func (k *Kernel) MarkPersonActive(personID string) {
	if k.services.ProfileAdmin == nil {
		return
	}
	k.services.ProfileAdmin.MarkPersonActive(k.context(), personID)
}

// EnqueuePersonProfileRefresh 对齐 _enqueue_person_profile_refresh
func (k *Kernel) EnqueuePersonProfileRefresh(personID string, reason string) bool {
	if k.services.ProfileAdmin == nil {
		return false
	}
	return k.services.ProfileAdmin.EnqueuePersonProfileRefresh(k.context(), personID, reason)
}

// EffectiveFilterCache 返回共享的有效过滤缓存
func (k *Kernel) EffectiveFilterCache() map[string]any {
	if k == nil {
		return nil
	}
	if k.effectiveFilterCache == nil {
		k.effectiveFilterCache = map[string]any{"checked_at": 0.0, "needed": false}
	}
	return k.effectiveFilterCache
}

// InvalidateEffectiveFilterCache 对齐 correction 成功后强制 needed=True
func (k *Kernel) InvalidateEffectiveFilterCache() {
	cache := k.EffectiveFilterCache()
	if cache == nil {
		return
	}
	cache["checked_at"] = 0.0
	cache["needed"] = true
}

// HasPendingPersonProfileRefresh 对齐 _has_pending_person_profile_refresh
func (k *Kernel) HasPendingPersonProfileRefresh(personID string) bool {
	if k.services.ProfileAdmin == nil {
		return false
	}
	return k.services.ProfileAdmin.HasPendingPersonProfileRefresh(k.context(), personID)
}

// ProcessPersonProfileRefreshQueueBatch 对齐 _process_person_profile_refresh_queue_batch：
// Python 转发到 feedback 服务的批次处理，不是 ProfileAdmin 自身
func (k *Kernel) ProcessPersonProfileRefreshQueueBatch(limit int) (map[string]any, error) {
	if k.services.Feedback == nil {
		return map[string]any{}, nil
	}
	result, err := k.services.Feedback.ProcessFeedbackProfileRefreshBatch(k.context(), map[string]any{"limit": limit})
	if err != nil {
		return nil, err
	}
	if mapped, ok := result.(map[string]any); ok {
		return mapped, nil
	}
	return map[string]any{}, nil
}

// boolPtrValue 取地址，供内核门面把归一后的 bool 回填到指针字段
func boolPtrValue(value bool) *bool {
	return &value
}
