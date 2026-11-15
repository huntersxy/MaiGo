package services

// 记忆统计服务，对齐 A_memorix core/runtime/services/stats_service.py

import (
	"context"
	"fmt"

	"MaiGo/internal/A_memorix/core/utils"
)

// StatsDeps 统计服务依赖的存储能力
type StatsDeps struct {
	GetStatistics              func() (map[string]int, error)
	GetEpisodeRebuildSummary   func(failedLimit int) (map[string]any, error)
	GetParagraphBackfillCounts func() (map[string]int, error)
	Query                      func(sql string, params ...any) ([]map[string]any, error)
	LastMaintenanceAt          func() *float64
}

// StatsService 对齐 MemoryStatsService
type StatsService struct {
	Deps StatsDeps
}

// NewStatsService 构造统计服务
func NewStatsService(deps StatsDeps) *StatsService { return &StatsService{Deps: deps} }

// MemoryStats 对齐 memory_stats
func (s *StatsService) MemoryStats(ctx context.Context) (map[string]any, error) {
	if s.Deps.GetStatistics == nil || s.Deps.Query == nil {
		return nil, fmt.Errorf("metadata store unavailable")
	}
	stats, err := s.Deps.GetStatistics()
	if err != nil {
		return nil, err
	}
	episodeRows, err := s.Deps.Query("SELECT COUNT(*) AS c FROM episodes")
	if err != nil {
		return nil, err
	}
	profileRows, err := s.Deps.Query("SELECT COUNT(*) AS c FROM person_profile_snapshots")
	if err != nil {
		return nil, err
	}
	backfill := map[string]int{}
	if s.Deps.GetParagraphBackfillCounts != nil {
		backfill, err = s.Deps.GetParagraphBackfillCounts()
		if err != nil {
			return nil, err
		}
	}
	rebuildCounts := map[string]any{}
	if s.Deps.GetEpisodeRebuildSummary != nil {
		summary, err := s.Deps.GetEpisodeRebuildSummary(20)
		if err != nil {
			return nil, err
		}
		if counts, ok := summary["counts"].(map[string]any); ok {
			rebuildCounts = counts
		}
	}
	var lastMaintenance any
	if s.Deps.LastMaintenanceAt != nil {
		lastMaintenance = s.Deps.LastMaintenanceAt()
	}
	return map[string]any{
		"paragraphs":                        stats["paragraph_count"],
		"relations":                         stats["relation_count"],
		"episodes":                          statsCount(episodeRows),
		"profiles":                          statsCount(profileRows),
		"stale_paragraph_marks":             stats["stale_paragraph_mark_count"],
		"profile_refresh_pending":           stats["person_profile_refresh_pending_count"],
		"profile_refresh_failed":            stats["person_profile_refresh_failed_count"],
		"episode_rebuild_pending":           utils.PyIntOrZero(rebuildCounts["pending"]) + utils.PyIntOrZero(rebuildCounts["running"]) + utils.PyIntOrZero(rebuildCounts["failed"]),
		"paragraph_vector_backfill_pending": backfill["pending"],
		"paragraph_vector_backfill_failed":  backfill["failed"],
		"last_maintenance_at":               lastMaintenance,
	}, nil
}

func statsCount(rows []map[string]any) int {
	if len(rows) == 0 {
		return 0
	}
	return utils.PyIntOrZero(rows[0]["c"])
}
