package memruntime

// 对齐 kernel_compat.py 的删除管理转发，逐方法委托给注入的服务

import "context"

// SelectorDict 对齐 kernel_compat.py 的 SelectorDict 转发
func (k *Kernel) SelectorDict(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.SelectorDict(ctx, payload)
}

// ResolveParagraphTargets 对齐 kernel_compat.py 的 ResolveParagraphTargets 转发
func (k *Kernel) ResolveParagraphTargets(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.ResolveParagraphTargets(ctx, payload)
}

// ResolveEntityTargets 对齐 kernel_compat.py 的 ResolveEntityTargets 转发
func (k *Kernel) ResolveEntityTargets(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.ResolveEntityTargets(ctx, payload)
}

// ResolveSourceTargets 对齐 kernel_compat.py 的 ResolveSourceTargets 转发
func (k *Kernel) ResolveSourceTargets(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.ResolveSourceTargets(ctx, payload)
}

// SnapshotRelationItem 对齐 kernel_compat.py 的 SnapshotRelationItem 转发
func (k *Kernel) SnapshotRelationItem(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.SnapshotRelationItem(ctx, payload)
}

// SnapshotParagraphItem 对齐 kernel_compat.py 的 SnapshotParagraphItem 转发
func (k *Kernel) SnapshotParagraphItem(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.SnapshotParagraphItem(ctx, payload)
}

// SnapshotEntityItem 对齐 kernel_compat.py 的 SnapshotEntityItem 转发
func (k *Kernel) SnapshotEntityItem(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.SnapshotEntityItem(ctx, payload)
}

// RelationHasRemainingParagraphs 对齐 kernel_compat.py 的 RelationHasRemainingParagraphs 转发
func (k *Kernel) RelationHasRemainingParagraphs(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.RelationHasRemainingParagraphs(ctx, payload)
}

// BuildDeletePreviewItem 对齐 kernel_compat.py 的 BuildDeletePreviewItem 转发
func (k *Kernel) BuildDeletePreviewItem(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.BuildDeletePreviewItem(ctx, payload)
}

// BuildStandardDeleteResult 对齐 kernel_compat.py 的 BuildStandardDeleteResult 转发
func (k *Kernel) BuildStandardDeleteResult(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.BuildStandardDeleteResult(ctx, payload)
}

// BuildDeletePlan 对齐 kernel_compat.py 的 BuildDeletePlan 转发
func (k *Kernel) BuildDeletePlan(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.BuildDeletePlan(ctx, payload)
}

// PreviewDeleteAction 对齐 kernel_compat.py 的 PreviewDeleteAction 转发
func (k *Kernel) PreviewDeleteAction(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.PreviewDeleteAction(ctx, payload)
}

// ExecuteDeleteAction 对齐 kernel_compat.py 的 ExecuteDeleteAction 转发
func (k *Kernel) ExecuteDeleteAction(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.ExecuteDeleteAction(ctx, payload)
}

// InvalidateImportManifestForSources 对齐 kernel_compat.py 的 InvalidateImportManifestForSources 转发
func (k *Kernel) InvalidateImportManifestForSources(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.InvalidateImportManifestForSources(ctx, payload)
}

// RestoreDeleteAction 对齐 kernel_compat.py 的 RestoreDeleteAction 转发
func (k *Kernel) RestoreDeleteAction(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.RestoreDeleteAction(ctx, payload)
}

// RestoreDeleteOperation 对齐 kernel_compat.py 的 RestoreDeleteOperation 转发
func (k *Kernel) RestoreDeleteOperation(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.RestoreDeleteOperation(ctx, payload)
}

// PurgeDeletedMemory 对齐 kernel_compat.py 的 PurgeDeletedMemory 转发
func (k *Kernel) PurgeDeletedMemory(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.DeleteAdmin == nil {
		return nil, nil
	}
	return k.services.DeleteAdmin.PurgeDeletedMemory(ctx, payload)
}
