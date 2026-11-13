package memruntime

// 内核依赖的向量运行时与后台任务服务接口

import (
	"context"

	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// VectorRecoveryService 对齐 MemoryVectorRecoveryService
type VectorRecoveryService interface {
	VectorHealthSnapshot(ctx context.Context) map[string]any
	SetVectorHealth(ctx context.Context, patch map[string]any)
	DisableVectorChannel(ctx context.Context, err error)
	RecoverKnownVectorFailure(ctx context.Context, err error) bool
	ResumeVectorRecoveryIfNeeded(ctx context.Context) error
	CopyLegacyVectorsOnce(ctx context.Context, batchSize int) (map[string]any, error)
	CleanupVectorQuarantine(ctx context.Context) (map[string]any, error)
	RecoverEmbeddingOnce(ctx context.Context, sampleText string) (map[string]any, error)
	V1ReconciliationEvidenceRoot(ctx context.Context) string
	V1ValidHashesForPool(ctx context.Context, pool string) []string
}

// VectorRuntimeService 对齐 MemoryVectorRuntimeService
type VectorRuntimeService interface {
	VectorRebuildStatus(ctx context.Context) (map[string]any, error)
	VectorStoreSnapshot(ctx context.Context, store *storage.VectorStore) map[string]any
	VectorPoolsStatus(ctx context.Context) map[string]any
	CountVectorRebuildTargets(ctx context.Context) map[string]int
	TableHasColumn(ctx context.Context, table string, column string) bool
	ActiveRowFilterSQL(ctx context.Context, table string) string
	BackfillMissingDualVectorPoolEntries(ctx context.Context, batchSize int) (map[string]any, error)
	RefreshRuntimeDependents(ctx context.Context, preserveManagers bool)
	EncodeAndAddRebuildVectors(ctx context.Context, params map[string]any) (map[string]any, error)
	CopyRebuildVectorsFromStore(ctx context.Context, params map[string]any) (map[string]int, error)
	CopyOrEncodeDualRebuildVectors(ctx context.Context, params map[string]any) (map[string]any, error)
	RebuildAllVectors(ctx context.Context) (map[string]any, error)
	RebuildAllVectorsLocked(ctx context.Context) (map[string]any, error)
	DetectCurrentEmbeddingDimensionForRebuild(ctx context.Context) int
	MemoryRuntimeAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
}

// DualVectorStateService 对齐 MemoryDualVectorStateService
type DualVectorStateService interface {
	VectorPoolMode(ctx context.Context) string
	DualVectorPoolsConfigEnabled(ctx context.Context) bool
	DualVectorPoolsEnabled(ctx context.Context) bool
	VectorsRoot(ctx context.Context) string
	ParagraphVectorDir(ctx context.Context) string
	GraphVectorDir(ctx context.Context) string
	DualVectorReadyManifestPath(ctx context.Context) string
	ReadDualVectorReadyManifest(ctx context.Context) map[string]any
	DualVectorReady(ctx context.Context, expectedDimension *int) bool
	WriteDualVectorReadyManifest(ctx context.Context, stats map[string]map[string]int, migrationStats map[string]map[string]int, generationReason string, buildFingerprint map[string]any)
	RemoveDualVectorReadyManifest(ctx context.Context)
	RefreshDualVectorReadyManifestFromStores(ctx context.Context)
	ClearLegacySingleVectorFilesAfterDualReady(ctx context.Context)
	PrepareDualVectorBuildDirs(ctx context.Context) (string, string, string)
	ActivateDualVectorBuildDirs(ctx context.Context, buildRoot string)
	CleanupStaleDualVectorBuildDirs(ctx context.Context)
	MakeVectorStore(ctx context.Context, dataDir string, dimension *int) (*storage.VectorStore, error)
	SaveVectorStore(ctx context.Context, store *storage.VectorStore)
	ReloadDualVectorStoresFromDisk(ctx context.Context) bool
	TryRecoverDualReadyManifest(ctx context.Context) bool
	DropDualBuildRoot(ctx context.Context, buildRoot string)
	ParagraphStore(ctx context.Context) *storage.VectorStore
	GraphVectorStore(ctx context.Context) *storage.VectorStore
	GraphVectorID(itemType string, hashValue string) string
}

// DualVectorMigrationService 对齐 MemoryDualVectorMigrationService
type DualVectorMigrationService interface {
	ShouldStartAutoMigration(ctx context.Context) bool
	NormalizeAutoMigrationProgress(ctx context.Context, progress any) map[string]any
	UpdateAutoMigrationStage(ctx context.Context, stage string, progress map[string]any)
}

// BackgroundTaskService 对齐 MemoryBackgroundTaskService
type BackgroundTaskService interface {
	StartBackgroundTasks(ctx context.Context) error
	EnsureBackgroundTask(ctx context.Context, name string, factory func(ctx context.Context) error) error
	SleepBackground(ctx context.Context, seconds float64) error
	DualVectorAutoMigrationLoop(ctx context.Context) error
	StopBackgroundTasks(ctx context.Context) error
	CancelAllBackgroundTasks()
	AutoSaveLoop(ctx context.Context) error
	VectorIndexTrainingLoop(ctx context.Context) error
	TrainRuntimeVectorIndexesOnce(ctx context.Context) (map[string]map[string]any, error)
	EpisodeMaterializationLoop(ctx context.Context) error
	EmbeddingProbeLoop(ctx context.Context) error
	ParagraphVectorBackfillLoop(ctx context.Context) error
	PersonProfileRefreshLoop(ctx context.Context) error
	PersonProfileRefreshQueueLoop(ctx context.Context) error
}

// MaintenanceService 对齐 MemoryMaintenanceService
type MaintenanceService interface {
	ApplyRelationLifecycleEvent(ctx context.Context, hashes []string, event utils.RelationLifecycleEvent) error
	MemoryMaintenanceLoop(ctx context.Context) error
	RunMemoryMaintenanceCycle(ctx context.Context, intervalHours float64) error
	ProcessFreezeAndPrune(ctx context.Context) error
	OrphanGCPhase(ctx context.Context) error
}

// StatsService 对齐 MemoryStatsService
type StatsService interface {
	MemoryStats(ctx context.Context) (map[string]any, error)
}
