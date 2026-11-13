package services

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"maibot-go/internal/services/statistics"
)

// StatisticsService 是 MaiBot statistics_service 在 services 层的门面，
// 查询实现复用 internal/stats 既有聚合，不重复建表或伪算缓存。
type StatisticsService struct {
	DB          *gorm.DB
	InstanceDir string
}

func NewStatisticsService(db *gorm.DB, instanceDir string) *StatisticsService {
	return &StatisticsService{DB: db, InstanceDir: instanceDir}
}

func (s *StatisticsService) ready() (*gorm.DB, error) {
	if s == nil || s.DB == nil {
		return nil, errors.New("统计数据库未接线")
	}
	return s.DB, nil
}

// GetSummaryStatistics 对齐 get_summary_statistics
func (s *StatisticsService) GetSummaryStatistics(ctx context.Context, start, end time.Time) (stats.StatisticsSummary, error) {
	db, err := s.ready()
	if err != nil {
		return stats.StatisticsSummary{}, err
	}
	return stats.GetStatisticsSummary(ctx, db, start, end)
}

// GetModelStatistics 对齐 get_model_statistics
func (s *StatisticsService) GetModelStatistics(ctx context.Context, start, end time.Time) ([]stats.ModelStatistics, error) {
	db, err := s.ready()
	if err != nil {
		return nil, err
	}
	return stats.GetModelStatistics(ctx, db, start, end)
}

// GetHourlyStatistics 对齐 get_hourly_statistics
func (s *StatisticsService) GetHourlyStatistics(ctx context.Context, start, end time.Time) ([]stats.TimeSeriesData, error) {
	db, err := s.ready()
	if err != nil {
		return nil, err
	}
	return stats.GetHourlyStatistics(ctx, db, start, end)
}

// GetDailyStatistics 对齐 get_daily_statistics
func (s *StatisticsService) GetDailyStatistics(ctx context.Context, start, end time.Time) ([]stats.TimeSeriesData, error) {
	db, err := s.ready()
	if err != nil {
		return nil, err
	}
	return stats.GetDailyStatistics(ctx, db, start, end)
}

// GetRecentActivity 对齐 get_recent_activity
func (s *StatisticsService) GetRecentActivity(ctx context.Context, start, end *time.Time, limit int) ([]stats.RecentActivity, error) {
	db, err := s.ready()
	if err != nil {
		return nil, err
	}
	if start == nil || end == nil {
		now := time.Now()
		if end == nil {
			end = &now
		}
		if start == nil {
			begin := now.Add(-24 * time.Hour)
			start = &begin
		}
	}
	return stats.GetRecentActivity(ctx, db, *start, *end, limit)
}

// GetDashboardStatistics 对齐 get_dashboard_statistics
func (s *StatisticsService) GetDashboardStatistics(ctx context.Context, hours int, useCache bool) (stats.DashboardData, error) {
	db, err := s.ready()
	if err != nil {
		return stats.DashboardData{}, err
	}
	return stats.GetDashboardStatistics(ctx, db, hours, useCache, s.InstanceDir)
}

// EmptyDashboardStatistics 对齐 build_empty_dashboard_statistics
func (s *StatisticsService) EmptyDashboardStatistics() stats.DashboardData {
	return stats.EmptyDashboardStatistics()
}
