package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	// ToolRecordPromptPayloadCleanupTask 对齐 Python v27_to_v28 的一次性维护任务名
	ToolRecordPromptPayloadCleanupTask = "tool_record_prompt_payload_cleanup_v1"

	maintenancePhaseAwaitingVacuum = "awaiting_vacuum"
	maintenancePhaseDone           = "done"
	maintenanceStatusRunning       = "running"
	maintenanceStatusFailed        = "failed"
	maintenanceStatusDone          = "done"
	maintenanceStatusPending       = "pending"
	maintenanceLastErrorLimit      = 1000
)

// MaintenanceTaskState 是一次性维护任务的可恢复状态
type MaintenanceTaskState struct {
	Phase          string
	Status         string
	CursorID       int64
	ScannedRecords int64
	UpdatedRecords int64
}

type startupMaintenanceOptions struct {
	vacuum func(context.Context, querier) error
}

// startupMaintenanceGate 串行化一次性维护；等待期间仍响应 ctx 取消
var startupMaintenanceGate = func() chan struct{} {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return gate
}()

// RunStartupMaintenance 执行 Python 启动期的 tool_records VACUUM 收尾
// 已 done 的任务直接返回；awaiting_vacuum 或 failed 会重试，重复启动不会重复执行
func RunStartupMaintenance(ctx context.Context, db *gorm.DB) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("启动一次性维护数据库未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("启动一次性维护已取消: %w", err)
	}
	select {
	case <-ctx.Done():
		return false, fmt.Errorf("启动一次性维护已取消: %w", ctx.Err())
	case <-startupMaintenanceGate:
	}
	defer func() { startupMaintenanceGate <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("启动一次性维护已取消: %w", err)
	}
	return runStartupMaintenance(ctx, gormQuerier{db: db}, startupMaintenanceOptions{})
}

func runStartupMaintenance(ctx context.Context, q querier, opts startupMaintenanceOptions) (bool, error) {
	state, err := loadMaintenanceTaskState(ctx, q)
	if err != nil {
		return false, err
	}
	if state.Phase != maintenancePhaseAwaitingVacuum {
		return state.Phase == maintenancePhaseDone, nil
	}
	state.Status = maintenanceStatusRunning
	if err := saveMaintenanceTaskState(ctx, q, state, false, ""); err != nil {
		return false, err
	}
	vacuum := opts.vacuum
	if vacuum == nil {
		vacuum = vacuumMaintenanceDatabase
	}
	if err := vacuum(ctx, q); err != nil {
		latest, loadErr := loadMaintenanceTaskState(ctx, q)
		if loadErr != nil {
			latest = state
		}
		latest.Phase = maintenancePhaseAwaitingVacuum
		latest.Status = maintenanceStatusFailed
		saveCtx := ctx
		var cancel context.CancelFunc
		if ctx.Err() != nil {
			saveCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
		}
		message := fmt.Sprintf("工具记录维护 VACUUM 失败，请关闭占用数据库的程序后重启: %v", err)
		if saveErr := saveMaintenanceTaskState(saveCtx, q, latest, false, message); saveErr != nil {
			return false, fmt.Errorf("%s（保存维护状态失败: %v）", message, saveErr)
		}
		return false, fmt.Errorf("工具记录维护 VACUUM 失败，请关闭占用数据库的程序后重启: %w", err)
	}
	latest, err := loadMaintenanceTaskState(ctx, q)
	if err != nil {
		return false, err
	}
	latest.Phase = maintenancePhaseDone
	latest.Status = maintenanceStatusDone
	if err := saveMaintenanceTaskState(ctx, q, latest, true, ""); err != nil {
		return false, err
	}
	return true, nil
}

func loadMaintenanceTaskState(ctx context.Context, q querier) (MaintenanceTaskState, error) {
	row := q.QueryRow(ctx,
		"SELECT phase,status,cursor_id,stats_json FROM one_time_maintenance_tasks WHERE task_name=?",
		ToolRecordPromptPayloadCleanupTask)
	var phase, status, statsJSON sql.NullString
	var cursorID sql.NullInt64
	if err := row.Scan(&phase, &status, &cursorID, &statsJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MaintenanceTaskState{Phase: maintenancePhaseDone, Status: maintenanceStatusDone}, nil
		}
		return MaintenanceTaskState{}, fmt.Errorf("读取一次性维护任务状态失败: %w", err)
	}
	state := MaintenanceTaskState{
		Phase:    phase.String,
		Status:   status.String,
		CursorID: cursorID.Int64,
	}
	if state.Phase == "" {
		state.Phase = maintenancePhaseDone
	}
	if state.Status == "" {
		state.Status = maintenanceStatusDone
	}
	state.ScannedRecords, state.UpdatedRecords = parseMaintenanceStats(statsJSON.String)
	return state, nil
}

func parseMaintenanceStats(raw string) (int64, int64) {
	if strings.TrimSpace(raw) == "" {
		return 0, 0
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return 0, 0
	}
	return maintenanceInt64(payload["scanned_records"]), maintenanceInt64(payload["updated_records"])
}

func maintenanceInt64(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case json.Number:
		result, _ := typed.Int64()
		return result
	case string:
		result, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return result
	default:
		return 0
	}
}

func saveMaintenanceTaskState(ctx context.Context, q querier, state MaintenanceTaskState, completed bool, lastError string) error {
	stats := fmt.Sprintf(`{"scanned_records":%d,"updated_records":%d}`, state.ScannedRecords, state.UpdatedRecords)
	var lastErrorValue any
	if lastError != "" {
		lastErrorValue = truncateMaintenanceError(lastError)
	}
	completedFlag := 0
	if completed {
		completedFlag = 1
	}
	err := q.Exec(ctx,
		"INSERT INTO one_time_maintenance_tasks(task_name,phase,status,cursor_id,stats_json,last_error,completed_at,updated_at) "+
			"VALUES(?,?,?,?,?,?,CASE WHEN ?=1 THEN CURRENT_TIMESTAMP ELSE NULL END,CURRENT_TIMESTAMP) "+
			"ON CONFLICT (task_name) DO UPDATE SET phase=excluded.phase,status=excluded.status,cursor_id=excluded.cursor_id,"+
			"stats_json=excluded.stats_json,last_error=excluded.last_error,"+
			"completed_at=CASE WHEN ?=1 THEN CURRENT_TIMESTAMP ELSE one_time_maintenance_tasks.completed_at END,"+
			"updated_at=excluded.updated_at",
		ToolRecordPromptPayloadCleanupTask, state.Phase, state.Status, state.CursorID, stats, lastErrorValue, completedFlag, completedFlag)
	if err != nil {
		return fmt.Errorf("保存一次性维护任务状态失败: %w", err)
	}
	return nil
}

func truncateMaintenanceError(value string) string {
	runes := []rune(value)
	if len(runes) <= maintenanceLastErrorLimit {
		return value
	}
	return string(runes[:maintenanceLastErrorLimit])
}

func vacuumMaintenanceDatabase(ctx context.Context, q querier) error {
	if q.Dialect() == DialectSQLite {
		if err := q.Exec(ctx, "PRAGMA busy_timeout=30000"); err != nil {
			return err
		}
	}
	if err := q.Exec(ctx, "VACUUM"); err != nil {
		return err
	}
	if q.Dialect() != DialectSQLite {
		return nil
	}
	var busy, logFrames, checkpointed int
	if err := q.QueryRow(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("VACUUM 后 WAL checkpoint 未完成: busy=%d log=%d checkpointed=%d", busy, logFrames, checkpointed)
	}
	return nil
}
