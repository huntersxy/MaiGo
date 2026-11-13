package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maibot-go/internal/webui/httputil"
	"net/http"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"

	"maibot-go/internal/plugin_runtime"
	"maibot-go/internal/plugin_runtime/capabilities"
)

// handleRuntimeWebUIUpload 对齐 POST /api/webui/plugins/runtime/webui/{plugin_id}/{page_id}/uploads/{name}
func (d Deps) handleRuntimeWebUIUpload(w http.ResponseWriter, r *http.Request) {
	pluginID := strings.TrimSpace(chi.URLParam(r, "plugin_id"))
	pageID := strings.TrimSpace(chi.URLParam(r, "page_id"))
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	extension, exists := d.webUIExtension(pluginID)
	if !exists {
		httputil.WriteAPIError(w, http.StatusNotFound, "插件 WebUI 已下线")
		return
	}
	page := webUIFindPage(extension, pageID)
	if page == nil {
		httputil.WriteAPIError(w, http.StatusNotFound, "插件页面不存在")
		return
	}
	if webUIMode(page) != "custom" {
		httputil.WriteAPIError(w, http.StatusForbidden, "页面未声明该上传操作")
		return
	}
	uploads, _ := page["uploads"].(map[string]any)
	rawUpload, ok := uploads[name].(map[string]any)
	if !ok {
		httputil.WriteAPIError(w, http.StatusForbidden, "页面未声明该上传操作")
		return
	}
	actionName := strings.TrimSpace(stringFromAny(rawUpload["action"]))
	actions, _ := page["actions"].(map[string]any)
	rawBinding, ok := actions[actionName].(map[string]any)
	if !ok {
		httputil.WriteAPIError(w, http.StatusForbidden, "页面未声明该上传操作")
		return
	}
	apiName := strings.TrimSpace(stringFromAny(rawBinding["api"]))
	apiVersion := strings.TrimSpace(stringFromAny(rawBinding["version"]))
	if apiVersion == "" {
		apiVersion = "1"
	}
	if d.PluginHost == nil {
		httputil.WriteAPIError(w, http.StatusNotFound, "上传操作的静态 API 已下线")
		return
	}
	entry := webUIBindingAPI(d.PluginHost, pluginID, apiName, apiVersion)
	if entry == nil || entry.Dynamic {
		httputil.WriteAPIError(w, http.StatusNotFound, "上传操作的静态 API 已下线")
		return
	}

	limit := int64(pluginruntime.MaxUploadBytes + pluginruntime.MaxWebUIArgumentBytes)
	if rawLength := strings.TrimSpace(r.Header.Get("Content-Length")); rawLength != "" {
		length, err := strconv.ParseInt(rawLength, 10, 64)
		if err != nil {
			httputil.WriteAPIError(w, http.StatusBadRequest, "无效 Content-Length")
			return
		}
		if length > limit {
			httputil.WriteAPIError(w, http.StatusRequestEntityTooLarge, "请求超过大小限制")
			return
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) || strings.Contains(err.Error(), "request body too large") {
			httputil.WriteAPIError(w, http.StatusRequestEntityTooLarge, "请求超过大小限制")
			return
		}
		httputil.WriteValidationError(w, "上传表单无效")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		httputil.WriteValidationError(w, "缺少上传文件")
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, int64(pluginruntime.MaxUploadBytes)+1))
	if err != nil {
		httputil.WriteValidationError(w, "读取上传文件失败")
		return
	}
	if len(raw) > pluginruntime.MaxUploadBytes {
		httputil.WriteValidationError(w, "上传文件超过 20 MiB")
		return
	}

	args := map[string]any{}
	rawArgs := strings.TrimSpace(r.FormValue("args"))
	if rawArgs == "" {
		rawArgs = "{}"
	}
	if err := sonic.Unmarshal([]byte(rawArgs), &args); err != nil {
		httputil.WriteValidationError(w, "args 不是合法 JSON")
		return
	}
	if args == nil {
		args = map[string]any{}
	}
	if _, exists := args["upload_id"]; exists {
		httputil.WriteValidationError(w, "upload_id 由宿主签发")
		return
	}
	confirmed, err := parseWebUIUploadConfirmed(r.FormValue("confirmed"))
	if err != nil {
		httputil.WriteValidationError(w, err.Error())
		return
	}
	if webUIBindingConfirmation(rawBinding) != "" && !confirmed {
		httputil.WriteAPIError(w, http.StatusConflict, "该操作需要确认")
		return
	}
	validationArgs := make(map[string]any, len(args)+1)
	for key, value := range args {
		validationArgs[key] = value
	}
	validationArgs["upload_id"] = strings.Repeat("0", 64)
	if err := validateWebUIBindingArgs(rawBinding, validationArgs); err != nil {
		httputil.WriteValidationError(w, err.Error())
		return
	}
	// 对齐 Python upload_budget.reserve：先占即时配额再接收，超限回 429
	releaseBudget, err := capabilities.DefaultUploadBudget().Acquire(pluginID)
	if err != nil {
		httputil.WriteAPIError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	defer releaseBudget()

	token, err := d.PluginHost.StageWebUIUpload(pluginID, raw)
	if err != nil {
		var capacityError *pluginruntime.UploadCapacityError
		switch {
		case errors.As(err, &capacityError):
			httputil.WriteAPIError(w, http.StatusTooManyRequests, err.Error())
		case strings.Contains(err.Error(), "端口未接线"):
			httputil.WriteAPIError(w, http.StatusServiceUnavailable, err.Error())
		default:
			httputil.WriteValidationError(w, err.Error())
		}
		return
	}
	args["upload_id"] = token

	if !webUIInflightAcquire(pluginID) {
		httputil.WriteAPIError(w, http.StatusTooManyRequests, "该插件已有两个进行中的 WebUI 请求")
		return
	}
	defer webUIInflightRelease(pluginID)
	ctx, cancel := context.WithTimeout(r.Context(), webUIInvokeTimeout)
	defer cancel()
	response, err := d.PluginHost.InvokeAPI(ctx, pluginID, entry.HandlerName, args, 10000)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			httputil.WriteAPIError(w, http.StatusGatewayTimeout, "插件调用超时；操作可能仍在执行，请核实结果后再试")
			return
		}
		httputil.WriteAPIError(w, http.StatusBadGateway, "插件运行时调用失败，请查看插件日志")
		return
	}
	if webUIEnvelopeTimedOut(response) {
		httputil.WriteAPIError(w, http.StatusGatewayTimeout, "插件调用超时；操作可能仍在执行，请核实结果后再试")
		return
	}
	if len(response.Error) > 0 || !webUIPayloadSuccess(response.Payload) {
		httputil.WriteAPIError(w, http.StatusBadGateway, "插件 API 执行失败，请查看插件日志")
		return
	}
	result := response.Payload["result"]
	if err := pluginruntime.ValidateWebUIJSON(result, webUIResultLimit, pluginruntime.MaxWebUIArgumentDepth); err != nil {
		httputil.WriteAPIError(w, http.StatusBadGateway, fmt.Sprintf("插件返回结果非法：%v", err))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "result": result})
}

// parseWebUIUploadConfirmed 对齐 FastAPI Form(bool) 的常见取值
func parseWebUIUploadConfirmed(raw string) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, nil
	}
	switch strings.ToLower(raw) {
	case "on", "yes", "y":
		return true, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("confirmed 不是合法布尔值")
	}
	return value, nil
}
