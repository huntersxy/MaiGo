package webuiapi

import (
	"context"
	"net/http"
	"time"
	"unicode/utf8"

	"MaiGo/internal/services"
	"MaiGo/internal/services/statistics"

	"gorm.io/gorm"
)

// statisticsSummaryFunc 允许测试注入统计摘要；生产路径使用 stats.GetStatisticsSummary
type statisticsSummaryFunc func(context.Context, *gorm.DB, time.Time, time.Time) (stats.StatisticsSummary, error)

// statisticsTaskUsageFunc 允许测试注入模型任务用量；生产路径使用 services.ModelTaskUsageService
type statisticsTaskUsageFunc func(context.Context, *gorm.DB, string, int) (services.ModelTaskUsageSummary, error)

func (d Deps) handleStatisticsSummary(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	hours, ok := statisticsHours(w, r)
	if !ok {
		return
	}
	end := d.now()
	start := end.Add(-time.Duration(hours) * time.Hour)
	loader := d.statisticsSummary
	if loader == nil {
		if d.Memory == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
			return
		}
		loader = stats.GetStatisticsSummary
	}
	var db *gorm.DB
	if d.Memory != nil {
		db = d.Memory.GetDB()
	}
	summary, err := loader(r.Context(), db, start, end)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// statisticsHours 对齐 Python hours: int = 24 的 FastAPI Query，非法整数输出 int_parsing
func statisticsHours(w http.ResponseWriter, r *http.Request) (int, bool) {
	hours, _, present, verr := queryIntParam(r, "hours")
	if verr != nil {
		writeRequestValidationError(w, *verr)
		return 0, false
	}
	if !present {
		return 24, true
	}
	return hours, true
}

func (d Deps) handleStatisticsDashboard(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	hours, ok := statisticsHours(w, r)
	if !ok {
		return
	}
	if d.Memory == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
		return
	}
	dashboard, err := stats.GetDashboardStatisticsAt(r.Context(), d.Memory.GetDB(), hours, true, d.InstanceDir, d.now())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取统计数据失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dashboard)
}

// handleStatisticsTaskUsage 对齐 Python task_name Query(min_length=1, max_length=100)
// 与 hours Query(24, ge=1, le=168)，校验失败按参数顺序输出 FastAPI detail 数组
func (d Deps) handleStatisticsTaskUsage(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	errs := make([]fastAPIValidationError, 0, 2)
	taskValues, hasTask := r.URL.Query()["task_name"]
	taskName := ""
	if !hasTask || len(taskValues) == 0 {
		errs = append(errs, fastAPIValidationError{
			Type:  "missing",
			Loc:   []any{"query", "task_name"},
			Msg:   "Field required",
			Input: nil,
		})
	} else {
		taskName = taskValues[len(taskValues)-1]
		switch length := utf8.RuneCountInString(taskName); {
		case length < 1:
			errs = append(errs, fastAPIValidationError{
				Type:  "string_too_short",
				Loc:   []any{"query", "task_name"},
				Msg:   "String should have at least 1 character",
				Input: taskName,
				Ctx:   map[string]any{"min_length": 1},
			})
		case length > 100:
			errs = append(errs, fastAPIValidationError{
				Type:  "string_too_long",
				Loc:   []any{"query", "task_name"},
				Msg:   "String should have at most 100 characters",
				Input: taskName,
				Ctx:   map[string]any{"max_length": 100},
			})
		}
	}
	hours, rawHours, hasHours, hoursErr := queryIntParam(r, "hours")
	switch {
	case hoursErr != nil:
		errs = append(errs, *hoursErr)
	case !hasHours:
		hours = 24
	case hours < 1:
		errs = append(errs, fastAPIValidationError{
			Type:  "greater_than_equal",
			Loc:   []any{"query", "hours"},
			Msg:   "Input should be greater than or equal to 1",
			Input: rawHours,
			Ctx:   map[string]any{"ge": 1},
		})
	case hours > 168:
		errs = append(errs, fastAPIValidationError{
			Type:  "less_than_equal",
			Loc:   []any{"query", "hours"},
			Msg:   "Input should be less than or equal to 168",
			Input: rawHours,
			Ctx:   map[string]any{"le": 168},
		})
	}
	if len(errs) > 0 {
		writeRequestValidationError(w, errs...)
		return
	}
	loader := d.statisticsTaskUsage
	var db *gorm.DB
	if d.Memory != nil {
		db = d.Memory.GetDB()
	}
	if loader == nil {
		if db == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
			return
		}
		loader = func(ctx context.Context, db *gorm.DB, task string, hours int) (services.ModelTaskUsageSummary, error) {
			return services.NewModelTaskUsageService(db).GetModelTaskUsageAt(ctx, task, hours, d.now())
		}
	}
	summary, err := loader(r.Context(), db, taskName, hours)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if summary.Models == nil {
		summary.Models = []services.ModelTaskUsageModel{}
	}
	writeJSON(w, http.StatusOK, summary)
}

// statisticsDetailedFunc 允许测试注入详细统计快照；生产路径读取 15 分钟任务生成的报告快照
type statisticsDetailedFunc func(context.Context) (stats.DetailedStatisticsData, error)

// handleStatisticsDetailed 对齐 Python statistics.py:30-37：快照尚未生成时返回 503
// Python 侧快照由 statistic.py 的 15 分钟任务生成，Go 对应 detailed_report.go 后台任务，禁止请求内同步构建
func (d Deps) handleStatisticsDetailed(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	if loader := d.statisticsDetailed; loader != nil {
		snapshot, err := loader(r.Context())
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "获取详细统计失败: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	if snapshot, ok := stats.LoadDetailedStatisticsSnapshot(d.InstanceDir); ok {
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "详细统计正在生成，请稍后重试"})
}
