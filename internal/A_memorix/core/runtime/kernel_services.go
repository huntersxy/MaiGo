package memruntime

// 内核服务集合，对齐 SDKMemoryKernel.__init__ 中构造的 31 个服务对象

// KernelServices 保存内核依赖的全部运行时服务
type KernelServices struct {
	VectorSpace         VectorSpaceService
	GraphAdmin          GraphAdminService
	BackgroundTask      BackgroundTaskService
	BundleAdmin         BundleAdminService
	ChatFilter          ChatFilterService
	DeleteAdmin         DeleteAdminService
	DualVectorMigration DualVectorMigrationService
	DualVectorState     DualVectorStateService
	EmbeddingState      EmbeddingStateService
	CorrectionAdmin     CorrectionAdminService
	RuntimeConfig       RuntimeConfigService
	RuntimeDependency   RuntimeDependencyService
	RuntimeLifecycle    RuntimeLifecycleService
	Search              SearchService
	SearchHit           SearchHitService
	SourceAdmin         SourceAdminService
	Feedback            FeedbackService
	FactAdmin           FactAdminService
	VectorRuntime       VectorRuntimeService
	Ingest              IngestService
	Maintenance         MaintenanceService
	EpisodeAdmin        EpisodeAdminService
	ProfileAdmin        ProfileAdminService
	ImportTuningAdmin   ImportTuningAdminService
	Image               ImageService
	V5Admin             V5AdminService
	VectorDelete        VectorDeleteService
	VectorRecovery      VectorRecoveryService
	RequestDedup        RequestDedupService
	Summary             SummaryService
	Stats               StatsService
}

// Validate 返回缺失的必需服务名，空切片表示可用于初始化
func (s KernelServices) Validate() []string {
	missing := make([]string, 0, 8)
	checks := []struct {
		name string
		ok   bool
	}{
		{"runtime_lifecycle", s.RuntimeLifecycle != nil},
		{"runtime_config", s.RuntimeConfig != nil},
		{"runtime_dependency", s.RuntimeDependency != nil},
		{"search", s.Search != nil},
		{"ingest", s.Ingest != nil},
		{"profile_admin", s.ProfileAdmin != nil},
		{"stats", s.Stats != nil},
		{"maintenance", s.Maintenance != nil},
	}
	for _, check := range checks {
		if !check.ok {
			missing = append(missing, check.name)
		}
	}
	return missing
}
