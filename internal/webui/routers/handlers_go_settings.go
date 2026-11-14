package webuiapi

import (
	"io"
	"net/http"
	"path/filepath"

	"maibot-go/internal/runtime/execution"
	"maibot-go/internal/services/statistics"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"
)

// handleGoSettingsGet 返回 Go 平台执行模式与功能开关的完整状态
//
// GET /api/webui/go/settings
func (d Deps) handleGoSettingsGet(w http.ResponseWriter, r *http.Request) {
	instanceDir := d.InstanceDir
	if instanceDir == "" {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}

	profile := execution.LoadProfile(instanceDir)
	toggles := execution.LoadFeatureToggles(instanceDir)

	startTime := ""
	snap := stats.ProcessSnapshot(d.buildVersion())
	if !snap.StartTime.IsZero() {
		startTime = formatSystemStartTime(snap.StartTime.Local())
	}

	sqlitePath := ""
	if d.DatabaseDSN != "" {
		sqlitePath = d.DatabaseDSN
	} else {
		sqlitePath = filepath.Join(instanceDir, "data", "MaiBot.db")
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":          true,
		"execution_mode":   string(profile.Mode),
		"worker_pool_size": profile.WorkerPoolSize,
		"features":         toggles.ToInfoList(),
		"requires_restart": true,
		"start_time":       startTime,
		"sqlite_path":      sqlitePath,
	})
}

// handleGoSettingsUpdate 批量更新功能开关
//
// POST /api/webui/go/settings
// Body: { "features": { "native_session_scheduler": true, ... } }
func (d Deps) handleGoSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	instanceDir := d.InstanceDir
	if instanceDir == "" {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "读取请求体失败")
		return
	}

	var req struct {
		Features map[string]bool `json:"features"`
	}
	if err := sonic.Unmarshal(body, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "JSON 格式错误")
		return
	}
	if req.Features == nil {
		writeAPIError(w, http.StatusBadRequest, "缺少 features 字段")
		return
	}

	// 读取现有开关并合并
	toggles := execution.LoadFeatureToggles(instanceDir)
	toggles.MergeFromMap(req.Features)

	if err := execution.SaveFeatureToggles(instanceDir, toggles); err != nil {
		writeInternalError(w, err)
		return
	}

	profile := execution.LoadProfile(instanceDir)
	writeJSON(w, http.StatusOK, map[string]any{
		"success":          true,
		"message":          "保存成功，需要重启生效",
		"execution_mode":   string(profile.Mode),
		"worker_pool_size": profile.WorkerPoolSize,
		"features":         toggles.ToInfoList(),
		"requires_restart": true,
	})
}

// handleGoSettingsToggle 切换单个功能开关
//
// POST /api/webui/go/toggle/{feature_key}
// Body: { "enabled": true }
func (d Deps) handleGoSettingsToggle(w http.ResponseWriter, r *http.Request) {
	instanceDir := d.InstanceDir
	if instanceDir == "" {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}

	featureKey := chi.URLParam(r, "feature_key")
	if featureKey == "" {
		writeAPIError(w, http.StatusBadRequest, "缺少功能键名")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "读取请求体失败")
		return
	}

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := sonic.Unmarshal(body, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "JSON 格式错误")
		return
	}

	toggles := execution.LoadFeatureToggles(instanceDir)
	if !toggles.SetByKey(featureKey, req.Enabled) {
		writeAPIError(w, http.StatusBadRequest, "未知的功能键: "+featureKey)
		return
	}

	if err := execution.SaveFeatureToggles(instanceDir, toggles); err != nil {
		writeInternalError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"message":  "保存成功，需要重启生效",
		"features": toggles.ToInfoList(),
	})
}

// handleGoSettingsReset 重置所有功能开关为默认值（全部关闭）
//
// POST /api/webui/go/reset
func (d Deps) handleGoSettingsReset(w http.ResponseWriter, r *http.Request) {
	instanceDir := d.InstanceDir
	if instanceDir == "" {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}

	toggles := execution.DefaultFeatureToggles()
	if err := execution.SaveFeatureToggles(instanceDir, toggles); err != nil {
		writeInternalError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"message":  "已重置为默认设置",
		"features": toggles.ToInfoList(),
	})
}

// handleGoSettingsMode 切换执行模式
//
// POST /api/webui/go/mode
// Body: { "mode": "native" }
func (d Deps) handleGoSettingsMode(w http.ResponseWriter, r *http.Request) {
	instanceDir := d.InstanceDir
	if instanceDir == "" {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "读取请求体失败")
		return
	}

	var req struct {
		Mode string `json:"mode"`
	}
	if err := sonic.Unmarshal(body, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "JSON 格式错误")
		return
	}

	mode := execution.Mode(req.Mode)
	if !execution.IsValidMode(mode) {
		writeAPIError(w, http.StatusBadRequest, "无效的执行模式，合法值: compatibility, native")
		return
	}

	profile := execution.LoadProfile(instanceDir)
	profile.Mode = mode
	if err := execution.SaveProfile(instanceDir, profile); err != nil {
		writeInternalError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":          true,
		"message":          "保存成功，需要重启生效",
		"execution_mode":   string(profile.Mode),
		"worker_pool_size": profile.WorkerPoolSize,
	})
}


