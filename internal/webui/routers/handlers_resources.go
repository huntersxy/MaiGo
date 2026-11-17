package webuiapi

import (
	"net/http"
	"time"

	"MaiGo/internal/services/statistics"
)

func (d Deps) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	snap := stats.ProcessSnapshot(d.buildVersion())
	startTime := ""
	if !snap.StartTime.IsZero() {
		startTime = formatSystemStartTime(snap.StartTime.Local())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running":    snap.Running,
		"uptime":     snap.UptimeSec,
		"version":    snap.Version,
		"start_time": startTime,
	})
}

// formatSystemStartTime 对齐 Python datetime.isoformat：本地时间、无时区；
// 微秒非零时固定 6 位（不裁掉尾随 0），微秒为零时省略小数部分。
func formatSystemStartTime(value time.Time) string {
	value = value.Truncate(time.Microsecond)
	if value.Nanosecond() == 0 {
		return value.Format("2006-01-02T15:04:05")
	}
	return value.Format("2006-01-02T15:04:05.000000")
}

func (d Deps) handleStatisticsChats(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	if d.Memory == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
		return
	}
	hours := queryInt(r, "hours", 24, 1, 168)
	limit := queryInt(r, "limit", 10, 1, 100)
	since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)
	var pending map[string]int64
	if c := stats.DefaultMessageCounters(); c != nil {
		pending = c.PendingChatTotals()
	}
	rows, err := stats.QueryTopChats(r.Context(), d.Memory.GetDB(), since, limit, pending)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"hours":   hours,
		"since":   since.Format(time.RFC3339),
		"limit":   limit,
		"data":    rows,
	})
}

func (d Deps) handleStatisticsModels(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	// FastAPI 在进入端点前完成 Query 校验，因此先解析 hours 再判断依赖
	hours, ok := statisticsHours(w, r)
	if !ok {
		return
	}
	if d.Memory == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
		return
	}
	end := d.now()
	start := end.Add(-time.Duration(hours) * time.Hour)
	rows, err := stats.GetModelStatistics(r.Context(), d.Memory.GetDB(), start, end)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}
