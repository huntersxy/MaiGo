package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	imagePathMaintenanceTaskName = "image_storage_path_normalization_v1"
	imagePathMaintenancePhase    = "normalize"
	imagePathMaintenancePending  = "pending"
	imagePathMaintenanceRunning  = "running"
	imagePathMaintenanceDone     = "done"
	imagePathMaintenanceBatch    = 100
)

// ImagePathMaintenanceState 对齐 Python ImagePathMaintenanceState
type ImagePathMaintenanceState struct {
	Phase            string `json:"phase"`
	Status           string `json:"status"`
	CursorID         int64  `json:"cursor_id"`
	ScannedRecords   int64  `json:"scanned_records"`
	ConvertedRecords int64  `json:"converted_records"`
	DiscardedRecords int64  `json:"discarded_records"`
}

// ImagePathMaintenanceBatchResult 对齐 Python ImagePathMaintenanceBatchResult
type ImagePathMaintenanceBatchResult struct {
	Completed             bool  `json:"completed"`
	ScannedRecords        int64 `json:"scanned_records"`
	ConvertedRecords      int64 `json:"converted_records"`
	DiscardedRecords      int64 `json:"discarded_records"`
	SkippedRecords        int64 `json:"skipped_records"`
	LastProcessedID       int64 `json:"last_processed_id"`
	TotalScannedRecords   int64 `json:"total_scanned_records"`
	TotalConvertedRecords int64 `json:"total_converted_records"`
	TotalDiscardedRecords int64 `json:"total_discarded_records"`
}

type oneTimeMaintenanceTask struct {
	TaskName    string     `gorm:"primaryKey;size:200;column:task_name"`
	Phase       string     `gorm:"size:64;not null;column:phase"`
	Status      string     `gorm:"size:32;not null;column:status"`
	CursorID    int64      `gorm:"not null;default:0;column:cursor_id"`
	StatsJSON   string     `gorm:"type:text;not null;default:'';column:stats_json"`
	LastError   *string    `gorm:"type:text;column:last_error"`
	CompletedAt *time.Time `gorm:"column:completed_at"`
	UpdatedAt   time.Time  `gorm:"column:updated_at"`
}

func (oneTimeMaintenanceTask) TableName() string { return "one_time_maintenance_tasks" }

// ImagePathMaintenanceService 对齐 MaiBot image_path_maintenance_service
type ImagePathMaintenanceService struct {
	DB   *gorm.DB
	Root string
}

func NewImagePathMaintenanceService(db *gorm.DB, projectRoot string) *ImagePathMaintenanceService {
	return &ImagePathMaintenanceService{DB: db, Root: projectRoot}
}

func (s *ImagePathMaintenanceService) ensure(ctx context.Context) (*gorm.DB, error) {
	if s == nil || s.DB == nil {
		return nil, errors.New("图片路径维护数据库未接线")
	}
	if strings.TrimSpace(s.Root) == "" {
		return nil, errors.New("图片路径维护项目根目录未配置")
	}
	db := s.DB.WithContext(ctx)
	if err := db.AutoMigrate(&oneTimeMaintenanceTask{}); err != nil {
		return nil, fmt.Errorf("初始化一次性维护任务表失败: %w", err)
	}
	return db, nil
}

func defaultImagePathState() ImagePathMaintenanceState {
	return ImagePathMaintenanceState{Phase: imagePathMaintenancePhase, Status: imagePathMaintenancePending}
}

func decodeImagePathStats(raw string) (int64, int64, int64) {
	if strings.TrimSpace(raw) == "" {
		return 0, 0, 0
	}
	var payload struct {
		Scanned   int64 `json:"scanned_records"`
		Converted int64 `json:"converted_records"`
		Discarded int64 `json:"discarded_records"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return 0, 0, 0
	}
	return payload.Scanned, payload.Converted, payload.Discarded
}

func encodeImagePathStats(scanned, converted, discarded int64) string {
	raw, _ := json.Marshal(map[string]int64{"scanned_records": scanned, "converted_records": converted, "discarded_records": discarded})
	return string(raw)
}

func (s *ImagePathMaintenanceService) getState(ctx context.Context, db *gorm.DB) (ImagePathMaintenanceState, error) {
	var row oneTimeMaintenanceTask
	result := db.Where("task_name = ?", imagePathMaintenanceTaskName).First(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return defaultImagePathState(), nil
	}
	if result.Error != nil {
		return ImagePathMaintenanceState{}, fmt.Errorf("读取图片路径维护状态失败: %w", result.Error)
	}
	scanned, converted, discarded := decodeImagePathStats(row.StatsJSON)
	phase := strings.TrimSpace(row.Phase)
	if phase == "" {
		phase = imagePathMaintenancePhase
	}
	status := strings.TrimSpace(row.Status)
	if status == "" {
		status = imagePathMaintenancePending
	}
	return ImagePathMaintenanceState{Phase: phase, Status: status, CursorID: row.CursorID, ScannedRecords: scanned, ConvertedRecords: converted, DiscardedRecords: discarded}, nil
}

func (s *ImagePathMaintenanceService) saveState(ctx context.Context, db *gorm.DB, state ImagePathMaintenanceState, completed bool) error {
	now := time.Now()
	row := oneTimeMaintenanceTask{
		TaskName: imagePathMaintenanceTaskName, Phase: state.Phase, Status: state.Status, CursorID: state.CursorID,
		StatsJSON: encodeImagePathStats(state.ScannedRecords, state.ConvertedRecords, state.DiscardedRecords), UpdatedAt: now,
	}
	updates := map[string]any{
		"phase": state.Phase, "status": state.Status, "cursor_id": state.CursorID,
		"stats_json": row.StatsJSON, "last_error": nil, "updated_at": now,
	}
	if completed {
		updates["completed_at"] = now
	}
	result := db.Model(&oneTimeMaintenanceTask{}).Where("task_name = ?", imagePathMaintenanceTaskName).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("保存图片路径维护状态失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		if err := db.Create(&row).Error; err != nil {
			return fmt.Errorf("保存图片路径维护状态失败: %w", err)
		}
	}
	return nil
}

func (s *ImagePathMaintenanceService) imagesTableExists(db *gorm.DB) bool {
	return db.Migrator().HasTable("images")
}

func (s *ImagePathMaintenanceService) fetchRows(ctx context.Context, db *gorm.DB, cursor int64) ([]struct {
	ID       int64
	FullPath string
}, error) {
	var rows []struct {
		ID       int64
		FullPath string
	}
	if err := db.WithContext(ctx).Table("images").Select("id, full_path").Where("id > ? AND full_path IS NOT NULL", cursor).Order("id asc").Limit(imagePathMaintenanceBatch).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取图片路径失败: %w", err)
	}
	return rows, nil
}

func (s *ImagePathMaintenanceService) countRemaining(ctx context.Context, db *gorm.DB, cursor int64) (int64, error) {
	var count int64
	if err := db.WithContext(ctx).Table("images").Where("id > ? AND full_path IS NOT NULL", cursor).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("统计图片路径失败: %w", err)
	}
	return count, nil
}

func (s *ImagePathMaintenanceService) normalizeStoredPath(raw string) (string, bool) {
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return "", false
	}
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", false
	}
	var resolved string
	if filepath.IsAbs(path) {
		resolved, err = filepath.Abs(path)
	} else {
		resolved, err = filepath.Abs(filepath.Join(root, path))
	}
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return filepath.ToSlash(filepath.Clean(rel)), true
}

// RunBatch 执行一批图片路径规整
func (s *ImagePathMaintenanceService) RunBatch(ctx context.Context) (ImagePathMaintenanceBatchResult, error) {
	db, err := s.ensure(ctx)
	if err != nil {
		return ImagePathMaintenanceBatchResult{}, err
	}
	var result ImagePathMaintenanceBatchResult
	err = db.Transaction(func(tx *gorm.DB) error {
		state, err := s.getState(ctx, tx)
		if err != nil {
			return err
		}
		if state.Phase != imagePathMaintenancePhase {
			result = ImagePathMaintenanceBatchResult{Completed: true, LastProcessedID: state.CursorID, TotalScannedRecords: state.ScannedRecords, TotalConvertedRecords: state.ConvertedRecords, TotalDiscardedRecords: state.DiscardedRecords}
			return nil
		}
		finish := func(current ImagePathMaintenanceState) error {
			current.Phase = imagePathMaintenanceDone
			current.Status = imagePathMaintenanceDone
			if err := s.saveState(ctx, tx, current, true); err != nil {
				return err
			}
			result = ImagePathMaintenanceBatchResult{Completed: true, LastProcessedID: current.CursorID, TotalScannedRecords: current.ScannedRecords, TotalConvertedRecords: current.ConvertedRecords, TotalDiscardedRecords: current.DiscardedRecords}
			return nil
		}
		if !s.imagesTableExists(tx) {
			return finish(state)
		}
		rows, err := s.fetchRows(ctx, tx, state.CursorID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return finish(state)
		}
		var converted, discarded, skipped, lastID int64
		for _, row := range rows {
			lastID = row.ID
			raw := strings.TrimSpace(row.FullPath)
			if raw == "" {
				skipped++
				continue
			}
			normalized, ok := s.normalizeStoredPath(raw)
			if !ok {
				if err := tx.Exec("DELETE FROM images WHERE id = ?", row.ID).Error; err != nil {
					return fmt.Errorf("删除越界图片记录失败: %w", err)
				}
				discarded++
				continue
			}
			if normalized == row.FullPath {
				skipped++
				continue
			}
			if err := tx.Exec("UPDATE images SET full_path = ? WHERE id = ?", normalized, row.ID).Error; err != nil {
				return fmt.Errorf("更新图片路径失败: %w", err)
			}
			converted++
		}
		state.CursorID = lastID
		state.Status = imagePathMaintenanceRunning
		state.ScannedRecords += int64(len(rows))
		state.ConvertedRecords += converted
		state.DiscardedRecords += discarded
		if err := s.saveState(ctx, tx, state, false); err != nil {
			return err
		}
		result = ImagePathMaintenanceBatchResult{ScannedRecords: int64(len(rows)), ConvertedRecords: converted, DiscardedRecords: discarded, SkippedRecords: skipped, LastProcessedID: lastID, TotalScannedRecords: state.ScannedRecords, TotalConvertedRecords: state.ConvertedRecords, TotalDiscardedRecords: state.DiscardedRecords}
		return nil
	})
	if err != nil {
		return ImagePathMaintenanceBatchResult{}, err
	}
	return result, nil
}

// GetState 读取当前维护状态
func (s *ImagePathMaintenanceService) GetState(ctx context.Context) (ImagePathMaintenanceState, error) {
	db, err := s.ensure(ctx)
	if err != nil {
		return ImagePathMaintenanceState{}, err
	}
	return s.getState(ctx, db)
}

// CountRemaining 统计剩余待检查记录
func (s *ImagePathMaintenanceService) CountRemaining(ctx context.Context) (int64, error) {
	db, err := s.ensure(ctx)
	if err != nil {
		return 0, err
	}
	state, err := s.getState(ctx, db)
	if err != nil {
		return 0, err
	}
	if state.Phase != imagePathMaintenancePhase || !s.imagesTableExists(db) {
		return 0, nil
	}
	return s.countRemaining(ctx, db, state.CursorID)
}

// ShouldScheduleBackground 判断是否需要后台执行
func (s *ImagePathMaintenanceService) ShouldScheduleBackground(ctx context.Context) (bool, error) {
	state, err := s.GetState(ctx)
	if err != nil {
		return false, err
	}
	return state.Phase == imagePathMaintenancePhase, nil
}

// RunBackground 启动时一次性规整图片路径，语义对齐 MaiBot run_image_path_maintenance_background
// 单批失败只记录并返回，下次启动继续；ctx 取消视为中断，下次启动继续
func (s *ImagePathMaintenanceService) RunBackground(ctx context.Context) {
	state, err := s.GetState(ctx)
	if err != nil {
		zap.L().Error("图片路径规整一次性维护读取状态失败", zap.Error(err))
		return
	}
	remaining, err := s.CountRemaining(ctx)
	if err != nil {
		zap.L().Warn("图片路径规整一次性维护统计剩余失败", zap.Error(err))
	}
	zap.L().Info(fmt.Sprintf("图片路径规整一次性维护开始：从 images.id > %d 继续，已检查 %d 条，转换 %d 条，丢弃 %d 条，剩余约 %d 条，批量大小 %d",
		state.CursorID, state.ScannedRecords, state.ConvertedRecords, state.DiscardedRecords, remaining, imagePathMaintenanceBatch))
	lastProgressBucket := state.ScannedRecords / 1000
	for {
		if err := ctx.Err(); err != nil {
			zap.L().Info("图片路径规整一次性维护已中断，下次启动后继续")
			return
		}
		result, batchErr := s.RunBatch(ctx)
		if batchErr != nil {
			if ctx.Err() != nil {
				zap.L().Info("图片路径规整一次性维护已中断，下次启动后继续")
				return
			}
			zap.L().Warn(fmt.Sprintf("图片路径规整批次失败，将在下次启动后继续: %v", batchErr))
			return
		}
		if result.Completed {
			skipped := result.TotalScannedRecords - result.TotalConvertedRecords - result.TotalDiscardedRecords
			zap.L().Info(fmt.Sprintf("图片路径规整一次性维护完成：累计检查 %d 条，转换 %d 条，丢弃 %d 条，跳过 %d 条，最后处理 id=%d",
				result.TotalScannedRecords, result.TotalConvertedRecords, result.TotalDiscardedRecords, skipped, result.LastProcessedID))
			return
		}
		bucket := result.TotalScannedRecords / 1000
		if bucket > lastProgressBucket {
			lastProgressBucket = bucket
			skipped := result.TotalScannedRecords - result.TotalConvertedRecords - result.TotalDiscardedRecords
			zap.L().Info(fmt.Sprintf("图片路径规整进行中：累计检查 %d 条，转换 %d 条，丢弃 %d 条，跳过 %d 条；本批检查 %d 条，转换 %d 条，丢弃 %d 条，进度 id=%d",
				result.TotalScannedRecords, result.TotalConvertedRecords, result.TotalDiscardedRecords, skipped,
				result.ScannedRecords, result.ConvertedRecords, result.DiscardedRecords, result.LastProcessedID))
		}
	}
}
