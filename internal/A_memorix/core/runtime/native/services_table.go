package native

// 服务装配表，对齐 Python SDKMemoryKernel.__init__ 中 31 个服务对象的构造顺序
// 表项按依赖顺序排列，新增服务只需追加一行，缺失服务由 Report 如实记录

import (
	"fmt"
	"log/slog"

	imagememory "maibot-go/internal/A_memorix/core/image"
	memruntime "maibot-go/internal/A_memorix/core/runtime"
	"maibot-go/internal/A_memorix/core/runtime/services"
	"maibot-go/internal/A_memorix/core/utils"
)

// serviceSlot 单个服务槽位，fill 返回 false 表示依赖不足、暂不装配
type serviceSlot struct {
	name string
	fill func(runtime *Runtime, target *memruntime.KernelServices) bool
}

// serviceSlots 有序服务表，顺序与 Python __init__ 的服务构造顺序一致
var serviceSlots = []serviceSlot{
	{"runtime_lifecycle", func(rt *Runtime, target *memruntime.KernelServices) bool {
		if rt.lifecycle == nil {
			return false
		}
		target.RuntimeLifecycle = rt.lifecycle
		return true
	}},
	{"request_dedup", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.RequestDedup = services.NewRequestDedupService()
		return true
	}},
	{"runtime_config", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.RuntimeConfig = services.NewRuntimeConfigService(rt.runtimeConfigDeps())
		return true
	}},
	{"runtime_dependency", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.RuntimeDependency = services.NewRuntimeDependencyService(rt.runtimeDependencyDeps())
		return true
	}},
	{"dual_vector_migration", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.DualVectorMigration = rt.dualMigrationService()
		return true
	}},
	{"vector_space", func(rt *Runtime, target *memruntime.KernelServices) bool {
		service := services.NewVectorSpaceService(rt.base, rt.vectorSpaceDeps())
		rt.vectorSpace = service
		target.VectorSpace = &vectorSpaceAdapter{
			inner:     service,
			migration: rt.dualMigrationService(),
		}
		return true
	}},
	{"chat_filter", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.ChatFilter = &chatFilterAdapter{inner: services.NewChatFilterService(rt.base)}
		return true
	}},
	{"search", func(rt *Runtime, target *memruntime.KernelServices) bool {
		inner := services.NewMemorySearchService(rt.base, rt.searchDeps())
		target.Search = &searchAdapter{inner: inner, pluginConfig: func() map[string]any {
			config, err := rt.buildRuntimeConfig()
			if err != nil {
				rt.logger.Warning(fmt.Sprintf("[native] 构造检索运行时配置失败: %v", err))
				return map[string]any{}
			}
			return config
		}}
		return true
	}},
	{"ingest", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.Ingest = &ingestAdapter{inner: services.NewIngestService(rt.base, rt.ingestDeps())}
		return true
	}},
	{"profile_admin", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.ProfileAdmin = services.NewProfileAdminService(rt.base, rt.profileAdminDeps())
		return true
	}},
	{"maintenance", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.Maintenance = services.NewMaintenanceService(rt.base, rt.maintenanceDeps())
		return true
	}},
	{"background_task", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.BackgroundTask = services.NewBackgroundTaskService(rt.base, rt.backgroundTaskDeps())
		return true
	}},
	{"search_hit", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.SearchHit = services.NewSearchHitService(rt.base, rt.searchHitDeps())
		return true
	}},
	{"stats", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.Stats = services.NewStatsService(rt.statsDeps())
		return true
	}},
	{"fact_admin", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.FactAdmin = services.NewFactAdminService(rt.base, rt.factAdminDeps())
		return true
	}},
	{"source_admin", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.SourceAdmin = services.NewSourceAdminService(rt.sourceAdminDeps())
		return true
	}},
	{"episode_admin", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.EpisodeAdmin = services.NewEpisodeAdminService(rt.episodeAdminDeps())
		return true
	}},
	{"dual_vector_state", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.DualVectorState = services.NewDualVectorStateService(rt.base, rt.dualVectorStateDeps())
		return true
	}},
	{"embedding_state", func(rt *Runtime, target *memruntime.KernelServices) bool {
		rt.embeddingState = services.NewEmbeddingStateService(rt.base, rt.embeddingStateDeps())
		target.EmbeddingState = rt.embeddingState
		return true
	}},
	{"graph_admin", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.GraphAdmin = services.NewGraphAdminService(rt.graphAdminOptions())
		return true
	}},
	{"delete_admin", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.DeleteAdmin = services.NewDeleteAdminService(rt.deleteAdminOptions())
		return true
	}},
	{"correction_admin", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.CorrectionAdmin = services.NewCorrectionAdminService(rt.correctionAdminOptions())
		return true
	}},
	{"v5_admin", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.V5Admin = services.NewV5AdminService(rt.base, rt.v5AdminDeps())
		return true
	}},
	{"vector_delete", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.VectorDelete = services.NewVectorDeleteService(rt.vectorDeleteDeps())
		return true
	}},
	{"bundle_admin", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.BundleAdmin = services.NewBundleAdminServiceWithDeps(rt.base, services.BundleAdminDeps{
			ImageRuntime:           rt.imageRuntime,
			ParagraphVectorStore:   rt.paragraphPoolStore,
			GraphVectorStore:       rt.graphPoolStore,
			DualVectorPoolsEnabled: rt.kernel.DualVectorPoolsEnabled,
			GraphVectorID: func(itemType string, hashValue string) string {
				if typed := rt.kernel.GraphVectorID(itemType, hashValue); typed != "" {
					return typed
				}
				return hashValue
			},
			StoredEmbeddingFingerprint: rt.kernel.StoredEmbeddingFingerprint,
			CurrentEmbeddingFingerprint: func() map[string]any {
				return rt.kernel.CurrentEmbeddingFingerprintForValidation(nil)
			},
			RelationVectorsEnabled: rt.kernel.RelationVectorsEnabled,
			EnsureParagraphVector:  rt.kernel.EnsureParagraphVector,
			EnsureEntityVector:     rt.kernel.EnsureEntityVector,
			EnsureRelationVector:   rt.kernel.EnsureRelationVector,
			PersistVectors:         rt.persistBundleVectorStores,
			RebuildGraph:           kernelGraphRebuilder{runtime: rt}.RebuildGraphFromMetadata,
		})
		return true
	}},
	{"vector_runtime", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.VectorRuntime = services.NewVectorRuntimeService(rt.base, rt.vectorRuntimeDeps())
		return true
	}},
	{"vector_recovery", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.VectorRecovery = services.NewVectorRecoveryService(rt.base, rt.vectorRecoveryDeps())
		return true
	}},
	{"feedback", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.Feedback = services.NewFeedbackService(rt.base, rt.feedbackServiceDeps(target))
		return true
	}},
	{"summary", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.Summary = &summaryAdapter{inner: services.NewSummaryService(rt.summaryServiceDeps())}
		return true
	}},
	{"image", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.Image = services.NewImageService(rt.base, services.ImageServiceDeps{
			Runtime: func() *imagememory.ImageMemoryRuntime { return rt.imageRuntime },
		})
		return true
	}},
	{"import_tuning_admin", func(rt *Runtime, target *memruntime.KernelServices) bool {
		target.ImportTuningAdmin = services.NewImportTuningAdminService(services.ImportTuningAdminDeps{
			Initialize:    rt.kernel.Initialize,
			ImportManager: func() *utils.WebImportManager { return rt.importManager },
			TuningManager: func() *utils.RetrievalTuningManager { return rt.tuningManager },
		})
		return true
	}},
}

// buildServices 按表装配服务，返回服务集合与真实报告
func (r *Runtime) buildServices() (memruntime.KernelServices, Report, error) {
	report := Report{
		DataDir:            r.kernel.DataDir(),
		EmbeddingDimension: r.kernel.EmbeddingDimension(),
	}
	if r.lifecycle == nil {
		r.base = r.servicesBase()
		r.lifecycle = services.NewRuntimeLifecycleService(r.base, r.lifecycleDeps())
	}
	var target memruntime.KernelServices
	for _, slot := range serviceSlots {
		if slot.fill == nil {
			continue
		}
		if slot.fill(r, &target) {
			report.Filled = append(report.Filled, slot.name)
			continue
		}
		report.Missing = append(report.Missing, slot.name)
	}
	report.MissingRequired = target.Validate()
	// Validate 只覆盖必需服务；把不在表内的必需服务并入 Missing，避免报告自相矛盾
	known := make(map[string]struct{}, len(report.Missing))
	for _, name := range report.Missing {
		known[name] = struct{}{}
	}
	for _, name := range report.MissingRequired {
		if _, ok := known[name]; ok {
			continue
		}
		report.Missing = append(report.Missing, name)
		known[name] = struct{}{}
	}
	r.mu.Lock()
	report.Degraded = append(report.Degraded, r.report.Degraded...)
	r.mu.Unlock()
	if len(report.MissingRequired) > 0 {
		r.logger.Warning(fmt.Sprintf("[native] 必需服务缺失，检索与写入通道按降级运行: %v", report.MissingRequired))
	}
	return target, report, nil
}

// rebuildServices 在存储就绪后按真实依赖重建服务表，保留既有生命周期实例
func (r *Runtime) rebuildServices() {
	lifecycle := r.lifecycle
	target, report, err := r.buildServices()
	if err != nil {
		r.logger.Error(fmt.Sprintf("[native] 重建服务表失败: %v", err))
		return
	}
	if lifecycle != nil {
		target.RuntimeLifecycle = lifecycle
	}
	r.setServices(target)
	r.mu.Lock()
	r.report = report
	r.mu.Unlock()
	r.logger.Info(fmt.Sprintf("[native] 服务表已按就绪存储重建: filled=%v, missing=%v", report.Filled, report.MissingRequired))
}

// slogLoggerOrNil 返回服务层可用的日志器，暂缺 utils 到 slog 的直接桥接时回退默认日志器
func slogLoggerOrNil() *slog.Logger {
	return slog.Default()
}
