package webuiapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"MaiGo/internal/runtime/execution"
	"MaiGo/internal/runtime/execution/policy"
	"MaiGo/internal/services/statistics"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"
)

const maxGoSettingsBody = 1 << 20
const goSettingsSwitchTimeout = 5 * time.Second

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
	if errors.Is(err, policy.ErrSwitchInProgress) {
		writeAPIError(w, http.StatusConflict, "已有榫卯切换正在进行")
		return
	}
	if errors.Is(err, execution.ErrConfigChanged) {
		writeAPIError(w, http.StatusConflict, "runtime.toml 已被外部修改。请重启以加载文件，或通过页面重置")
		return
	}
	if errors.Is(err, policy.ErrClosed) {
		writeAPIError(w, http.StatusServiceUnavailable, "执行策略正在关闭")
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		writeAPIError(w, http.StatusServiceUnavailable, "未能在期限内完成切换，原有策略与设置未变更")
		return
	}
	if errors.Is(err, execution.ErrConfigParse) || errors.Is(err, execution.ErrConfigRead) {
		writeAPIError(w, http.StatusInternalServerError, "读取 runtime.toml 失败: "+err.Error())
		return
	}
	writeInternalError(w, err)
}

// goSettingsState 构造各设置接口共享的状态字段。
func goSettingsState(state execution.Status) map[string]any {
	features := state.Features.ToInfoList()
	for index := range features {
		features[index].Active = state.Effective.Mode == execution.ModeNative && features[index].Enabled && features[index].Implemented
	}
	payload := map[string]any{
		"success":                   true,
		"execution_mode":            string(state.Effective.Mode),
		"configured_execution_mode": string(state.Configured.Mode),
		"env_execution_mode":        state.EnvMode,
		"worker_pool_size":          state.Effective.WorkerPoolSize,
		"native_mode_implemented":   execution.NativeModeImplemented,
		"native_mode_active":        execution.NativeModeActive(state.Effective, state.Features),
		"features":                  features,
		"requires_restart":          false,
	}
	if state.SessionScheduler != nil {
		payload["session_scheduler"] = state.SessionScheduler
	}
	if state.ConfigError != "" {
		payload["config_error"] = state.ConfigError
	}
	return payload
}

func (d Deps) applyGoSettings(w http.ResponseWriter, r *http.Request, mutate execution.MutateSettings, message string) {
	if d.ExecutionSettings == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "榫卯运行时未就绪")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), goSettingsSwitchTimeout)
	defer cancel()
	var state execution.Status
	var err error
	if mutate == nil {
		state, err = d.ExecutionSettings.Reset(ctx)
	} else {
		state, err = d.ExecutionSettings.Update(ctx, mutate)
	}
	if err != nil {
		writeGoSettingsError(w, err)
		return
	}
	payload := goSettingsState(state)
	payload["message"] = message
	writeJSON(w, http.StatusOK, payload)
}

// handleGoSettingsGet 返回 Go 平台执行模式与功能开关的完整状态
//
// GET /api/webui/go/settings
func (d Deps) handleGoSettingsGet(w http.ResponseWriter, r *http.Request) {
	instanceDir, ok := d.goInstanceDir(w, r)
	if !ok {
		return
	}

	if d.ExecutionSettings == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "榫卯运行时未就绪")
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

	payload := goSettingsState(d.ExecutionSettings.Status())
	payload["start_time"] = startTime
	payload["sqlite_path"] = sqlitePath
	payload["runtime_toml_path"] = execution.RuntimeTomlPath(instanceDir)
	writeJSON(w, http.StatusOK, payload)
}

// handleGoSettingsUpdate 批量更新功能开关及运行模式
//
// POST /api/webui/go/settings
// Body: { "features": { ... }, "execution_mode": "compatibility", "worker_pool_size": 0 }
func (d Deps) handleGoSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	_, ok := d.goInstanceDir(w, r)
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

	d.applyGoSettings(w, r, func(profile *execution.Profile, toggles *execution.FeatureToggles) error {
		if req.Features != nil {
			if err := validateFeatureChanges(*toggles, req.Features); err != nil {
				return err
			}
			toggles.MergeFromMap(req.Features)
		}
		if req.ExecutionMode != nil {
			profile.Mode = execution.Mode(*req.ExecutionMode)
		}
		if req.WorkerPoolSize != nil {
			profile.WorkerPoolSize = *req.WorkerPoolSize
		}
		return nil
	}, "榫卯设置已保存并应用")
}

// handleGoSettingsToggle 切换单个功能开关
//
// POST /api/webui/go/toggle/{feature_key}
// Body: { "enabled": true }
func (d Deps) handleGoSettingsToggle(w http.ResponseWriter, r *http.Request) {
	_, ok := d.goInstanceDir(w, r)
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
		Enabled *bool `json:"enabled"`
	}
	if err := sonic.Unmarshal(body, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "JSON 格式错误")
		return
	}
	if req.Enabled == nil {
		writeAPIError(w, http.StatusBadRequest, "缺少 enabled 字段")
		return
	}

	d.applyGoSettings(w, r, func(_ *execution.Profile, toggles *execution.FeatureToggles) error {
		if err := validateFeatureChanges(*toggles, map[string]bool{featureKey: *req.Enabled}); err != nil {
			return err
		}
		toggles.SetByKey(featureKey, *req.Enabled)
		return nil
	}, "单项引擎开关已保存并应用")
}

// handleGoSettingsReset 重置所有功能开关为默认值（全部关闭，对齐 Python 状态）
//
// POST /api/webui/go/reset
func (d Deps) handleGoSettingsReset(w http.ResponseWriter, r *http.Request) {
	_, ok := d.goInstanceDir(w, r)
	if !ok {
		return
	}

	d.applyGoSettings(w, r, nil, "已恢复默认配置，所有优化引擎已关闭")
}

// handleGoSettingsMode 切换全局执行模式
//
// POST /api/webui/go/mode
// Body: { "mode": "compatibility" | "native", "worker_pool_size": 0 }
func (d Deps) handleGoSettingsMode(w http.ResponseWriter, r *http.Request) {
	_, ok := d.goInstanceDir(w, r)
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

	d.applyGoSettings(w, r, func(profile *execution.Profile, _ *execution.FeatureToggles) error {
		profile.Mode = execution.Mode(req.Mode)
		if req.WorkerPoolSize != nil {
			profile.WorkerPoolSize = *req.WorkerPoolSize
		}
		return nil
	}, "执行模式已保存并应用，环境变量优先")
}

// handleGoSettingsRestart 从 Go 开发者页面触发进程重启
//
// POST /api/webui/go/restart
func (d Deps) handleGoSettingsRestart(w http.ResponseWriter, r *http.Request) {
	d.respondWorkerRestart(w, "麦麦正在重启中...")
}
