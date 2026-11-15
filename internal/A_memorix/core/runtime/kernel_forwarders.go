package memruntime

// 内核转发，对齐 sdk_memory_kernel.py 中尚缺的服务转发与工具入口

import (
	"context"

	"MaiGo/internal/A_memorix/core/utils"
)

// MergeRuntimeConfigPatch 对齐 _merge_runtime_config_patch
func (k *Kernel) MergeRuntimeConfigPatch(base map[string]any, patch map[string]any) map[string]any {
	return MergeRuntimeConfigPatch(base, patch)
}

// ResolveKnowledgeType 对齐 _resolve_knowledge_type
func (k *Kernel) ResolveKnowledgeType(sourceType string) string {
	return ResolveKnowledgeType(sourceType)
}

// TimeMeta 对齐 _time_meta
func (k *Kernel) TimeMeta(timestamp *float64, timeStart *float64, timeEnd *float64) map[string]any {
	return TimeMeta(timestamp, timeStart, timeEnd)
}

// EmptyPersonProfileResponse 对齐 _empty_person_profile_response
func (k *Kernel) EmptyPersonProfileResponse(personID string, personName string) map[string]any {
	return EmptyPersonProfileResponse(personID, personName)
}

// ProfileEvidenceTypeFromSource 对齐 _profile_evidence_type_from_source
func (k *Kernel) ProfileEvidenceTypeFromSource(source string, metadata map[string]any) string {
	return utils.ProfileEvidenceTypeFromSource(source, metadata)
}

// ProfileRelationContent 对齐 _profile_relation_content
func (k *Kernel) ProfileRelationContent(relation map[string]any) string {
	return utils.ProfileRelationContent(relation)
}

// SearchExecutionOnce 对齐 _search_execution_once
func (k *Kernel) SearchExecutionOnce(ctx context.Context, params map[string]any) (map[string]any, error) {
	if k.services.Search == nil {
		return nil, nil
	}
	return k.services.Search.SearchExecutionOnce(ctx, params)
}

// SearchExecutionForChatScope 对齐 _search_execution_for_chat_scope
func (k *Kernel) SearchExecutionForChatScope(ctx context.Context, params map[string]any) (map[string]any, error) {
	if k.services.Search == nil {
		return nil, nil
	}
	return k.services.Search.SearchExecutionForChatScope(ctx, params)
}

// EpisodeQueryForChatScope 对齐 _episode_query_for_chat_scope
func (k *Kernel) EpisodeQueryForChatScope(ctx context.Context, params map[string]any) (any, error) {
	if k.services.Search == nil {
		return nil, nil
	}
	return k.services.Search.EpisodeQueryForChatScope(ctx, params)
}

// EnsureVectorForText 对齐 _ensure_vector_for_text
func (k *Kernel) EnsureVectorForText(ctx context.Context, params map[string]any) (bool, error) {
	if k.services.Ingest == nil {
		return false, nil
	}
	return k.services.Ingest.EnsureVectorForText(ctx, params)
}

// EnsureRelationVector 对齐 _ensure_relation_vector
func (k *Kernel) EnsureRelationVector(ctx context.Context, payload map[string]any) (bool, error) {
	if k.services.Ingest == nil {
		return false, nil
	}
	return k.services.Ingest.EnsureRelationVector(ctx, payload)
}

// EnsureParagraphVector 对齐 _ensure_paragraph_vector
func (k *Kernel) EnsureParagraphVector(ctx context.Context, payload map[string]any) (bool, error) {
	if k.services.Ingest == nil {
		return false, nil
	}
	return k.services.Ingest.EnsureParagraphVector(ctx, payload)
}

// EnsureEntityVector 对齐 _ensure_entity_vector
func (k *Kernel) EnsureEntityVector(ctx context.Context, payload map[string]any) (bool, error) {
	if k.services.Ingest == nil {
		return false, nil
	}
	return k.services.Ingest.EnsureEntityVector(ctx, payload)
}

// RestoreRelationHashes 对齐 _restore_relation_hashes
func (k *Kernel) RestoreRelationHashes(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if k.services.Ingest == nil {
		return nil, nil
	}
	return k.services.Ingest.RestoreRelationHashes(ctx, payload)
}

// ResolveRelationHashes 对齐 _resolve_relation_hashes
func (k *Kernel) ResolveRelationHashes(ctx context.Context, target string) []string {
	if k.services.V5Admin == nil {
		return nil
	}
	return k.services.V5Admin.ResolveRelationHashes(ctx, target)
}

// ResolveDeletedRelationHashes 对齐 _resolve_deleted_relation_hashes
func (k *Kernel) ResolveDeletedRelationHashes(ctx context.Context, target string) []string {
	if k.services.V5Admin == nil {
		return nil
	}
	return k.services.V5Admin.ResolveDeletedRelationHashes(ctx, target)
}

// MemoryV5Status 对齐 _memory_v5_status
func (k *Kernel) MemoryV5Status(ctx context.Context, target string, limit int) (map[string]any, error) {
	if k.services.V5Admin == nil {
		return nil, nil
	}
	return k.services.V5Admin.MemoryV5Status(ctx, target, limit)
}

// ApplyV5RelationAction 对齐 _apply_v5_relation_action
func (k *Kernel) ApplyV5RelationAction(ctx context.Context, action string, hashes []string, strength float64) (map[string]any, error) {
	if k.services.V5Admin == nil {
		return nil, nil
	}
	return k.services.V5Admin.ApplyV5RelationAction(ctx, action, hashes, strength)
}

// HasEnabledRetrievalTypeFilter 对齐 _has_enabled_retrieval_type_filter
func (k *Kernel) HasEnabledRetrievalTypeFilter(ctx context.Context) bool {
	if k.services.SearchHit == nil {
		return false
	}
	return k.services.SearchHit.HasEnabledRetrievalTypeFilter(ctx)
}

// RetrievalTypeFilterRoot 对齐 _retrieval_type_filter_root
func (k *Kernel) RetrievalTypeFilterRoot(ctx context.Context) map[string]any {
	if k.services.SearchHit == nil {
		return nil
	}
	return k.services.SearchHit.RetrievalTypeFilterRoot(ctx)
}

// RetrievalTypeFilterConfig 对齐 _retrieval_type_filter_config
func (k *Kernel) RetrievalTypeFilterConfig(ctx context.Context, kind string) map[string]any {
	if k.services.SearchHit == nil {
		return nil
	}
	return k.services.SearchHit.RetrievalTypeFilterConfig(ctx, kind)
}

// RetrievalFilterContextsForHit 对齐 _retrieval_filter_contexts_for_hit
func (k *Kernel) RetrievalFilterContextsForHit(ctx context.Context, hit map[string]any, paragraphMap map[string]map[string]any, relationParagraphMap map[string][]map[string]any) []map[string]string {
	if k.services.SearchHit == nil {
		return nil
	}
	return k.services.SearchHit.RetrievalFilterContextsForHit(ctx, hit, paragraphMap, relationParagraphMap)
}

// RetrievalFilterContextFromHit 对齐 _retrieval_filter_context_from_hit
func (k *Kernel) RetrievalFilterContextFromHit(ctx context.Context, hit map[string]any) map[string]string {
	if k.services.SearchHit == nil {
		return nil
	}
	return k.services.SearchHit.RetrievalFilterContextFromHit(ctx, hit)
}

// RetrievalFilterContextFromParagraph 对齐 _retrieval_filter_context_from_paragraph
func (k *Kernel) RetrievalFilterContextFromParagraph(ctx context.Context, paragraph map[string]any) map[string]string {
	if k.services.SearchHit == nil {
		return nil
	}
	return k.services.SearchHit.RetrievalFilterContextFromParagraph(ctx, paragraph)
}

// FilterActiveRelationHits 对齐 _filter_active_relation_hits
func (k *Kernel) FilterActiveRelationHits(ctx context.Context, hits []map[string]any) ([]map[string]any, error) {
	if k.services.SearchHit == nil {
		return nil, nil
	}
	return k.services.SearchHit.FilterActiveRelationHits(ctx, hits)
}

// V1ReconciliationEvidenceRoot 对齐 _v1_reconciliation_evidence_root
func (k *Kernel) V1ReconciliationEvidenceRoot(ctx context.Context) string {
	if k.services.VectorRecovery == nil {
		return ""
	}
	return k.services.VectorRecovery.V1ReconciliationEvidenceRoot(ctx)
}

// V1ValidHashesForPool 对齐 _v1_valid_hashes_for_pool
func (k *Kernel) V1ValidHashesForPool(ctx context.Context, pool string) []string {
	if k.services.VectorRecovery == nil {
		return nil
	}
	return k.services.VectorRecovery.V1ValidHashesForPool(ctx, pool)
}

// BuildProfileRelationEvidenceItem 对齐 _build_profile_relation_evidence_item
func (k *Kernel) BuildProfileRelationEvidenceItem(ctx context.Context, relation map[string]any, index int) map[string]any {
	if k.services.ProfileAdmin == nil {
		return nil
	}
	return k.services.ProfileAdmin.BuildProfileRelationEvidenceItem(ctx, relation, index)
}

// BuildProfileParagraphEvidenceItem 对齐 _build_profile_paragraph_evidence_item
func (k *Kernel) BuildProfileParagraphEvidenceItem(ctx context.Context, item map[string]any, index int, fallbackHash string) map[string]any {
	if k.services.ProfileAdmin == nil {
		return nil
	}
	return k.services.ProfileAdmin.BuildProfileParagraphEvidenceItem(ctx, item, index, fallbackHash)
}

// BuildProfileEvidenceItems 对齐 _build_profile_evidence_items
func (k *Kernel) BuildProfileEvidenceItems(ctx context.Context, profile map[string]any) []map[string]any {
	if k.services.ProfileAdmin == nil {
		return nil
	}
	return k.services.ProfileAdmin.BuildProfileEvidenceItems(ctx, profile)
}

// ProfileEvidenceResponse 对齐 _profile_evidence_response
func (k *Kernel) ProfileEvidenceResponse(ctx context.Context, profile map[string]any, requestedPersonID string, limit int) map[string]any {
	if k.services.ProfileAdmin == nil {
		return nil
	}
	return k.services.ProfileAdmin.ProfileEvidenceResponse(ctx, profile, requestedPersonID, limit)
}

// ProfileEvidenceAdmin 对齐 _profile_evidence_admin
func (k *Kernel) ProfileEvidenceAdmin(ctx context.Context, personID string, personKeyword string, limit int, forceRefresh bool) (map[string]any, error) {
	if k.services.ProfileAdmin == nil {
		return nil, nil
	}
	return k.services.ProfileAdmin.ProfileEvidenceAdmin(ctx, personID, personKeyword, limit, forceRefresh)
}

// ProfileCorrectEvidenceAdmin 对齐 _profile_correct_evidence_admin
func (k *Kernel) ProfileCorrectEvidenceAdmin(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if k.services.ProfileAdmin == nil {
		return nil, nil
	}
	return k.services.ProfileAdmin.ProfileCorrectEvidenceAdmin(ctx, payload)
}
