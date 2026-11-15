package services

// 记忆维护服务，逐段对齐 memory_maintenance_service.py

import (
	"context"
	"fmt"
	"math"
	"time"

	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/utils"
)

// maxProjectionReconcileRounds 单轮关系图投影最多重试轮数
const maxProjectionReconcileRounds = 3

// MaintenanceDeps 记忆维护服务的依赖端口
type MaintenanceDeps struct {
	// Metadata 元数据存储
	Metadata func() *storage.Store
	// Graph 图存储
	Graph func() *storage.GraphStore
	// RebuildGraphFromMetadata 从元数据重建整图
	RebuildGraphFromMetadata func(ctx context.Context) (map[string]any, error)
	// ExecuteDeleteAction 执行删除动作，payload 对齐 mode/selector/requested_by/reason
	ExecuteDeleteAction func(ctx context.Context, payload map[string]any) (map[string]any, error)
	// PurgeDeletedMemory 清理已删除记忆
	PurgeDeletedMemory func(ctx context.Context, payload map[string]any) (map[string]any, error)
	// Sleep 可中断休眠
	Sleep func(ctx context.Context, seconds float64) error
	// BackgroundStopping 后台任务是否正在停止
	BackgroundStopping func() bool
	// LastMaintenanceAt 上次维护时间
	LastMaintenanceAt func() float64
	// SetLastMaintenanceAt 写回上次维护时间
	SetLastMaintenanceAt func(float64)
	// Persist 持久化内核状态
	Persist func()
}

// MaintenanceService 记忆维护服务
type MaintenanceService struct {
	*Base
	deps MaintenanceDeps
}

// NewMaintenanceService 构造记忆维护服务
func NewMaintenanceService(base *Base, deps MaintenanceDeps) *MaintenanceService {
	return &MaintenanceService{Base: base, deps: deps}
}

func (s *MaintenanceService) metadataStore() (*storage.Store, error) {
	if s.deps.Metadata == nil {
		return nil, fmt.Errorf("元数据存储未初始化")
	}
	store := s.deps.Metadata()
	if store == nil {
		return nil, fmt.Errorf("元数据存储未初始化")
	}
	return store, nil
}

func (s *MaintenanceService) graphStore() (*storage.GraphStore, error) {
	if s.deps.Graph == nil {
		return nil, fmt.Errorf("图存储未初始化")
	}
	graph := s.deps.Graph()
	if graph == nil {
		return nil, fmt.Errorf("图存储未初始化")
	}
	return graph, nil
}

// RelationLifecyclePolicyForConfig 对齐 _relation_lifecycle_policy
func (s *MaintenanceService) RelationLifecyclePolicyForConfig() utils.RelationLifecyclePolicy {
	freezeThreshold, _ := asFloat(s.Cfg("memory.prune_threshold", 0.1))
	halfLife, _ := asFloat(s.Cfg("memory.half_life_hours", 24.0))
	revive, _ := asFloat(s.Cfg("memory.revive_threshold", freezeThreshold*1.5))
	accessAlpha, _ := asFloat(s.Cfg("memory.access_reinforcement_alpha", 0.05))
	cooldownMinutes, _ := asFloat(s.Cfg("memory.access_reinforcement_cooldown_minutes", 60.0))
	reinforceAlpha, _ := asFloat(s.Cfg("memory.explicit_reinforcement_alpha", 0.5))
	weakenAlpha, _ := asFloat(s.Cfg("memory.weaken_alpha", 0.5))
	return utils.RelationLifecyclePolicy{
		HalfLifeHours:         halfLife,
		FreezeThreshold:       freezeThreshold,
		ReviveThreshold:       revive,
		AccessAlpha:           accessAlpha,
		AccessCooldownSeconds: cooldownMinutes * 60.0,
		ReinforceAlpha:        reinforceAlpha,
		WeakenAlpha:           weakenAlpha,
	}
}

// projectionAuthorityKey 对齐 _projection_authority_key
func projectionAuthorityKey(item map[string]any) string {
	return fmt.Sprintf("%s\x00%d\x00%s", asString(item["relation_hash"]), intOr(item["job_revision"], 0, math.MinInt), asString(item["lease_token"]))
}

// projectionAuthoritySignature 对齐 _projection_authority_signature
func projectionAuthoritySignature(item map[string]any) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%t", projectionAuthorityKey(item), asString(item["subject"]), asString(item["object"]), pyTruthy(item["authoritative_active"]))
}

// ReconcileRelationGraphProjectionJobs 对齐 _reconcile_relation_graph_projection_jobs
func (s *MaintenanceService) ReconcileRelationGraphProjectionJobs(ctx context.Context, resetLeases bool, batchSize int) (map[string]int, error) {
	return s.reconcileRelationGraphProjectionJobsLocked(ctx, resetLeases, batchSize, false)
}

// PublishAuthoritativeGraphProjection 对齐 _publish_authoritative_graph_projection
func (s *MaintenanceService) PublishAuthoritativeGraphProjection(ctx context.Context) (map[string]int, error) {
	graph, err := s.graphStore()
	if err != nil {
		return nil, err
	}
	result, err := s.reconcileRelationGraphProjectionJobsLocked(ctx, false, 0, true)
	if err != nil {
		return nil, err
	}
	result["node_count"] = graph.NumNodes()
	result["edge_count"] = graph.NumEdges()
	return result, nil
}

// claimAllRelationGraphProjectionJobs 对齐 _claim_all_relation_graph_projection_jobs
func (s *MaintenanceService) claimAllRelationGraphProjectionJobs(ctx context.Context, store *storage.Store, batchSize int) ([]map[string]any, error) {
	targetCount, err := store.CountClaimableRelationGraphProjectionJobs(nil)
	if err != nil {
		return nil, err
	}
	claimed := make([]map[string]any, 0, targetCount)
	for len(claimed) < targetCount {
		limit := batchSize
		if remaining := targetCount - len(claimed); remaining < limit {
			limit = remaining
		}
		batch, err := store.ClaimRelationGraphProjectionJobs(limit, 300.0, nil)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		claimed = append(claimed, batch...)
	}
	return claimed, nil
}

// failUnauthorizedProjectionJobs 对齐 _fail_unauthorized_projection_jobs
func (s *MaintenanceService) failUnauthorizedProjectionJobs(ctx context.Context, store *storage.Store, claimed []map[string]any, authorized []map[string]any) ([]map[string]any, error) {
	authorizedKeys := make(map[string]struct{}, len(authorized))
	for _, item := range authorized {
		authorizedKeys[projectionAuthorityKey(item)] = struct{}{}
	}
	unauthorized := make([]map[string]any, 0)
	for _, item := range claimed {
		key := projectionAuthorityKey(item)
		if _, ok := authorizedKeys[key]; !ok {
			unauthorized = append(unauthorized, item)
		}
	}
	if len(unauthorized) > 0 {
		if _, err := store.FailRelationGraphProjectionJobs(unauthorized, "关系图投影任务授权失效", nil); err != nil {
			return unauthorized, err
		}
	}
	return unauthorized, nil
}

// applyAuthorizedGraphProjection 对齐 _apply_authorized_graph_projection
func (s *MaintenanceService) applyAuthorizedGraphProjection(ctx context.Context, graph *storage.GraphStore, authorized []map[string]any) error {
	active := make([]map[string]any, 0, len(authorized))
	inactive := make([]map[string]any, 0, len(authorized))
	for _, item := range authorized {
		if pyTruthy(item["authoritative_active"]) {
			active = append(active, item)
		} else {
			inactive = append(inactive, item)
		}
	}
	if len(active) > 0 {
		edges := make([]storage.Edge, 0, len(active))
		hashes := make([]string, 0, len(active))
		weights := make([]float64, 0, len(active))
		for _, item := range active {
			edges = append(edges, storage.Edge{Source: asString(item["subject"]), Target: asString(item["object"])})
			hashes = append(hashes, asString(item["relation_hash"]))
			weights = append(weights, 1.0)
		}
		if _, err := graph.AddEdges(edges, weights, hashes); err != nil {
			return err
		}
	}
	if len(inactive) > 0 {
		operations := make([]storage.RelationHashOperation, 0, len(inactive))
		for _, item := range inactive {
			operations = append(operations, storage.RelationHashOperation{
				Source: asString(item["subject"]),
				Target: asString(item["object"]),
				Hash:   asString(item["relation_hash"]),
			})
		}
		graph.PruneRelationHashes(operations)
	}
	return nil
}

// reconcileRelationGraphProjectionJobsLocked 对齐 _reconcile_relation_graph_projection_jobs_locked
func (s *MaintenanceService) reconcileRelationGraphProjectionJobsLocked(ctx context.Context, resetLeases bool, batchSize int, forceFullRebuildInitial bool) (map[string]int, error) {
	store, err := s.metadataStore()
	if err != nil {
		return nil, err
	}
	graph, err := s.graphStore()
	if err != nil {
		return nil, err
	}
	if resetLeases {
		if _, err := store.ResetRelationGraphProjectionLeases(nil); err != nil {
			return nil, err
		}
	}
	limit := batchSize
	if limit == 0 {
		limit = pyIntOr(s.Cfg("memory.lifecycle_batch_size", 1000), 1000, math.MinInt)
	}
	if limit <= 0 {
		return nil, fmt.Errorf("关系图投影 batch_size 必须大于0")
	}

	claimedCount := 0
	completedCount := 0
	savedBatches := 0
	forceFullRebuild := resetLeases || forceFullRebuildInitial
	rounds := 0
	for reconcileRound := 0; reconcileRound < maxProjectionReconcileRounds; reconcileRound++ {
		rounds = reconcileRound + 1
		claimed, err := s.claimAllRelationGraphProjectionJobs(ctx, store, limit)
		if err != nil {
			return nil, err
		}
		if len(claimed) == 0 {
			if forceFullRebuild {
				// 启动时即使队列为空，也不能把旧磁盘快照当作活跃态事实源
				if err := s.rebuildGraphFromMetadata(ctx); err != nil {
					return nil, err
				}
				if err := graph.Save(""); err != nil {
					return nil, err
				}
				savedBatches++
			}
			break
		}
		claimedCount += len(claimed)
		initialAuthorized, err := store.AuthorizeRelationGraphProjectionJobs(claimed)
		if err != nil {
			return nil, err
		}
		unauthorized, err := s.failUnauthorizedProjectionJobs(ctx, store, claimed, initialAuthorized)
		if err != nil {
			return nil, err
		}
		authorized, err := store.AuthorizeRelationGraphProjectionJobs(initialAuthorized)
		if err != nil {
			return nil, err
		}
		retryUnauthorized, err := s.failUnauthorizedProjectionJobs(ctx, store, initialAuthorized, authorized)
		if err != nil {
			return nil, err
		}
		unauthorized = append(unauthorized, retryUnauthorized...)
		if len(authorized) == 0 {
			if forceFullRebuild {
				if err := s.rebuildGraphFromMetadata(ctx); err != nil {
					return nil, err
				}
				if err := graph.Save(""); err != nil {
					return nil, err
				}
				savedBatches++
			}
			break
		}
		if forceFullRebuild {
			if err := s.rebuildGraphFromMetadata(ctx); err != nil {
				return nil, err
			}
		} else if err := s.applyAuthorizedGraphProjection(ctx, graph, authorized); err != nil {
			return nil, err
		}

		// 图已在内存中变更，发布快照前再次验证同一租约和任务版本
		beforeSave, err := store.AuthorizeRelationGraphProjectionJobs(authorized)
		if err != nil {
			return nil, err
		}
		beforeSignatures := make(map[string]struct{}, len(beforeSave))
		for _, item := range beforeSave {
			beforeSignatures[projectionAuthoritySignature(item)] = struct{}{}
		}
		authorizedSignatures := make(map[string]struct{}, len(authorized))
		for _, item := range authorized {
			authorizedSignatures[projectionAuthoritySignature(item)] = struct{}{}
		}
		if !stringSetEqual(beforeSignatures, authorizedSignatures) {
			if _, err := store.ReenqueueAuthoritativeRelationGraphProjectionJobs(authorized, nil); err != nil {
				return nil, err
			}
			forceFullRebuild = true
			if reconcileRound+1 >= maxProjectionReconcileRounds {
				// 当前轮已经改过内存图，超限返回前先恢复到此刻的权威态
				if err := s.rebuildGraphFromMetadata(ctx); err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("关系图投影在保存前持续变化，已保留任务等待重试")
			}
			continue
		}
		if err := graph.Save(""); err != nil {
			if _, failErr := store.FailRelationGraphProjectionJobs(authorized, err.Error(), nil); failErr != nil {
				return nil, failErr
			}
			return nil, err
		}
		savedBatches++
		completed, err := store.CompleteRelationGraphProjectionJobs(authorized, nil)
		if err != nil {
			return nil, err
		}
		completedCount += completed
		if completed != len(authorized) {
			// save 已成功但 CAS 失败时，必须先恢复 durable intent
			if _, err := store.ReenqueueAuthoritativeRelationGraphProjectionJobs(authorized, nil); err != nil {
				return nil, err
			}
			forceFullRebuild = true
			if reconcileRound+1 >= maxProjectionReconcileRounds {
				// 最后一轮的旧快照已经落盘，必须在抛错前补发一次权威全图快照
				if err := s.rebuildGraphFromMetadata(ctx); err != nil {
					return nil, err
				}
				if err := graph.Save(""); err != nil {
					return nil, err
				}
				savedBatches++
				return nil, fmt.Errorf("关系图投影 CAS 持续冲突，已保留任务等待重试")
			}
			continue
		}
		forceFullRebuild = false
		if len(unauthorized) > 0 {
			break
		}
	}
	if rounds >= maxProjectionReconcileRounds {
		remaining, err := store.CountClaimableRelationGraphProjectionJobs(nil)
		if err != nil {
			return nil, err
		}
		if remaining > 0 {
			return nil, fmt.Errorf("关系图投影单轮任务持续增长，已保留剩余任务等待重试")
		}
	}
	return map[string]int{"claimed": claimedCount, "completed": completedCount, "saved_batches": savedBatches}, nil
}

func (s *MaintenanceService) rebuildGraphFromMetadata(ctx context.Context) error {
	if s.deps.RebuildGraphFromMetadata == nil {
		return fmt.Errorf("关系图重建端口未注入")
	}
	_, err := s.deps.RebuildGraphFromMetadata(ctx)
	return err
}

func stringSetEqual(a map[string]struct{}, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for key := range a {
		if _, ok := b[key]; !ok {
			return false
		}
	}
	return true
}

// ApplyRelationLifecycleEventWithStrength 对齐 apply_relation_lifecycle_event
func (s *MaintenanceService) ApplyRelationLifecycleEventWithStrength(ctx context.Context, hashes []string, event utils.RelationLifecycleEvent, strength float64, now *float64) ([]map[string]any, error) {
	store, err := s.metadataStore()
	if err != nil {
		return nil, err
	}
	eventTime := float64(time.Now().UnixNano()) / 1e9
	if now != nil {
		eventTime = *now
	}
	transitions, err := store.ApplyRelationLifecycleEvent(hashes, event, s.RelationLifecyclePolicyForConfig(), eventTime, strength)
	if err != nil {
		return nil, err
	}
	changed := false
	for _, item := range transitions {
		if pyTruthy(item["was_inactive"]) != pyTruthy(item["is_inactive"]) {
			changed = true
			break
		}
	}
	if changed {
		if _, err := s.ReconcileRelationGraphProjectionJobs(ctx, false, 0); err != nil {
			return transitions, err
		}
	}
	return transitions, nil
}

// ApplyRelationLifecycleEvent 对齐 apply_relation_lifecycle_event 的接口签名
func (s *MaintenanceService) ApplyRelationLifecycleEvent(ctx context.Context, hashes []string, event utils.RelationLifecycleEvent) error {
	_, err := s.ApplyRelationLifecycleEventWithStrength(ctx, hashes, event, 1.0, nil)
	return err
}

// ProcessFreezeAndPrune 对齐 _process_freeze_and_prune
func (s *MaintenanceService) ProcessFreezeAndPrune(ctx context.Context) error {
	store, err := s.metadataStore()
	if err != nil {
		return err
	}
	policy := s.RelationLifecyclePolicyForConfig()
	freezeDuration := spaceFloatAny(s.Cfg("memory.freeze_duration_hours", 24.0)) * 3600.0
	if freezeDuration < 0 {
		return fmt.Errorf("memory.freeze_duration_hours 不能小于0")
	}
	now := float64(time.Now().UnixNano()) / 1e9

	batchSize := pyIntOr(s.Cfg("memory.lifecycle_batch_size", 1000), 1000, math.MinInt)
	if batchSize <= 0 {
		return fmt.Errorf("memory.lifecycle_batch_size 必须大于0")
	}
	for {
		evaluated, err := store.EvaluateDueRelationLifecycles(policy, now, batchSize)
		if err != nil {
			return err
		}
		if len(evaluated.Frozen)+len(evaluated.Scheduled) < batchSize {
			break
		}
	}

	if _, err := s.ReconcileRelationGraphProjectionJobs(ctx, false, batchSize); err != nil {
		return err
	}

	cutoff := now - freezeDuration
	expiredRows, err := store.GetDecayPruneCandidateRows(cutoff, now, policy, batchSize)
	if err != nil {
		return err
	}
	if len(expiredRows) == 0 {
		return nil
	}
	deletedHashes := make([]string, 0, len(expiredRows))
	for _, item := range expiredRows {
		deletedHashes = append(deletedHashes, asString(item["hash"]))
	}
	expectedStates := make(map[string]any, len(expiredRows))
	for _, item := range expiredRows {
		state := make(map[string]any, 9)
		for _, key := range []string{
			"expected_lifecycle_revision", "expected_retention_strength", "expected_retention_anchor_at",
			"expected_inactive_since", "expected_inactive_reason", "expected_is_inactive",
			"expected_is_permanent", "expected_is_pinned", "expected_protected_until",
		} {
			state[key] = item[key]
		}
		expectedStates[asString(item["hash"])] = state
	}
	if s.deps.ExecuteDeleteAction == nil {
		return fmt.Errorf("删除执行器未注入")
	}
	result, err := s.deps.ExecuteDeleteAction(ctx, map[string]any{
		"mode":         "relation",
		"selector":     map[string]any{"hashes": deletedHashes, "expected_relation_states": expectedStates},
		"requested_by": "memory_lifecycle",
		"reason":       "lifecycle_decay_archive",
	})
	if err != nil {
		return err
	}
	if !pyTruthy(result["success"]) {
		message := asString(result["error"])
		if message == "" {
			message = "关系生命周期归档失败"
		}
		return fmt.Errorf("%s", message)
	}
	return nil
}

// OrphanGCPhase 对齐 _orphan_gc_phase
func (s *MaintenanceService) OrphanGCPhase(ctx context.Context) error {
	store, err := s.metadataStore()
	if err != nil {
		return err
	}
	graph, err := s.graphStore()
	if err != nil {
		return err
	}
	orphanCfg := s.CfgMap("memory.orphan")
	enable, ok := orphanCfg["enable_soft_delete"]
	if !ok || !pyTruthy(enable) {
		if ok && !pyTruthy(enable) {
			return nil
		}
	}
	entityRetention := mathMax(0.0, spaceFloatAny(orphanCfg["entity_retention_days"])) * 86400.0
	if _, ok := orphanCfg["entity_retention_days"]; !ok {
		entityRetention = 7.0 * 86400.0
	}
	graceValue := 24.0
	if value, ok := orphanCfg["sweep_grace_hours"]; ok {
		graceValue, _ = asFloat(value)
	}
	graceHours := mathMax(0.0, graceValue)

	execute := func(payload map[string]any) (map[string]any, error) {
		if s.deps.ExecuteDeleteAction == nil {
			return nil, fmt.Errorf("删除执行器未注入")
		}
		return s.deps.ExecuteDeleteAction(ctx, payload)
	}

	isolated := graph.GetIsolatedNodes(true)
	if len(isolated) > 0 {
		entityHashes, err := store.GetEntityGCCandidates(isolated, entityRetention)
		if err != nil {
			return err
		}
		if len(entityHashes) > 0 {
			result, err := execute(map[string]any{
				"mode":         "entity",
				"selector":     map[string]any{"hashes": entityHashes},
				"requested_by": "memory_maintenance",
				"reason":       "entity_orphan_expired",
			})
			if err != nil {
				return err
			}
			if !pyTruthy(result["success"]) {
				message := asString(result["error"])
				if message == "" {
					message = "实体回收失败"
				}
				return fmt.Errorf("%s", message)
			}
		}
	}

	paragraphHashes, err := store.GetExpiredParagraphHashes(storage.ExpiredParagraphOptions{Limit: 1000})
	if err != nil {
		return err
	}
	if len(paragraphHashes) > 0 {
		result, err := execute(map[string]any{
			"mode":         "paragraph",
			"selector":     map[string]any{"hashes": paragraphHashes},
			"requested_by": "memory_maintenance",
			"reason":       "paragraph_explicit_expiration",
		})
		if err != nil {
			return err
		}
		if !pyTruthy(result["success"]) {
			message := asString(result["error"])
			if message == "" {
				message = "段落显式过期失败"
			}
			return fmt.Errorf("%s", message)
		}
	}

	if s.deps.PurgeDeletedMemory == nil {
		return fmt.Errorf("删除清理端口未注入")
	}
	if _, err := s.deps.PurgeDeletedMemory(ctx, map[string]any{"grace_hours": graceHours, "limit": 1000}); err != nil {
		return err
	}
	return nil
}

// RunMemoryMaintenanceCycle 对齐 _run_memory_maintenance_cycle
func (s *MaintenanceService) RunMemoryMaintenanceCycle(ctx context.Context, intervalHours float64) error {
	_ = intervalHours
	if err := s.ProcessFreezeAndPrune(ctx); err != nil {
		return err
	}
	if err := s.OrphanGCPhase(ctx); err != nil {
		return err
	}
	now := float64(time.Now().UnixNano()) / 1e9
	if s.deps.SetLastMaintenanceAt != nil {
		s.deps.SetLastMaintenanceAt(now)
	}
	if s.deps.Persist != nil {
		s.deps.Persist()
	}
	return nil
}

// MemoryMaintenanceLoop 对齐 _memory_maintenance_loop
func (s *MaintenanceService) MemoryMaintenanceLoop(ctx context.Context) error {
	for {
		if s.backgroundStopping() {
			return nil
		}
		intervalValue := s.Cfg("memory.base_decay_interval_hours", 1.0)
		intervalHours := 1.0
		if intervalValue != nil {
			intervalHours, _ = asFloat(intervalValue)
		}
		intervalHours = mathMax(1.0/60.0, intervalHours)
		if err := s.sleep(ctx, mathMax(60.0, intervalHours*3600.0)); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.LoggerWarn("memory_maintenance cycle 异常，将在下一周期重试: %v", err)
			continue
		}
		if s.backgroundStopping() {
			return nil
		}
		if !pyTruthy(s.Cfg("memory.enabled", true)) {
			continue
		}
		if err := s.RunMemoryMaintenanceCycle(ctx, intervalHours); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.LoggerWarn("memory_maintenance cycle 异常，将在下一周期重试: %v", err)
		}
	}
}

func (s *MaintenanceService) backgroundStopping() bool {
	return s.deps.BackgroundStopping != nil && s.deps.BackgroundStopping()
}

func (s *MaintenanceService) sleep(ctx context.Context, seconds float64) error {
	if s.deps.Sleep != nil {
		return s.deps.Sleep(ctx, seconds)
	}
	timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
