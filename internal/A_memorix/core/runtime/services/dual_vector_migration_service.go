package services

// 双向量池自动迁移服务，对齐 Python core/runtime/services/dual_vector_migration_service.py

import (
	"context"
	"math"
	"time"
)

// DualVectorMigrationDeps 双向量池自动迁移服务依赖的状态端口
type DualVectorMigrationDeps struct {
	// Status 返回 _dual_vector_auto_migration_status 快照
	Status func() map[string]any
	// UpdateStatus 合并写回 _dual_vector_auto_migration_status
	UpdateStatus func(patch map[string]any)
	// Now 返回当前时间戳，nil 时使用系统时间
	Now func() float64
}

// DualVectorMigrationService 对齐 MemoryDualVectorMigrationService
type DualVectorMigrationService struct {
	Deps DualVectorMigrationDeps
}

// NewDualVectorMigrationService 构造双向量池自动迁移服务
func NewDualVectorMigrationService(deps DualVectorMigrationDeps) *DualVectorMigrationService {
	return &DualVectorMigrationService{Deps: deps}
}

// ShouldStartAutoMigration 对齐 _should_start_dual_vector_auto_migration
// 历史数据不得因启动自动迁移而重新调用 embedding，因此恒返回 false
func (s *DualVectorMigrationService) ShouldStartAutoMigration(ctx context.Context) bool {
	_ = ctx
	return false
}

// migrationNormalizeOptions 对齐 _normalize_dual_vector_auto_migration_progress 的额外关键字参数
type migrationNormalizeOptions struct {
	ExplicitProcessed bool
	Completed         bool
	Success           bool
}

// NormalizeAutoMigrationProgress 对齐 _normalize_dual_vector_auto_migration_progress 的接口入口
func (s *DualVectorMigrationService) NormalizeAutoMigrationProgress(ctx context.Context, progress any) map[string]any {
	return s.NormalizeProgress(ctx, progress, migrationNormalizeOptions{})
}

// NormalizeProgress 带显式进度标记的归一化实现
func (s *DualVectorMigrationService) NormalizeProgress(ctx context.Context, progress any, options migrationNormalizeOptions) map[string]any {
	_ = ctx
	payload := map[string]any{}
	if source, ok := progress.(map[string]any); ok && source != nil {
		payload = cloneAnyMap(source)
	}
	nowTS := s.now()
	startedAt, hasStartedAt := s.status()["started_at"]
	elapsedSeconds := 0.0
	if hasStartedAt {
		if start, ok := numberValuePy(startedAt); ok {
			elapsedSeconds = math.Max(0, nowTS-start)
		}
	}
	total := nonNegativeInt(payload["total"], 0)
	if total <= 0 {
		if counts, ok := payload["counts"].(map[string]any); ok {
			total = 0
			for _, key := range []string{"paragraphs", "entities", "relations"} {
				total += nonNegativeInt(counts[key], 0)
			}
		}
	}
	processedKeys := []string{
		"paragraph_done",
		"paragraph_failed",
		"entity_done",
		"entity_failed",
		"relation_done",
		"relation_failed",
	}
	processed := 0
	if options.ExplicitProcessed {
		processed = nonNegativeInt(payload["processed"], 0)
	} else if anyKeyIn(payload, processedKeys) {
		for _, key := range processedKeys {
			processed += nonNegativeInt(payload[key], 0)
		}
	} else {
		processed = nonNegativeInt(payload["processed"], 0)
	}
	if total > 0 && processed > total {
		processed = total
	}
	percent := 0.0
	if options.Completed && options.Success {
		if total > 0 {
			processed = total
		}
		percent = 100.0
	} else if total > 0 {
		percent = math.Min(99.5, math.Max(0, (float64(processed)/float64(total))*100))
	}
	var estimatedRemaining any
	if !options.Completed && total > 0 && processed > 0 && processed < total && elapsedSeconds > 0 {
		rate := float64(processed) / elapsedSeconds
		if rate > 0 {
			remaining := (float64(total) - float64(processed)) / rate
			estimatedRemaining = int(math.Max(0, math.Floor(remaining+0.999)))
		}
	}
	payload["total"] = total
	payload["processed"] = processed
	payload["percent"] = roundTo(percent, 2)
	payload["elapsed_seconds"] = roundTo(elapsedSeconds, 3)
	payload["estimated_remaining_seconds"] = estimatedRemaining
	return payload
}

// UpdateAutoMigrationStage 对齐 _update_dual_vector_auto_migration_stage
func (s *DualVectorMigrationService) UpdateAutoMigrationStage(ctx context.Context, stage string, progress map[string]any) {
	_ = ctx
	status := s.status()
	if !pyTruthy(status["running"]) {
		return
	}
	nowTS := s.now()
	_, explicitProcessed := progress["processed"]
	payload := map[string]any{}
	if current, ok := status["progress"].(map[string]any); ok && current != nil {
		payload = cloneAnyMap(current)
	}
	for key, value := range progress {
		payload[key] = value
	}
	normalized := s.NormalizeProgress(ctx, payload, migrationNormalizeOptions{ExplicitProcessed: explicitProcessed})
	if s.Deps.UpdateStatus == nil {
		return
	}
	stageText := stage
	if stageText == "" {
		stageText = "unknown"
	}
	s.Deps.UpdateStatus(map[string]any{
		"stage":      stageText,
		"progress":   normalized,
		"updated_at": nowTS,
	})
}

// status 返回状态快照
func (s *DualVectorMigrationService) status() map[string]any {
	if s.Deps.Status == nil {
		return map[string]any{}
	}
	status := s.Deps.Status()
	if status == nil {
		return map[string]any{}
	}
	return status
}

// now 返回当前时间戳
func (s *DualVectorMigrationService) now() float64 {
	if s.Deps.Now != nil {
		return s.Deps.Now()
	}
	return float64(time.Now().UnixNano()) / float64(time.Second)
}

// nonNegativeInt 对齐 _coerce_non_negative_int
func nonNegativeInt(value any, fallback int) int {
	parsed := optionalFloat(value)
	if parsed == nil {
		return fallback
	}
	number := int(*parsed)
	if number < 0 {
		return 0
	}
	return number
}

// anyKeyIn 判断对象是否包含任一键
func anyKeyIn(values map[string]any, keys []string) bool {
	for _, key := range keys {
		if _, ok := values[key]; ok {
			return true
		}
	}
	return false
}

// numberValuePy 把任意数值转换为 float64
func numberValuePy(value any) (float64, bool) {
	parsed := optionalFloat(value)
	if parsed == nil {
		return 0, false
	}
	return *parsed, true
}

// roundTo 对齐 Python round(value, digits)
func roundTo(value float64, digits int) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return value
	}
	factor := math.Pow(10, float64(digits))
	return math.Round(value*factor) / factor
}
