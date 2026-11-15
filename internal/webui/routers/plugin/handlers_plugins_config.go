package plugin

import (
	"MaiGo/internal/webui/httputil"
	"net/http"
	"os"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"
	"github.com/pelletier/go-toml/v2"
)

// pluginConfigSnapshot 汇总运行时 inspect 结果，字段对齐 InspectPluginConfigResultPayload
type pluginConfigSnapshot struct {
	ConfigSchema     map[string]any
	NormalizedConfig map[string]any
	DefaultConfig    map[string]any
	ConfigError      string
	Enabled          bool
}

type pluginRawConfigRequest struct {
	Config string `json:"config"`
}

type pluginConfigUpdateRequest struct {
	Config any `json:"config"`
}

// handlePluginConfigBundle 对齐 GET /plugins/config/{plugin_id}/bundle
func (d Deps) handlePluginConfigBundle(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "plugin_id")
	paths, err := d.resolvePluginConfigPaths(pluginID)
	if err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	snapshot, err := d.inspectPluginConfig(r.Context(), pluginID, nil, false)
	if err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	rawConfig, rawErr := readPluginConfigText(paths.configPath)
	if rawErr != nil {
		writePluginRouteFailure(w, rawErr)
		return
	}
	if snapshot.ConfigError != "" {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"success":      true,
			"schema":       nil,
			"config":       map[string]any{},
			"raw_config":   rawConfig,
			"config_error": snapshot.ConfigError,
		})
		return
	}
	currentConfig := copyStringAnyMap(snapshot.NormalizedConfig)
	if fields := virtualGeneralFieldNames(snapshot); len(fields) > 0 {
		currentConfig = foldRootFieldsIntoVirtualGeneral(currentConfig, fields)
	}
	if len(snapshot.ConfigSchema) == 0 {
		httputil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"detail": "插件未提供配置定义"})
		return
	}
	message := ""
	if _, statErr := os.Stat(paths.configPath); os.IsNotExist(statErr) {
		message = "配置文件不存在，已返回默认配置"
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"schema":     snapshot.ConfigSchema,
		"config":     currentConfig,
		"raw_config": rawConfig,
		"message":    message,
	})
}

// handlePluginConfigSchema 对齐 GET /plugins/config/{plugin_id}/schema
func (d Deps) handlePluginConfigSchema(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "plugin_id")
	if _, err := d.resolvePluginConfigPaths(pluginID); err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	snapshot, err := d.inspectPluginConfig(r.Context(), pluginID, nil, false)
	if err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	if snapshot.ConfigError != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": snapshot.ConfigError})
		return
	}
	if len(snapshot.ConfigSchema) == 0 {
		httputil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"detail": "插件未提供配置定义"})
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "schema": snapshot.ConfigSchema})
}

// handlePluginConfigRawGet 对齐 GET /plugins/config/{plugin_id}/raw
func (d Deps) handlePluginConfigRawGet(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "plugin_id")
	paths, err := d.resolvePluginConfigPaths(pluginID)
	if err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	rawConfig, err := readPluginConfigText(paths.configPath)
	if err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	if _, statErr := os.Stat(paths.configPath); os.IsNotExist(statErr) {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "config": "", "message": "配置文件不存在"})
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "config": rawConfig})
}

// handlePluginConfigRawUpdate 对齐 PUT /plugins/config/{plugin_id}/raw
func (d Deps) handlePluginConfigRawUpdate(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "plugin_id")
	paths, err := d.resolvePluginConfigPaths(pluginID)
	if err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	body, ok := httputil.ReadJSONBody(w, r)
	if !ok {
		return
	}
	if len(body) == 0 {
		httputil.WriteValidationError(w, "请求格式错误")
		return
	}
	var request pluginRawConfigRequest
	if err := sonic.Unmarshal(body, &request); err != nil {
		httputil.WriteValidationError(w, "请求格式错误")
		return
	}
	var document map[string]any
	if err := toml.Unmarshal([]byte(request.Config), &document); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": "TOML 格式错误: " + err.Error()})
		return
	}
	if _, err := backupPluginConfigFile(paths.configPath, "backup", false); err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	if err := writePluginConfigText(paths.configPath, request.Config); err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	runtimeStatus, syncErr := d.pluginConfigRuntimeStatus(r.Context(), pluginID)
	if syncErr != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"detail": "服务器错误: " + syncErr.Error()})
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":        true,
		"message":        "配置已保存",
		"runtime_status": runtimeStatus,
		"note":           pluginConfigStatusNote(runtimeStatus),
	})
}

// handlePluginConfigGet 对齐 GET /plugins/config/{plugin_id}
func (d Deps) handlePluginConfigGet(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "plugin_id")
	if _, err := d.resolvePluginConfigPaths(pluginID); err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	snapshot, err := d.inspectPluginConfig(r.Context(), pluginID, nil, false)
	if err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	if snapshot.ConfigError != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": snapshot.ConfigError})
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "config": copyStringAnyMap(snapshot.NormalizedConfig)})
}

// handlePluginConfigUpdate 对齐 PUT /plugins/config/{plugin_id}
func (d Deps) handlePluginConfigUpdate(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "plugin_id")
	paths, err := d.resolvePluginConfigPaths(pluginID)
	if err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	body, ok := httputil.ReadJSONBody(w, r)
	if !ok {
		return
	}
	if len(body) == 0 {
		httputil.WriteValidationError(w, "请求格式错误")
		return
	}
	var request pluginConfigUpdateRequest
	if err := sonic.Unmarshal(body, &request); err != nil {
		httputil.WriteValidationError(w, "请求格式错误")
		return
	}
	configData := request.Config
	patch, isObject := configData.(map[string]any)
	if isObject {
		normalizedPatch := normalizeDottedKeys(patch)
		snapshot, inspectErr := d.inspectPluginConfig(r.Context(), pluginID, nil, false)
		if inspectErr != nil {
			writePluginRouteFailure(w, inspectErr)
			return
		}
		if snapshot.ConfigError != "" {
			httputil.WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": snapshot.ConfigError})
			return
		}
		if fields := virtualGeneralFieldNames(snapshot); len(fields) > 0 {
			unfoldVirtualGeneralIntoRoot(normalizedPatch)
		}
		merged := copyStringAnyMap(snapshot.NormalizedConfig)
		deepMergeMaps(merged, normalizedPatch)
		validated, validateErr := d.validatePluginConfig(r.Context(), pluginID, merged)
		if validateErr != nil {
			writePluginRouteFailure(w, validateErr)
			return
		}
		configData = validated
	}
	if _, err := backupPluginConfigFile(paths.configPath, "backup", false); err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	encoded, err := toml.Marshal(configData)
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"detail": "服务器错误: " + err.Error()})
		return
	}
	if err := writePluginConfigText(paths.configPath, string(encoded)); err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "配置已保存",
		"note":    "配置更改将自动热更新到对应插件",
	})
}

// handlePluginConfigReset 对齐 POST /plugins/config/{plugin_id}/reset
func (d Deps) handlePluginConfigReset(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "plugin_id")
	paths, err := d.resolvePluginConfigPaths(pluginID)
	if err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	if _, statErr := os.Stat(paths.configPath); os.IsNotExist(statErr) {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "message": "配置文件不存在，无需重置"})
		return
	}
	backupPath, err := movePluginConfigBackup(paths.configPath, "reset")
	if err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "配置已重置，运行时将自动刷新为默认配置",
		"backup":  backupPath,
	})
}

// handlePluginConfigToggle 对齐 POST /plugins/config/{plugin_id}/toggle
func (d Deps) handlePluginConfigToggle(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "plugin_id")
	paths, err := d.resolvePluginConfigPaths(pluginID)
	if err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	rawConfig, err := readPluginConfigText(paths.configPath)
	if err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	currentEnabled, err := readPluginEnabledValue(rawConfig)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	newEnabled := !currentEnabled
	if err := writePluginConfigText(paths.configPath, setPluginEnabledText(rawConfig, newEnabled)); err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	// 对齐 Python integration.py:278-292：启用插件前先同步依赖，环境或阻止列表变化时重启运行时
	if newEnabled {
		if err := d.syncPluginDependenciesOnEnable(r.Context(), pluginID); err != nil {
			httputil.WriteJSON(w, http.StatusConflict, map[string]any{"detail": "启用插件时同步依赖失败：" + err.Error()})
			return
		}
	}
	runtimeStatus, syncErr := d.pluginConfigRuntimeStatus(r.Context(), pluginID)
	if syncErr != nil {
		httputil.WriteJSON(w, http.StatusConflict, map[string]any{"detail": "插件状态同步失败：" + syncErr.Error()})
		return
	}
	statusText := "启用"
	if !newEnabled {
		statusText = "禁用"
	}
	message := "插件已" + statusText
	note := "状态更改已同步到插件运行时"
	if runtimeStatus == "stopped" {
		message = "插件" + statusText + "配置已保存"
		note = "运行时尚未启动"
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":        true,
		"enabled":        newEnabled,
		"runtime_status": runtimeStatus,
		"message":        message,
		"note":           note,
	})
}

// handlePluginConfigWidgets 对齐 GET /plugins/config/{plugin_id}/widgets
func (d Deps) handlePluginConfigWidgets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	pluginID := chi.URLParam(r, "plugin_id")
	if _, err := d.resolvePluginConfigPaths(pluginID); err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	snapshot, err := d.configWidgets(r.Context(), pluginID, "")
	if err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, snapshot)
}

// handlePluginConfigWidgetAction 对齐 POST /plugins/config/{plugin_id}/widgets/{action_id}
func (d Deps) handlePluginConfigWidgetAction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	pluginID := chi.URLParam(r, "plugin_id")
	if _, err := d.resolvePluginConfigPaths(pluginID); err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	snapshot, err := d.configWidgets(r.Context(), pluginID, chi.URLParam(r, "action_id"))
	if err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, snapshot)
}
