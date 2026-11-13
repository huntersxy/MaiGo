package memruntime

// 对齐 kernel_compat.py 的纠错与模糊修改转发，逐方法委托给注入的服务

import "context"

// IsFuzzyModifyCandidateMutable 对齐 kernel_compat.py 的 IsFuzzyModifyCandidateMutable 转发
func (k *Kernel) IsFuzzyModifyCandidateMutable(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.IsFuzzyModifyCandidateMutable(ctx, payload)
}

// NormalizeFuzzyModifyPlan 对齐 kernel_compat.py 的 NormalizeFuzzyModifyPlan 转发
func (k *Kernel) NormalizeFuzzyModifyPlan(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.NormalizeFuzzyModifyPlan(ctx, payload)
}

// NormalizeFuzzyModifyCandidate 对齐 kernel_compat.py 的 NormalizeFuzzyModifyCandidate 转发
func (k *Kernel) NormalizeFuzzyModifyCandidate(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.NormalizeFuzzyModifyCandidate(ctx, payload)
}

// NormalizeFuzzyModifyRelations 对齐 kernel_compat.py 的 NormalizeFuzzyModifyRelations 转发
func (k *Kernel) NormalizeFuzzyModifyRelations(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.NormalizeFuzzyModifyRelations(ctx, payload)
}

// BuildFuzzyModifyCascadePreview 对齐 kernel_compat.py 的 BuildFuzzyModifyCascadePreview 转发
func (k *Kernel) BuildFuzzyModifyCascadePreview(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.BuildFuzzyModifyCascadePreview(ctx, payload)
}

// BuildFuzzyModifyParagraphCascade 对齐 kernel_compat.py 的 BuildFuzzyModifyParagraphCascade 转发
func (k *Kernel) BuildFuzzyModifyParagraphCascade(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.BuildFuzzyModifyParagraphCascade(ctx, payload)
}

// FuzzyModifyStaleSourceOperationID 对齐 kernel_compat.py 的 FuzzyModifyStaleSourceOperationID 转发
func (k *Kernel) FuzzyModifyStaleSourceOperationID(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.FuzzyModifyStaleSourceOperationID(ctx, payload)
}

// ExecuteFuzzyModifyParagraphCascade 对齐 kernel_compat.py 的 ExecuteFuzzyModifyParagraphCascade 转发
func (k *Kernel) ExecuteFuzzyModifyParagraphCascade(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.ExecuteFuzzyModifyParagraphCascade(ctx, payload)
}

// MarkFuzzyModifyTargetSuperseded 对齐 kernel_compat.py 的 MarkFuzzyModifyTargetSuperseded 转发
func (k *Kernel) MarkFuzzyModifyTargetSuperseded(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.MarkFuzzyModifyTargetSuperseded(ctx, payload)
}

// NormalizeFuzzyModifyScope 对齐 kernel_compat.py 的 NormalizeFuzzyModifyScope 转发
func (k *Kernel) NormalizeFuzzyModifyScope(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.NormalizeFuzzyModifyScope(ctx, payload)
}

// PreviewFuzzyModifyAction 对齐 kernel_compat.py 的 PreviewFuzzyModifyAction 转发
func (k *Kernel) PreviewFuzzyModifyAction(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.PreviewFuzzyModifyAction(ctx, payload)
}

// ExecuteFuzzyModifyAction 对齐 kernel_compat.py 的 ExecuteFuzzyModifyAction 转发
func (k *Kernel) ExecuteFuzzyModifyAction(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.ExecuteFuzzyModifyAction(ctx, payload)
}

// RollbackFuzzyModifyAction 对齐 kernel_compat.py 的 RollbackFuzzyModifyAction 转发
func (k *Kernel) RollbackFuzzyModifyAction(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.RollbackFuzzyModifyAction(ctx, payload)
}

// CollectFuzzyModifyCandidates 对齐 kernel_compat.py 的 CollectFuzzyModifyCandidates 转发
func (k *Kernel) CollectFuzzyModifyCandidates(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.CollectFuzzyModifyCandidates(ctx, payload)
}

// BuildFuzzyModifyLLMPlan 对齐 kernel_compat.py 的 BuildFuzzyModifyLLMPlan 转发
func (k *Kernel) BuildFuzzyModifyLLMPlan(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.BuildFuzzyModifyLLMPlan(ctx, payload)
}

// ApplyFuzzyModifyPlan 对齐 kernel_compat.py 的 ApplyFuzzyModifyPlan 转发
func (k *Kernel) ApplyFuzzyModifyPlan(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.CorrectionAdmin == nil {
		return nil, nil
	}
	return k.services.CorrectionAdmin.ApplyFuzzyModifyPlan(ctx, payload)
}
