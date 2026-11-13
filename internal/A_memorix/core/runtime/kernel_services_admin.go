package memruntime

// 内核依赖的服务接口，对齐 core/runtime/services 下各服务被内核转发的调用面
// 说明：Python 服务方法签名宽松（*args/**kwargs），Go 版统一用 payload 承载关键字参数

import "context"

// DeleteAdminService 对齐 MemoryDeleteAdminService
type DeleteAdminService interface {
	SelectorDict(ctx context.Context, payload map[string]any) (any, error)
	ResolveParagraphTargets(ctx context.Context, payload map[string]any) (any, error)
	ResolveEntityTargets(ctx context.Context, payload map[string]any) (any, error)
	ResolveSourceTargets(ctx context.Context, payload map[string]any) (any, error)
	SnapshotRelationItem(ctx context.Context, payload map[string]any) (any, error)
	SnapshotParagraphItem(ctx context.Context, payload map[string]any) (any, error)
	SnapshotEntityItem(ctx context.Context, payload map[string]any) (any, error)
	RelationHasRemainingParagraphs(ctx context.Context, payload map[string]any) (any, error)
	BuildDeletePreviewItem(ctx context.Context, payload map[string]any) (any, error)
	BuildStandardDeleteResult(ctx context.Context, payload map[string]any) (any, error)
	BuildDeletePlan(ctx context.Context, payload map[string]any) (any, error)
	PreviewDeleteAction(ctx context.Context, payload map[string]any) (any, error)
	ExecuteDeleteAction(ctx context.Context, payload map[string]any) (any, error)
	InvalidateImportManifestForSources(ctx context.Context, payload map[string]any) (any, error)
	RestoreDeleteAction(ctx context.Context, payload map[string]any) (any, error)
	RestoreDeleteOperation(ctx context.Context, payload map[string]any) (any, error)
	PurgeDeletedMemory(ctx context.Context, payload map[string]any) (any, error)
	MemoryDeleteAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
	ProcessPendingStorageCleanupJobs(ctx context.Context, operationID string, limit int) (map[string]any, error)
}

// FeedbackService 对齐 MemoryFeedbackCorrectionService
type FeedbackService interface {
	ResolveFeedbackRelatedPersonIDs(ctx context.Context, payload map[string]any) (any, error)
	MarkFeedbackStaleParagraphs(ctx context.Context, payload map[string]any) (any, error)
	EnqueueFeedbackEpisodeRebuilds(ctx context.Context, payload map[string]any) (any, error)
	EnqueueFeedbackProfileRefreshes(ctx context.Context, payload map[string]any) (any, error)
	FeedbackAffectedCounts(ctx context.Context, payload map[string]any) (any, error)
	BuildFeedbackRollbackPlanSummary(ctx context.Context, payload map[string]any) (any, error)
	BuildFeedbackTaskSummary(ctx context.Context, payload map[string]any) (any, error)
	BuildFeedbackTaskDetail(ctx context.Context, payload map[string]any) (any, error)
	SoftDeleteFeedbackCorrectionParagraphs(ctx context.Context, payload map[string]any) (any, error)
	ExtractFeedbackMessages(ctx context.Context, payload map[string]any) (any, error)
	BuildFeedbackHitBriefs(ctx context.Context, payload map[string]any) (any, error)
	ShouldInvokeFeedbackClassifier(ctx context.Context, payload map[string]any) (any, error)
	NormalizeFeedbackDecision(ctx context.Context, payload map[string]any) (any, error)
	FeedbackApplyResultStatus(ctx context.Context, payload map[string]any) (any, error)
	RestoreFeedbackRelationsFromSnapshots(ctx context.Context, payload map[string]any) (any, error)
	ResolveFeedbackRelationHashes(ctx context.Context, payload map[string]any) (any, error)
	RollbackFeedbackTask(ctx context.Context, payload map[string]any) (any, error)
	ProcessFeedbackProfileRefreshBatch(ctx context.Context, payload map[string]any) (any, error)
	ProcessFeedbackEpisodeRebuildBatch(ctx context.Context, payload map[string]any) (any, error)
	FeedbackCorrectionReconcileLoop(ctx context.Context, payload map[string]any) (any, error)
	EnqueueFeedbackTask(ctx context.Context, payload map[string]any) (any, error)
	ClassifyFeedback(ctx context.Context, payload map[string]any) (any, error)
	IngestFeedbackRelations(ctx context.Context, payload map[string]any) (any, error)
	ApplyFeedbackDecision(ctx context.Context, payload map[string]any) (any, error)
	ProcessFeedbackTask(ctx context.Context, payload map[string]any) (any, error)
	FeedbackCorrectionLoop(ctx context.Context, payload map[string]any) (any, error)
	MemoryFeedbackAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
}

// GraphAdminService 对齐 MemoryGraphAdminService
type GraphAdminService interface {
	SerializeGraph(ctx context.Context, payload map[string]any) (any, error)
	GraphSearchMatchRank(ctx context.Context, payload map[string]any) (any, error)
	PickGraphSearchMatch(ctx context.Context, payload map[string]any) (any, error)
	SearchGraph(ctx context.Context, payload map[string]any) (any, error)
	DedupeStrings(ctx context.Context, payload map[string]any) (any, error)
	BuildGraphEdgeLabel(ctx context.Context, payload map[string]any) (any, error)
	TrimText(ctx context.Context, payload map[string]any) (any, error)
	FormatRelationText(ctx context.Context, payload map[string]any) (any, error)
	QueryRelationRowsByHashes(ctx context.Context, payload map[string]any) (any, error)
	QueryDistinctParagraphHashesForRelations(ctx context.Context, payload map[string]any) (any, error)
	LoadParagraphRows(ctx context.Context, payload map[string]any) (any, error)
	ResolveGraphNodeName(ctx context.Context, payload map[string]any) (any, error)
	GetRelatedRelationRowsForEntity(ctx context.Context, payload map[string]any) (any, error)
	BuildRelationSummary(ctx context.Context, payload map[string]any) (any, error)
	BuildParagraphSummary(ctx context.Context, payload map[string]any) (any, error)
	EvidenceEntityNodeID(ctx context.Context, payload map[string]any) (any, error)
	EvidenceRelationNodeID(ctx context.Context, payload map[string]any) (any, error)
	EvidenceParagraphNodeID(ctx context.Context, payload map[string]any) (any, error)
	BuildEvidenceGraph(ctx context.Context, payload map[string]any) (any, error)
	BuildGraphNodeDetail(ctx context.Context, payload map[string]any) (any, error)
	BuildGraphEdgeDetail(ctx context.Context, payload map[string]any) (any, error)
	DeleteSources(ctx context.Context, payload map[string]any) (any, error)
	RebuildGraphFromMetadata(ctx context.Context, payload map[string]any) (any, error)
	RenameNode(ctx context.Context, payload map[string]any) (any, error)
	UpdateEdgeWeight(ctx context.Context, payload map[string]any) (any, error)
	MemoryGraphAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
}

// CorrectionAdminService 对齐 MemoryCorrectionAdminService
type CorrectionAdminService interface {
	IsFuzzyModifyCandidateMutable(ctx context.Context, payload map[string]any) (any, error)
	NormalizeFuzzyModifyPlan(ctx context.Context, payload map[string]any) (any, error)
	NormalizeFuzzyModifyCandidate(ctx context.Context, payload map[string]any) (any, error)
	NormalizeFuzzyModifyRelations(ctx context.Context, payload map[string]any) (any, error)
	BuildFuzzyModifyCascadePreview(ctx context.Context, payload map[string]any) (any, error)
	BuildFuzzyModifyParagraphCascade(ctx context.Context, payload map[string]any) (any, error)
	FuzzyModifyStaleSourceOperationID(ctx context.Context, payload map[string]any) (any, error)
	ExecuteFuzzyModifyParagraphCascade(ctx context.Context, payload map[string]any) (any, error)
	MarkFuzzyModifyTargetSuperseded(ctx context.Context, payload map[string]any) (any, error)
	NormalizeFuzzyModifyScope(ctx context.Context, payload map[string]any) (any, error)
	PreviewFuzzyModifyAction(ctx context.Context, payload map[string]any) (any, error)
	ExecuteFuzzyModifyAction(ctx context.Context, payload map[string]any) (any, error)
	RollbackFuzzyModifyAction(ctx context.Context, payload map[string]any) (any, error)
	CollectFuzzyModifyCandidates(ctx context.Context, payload map[string]any) (any, error)
	BuildFuzzyModifyLLMPlan(ctx context.Context, payload map[string]any) (any, error)
	ApplyFuzzyModifyPlan(ctx context.Context, payload map[string]any) (any, error)
	MemoryCorrectionAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
	MemoryFuzzyModifyAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
}
