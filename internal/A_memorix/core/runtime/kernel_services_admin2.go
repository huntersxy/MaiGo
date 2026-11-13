package memruntime

// 内核依赖的 admin 与运行时服务接口续

import "context"

// SourceAdminService 对齐 MemorySourceAdminService
type SourceAdminService interface {
	MemorySourceAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
}

// FactAdminService 对齐 MemoryFactAdminService
type FactAdminService interface {
	MemoryFactAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
}

// EpisodeAdminService 对齐 MemoryEpisodeAdminService
type EpisodeAdminService interface {
	RebuildEpisodesForSources(ctx context.Context, sources []string) (map[string]any, error)
	MemoryEpisodeAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
}

// ProfileAdminService 对齐 MemoryProfileAdminService
type ProfileAdminService interface {
	GetPersonProfile(ctx context.Context, personID string, chatID string, limit int) (map[string]any, error)
	RefreshPersonProfile(ctx context.Context, payload map[string]any) (map[string]any, error)
	QueryPersonProfileWithFeedbackRefresh(ctx context.Context, personID string, chatID string, limit int) (map[string]any, error)
	BuildPersonProfileResponse(ctx context.Context, payload map[string]any) (map[string]any, error)
	MarkPersonActive(ctx context.Context, personID string)
	EnqueuePersonProfileRefresh(ctx context.Context, personID string, reason string) bool
	HasPendingPersonProfileRefresh(ctx context.Context, personID string) bool
	ProcessPersonProfileRefreshQueueBatch(ctx context.Context, limit int) (map[string]any, error)
	MemoryProfileAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
	BuildProfileRelationEvidenceItem(ctx context.Context, relation map[string]any, index int) map[string]any
	BuildProfileParagraphEvidenceItem(ctx context.Context, item map[string]any, index int, fallbackHash string) map[string]any
	BuildProfileEvidenceItems(ctx context.Context, profile map[string]any) []map[string]any
	ProfileEvidenceResponse(ctx context.Context, profile map[string]any, requestedPersonID string, limit int) map[string]any
	ProfileEvidenceAdmin(ctx context.Context, personID string, personKeyword string, limit int, forceRefresh bool) (map[string]any, error)
	ProfileCorrectEvidenceAdmin(ctx context.Context, payload map[string]any) (map[string]any, error)
}

// BundleAdminService 对齐 MemoryBundleAdminService
type BundleAdminService interface {
	MemoryBundleAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
}

// ImportTuningAdminService 对齐 MemoryImportTuningAdminService
type ImportTuningAdminService interface {
	MemoryImportAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
	MemoryTuningAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
}

// ImageService 对齐 MemoryImageService
type ImageService interface {
	Execute(ctx context.Context, action string, payload map[string]any) (map[string]any, error)
}

// V5AdminService 对齐 MemoryV5AdminService
type V5AdminService interface {
	MaintainMemory(ctx context.Context, action string, target string, hours *float64, reason string, limit int) (map[string]any, error)
	V5Status(ctx context.Context, target string, limit int) (map[string]any, error)
	ApplyV5RelationAction(ctx context.Context, action string, hashes []string, strength float64) (map[string]any, error)
	MemoryV5Admin(ctx context.Context, action string, payload map[string]any) (any, error)
	ResolveRelationHashes(ctx context.Context, target string) []string
	ResolveDeletedRelationHashes(ctx context.Context, target string) []string
	MemoryV5Status(ctx context.Context, target string, limit int) (map[string]any, error)
}

// VectorDeleteService 对齐 MemoryVectorDeleteService
type VectorDeleteService interface {
	DeleteVectorsByType(ctx context.Context, storeType string) (map[string]any, error)
}

// VectorSpaceService 对齐 MemoryVectorSpaceService
type VectorSpaceService interface {
	VectorRebuildStatus(ctx context.Context) (map[string]any, error)
	VectorStoreSnapshot(ctx context.Context, store any) map[string]any
	VectorPoolsStatus(ctx context.Context) map[string]any
	ShouldStartDualVectorAutoMigration(ctx context.Context) bool
	NormalizeDualVectorAutoMigrationProgress(ctx context.Context, progress any) map[string]any
	UpdateDualVectorAutoMigrationStage(ctx context.Context, stage string, progress map[string]any)
}
