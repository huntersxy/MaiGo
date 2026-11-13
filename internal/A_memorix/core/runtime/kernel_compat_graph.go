package memruntime

// 对齐 kernel_compat.py 的图管理转发，逐方法委托给注入的服务

import "context"

// SerializeGraph 对齐 kernel_compat.py 的 SerializeGraph 转发
func (k *Kernel) SerializeGraph(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.SerializeGraph(ctx, payload)
}

// GraphSearchMatchRank 对齐 kernel_compat.py 的 GraphSearchMatchRank 转发
func (k *Kernel) GraphSearchMatchRank(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.GraphSearchMatchRank(ctx, payload)
}

// PickGraphSearchMatch 对齐 kernel_compat.py 的 PickGraphSearchMatch 转发
func (k *Kernel) PickGraphSearchMatch(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.PickGraphSearchMatch(ctx, payload)
}

// SearchGraph 对齐 kernel_compat.py 的 SearchGraph 转发
func (k *Kernel) SearchGraph(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.SearchGraph(ctx, payload)
}

// DedupeStrings 对齐 kernel_compat.py 的 DedupeStrings 转发
func (k *Kernel) DedupeStrings(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.DedupeStrings(ctx, payload)
}

// BuildGraphEdgeLabel 对齐 kernel_compat.py 的 BuildGraphEdgeLabel 转发
func (k *Kernel) BuildGraphEdgeLabel(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.BuildGraphEdgeLabel(ctx, payload)
}

// TrimText 对齐 kernel_compat.py 的 TrimText 转发
func (k *Kernel) TrimText(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.TrimText(ctx, payload)
}

// FormatRelationText 对齐 kernel_compat.py 的 FormatRelationText 转发
func (k *Kernel) FormatRelationText(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.FormatRelationText(ctx, payload)
}

// QueryRelationRowsByHashes 对齐 kernel_compat.py 的 QueryRelationRowsByHashes 转发
func (k *Kernel) QueryRelationRowsByHashes(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.QueryRelationRowsByHashes(ctx, payload)
}

// QueryDistinctParagraphHashesForRelations 对齐 kernel_compat.py 的 QueryDistinctParagraphHashesForRelations 转发
func (k *Kernel) QueryDistinctParagraphHashesForRelations(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.QueryDistinctParagraphHashesForRelations(ctx, payload)
}

// LoadParagraphRows 对齐 kernel_compat.py 的 LoadParagraphRows 转发
func (k *Kernel) LoadParagraphRows(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.LoadParagraphRows(ctx, payload)
}

// ResolveGraphNodeName 对齐 kernel_compat.py 的 ResolveGraphNodeName 转发
func (k *Kernel) ResolveGraphNodeName(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.ResolveGraphNodeName(ctx, payload)
}

// GetRelatedRelationRowsForEntity 对齐 kernel_compat.py 的 GetRelatedRelationRowsForEntity 转发
func (k *Kernel) GetRelatedRelationRowsForEntity(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.GetRelatedRelationRowsForEntity(ctx, payload)
}

// BuildRelationSummary 对齐 kernel_compat.py 的 BuildRelationSummary 转发
func (k *Kernel) BuildRelationSummary(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.BuildRelationSummary(ctx, payload)
}

// BuildParagraphSummary 对齐 kernel_compat.py 的 BuildParagraphSummary 转发
func (k *Kernel) BuildParagraphSummary(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.BuildParagraphSummary(ctx, payload)
}

// EvidenceEntityNodeID 对齐 kernel_compat.py 的 EvidenceEntityNodeID 转发
func (k *Kernel) EvidenceEntityNodeID(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.EvidenceEntityNodeID(ctx, payload)
}

// EvidenceRelationNodeID 对齐 kernel_compat.py 的 EvidenceRelationNodeID 转发
func (k *Kernel) EvidenceRelationNodeID(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.EvidenceRelationNodeID(ctx, payload)
}

// EvidenceParagraphNodeID 对齐 kernel_compat.py 的 EvidenceParagraphNodeID 转发
func (k *Kernel) EvidenceParagraphNodeID(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.EvidenceParagraphNodeID(ctx, payload)
}

// BuildEvidenceGraph 对齐 kernel_compat.py 的 BuildEvidenceGraph 转发
func (k *Kernel) BuildEvidenceGraph(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.BuildEvidenceGraph(ctx, payload)
}

// BuildGraphNodeDetail 对齐 kernel_compat.py 的 BuildGraphNodeDetail 转发
func (k *Kernel) BuildGraphNodeDetail(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.BuildGraphNodeDetail(ctx, payload)
}

// BuildGraphEdgeDetail 对齐 kernel_compat.py 的 BuildGraphEdgeDetail 转发
func (k *Kernel) BuildGraphEdgeDetail(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.BuildGraphEdgeDetail(ctx, payload)
}

// DeleteSources 对齐 kernel_compat.py 的 DeleteSources 转发
func (k *Kernel) DeleteSources(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.DeleteSources(ctx, payload)
}

// RebuildGraphFromMetadata 对齐 kernel_compat.py 的 RebuildGraphFromMetadata 转发
func (k *Kernel) RebuildGraphFromMetadata(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.RebuildGraphFromMetadata(ctx, payload)
}

// RenameNode 对齐 kernel_compat.py 的 RenameNode 转发
func (k *Kernel) RenameNode(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.RenameNode(ctx, payload)
}

// UpdateEdgeWeight 对齐 kernel_compat.py 的 UpdateEdgeWeight 转发
func (k *Kernel) UpdateEdgeWeight(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.GraphAdmin == nil {
		return nil, nil
	}
	return k.services.GraphAdmin.UpdateEdgeWeight(ctx, payload)
}
