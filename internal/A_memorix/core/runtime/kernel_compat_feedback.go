package memruntime

// 对齐 kernel_compat.py 的反馈纠错转发，逐方法委托给注入的服务

import "context"

// ResolveFeedbackRelatedPersonIDs 对齐 kernel_compat.py 的 ResolveFeedbackRelatedPersonIDs 转发
func (k *Kernel) ResolveFeedbackRelatedPersonIDs(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.ResolveFeedbackRelatedPersonIDs(ctx, payload)
}

// MarkFeedbackStaleParagraphs 对齐 kernel_compat.py 的 MarkFeedbackStaleParagraphs 转发
func (k *Kernel) MarkFeedbackStaleParagraphs(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.MarkFeedbackStaleParagraphs(ctx, payload)
}

// EnqueueFeedbackEpisodeRebuilds 对齐 kernel_compat.py 的 EnqueueFeedbackEpisodeRebuilds 转发
func (k *Kernel) EnqueueFeedbackEpisodeRebuilds(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.EnqueueFeedbackEpisodeRebuilds(ctx, payload)
}

// EnqueueFeedbackProfileRefreshes 对齐 kernel_compat.py 的 EnqueueFeedbackProfileRefreshes 转发
func (k *Kernel) EnqueueFeedbackProfileRefreshes(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.EnqueueFeedbackProfileRefreshes(ctx, payload)
}

// FeedbackAffectedCounts 对齐 kernel_compat.py 的 FeedbackAffectedCounts 转发
func (k *Kernel) FeedbackAffectedCounts(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.FeedbackAffectedCounts(ctx, payload)
}

// BuildFeedbackRollbackPlanSummary 对齐 kernel_compat.py 的 BuildFeedbackRollbackPlanSummary 转发
func (k *Kernel) BuildFeedbackRollbackPlanSummary(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.BuildFeedbackRollbackPlanSummary(ctx, payload)
}

// BuildFeedbackTaskSummary 对齐 kernel_compat.py 的 BuildFeedbackTaskSummary 转发
func (k *Kernel) BuildFeedbackTaskSummary(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.BuildFeedbackTaskSummary(ctx, payload)
}

// BuildFeedbackTaskDetail 对齐 kernel_compat.py 的 BuildFeedbackTaskDetail 转发
func (k *Kernel) BuildFeedbackTaskDetail(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.BuildFeedbackTaskDetail(ctx, payload)
}

// SoftDeleteFeedbackCorrectionParagraphs 对齐 kernel_compat.py 的 SoftDeleteFeedbackCorrectionParagraphs 转发
func (k *Kernel) SoftDeleteFeedbackCorrectionParagraphs(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.SoftDeleteFeedbackCorrectionParagraphs(ctx, payload)
}

// ExtractFeedbackMessages 对齐 kernel_compat.py 的 ExtractFeedbackMessages 转发
func (k *Kernel) ExtractFeedbackMessages(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.ExtractFeedbackMessages(ctx, payload)
}

// BuildFeedbackHitBriefs 对齐 kernel_compat.py 的 BuildFeedbackHitBriefs 转发
func (k *Kernel) BuildFeedbackHitBriefs(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.BuildFeedbackHitBriefs(ctx, payload)
}

// ShouldInvokeFeedbackClassifier 对齐 kernel_compat.py 的 ShouldInvokeFeedbackClassifier 转发
func (k *Kernel) ShouldInvokeFeedbackClassifier(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.ShouldInvokeFeedbackClassifier(ctx, payload)
}

// NormalizeFeedbackDecision 对齐 kernel_compat.py 的 NormalizeFeedbackDecision 转发
func (k *Kernel) NormalizeFeedbackDecision(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.NormalizeFeedbackDecision(ctx, payload)
}

// FeedbackApplyResultStatus 对齐 kernel_compat.py 的 FeedbackApplyResultStatus 转发
func (k *Kernel) FeedbackApplyResultStatus(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.FeedbackApplyResultStatus(ctx, payload)
}

// RestoreFeedbackRelationsFromSnapshots 对齐 kernel_compat.py 的 RestoreFeedbackRelationsFromSnapshots 转发
func (k *Kernel) RestoreFeedbackRelationsFromSnapshots(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.RestoreFeedbackRelationsFromSnapshots(ctx, payload)
}

// ResolveFeedbackRelationHashes 对齐 kernel_compat.py 的 ResolveFeedbackRelationHashes 转发
func (k *Kernel) ResolveFeedbackRelationHashes(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.ResolveFeedbackRelationHashes(ctx, payload)
}

// RollbackFeedbackTask 对齐 kernel_compat.py 的 RollbackFeedbackTask 转发
func (k *Kernel) RollbackFeedbackTask(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.RollbackFeedbackTask(ctx, payload)
}

// ProcessFeedbackProfileRefreshBatch 对齐 kernel_compat.py 的 ProcessFeedbackProfileRefreshBatch 转发
func (k *Kernel) ProcessFeedbackProfileRefreshBatch(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.ProcessFeedbackProfileRefreshBatch(ctx, payload)
}

// ProcessFeedbackEpisodeRebuildBatch 对齐 kernel_compat.py 的 ProcessFeedbackEpisodeRebuildBatch 转发
func (k *Kernel) ProcessFeedbackEpisodeRebuildBatch(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.ProcessFeedbackEpisodeRebuildBatch(ctx, payload)
}

// FeedbackCorrectionReconcileLoop 对齐 kernel_compat.py 的 FeedbackCorrectionReconcileLoop 转发
func (k *Kernel) FeedbackCorrectionReconcileLoop(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.FeedbackCorrectionReconcileLoop(ctx, payload)
}

// EnqueueFeedbackTask 对齐 kernel_compat.py 的 EnqueueFeedbackTask 转发
func (k *Kernel) EnqueueFeedbackTask(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.EnqueueFeedbackTask(ctx, payload)
}

// ClassifyFeedback 对齐 kernel_compat.py 的 ClassifyFeedback 转发
func (k *Kernel) ClassifyFeedback(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.ClassifyFeedback(ctx, payload)
}

// IngestFeedbackRelations 对齐 kernel_compat.py 的 IngestFeedbackRelations 转发
func (k *Kernel) IngestFeedbackRelations(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.IngestFeedbackRelations(ctx, payload)
}

// ApplyFeedbackDecision 对齐 kernel_compat.py 的 ApplyFeedbackDecision 转发
func (k *Kernel) ApplyFeedbackDecision(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.ApplyFeedbackDecision(ctx, payload)
}

// ProcessFeedbackTask 对齐 kernel_compat.py 的 ProcessFeedbackTask 转发
func (k *Kernel) ProcessFeedbackTask(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.ProcessFeedbackTask(ctx, payload)
}

// FeedbackCorrectionLoop 对齐 kernel_compat.py 的 FeedbackCorrectionLoop 转发
func (k *Kernel) FeedbackCorrectionLoop(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.Feedback == nil {
		return nil, nil
	}
	return k.services.Feedback.FeedbackCorrectionLoop(ctx, payload)
}
