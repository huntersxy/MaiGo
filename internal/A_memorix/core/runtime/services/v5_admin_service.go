package services

// V5 记忆维护服务，逐段对齐 v5_admin_service.py 的 MemoryV5AdminService

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"MaiGo/internal/A_memorix/core/storage"
	memorixutils "MaiGo/internal/A_memorix/core/utils"
)

// V5AdminDeps V5 管理服务的依赖端口
type V5AdminDeps struct {
	// Initialize 确保运行时已初始化
	Initialize func(ctx context.Context) error
	// GetRelation 读取关系，不存在返回 nil
	GetRelation func(ctx context.Context, hashValue string) map[string]any
	// GetDeletedRelation 读取已删除关系，不存在返回 nil
	GetDeletedRelation func(ctx context.Context, hashValue string) map[string]any
	// SearchRelationHashesByText 按文本检索关系 hash
	SearchRelationHashesByText func(ctx context.Context, text string, limit int) ([]string, error)
	// SearchDeletedRelationHashesByText 按文本检索已删除关系 hash
	SearchDeletedRelationHashesByText func(ctx context.Context, text string, limit int) ([]string, error)
	// GetRelationsBySubject 按主语读取关系
	GetRelationsBySubject func(ctx context.Context, subject string) ([]map[string]any, error)
	// GetDeletedRelations 读取回收站
	GetDeletedRelations func(ctx context.Context, limit int) ([]map[string]any, error)
	// GetMemoryStatusSummary 记忆状态汇总
	GetMemoryStatusSummary func(ctx context.Context, now float64) (map[string]int, error)
	// GetRelationStatusBatch 批量关系状态
	GetRelationStatusBatch func(ctx context.Context, hashes []string) (map[string]map[string]any, error)
	// ProtectRelations 设置保护期或固定
	ProtectRelations func(ctx context.Context, hashes []string, isPinned bool, ttlSeconds float64) error
	// UpdateRelationsProtection 更新保护与强化时间
	UpdateRelationsProtection func(ctx context.Context, hashes []string, protectedUntil *float64, isPinned *bool, lastReinforced *float64) error
	// RecordV5Operation 记录 V5 操作
	RecordV5Operation func(ctx context.Context, action string, target string, hashes []string, reason string, updatedBy string, result map[string]any) (map[string]any, error)
	// ApplyRelationLifecycleEvent 应用生命周期事件，返回每条关系的迁移结果
	ApplyRelationLifecycleEvent func(ctx context.Context, hashes []string, event memorixutils.RelationLifecycleEvent, strength float64, now float64) ([]map[string]any, error)
	// RelationLifecyclePolicy 当前生命周期策略
	RelationLifecyclePolicy func(ctx context.Context) memorixutils.RelationLifecyclePolicy
	// Persist 持久化状态
	Persist func(ctx context.Context)
	// RestoreDeletedRelations 恢复已删除关系
	RestoreDeletedRelations func(ctx context.Context, hashes []string, requestedBy string, reason string) (map[string]any, error)
	// LastMaintenanceAt 上次维护时间
	LastMaintenanceAt func() any
}

// V5AdminService V5 记忆维护服务
type V5AdminService struct {
	*Base
	deps V5AdminDeps
}

// NewV5AdminService 构造 V5 管理服务
func NewV5AdminService(base *Base, deps V5AdminDeps) *V5AdminService {
	return &V5AdminService{Base: base, deps: deps}
}

func (s *V5AdminService) initialized(ctx context.Context) error {
	if s.deps.Initialize == nil {
		return nil
	}
	return s.deps.Initialize(ctx)
}

// MaintainMemory 对齐 maintain_memory
func (s *V5AdminService) MaintainMemory(ctx context.Context, action string, target string, hours *float64, reason string, limit int) (map[string]any, error) {
	if err := s.initialized(ctx); err != nil {
		return nil, err
	}
	act := strings.ToLower(strings.TrimSpace(action))
	if limit < 1 {
		limit = 50
	}
	if act == "recycle_bin" {
		items, err := s.deps.GetDeletedRelations(ctx, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "items": items, "count": len(items)}, nil
	}
	var hashes []string
	if act == "restore" {
		hashes = s.ResolveDeletedRelationHashes(ctx, target)
	} else {
		hashes = s.ResolveRelationHashes(ctx, target)
	}
	if len(hashes) == 0 {
		return map[string]any{"success": false, "detail": "未命中可维护关系"}, nil
	}
	switch act {
	case "reinforce", "freeze":
		result, err := s.ApplyV5RelationAction(ctx, act, hashes, 1.0)
		if err != nil {
			return nil, err
		}
		if !pyTruthy(result["success"]) {
			detail := asString(result["error"])
			if detail == "" {
				detail = "生命周期操作失败"
			}
			return map[string]any{"success": false, "detail": detail}, nil
		}
		return result, nil
	case "protect":
		ttlSeconds := math.Max(0.0, spaceFloatPtr(hours)) * 3600.0
		if err := s.deps.ProtectRelations(ctx, hashes, ttlSeconds <= 0, ttlSeconds); err != nil {
			return nil, err
		}
	case "restore":
		return s.restoreRelationHashes(ctx, hashes, "", reason)
	default:
		return map[string]any{"success": false, "detail": fmt.Sprintf("不支持的维护动作: %s", act)}, nil
	}
	if s.deps.Persist != nil {
		s.deps.Persist(ctx)
	}
	return map[string]any{"success": true, "detail": fmt.Sprintf("%s %d 条关系", act, len(hashes))}, nil
}

// MemoryV5Admin 对齐 memory_v5_admin
func (s *V5AdminService) MemoryV5Admin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if err := s.initialized(ctx); err != nil {
		return nil, err
	}
	act := strings.ToLower(strings.TrimSpace(action))
	target := strings.TrimSpace(stringOr(asString(payload["target"]), asString(payload["query"])))
	reason := strings.TrimSpace(asString(payload["reason"]))
	updatedBy := strings.TrimSpace(stringOr(asString(payload["updated_by"]), asString(payload["requested_by"])))
	limit := pyIntOr(payload["limit"], 50, 1)
	if limit < 1 {
		limit = 50
	}
	if act == "recycle_bin" {
		items, err := s.deps.GetDeletedRelations(ctx, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "items": items, "count": len(items)}, nil
	}
	if act == "status" {
		return s.MemoryV5Status(ctx, target, limit)
	}
	if act == "restore" {
		hashes := s.ResolveDeletedRelationHashes(ctx, target)
		if len(hashes) == 0 {
			return map[string]any{"success": false, "error": "未命中可恢复关系"}, nil
		}
		result, err := s.restoreRelationHashes(ctx, hashes, updatedBy, reason)
		if err != nil {
			return nil, err
		}
		operation, err := s.deps.RecordV5Operation(ctx, act, target, hashes, reason, updatedBy, result)
		if err != nil {
			return nil, err
		}
		merged := map[string]any{"operation": operation}
		for key, value := range result {
			merged[key] = value
		}
		return merged, nil
	}
	hashes := s.ResolveRelationHashes(ctx, target)
	if len(hashes) == 0 {
		return map[string]any{"success": false, "error": "未命中可维护关系"}, nil
	}
	strength := 1.0
	if value, ok := asFloat(payload["strength"]); ok && value != 0 {
		strength = value
	}
	result, err := s.ApplyV5RelationAction(ctx, act, hashes, strength)
	if err != nil {
		return nil, err
	}
	operation, err := s.deps.RecordV5Operation(ctx, act, target, hashes, reason, updatedBy, result)
	if err != nil {
		return nil, err
	}
	merged := map[string]any{"success": pyTruthy(result["success"]), "operation": operation}
	for key, value := range result {
		merged[key] = value
	}
	return merged, nil
}

// ResolveRelationHashes 对齐 _resolve_relation_hashes
func (s *V5AdminService) ResolveRelationHashes(ctx context.Context, target string) []string {
	token := strings.TrimSpace(target)
	if token == "" {
		return nil
	}
	if isHexHash(token) {
		if s.deps.GetRelation(ctx, strings.ToLower(token)) != nil {
			return []string{token}
		}
		return nil
	}
	hashes, err := s.deps.SearchRelationHashesByText(ctx, token, 10)
	if err != nil {
		return nil
	}
	if len(hashes) > 0 {
		return hashes
	}
	rows, err := s.deps.GetRelationsBySubject(ctx, token)
	if err != nil {
		return nil
	}
	result := make([]string, 0, len(rows))
	for index, row := range rows {
		if index >= 10 {
			break
		}
		if value := strings.TrimSpace(asString(row["hash"])); value != "" {
			result = append(result, value)
		}
	}
	return result
}

// ResolveDeletedRelationHashes 对齐 _resolve_deleted_relation_hashes
func (s *V5AdminService) ResolveDeletedRelationHashes(ctx context.Context, target string) []string {
	token := strings.TrimSpace(target)
	if token == "" {
		return nil
	}
	if isHexHash(token) {
		if s.deps.GetDeletedRelation(ctx, strings.ToLower(token)) != nil {
			return []string{token}
		}
		return nil
	}
	hashes, err := s.deps.SearchDeletedRelationHashesByText(ctx, token, 10)
	if err != nil {
		return nil
	}
	return hashes
}

// MemoryV5Status 对齐 _memory_v5_status
func (s *V5AdminService) MemoryV5Status(ctx context.Context, target string, limit int) (map[string]any, error) {
	now := float64(time.Now().UnixNano()) / 1e9
	summary, err := s.deps.GetMemoryStatusSummary(ctx, now)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"success": true}
	for key, value := range summary {
		payload[key] = value
	}
	payload["config"] = map[string]any{
		"half_life_hours":                       spaceFloat(s.Cfg("memory.half_life_hours", 24.0)),
		"base_decay_interval_hours":             spaceFloat(s.Cfg("memory.base_decay_interval_hours", 1.0)),
		"prune_threshold":                       spaceFloat(s.Cfg("memory.prune_threshold", 0.1)),
		"freeze_duration_hours":                 spaceFloat(s.Cfg("memory.freeze_duration_hours", 24.0)),
		"access_reinforcement_cooldown_minutes": spaceFloat(s.Cfg("memory.access_reinforcement_cooldown_minutes", 60.0)),
	}
	payload["last_maintenance_at"] = s.deps.LastMaintenanceAt()
	token := strings.TrimSpace(target)
	if token == "" {
		return payload, nil
	}
	activeHashes := s.ResolveRelationHashes(ctx, token)
	if len(activeHashes) > limit {
		activeHashes = activeHashes[:limit]
	}
	deletedHashes := s.ResolveDeletedRelationHashes(ctx, token)
	if len(deletedHashes) > limit {
		deletedHashes = deletedHashes[:limit]
	}
	statuses, err := s.deps.GetRelationStatusBatch(ctx, activeHashes)
	if err != nil {
		return nil, err
	}
	policy := memorixutils.DefaultRelationLifecyclePolicy()
	if s.deps.RelationLifecyclePolicy != nil {
		policy = s.deps.RelationLifecyclePolicy(ctx)
	}
	items := make([]map[string]any, 0, len(activeHashes)+len(deletedHashes))
	for _, hashValue := range activeHashes {
		relation := s.deps.GetRelation(ctx, hashValue)
		status := statuses[hashValue]
		if relation == nil || status == nil {
			continue
		}
		state := memorixutils.RelationLifecycleState{
			Strength:   spaceFloat(status["retention_strength"]),
			AnchorAt:   spaceFloat(status["retention_anchor_at"]),
			IsInactive: pyTruthy(status["is_inactive"]),
		}
		if value, ok := asOptionalFloat(status["inactive_since"]); ok {
			state.InactiveSince = &value
		}
		if reason := strings.TrimSpace(asString(status["inactive_reason"])); reason != "" {
			state.InactiveReason = &reason
		}
		retentionScore, retentionErr := memorixutils.RetentionAt(state, now, policy)
		if retentionErr != nil {
			retentionScore = 0
		}
		items = append(items, map[string]any{
			"hash":                      hashValue,
			"subject":                   asString(relation["subject"]),
			"predicate":                 asString(relation["predicate"]),
			"object":                    asString(relation["object"]),
			"state":                     ternaryString(pyTruthy(status["is_inactive"]), "inactive", "active"),
			"is_pinned":                 pyTruthy(status["is_pinned"]),
			"temp_protected":            spaceFloat(status["protected_until"]) > now,
			"protected_until":           status["protected_until"],
			"last_reinforced":           status["last_reinforced"],
			"last_access_reinforced_at": status["last_access_reinforced_at"],
			"confidence":                spaceFloat(status["confidence"]),
			"retention_score":           retentionScore,
			"retention_anchor_at":       status["retention_anchor_at"],
			"next_lifecycle_at":         status["next_lifecycle_at"],
			"reinforcement_count":       pyIntOr(status["reinforcement_count"], 0, 0),
			"inactive_reason":           asString(status["inactive_reason"]),
		})
	}
	for _, hashValue := range deletedHashes {
		relation := s.deps.GetDeletedRelation(ctx, hashValue)
		if relation == nil {
			continue
		}
		items = append(items, map[string]any{
			"hash":                      hashValue,
			"subject":                   asString(relation["subject"]),
			"predicate":                 asString(relation["predicate"]),
			"object":                    asString(relation["object"]),
			"state":                     "deleted",
			"is_pinned":                 pyTruthy(relation["is_pinned"]),
			"temp_protected":            false,
			"protected_until":           relation["protected_until"],
			"last_reinforced":           relation["last_reinforced"],
			"last_access_reinforced_at": relation["last_access_reinforced_at"],
			"confidence":                spaceFloat(relation["confidence"]),
			"retention_strength":        spaceFloat(relation["retention_strength"]),
			"retention_anchor_at":       relation["retention_anchor_at"],
			"reinforcement_count":       pyIntOr(relation["reinforcement_count"], 0, 0),
			"inactive_reason":           asString(relation["inactive_reason"]),
			"deleted_at":                relation["deleted_at"],
		})
	}
	if len(items) > limit {
		items = items[:limit]
	}
	payload["items"] = items
	payload["count"] = len(items)
	payload["target"] = token
	return payload, nil
}

// V5Status 对齐 memory_v5_status 的内核导出版本
func (s *V5AdminService) V5Status(ctx context.Context, target string, limit int) (map[string]any, error) {
	if err := s.initialized(ctx); err != nil {
		return nil, err
	}
	if limit < 1 {
		limit = 50
	}
	return s.MemoryV5Status(ctx, target, limit)
}

// ApplyV5RelationAction 对齐 _apply_v5_relation_action
func (s *V5AdminService) ApplyV5RelationAction(ctx context.Context, action string, hashes []string, strength float64) (map[string]any, error) {
	act := strings.ToLower(strings.TrimSpace(action))
	normalized := make([]string, 0, len(hashes))
	for _, item := range hashes {
		if value := strings.TrimSpace(item); value != "" {
			normalized = append(normalized, value)
		}
	}
	if len(normalized) == 0 {
		return map[string]any{"success": false, "error": "未命中可维护关系"}, nil
	}
	now := float64(time.Now().UnixNano()) / 1e9
	if math.IsInf(strength, 0) || math.IsNaN(strength) || strength <= 0 {
		return map[string]any{"success": false, "error": "strength 必须是大于0的有限数"}, nil
	}
	var transitions []map[string]any
	var detail string
	event := func(value memorixutils.RelationLifecycleEvent) ([]map[string]any, error) {
		return s.deps.ApplyRelationLifecycleEvent(ctx, normalized, value, strength, now)
	}
	switch act {
	case "reinforce":
		var err error
		transitions, err = event(memorixutils.LifecycleEventReinforce)
		if err != nil {
			return nil, err
		}
		protectHours := math.Max(1.0, 24.0*strength)
		logicalNow := now
		for _, item := range transitions {
			if item["retention_anchor_at"] == nil {
				continue
			}
			if value := spaceFloat(item["retention_anchor_at"]); value > logicalNow {
				logicalNow = value
			}
		}
		protectedUntil := logicalNow + protectHours*3600.0
		lastReinforced := logicalNow
		if err := s.deps.UpdateRelationsProtection(ctx, normalized, &protectedUntil, nil, &lastReinforced); err != nil {
			return nil, err
		}
		detail = fmt.Sprintf("reinforce %d 条关系", len(normalized))
	case "weaken":
		var err error
		transitions, err = event(memorixutils.LifecycleEventWeaken)
		if err != nil {
			return nil, err
		}
		detail = fmt.Sprintf("weaken %d 条关系", len(normalized))
	case "remember_forever":
		var err error
		transitions, err = event(memorixutils.LifecycleEventReinforce)
		if err != nil {
			return nil, err
		}
		pinned := true
		zero := 0.0
		if err := s.deps.UpdateRelationsProtection(ctx, normalized, &zero, &pinned, nil); err != nil {
			return nil, err
		}
		detail = fmt.Sprintf("remember_forever %d 条关系", len(normalized))
	case "forget":
		pinned := false
		zero := 0.0
		if err := s.deps.UpdateRelationsProtection(ctx, normalized, &zero, &pinned, nil); err != nil {
			return nil, err
		}
		var err error
		transitions, err = event(memorixutils.LifecycleEventForget)
		if err != nil {
			return nil, err
		}
		detail = fmt.Sprintf("forget %d 条关系", len(normalized))
	case "freeze":
		var err error
		transitions, err = event(memorixutils.LifecycleEventFreeze)
		if err != nil {
			return nil, err
		}
		detail = fmt.Sprintf("freeze %d 条关系", len(normalized))
	default:
		return map[string]any{"success": false, "error": fmt.Sprintf("不支持的 V5 动作: %s", act)}, nil
	}
	if s.deps.Persist != nil {
		s.deps.Persist(ctx)
	}
	statuses, err := s.deps.GetRelationStatusBatch(ctx, normalized)
	if err != nil {
		return nil, err
	}
	retentionScores := make(map[string]any, len(transitions))
	for _, item := range transitions {
		retentionScores[asString(item["hash"])] = spaceFloat(item["retention_score"])
	}
	return map[string]any{
		"success":          true,
		"detail":           detail,
		"hashes":           normalized,
		"count":            len(normalized),
		"retention_scores": retentionScores,
		"statuses":         statuses,
	}, nil
}

func (s *V5AdminService) restoreRelationHashes(ctx context.Context, hashes []string, requestedBy string, reason string) (map[string]any, error) {
	if s.deps.RestoreDeletedRelations == nil {
		return nil, fmt.Errorf("restore_deleted_relations 未注入")
	}
	return s.deps.RestoreDeletedRelations(ctx, hashes, requestedBy, reason)
}

func isHexHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range strings.ToLower(value) {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func stringOr(primary string, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}

func ternaryString(condition bool, whenTrue string, whenFalse string) string {
	if condition {
		return whenTrue
	}
	return whenFalse
}

func asOptionalFloat(value any) (float64, bool) {
	return asFloat(value)
}

func spaceFloatPtr(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

var _ = storage.FactClaimQuery{}
