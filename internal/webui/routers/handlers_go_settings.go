package webuiapi

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"maibot-go/internal/runtime/execution"
	"maibot-go/internal/services/statistics"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"
)

const maxGoSettingsBody = 1 << 20

// goInstanceDir 校验实例目录；所有 Go 平台设置接口都依赖它定位 runtime.toml。
func (d Deps) goInstanceDir(w http.ResponseWriter, r *http.Request) (string, bool) {
	if d.InstanceDir == "" {
		writeInstanceDirMissing(w, r.URL.Path)
		return "", false
	}
	return d.InstanceDir, true
}

// loadGoSettings 读取执行配置与功能开关，失败时写出 500 并返回 false。
func (d Deps) loadGoSettings(w http.ResponseWriter) (execution.Profile, execution.FeatureToggles, bool) {
	profile, err := execution.LoadProfile(d.InstanceDir)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "读取执行配置失败: "+err.Error())
		return execution.Profile{}, execution.FeatureToggles{}, false
	}
	toggles, err := execution.LoadFeatureToggles(d.InstanceDir)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "读取功能开关失败: "+err.Error())
		return execution.Profile{}, execution.FeatureToggles{}, false
	}
	return profile, toggles, true
}

func readGoSettingsBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxGoSettingsBody))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "读取请求体失败")
		return nil, false
	}
	return body, true
}

// effectiveProfile 返回保存后实际生效的配置；环境变量覆盖时以它为准。
func effectiveProfile(instanceDir string, fallback execution.Profile) execution.Profile {
	profile, err := execution.LoadProfile(instanceDir)
	if err != nil {
		return fallback
	}
	return profile
}

// validateFeatureRequest 拒绝未知键，以及尚未接入运行时却要求开启的键。
func validateFeatureRequest(w http.ResponseWriter, features map[string]bool) bool {
	for key := range features {
		if !execution.IsKnownFeatureKey(key) {
			writeAPIError(w, http.StatusBadRequest, "未知的功能键: "+key)
			return false
		}
	}
	if keys := execution.UnimplementedEnabledKeys(features); len(keys) > 0 {
		writeAPIError(w, http.StatusConflict, "以下功能尚未接入运行时，暂不能开启: "+strings.Join(keys, ", "))
		return false
	}
	return true
}

// handleGoSettingsGet 返回 Go 平台执行模式与功能开关的完整状态
//
// GET /api/webui/go/settings
func (d Deps) handleGoSettingsGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.goInstanceDir(w, r); !ok {
		return
	}

	profile, toggles, ok := d.loadGoSettings(w)
	if !ok {
		return
	}

	startTime := ""
	snap := stats.ProcessSnapshot(d.buildVersion())
	if !snap.StartTime.IsZero() {
		startTime = formatSystemStartTime(snap.StartTime.Local())
	}

	sqlitePath := ""
	if d.DatabaseDSN != "" {
		sqlitePath = d.DatabaseDSN
	} else {
		sqlitePath = filepath.Join(d.InstanceDir, "data", "MaiBot.db")
	}

	payload := map[string]any{
		"success":                 true,
		"execution_mode":          string(profile.Mode),
		"worker_pool_size":        profile.WorkerPoolSize,
		"native_mode_implemented": execution.NativeModeImplemented,
		"native_mode_active":      execution.NativeModeActive(profile, toggles),
		"env_execution_mode":      strings.TrimSpace(os.Getenv(execution.EnvExecutionMode)),
		"features":                toggles.ToInfoList(),
		"requires_restart":        true,
		"start_time":              startTime,
		"sqlite_path":             sqlitePath,
		"runtime_toml_path":       execution.RuntimeTomlPath(d.InstanceDir),
	}
	if d.SessionSchedulerStats != nil {
		if stats := d.SessionSchedulerStats(); stats != nil {
			payload["session_scheduler"] = stats
		}
	}
	writeJSON(w, http.StatusOK, payload)
}

// handleGoSettingsUpdate 批量更新功能开关及运行模式
//
// POST /api/webui/go/settings
// Body: { "features": { ... }, "execution_mode": "compatibility", "worker_pool_size": 0 }
func (d Deps) handleGoSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	instanceDir, ok := d.goInstanceDir(w, r)
	if !ok {
		return
	}

	body, ok := readGoSettingsBody(w, r)
	if !ok {
		return
	}

	var req struct {
		Features       map[string]bool `json:"features"`
		ExecutionMode  *string         `json:"execution_mode"`
		WorkerPoolSize *int            `json:"worker_pool_size"`
	}
	if err := sonic.Unmarshal(body, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "JSON 格式错误")
		return
	}
	if req.Features != nil && !validateFeatureRequest(w, req.Features) {
		return
	}

	profile, toggles, ok := d.loadGoSettings(w)
	if !ok {
		return
	}

	if req.Features != nil {
		toggles.MergeFromMap(req.Features)
	}
	if req.ExecutionMode != nil {
		profile.Mode = execution.Mode(*req.ExecutionMode)
	}
	if req.WorkerPoolSize != nil {
		profile.WorkerPoolSize = *req.WorkerPoolSize
	}
	if err := execution.ValidateProfile(profile); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := execution.SaveAll(instanceDir, profile, toggles); err != nil {
		writeInternalError(w, err)
		return
	}
	profile = effectiveProfile(instanceDir, profile)

	writeJSON(w, http.StatusOK, map[string]any{
		"success":                 true,
		"message":                 "Go 平台设置已保存，需要重启服务生效",
		"execution_mode":          string(profile.Mode),
		"worker_pool_size":        profile.WorkerPoolSize,
		"native_mode_implemented": execution.NativeModeImplemented,
		"native_mode_active":      execution.NativeModeActive(profile, toggles),
		"features":                toggles.ToInfoList(),
		"requires_restart":        true,
	})
}

// handleGoSettingsToggle 切换单个功能开关
//
// POST /api/webui/go/toggle/{feature_key}
// Body: { "enabled": true }
func (d Deps) handleGoSettingsToggle(w http.ResponseWriter, r *http.Request) {
	instanceDir, ok := d.goInstanceDir(w, r)
	if !ok {
		return
	}

	featureKey := chi.URLParam(r, "feature_key")
	if featureKey == "" {
		writeAPIError(w, http.StatusBadRequest, "缺少功能键名")
		return
	}
	if !execution.IsKnownFeatureKey(featureKey) {
		writeAPIError(w, http.StatusBadRequest, "未知的功能键: "+featureKey)
		return
	}

	body, ok := readGoSettingsBody(w, r)
	if !ok {
		return
	}

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := sonic.Unmarshal(body, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "JSON 格式错误")
		return
	}
	if req.Enabled && !execution.IsFeatureImplemented(featureKey) {
		writeAPIError(w, http.StatusConflict, fmt.Sprintf("功能 %s 尚未接入运行时，暂不能开启", featureKey))
		return
	}

	toggles, err := execution.LoadFeatureToggles(instanceDir)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "读取功能开关失败: "+err.Error())
		return
	}
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
		"message":  "开关已更新，需要重启生效",
		"features": toggles.ToInfoList(),
	})
}

// handleGoSettingsReset 重置所有功能开关为默认值（全部关闭，对齐 Python 状态）
//
// POST /api/webui/go/reset
func (d Deps) handleGoSettingsReset(w http.ResponseWriter, r *http.Request) {
	instanceDir, ok := d.goInstanceDir(w, r)
	if !ok {
		return
	}

	toggles := execution.DefaultFeatureToggles()
	profile := execution.DefaultProfile()

	if err := execution.ResetAll(instanceDir); err != nil {
		writeInternalError(w, err)
		return
	}
	profile = effectiveProfile(instanceDir, profile)

	writeJSON(w, http.StatusOK, map[string]any{
		"success":                 true,
		"message":                 "已重置为默认状态（全关，对齐 Python）",
		"execution_mode":          string(profile.Mode),
		"worker_pool_size":        profile.WorkerPoolSize,
		"native_mode_implemented": execution.NativeModeImplemented,
		"native_mode_active":      execution.NativeModeActive(profile, toggles),
		"features":                toggles.ToInfoList(),
	})
}

// handleGoSettingsMode 切换全局执行模式
//
// POST /api/webui/go/mode
// Body: { "mode": "compatibility" | "native", "worker_pool_size": 0 }
func (d Deps) handleGoSettingsMode(w http.ResponseWriter, r *http.Request) {
	instanceDir, ok := d.goInstanceDir(w, r)
	if !ok {
		return
	}

	body, ok := readGoSettingsBody(w, r)
	if !ok {
		return
	}

	var req struct {
		Mode           string `json:"mode"`
		WorkerPoolSize *int   `json:"worker_pool_size"`
	}
	if err := sonic.Unmarshal(body, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "JSON 格式错误")
		return
	}

	profile, toggles, ok := d.loadGoSettings(w)
	if !ok {
		return
	}
	profile.Mode = execution.Mode(req.Mode)
	if req.WorkerPoolSize != nil {
		profile.WorkerPoolSize = *req.WorkerPoolSize
	}
	if err := execution.ValidateProfile(profile); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := execution.SaveProfile(instanceDir, profile); err != nil {
		writeInternalError(w, err)
		return
	}
	profile = effectiveProfile(instanceDir, profile)

	writeJSON(w, http.StatusOK, map[string]any{
		"success":                 true,
		"message":                 "执行模式已更新，需要重启生效",
		"execution_mode":          string(profile.Mode),
		"worker_pool_size":        profile.WorkerPoolSize,
		"native_mode_implemented": execution.NativeModeImplemented,
		"native_mode_active":      execution.NativeModeActive(profile, toggles),
	})
}

// handleGoSettingsRestart 从 Go 开发者页面触发进程重启
//
// POST /api/webui/go/restart
func (d Deps) handleGoSettingsRestart(w http.ResponseWriter, r *http.Request) {
	d.respondWorkerRestart(w, "麦麦正在重启中，设置将在重启后生效...")
}
