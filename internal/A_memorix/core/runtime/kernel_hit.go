package memruntime

// 对齐 MemorySearchHitProcessingService 的命中处理转发

import (
	"context"

	"MaiGo/internal/A_memorix/core/utils"
)

// RelationStatusIsInactive 对齐搜索命中处理的同名方法
func (k *Kernel) RelationStatusIsInactive(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.RelationStatusIsInactive(ctx, payload)
}

// LoadParagraphStaleMarks 对齐搜索命中处理的同名方法
func (k *Kernel) LoadParagraphStaleMarks(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.LoadParagraphStaleMarks(ctx, payload)
}

// ParagraphHiddenByStaleMarks 对齐搜索命中处理的同名方法
func (k *Kernel) ParagraphHiddenByStaleMarks(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.ParagraphHiddenByStaleMarks(ctx, payload)
}

// FilterEpisodeHits 对齐搜索命中处理的同名方法
func (k *Kernel) FilterEpisodeHits(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.FilterEpisodeHits(ctx, payload)
}

// FilterUserVisibleHits 对齐搜索命中处理的同名方法
func (k *Kernel) FilterUserVisibleHits(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.FilterUserVisibleHits(ctx, payload)
}

// FilterCurrentEffectiveHits 对齐搜索命中处理的同名方法
func (k *Kernel) FilterCurrentEffectiveHits(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.FilterCurrentEffectiveHits(ctx, payload)
}

// CurrentEffectiveFilterStoreCheckNeeded 对齐搜索命中处理的同名方法
func (k *Kernel) CurrentEffectiveFilterStoreCheckNeeded(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.CurrentEffectiveFilterStoreCheckNeeded(ctx, payload)
}

// FilterHitsByMemoryChangeMetadata 对齐搜索命中处理的同名方法
func (k *Kernel) FilterHitsByMemoryChangeMetadata(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.FilterHitsByMemoryChangeMetadata(ctx, payload)
}

// ChatSource 对齐搜索命中处理的同名方法
func (k *Kernel) ChatSource(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.ChatSource(ctx, payload)
}

// ChatSourceForSearchScope 对齐搜索命中处理的同名方法
func (k *Kernel) ChatSourceForSearchScope(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.ChatSourceForSearchScope(ctx, payload)
}

// ScopedSearchLimit 对齐搜索命中处理的同名方法
func (k *Kernel) ScopedSearchLimit(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.ScopedSearchLimit(ctx, payload)
}

// ResolveAllowedChatIDs 对齐搜索命中处理的同名方法
func (k *Kernel) ResolveAllowedChatIDs(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.ResolveAllowedChatIDs(ctx, payload)
}

// RankScoreFromItem 对齐搜索命中处理的同名方法
func (k *Kernel) RankScoreFromItem(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.RankScoreFromItem(ctx, payload)
}

// DedupeRankedItems 对齐搜索命中处理的同名方法
func (k *Kernel) DedupeRankedItems(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.DedupeRankedItems(ctx, payload)
}

// ParagraphMatchesChatScope 对齐搜索命中处理的同名方法
func (k *Kernel) ParagraphMatchesChatScope(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.ParagraphMatchesChatScope(ctx, payload)
}

// HitMetadataMatchesChatScope 对齐搜索命中处理的同名方法
func (k *Kernel) HitMetadataMatchesChatScope(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.HitMetadataMatchesChatScope(ctx, payload)
}

// ExtendChatScopeIDs 对齐搜索命中处理的同名方法
func (k *Kernel) ExtendChatScopeIDs(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.ExtendChatScopeIDs(ctx, payload)
}

// MetadataChatScopeIDs 对齐搜索命中处理的同名方法
func (k *Kernel) MetadataChatScopeIDs(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.MetadataChatScopeIDs(ctx, payload)
}

// FilterHitsByChatScope 对齐搜索命中处理的同名方法
func (k *Kernel) FilterHitsByChatScope(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.FilterHitsByChatScope(ctx, payload)
}

// FilterHitsByRetrievalTypeScope 对齐搜索命中处理的同名方法
func (k *Kernel) FilterHitsByRetrievalTypeScope(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.FilterHitsByRetrievalTypeScope(ctx, payload)
}

// RetrievalFilterKind 对齐搜索命中处理的同名方法
func (k *Kernel) RetrievalFilterKind(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.RetrievalFilterKind(ctx, payload)
}

// SourceStreamID 对齐搜索命中处理的同名方法
func (k *Kernel) SourceStreamID(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.SourceStreamID(ctx, payload)
}

// RetrievalFilterContext 对齐搜索命中处理的同名方法
func (k *Kernel) RetrievalFilterContext(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.RetrievalFilterContext(ctx, payload)
}

// CurrentRetrievalFilterContext 对齐搜索命中处理的同名方法
func (k *Kernel) CurrentRetrievalFilterContext(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.CurrentRetrievalFilterContext(ctx, payload)
}

// RetrievalFilterContextIsCurrentSource 对齐搜索命中处理的同名方法
func (k *Kernel) RetrievalFilterContextIsCurrentSource(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.RetrievalFilterContextIsCurrentSource(ctx, payload)
}

// RetrievalFilterContextAllowed 对齐搜索命中处理的同名方法
func (k *Kernel) RetrievalFilterContextAllowed(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.RetrievalFilterContextAllowed(ctx, payload)
}

// NormalizeSearchTimeBound 对齐搜索命中处理的同名方法
func (k *Kernel) NormalizeSearchTimeBound(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.NormalizeSearchTimeBound(ctx, payload)
}

// NormalizeSearchTimeWindow 对齐搜索命中处理的同名方法
func (k *Kernel) NormalizeSearchTimeWindow(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.NormalizeSearchTimeWindow(ctx, payload)
}

// RetrievalResultHit 对齐搜索命中处理的同名方法
func (k *Kernel) RetrievalResultHit(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.RetrievalResultHit(ctx, payload)
}

// EpisodeHit 对齐搜索命中处理的同名方法
func (k *Kernel) EpisodeHit(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.EpisodeHit(ctx, payload)
}

// Summary 对齐搜索命中处理的同名方法
func (k *Kernel) Summary(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.Summary(ctx, payload)
}

// FilterHits 对齐搜索命中处理的同名方法
func (k *Kernel) FilterHits(ctx context.Context, payload map[string]any) (any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.FilterHits(ctx, payload)
}

// OptionalFloat 对齐 _optional_float
func OptionalFloat(value any) *float64 { return utils.OptionalFloat(value) }

// OptionalInt 对齐 _optional_int
func OptionalInt(value any) *int { return utils.OptionalInt(value) }
