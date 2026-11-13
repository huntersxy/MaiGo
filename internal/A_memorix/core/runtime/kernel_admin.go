package memruntime

// 内核 admin 入口，对齐 sdk_memory_kernel.py 第 1132-1213 行的 admin 转发

import "context"

// MemoryGraphAdmin 对齐 memory_graph_admin
func (k *Kernel) MemoryGraphAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.MemoryGraphAdmin(ctx, action, payload)
}

// MemorySourceAdmin 对齐 memory_source_admin
func (k *Kernel) MemorySourceAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if k.services.SourceAdmin == nil {
		return nil, nil
	}
	return k.services.SourceAdmin.MemorySourceAdmin(ctx, action, payload)
}

// MemoryEpisodeAdmin 对齐 memory_episode_admin
func (k *Kernel) MemoryEpisodeAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if k.services.EpisodeAdmin == nil {
		return nil, nil
	}
	return k.services.EpisodeAdmin.MemoryEpisodeAdmin(ctx, action, payload)
}

// MemoryProfileAdmin 对齐 memory_profile_admin
func (k *Kernel) MemoryProfileAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if k.services.ProfileAdmin == nil {
		return nil, nil
	}
	return k.services.ProfileAdmin.MemoryProfileAdmin(ctx, action, payload)
}

// MemoryFeedbackAdmin 对齐 memory_feedback_admin
func (k *Kernel) MemoryFeedbackAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.MemoryFeedbackAdmin(ctx, action, payload)
}

// MemoryFactAdmin 对齐 memory_fact_admin
func (k *Kernel) MemoryFactAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if k.services.FactAdmin == nil {
		return nil, nil
	}
	return k.services.FactAdmin.MemoryFactAdmin(ctx, action, payload)
}

// MemoryImportAdmin 对齐 memory_import_admin
func (k *Kernel) MemoryImportAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if k.services.ImportTuningAdmin == nil {
		return nil, nil
	}
	return k.services.ImportTuningAdmin.MemoryImportAdmin(ctx, action, payload)
}

// MemoryBundleAdmin 对齐 memory_bundle_admin
func (k *Kernel) MemoryBundleAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if k.services.BundleAdmin == nil {
		return nil, nil
	}
	return k.services.BundleAdmin.MemoryBundleAdmin(ctx, action, payload)
}

// MemoryTuningAdmin 对齐 memory_tuning_admin
func (k *Kernel) MemoryTuningAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if k.services.ImportTuningAdmin == nil {
		return nil, nil
	}
	return k.services.ImportTuningAdmin.MemoryTuningAdmin(ctx, action, payload)
}

// MemoryV5Admin 对齐 memory_v5_admin
func (k *Kernel) MemoryV5Admin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if k.services.V5Admin == nil {
		return nil, nil
	}
	return k.services.V5Admin.MemoryV5Admin(ctx, action, payload)
}

// MemoryDeleteAdmin 对齐 memory_delete_admin
func (k *Kernel) MemoryDeleteAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.MemoryDeleteAdmin(ctx, action, payload)
}

// MemoryCorrectionAdmin 对齐 memory_correction_admin
func (k *Kernel) MemoryCorrectionAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.MemoryCorrectionAdmin(ctx, action, payload)
}

// MemoryFuzzyModifyAdmin 对齐 memory_fuzzy_modify_admin
func (k *Kernel) MemoryFuzzyModifyAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.MemoryFuzzyModifyAdmin(ctx, action, payload)
}

// ImageMemory 对齐 image_memory
func (k *Kernel) ImageMemory(ctx context.Context, action string, payload map[string]any) (map[string]any, error) {
	if k.services.Image == nil {
		return map[string]any{}, nil
	}
	return k.services.Image.Execute(ctx, action, payload)
}

// ImportTaskManager 对齐 get_import_task_manager
func (k *Kernel) ImportTaskManager() any { return k.importTaskManager }

// SetImportTaskManager 写入导入任务管理器
func (k *Kernel) SetImportTaskManager(manager any) { k.importTaskManager = manager }

// RetrievalTuningManager 对齐 get_retrieval_tuning_manager
func (k *Kernel) RetrievalTuningManager() any { return k.retrievalTuningManager }

// SetRetrievalTuningManager 写入检索调优管理器
func (k *Kernel) SetRetrievalTuningManager(manager any) { k.retrievalTuningManager = manager }

// ImageMemoryRuntime 返回图片记忆运行时
func (k *Kernel) ImageMemoryRuntime() any { return k.imageMemoryRuntime }

// SetImageMemoryRuntime 写入图片记忆运行时
func (k *Kernel) SetImageMemoryRuntime(runtime any) { k.imageMemoryRuntime = runtime }

// AggregateSearch 对齐 _aggregate_search
func (k *Kernel) AggregateSearch(ctx context.Context, query string, limit int, request KernelSearchRequest) (map[string]any, error) {
	if k.services.Search == nil {
		return map[string]any{}, nil
	}
	return k.services.Search.AggregateSearch(ctx, query, limit, request)
}

// AggregateTime 对齐 _aggregate_time
func (k *Kernel) AggregateTime(ctx context.Context, query string, limit int, request KernelSearchRequest, window NormalizedSearchTimeWindow) (map[string]any, error) {
	if k.services.Search == nil {
		return map[string]any{}, nil
	}
	return k.services.Search.AggregateTime(ctx, query, limit, request, window)
}

// AggregateEpisode 对齐 _aggregate_episode
func (k *Kernel) AggregateEpisode(ctx context.Context, query string, limit int, request KernelSearchRequest, window NormalizedSearchTimeWindow) (map[string]any, error) {
	if k.services.Search == nil {
		return map[string]any{}, nil
	}
	return k.services.Search.AggregateEpisode(ctx, query, limit, request, window)
}

// SearchHitService 返回搜索命中处理服务，对齐 _get_search_hit_service
func (k *Kernel) SearchHitService() SearchHitService { return k.services.SearchHit }
