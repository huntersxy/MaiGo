package webuiapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"maibot-go/internal/runtime/execution"
	"maibot-go/internal/services/statistics"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"
)

const maxGoSettingsBody = 1 << 20

// goSettingsRequestError 携带 HTTP 状态码的请求错误，供 UpdateRuntime 回调中止写入。
type goSettingsRequestError struct {
	status  int
	message string
}

func (e *goSettingsRequestError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

// goInstanceDir 校验实例目录；所有 Go 平台设置接口都依赖它定位 runtime.toml。
func (d Deps) goInstanceDir(w http.ResponseWriter, r *http.Request) (string, bool) {
	if d.InstanceDir == "" {
		writeInstanceDirMissing(w, r.URL.Path)
		return "", false
	}
	return d.InstanceDir, true
}

func readGoSettingsBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxGoSettingsBody))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "读取请求体失败")
		return nil, false
	}
	return body, true
}

// envExecutionMode 返回环境变量中的执行模式（保留原始大小写，仅用于展示）。
func envExecutionMode() string {
	return strings.TrimSpace(os.Getenv(execution.EnvExecutionMode))
}

// shouldPersistMode 判断请求中的模式是否应写入文件。
//
// 环境变量只应影响实际生效值，不应因为页面回传当前生效模式就被持久化。
// 用户显式选择与环境变量不同的模式时仍然写入，便于以后移除环境变量后生效。
func shouldPersistMode(requested execution.Mode) bool {
	envMode := strings.ToLower(envExecutionMode())
	return envMode == "" || string(requested) != envMode
}

// validateFeatureChanges 只拒绝“本次新开启”的未接入功能。
//
// 已存在于文件里的 true 值允许原样回传，避免整表保存被旧开关永久锁死；
// 关闭未接入功能始终允许。
func validateFeatureChanges(current execution.FeatureToggles, changes map[string]bool) error {
	unknown := make([]string, 0)
	unimplemented := make([]string, 0)
	for key, enabled := range changes {
		if !execution.IsKnownFeatureKey(key) {
			unknown = append(unknown, key)
			continue
		}
		if enabled && !current.Enabled(key) && !execution.IsFeatureImplemented(key) {
			unimplemented = append(unimplemented, key)
		}
	}
	sort.Strings(unknown)
	sort.Strings(unimplemented)
	if len(unknown) > 0 {
		return &goSettingsRequestError{
			status:  http.StatusBadRequest,
			message: "未知的功能键: " + strings.Join(unknown, ", "),
		}
	}
	if len(unimplemented) > 0 {
		return &goSettingsRequestError{
			status:  http.StatusConflict,
			message: "以下功能尚未接入运行时，暂不能开启: " + strings.Join(unimplemented, ", "),
		}
	}
	return nil
}

// writeGoSettingsError 把执行配置错误映射成稳定的 HTTP 响应。
func writeGoSettingsError(w http.ResponseWriter, err error) {
	var requestErr *goSettingsRequestError
	if errors.As(err, &requestErr) {
		writeAPIError(w, requestErr.status, requestErr.message)
		return
	}
	if errors.Is(err, execution.ErrInvalidMode) || errors.Is(err, execution.ErrInvalidWorkerPoolSize) {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, execution.ErrConfigParse) || errors.Is(err, execution.ErrConfigRead) {
		writeAPIError(w, http.StatusInternalServerError, "读取 runtime.toml 失败: "+err.Error())
		return
	}
	writeInternalError(w, err)
}

// goSettingsState 构造各设置接口共享的状态字段。
func goSettingsState(profile execution.Profile, toggles execution.FeatureToggles) map[string]any {
	return map[string]any{
		"success":                 true,
		"execution_mode":          string(profile.Mode),
		"worker_pool_size":        profile.WorkerPoolSize,
		"native_mode_implemented": execution.NativeModeImplemented,
		"native_mode_active":      execution.NativeModeActive(profile, toggles),
		"features":                toggles.ToInfoList(),
		"requires_restart":        true,
	}
}

// handleGoSettingsGet 返回 Go 平台执行模式与功能开关的完整状态
//
// GET /api/webui/go/settings
func (d Deps) handleGoSettingsGet(w http.ResponseWriter, r *http.Request) {
	instanceDir, ok := d.goInstanceDir(w, r)
	if !ok {
		return
	}

	profile, toggles, err := execution.LoadRuntime(instanceDir)
	if err != nil {
		writeGoSettingsError(w, err)
		return
	}

	startTime := ""
	snap := stats.ProcessSnapshot(d.buildVersion())
	if !snap.StartTime.IsZero() {
		startTime = formatSystemStartTime(snap.StartTime.Local())
	}

	sqlitePath := d.DatabaseDSN
	if sqlitePath == "" {
		sqlitePath = filepath.Join(instanceDir, "data", "MaiBot.db")
	}

	payload := goSettingsState(profile, toggles)
	payload["env_execution_mode"] = envExecutionMode()
	payload["start_time"] = startTime
	payload["sqlite_path"] = sqlitePath
	payload["runtime_toml_path"] = execution.RuntimeTomlPath(instanceDir)
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
	if req.Features == nil && req.ExecutionMode == nil && req.WorkerPoolSize == nil {
		writeAPIError(w, http.StatusBadRequest, "缺少可更新字段")
		return
	}

	profile, toggles, err := execution.UpdateRuntime(instanceDir, func(profile *execution.Profile, toggles *execution.FeatureToggles) error {
		if req.Features != nil {
			if err := validateFeatureChanges(*toggles, req.Features); err != nil {
				return err
			}
			toggles.MergeFromMap(req.Features)
		}
		if req.ExecutionMode != nil {
			mode := execution.Mode(*req.ExecutionMode)
			if !execution.IsValidMode(mode) {
				return &goSettingsRequestError{
					status:  http.StatusBadRequest,
					message: fmt.Sprintf("无效的执行模式 %q，合法值: compatibility, native", mode),
				}
			}
			if shouldPersistMode(mode) {
				profile.Mode = mode
			}
		}
		if req.WorkerPoolSize != nil {
			profile.WorkerPoolSize = *req.WorkerPoolSize
		}
		if err := execution.ValidateProfile(*profile); err != nil {
			return &goSettingsRequestError{status: http.StatusBadRequest, message: err.Error()}
		}
		return nil
	})
	if err != nil {
		writeGoSettingsError(w, err)
		return
	}

	effective, err := execution.ApplyEnvMode(profile)
	if err != nil {
		writeGoSettingsError(w, err)
		return
	}

	payload := goSettingsState(effective, toggles)
	payload["message"] = "Go 平台设置已保存，需要重启服务生效"
	writeJSON(w, http.StatusOK, payload)
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

	profile, toggles, err := execution.UpdateRuntime(instanceDir, func(_ *execution.Profile, toggles *execution.FeatureToggles) error {
		if req.Enabled && !toggles.Enabled(featureKey) && !execution.IsFeatureImplemented(featureKey) {
			return &goSettingsRequestError{
				status:  http.StatusConflict,
				message: fmt.Sprintf("功能 %s 尚未接入运行时，暂不能开启", featureKey),
			}
		}
		if !toggles.SetByKey(featureKey, req.Enabled) {
			return &goSettingsRequestError{
				status:  http.StatusBadRequest,
				message: "未知的功能键: " + featureKey,
			}
		}
		return nil
	})
	if err != nil {
		writeGoSettingsError(w, err)
		return
	}

	effective, err := execution.ApplyEnvMode(profile)
	if err != nil {
		writeGoSettingsError(w, err)
		return
	}

	payload := goSettingsState(effective, toggles)
	payload["message"] = "开关已更新，需要重启生效"
	writeJSON(w, http.StatusOK, payload)
}

// handleGoSettingsReset 重置所有功能开关为默认值（全部关闭，对齐 Python 状态）
//
// POST /api/webui/go/reset
func (d Deps) handleGoSettingsReset(w http.ResponseWriter, r *http.Request) {
	instanceDir, ok := d.goInstanceDir(w, r)
	if !ok {
		return
	}

	if err := execution.ResetAll(instanceDir); err != nil {
		writeInternalError(w, err)
		return
	}
	profile, toggles, err := execution.LoadRuntime(instanceDir)
	if err != nil {
		writeGoSettingsError(w, err)
		return
	}

	payload := goSettingsState(profile, toggles)
	payload["message"] = "已重置为默认状态（全关，对齐 Python）"
	writeJSON(w, http.StatusOK, payload)
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

	profile, toggles, err := execution.UpdateRuntime(instanceDir, func(profile *execution.Profile, _ *execution.FeatureToggles) error {
		mode := execution.Mode(req.Mode)
		if !execution.IsValidMode(mode) {
			return &goSettingsRequestError{
				status:  http.StatusBadRequest,
				message: fmt.Sprintf("无效的执行模式 %q，合法值: compatibility, native", mode),
			}
		}
		if shouldPersistMode(mode) {
			profile.Mode = mode
		}
		if req.WorkerPoolSize != nil {
			profile.WorkerPoolSize = *req.WorkerPoolSize
		}
		if err := execution.ValidateProfile(*profile); err != nil {
			return &goSettingsRequestError{status: http.StatusBadRequest, message: err.Error()}
		}
		return nil
	})
	if err != nil {
		writeGoSettingsError(w, err)
		return
	}

	effective, err := execution.ApplyEnvMode(profile)
	if err != nil {
		writeGoSettingsError(w, err)
		return
	}

	payload := goSettingsState(effective, toggles)
	payload["message"] = "执行模式已更新，需要重启生效"
	writeJSON(w, http.StatusOK, payload)
}

// handleGoSettingsRestart 从 Go 开发者页面触发进程重启
//
// POST /api/webui/go/restart
func (d Deps) handleGoSettingsRestart(w http.ResponseWriter, r *http.Request) {
	d.respondWorkerRestart(w, "麦麦正在重启中，设置将在重启后生效...")
}
