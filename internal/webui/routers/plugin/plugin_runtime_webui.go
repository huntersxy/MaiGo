package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maibot-go/internal/webui/httputil"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"

	"maibot-go/internal/plugin_runtime"
	"maibot-go/internal/plugin_runtime/protocol"
)

const (
	webUIInvokeTimeout      = 11 * time.Second
	webUIInvokePluginBudget = 2
	webUIResultLimit        = 524288
)

var webUIInflight = struct {
	mu     sync.Mutex
	counts map[string]int
}{counts: map[string]int{}}

func webUIInflightAcquire(pluginID string) bool {
	webUIInflight.mu.Lock()
	defer webUIInflight.mu.Unlock()
	if webUIInflight.counts[pluginID] >= webUIInvokePluginBudget {
		return false
	}
	webUIInflight.counts[pluginID]++
	return true
}

func webUIInflightRelease(pluginID string) {
	webUIInflight.mu.Lock()
	defer webUIInflight.mu.Unlock()
	remaining := webUIInflight.counts[pluginID] - 1
	if remaining <= 0 {
		delete(webUIInflight.counts, pluginID)
		return
	}
	webUIInflight.counts[pluginID] = remaining
}

func webUIPageDocument(page map[string]any) string {
	document, _ := page["document"].(string)
	return document
}

func webUIDocumentRevision(document string) string {
	sum := sha256.Sum256([]byte(document))
	return hex.EncodeToString(sum[:])
}

func webUIMode(page map[string]any) string {
	mode, _ := page["mode"].(string)
	return strings.TrimSpace(mode)
}

func webUIPageID(page map[string]any) string {
	id, _ := page["id"].(string)
	return strings.TrimSpace(id)
}

// webUIExtensionPages 返回插件的原始页面声明，保持 Runner 上报顺序
func webUIExtensionPages(extension map[string]any) []map[string]any {
	rawPages, _ := extension["pages"].([]any)
	pages := make([]map[string]any, 0, len(rawPages))
	for _, raw := range rawPages {
		if page, ok := raw.(map[string]any); ok {
			pages = append(pages, page)
		}
	}
	return pages
}

// handleRuntimeWebUIList 对齐 GET /api/webui/plugins/runtime/webui
func (d Deps) handleRuntimeWebUIList(w http.ResponseWriter, r *http.Request) {
	extensions := map[string]map[string]any{}
	if d.PluginHost != nil {
		extensions = d.PluginHost.GetWebUIExtensions()
	}
	pluginIDs := make([]string, 0, len(extensions))
	for pluginID := range extensions {
		pluginIDs = append(pluginIDs, pluginID)
	}
	sort.Strings(pluginIDs)
	items := make([]map[string]any, 0, len(pluginIDs))
	for _, pluginID := range pluginIDs {
		extension := extensions[pluginID]
		item := make(map[string]any, len(extension)+2)
		for key, value := range extension {
			if key == "pages" {
				continue
			}
			item[key] = value
		}
		item["plugin_id"] = pluginID
		pages := make([]map[string]any, 0)
		for _, page := range webUIExtensionPages(extension) {
			serialized := make(map[string]any, len(page)+1)
			for key, value := range page {
				if key == "document" {
					continue
				}
				serialized[key] = value
			}
			if webUIMode(page) == "custom" {
				serialized["document_revision"] = webUIDocumentRevision(webUIPageDocument(page))
			} else {
				serialized["document_revision"] = nil
			}
			pages = append(pages, serialized)
		}
		item["pages"] = pages
		items = append(items, item)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":        true,
		"schema_version": 2,
		"capabilities":   pluginruntime.WebUIRequiredCapabilities,
		"extensions":     items,
	})
}

// handleRuntimeWebUIReload 对齐 POST /api/webui/plugins/runtime/webui/{plugin_id}/reload
func (d Deps) handleRuntimeWebUIReload(w http.ResponseWriter, r *http.Request) {
	pluginID := strings.TrimSpace(chi.URLParam(r, "plugin_id"))
	if _, exists := d.webUIExtension(pluginID); !exists {
		httputil.WriteAPIError(w, http.StatusNotFound, "插件 WebUI 已下线")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	// Runner 协议方法与 Python supervisor.reload_webui_documents 一致：
	// 先让 Runner 重读插件页面文件，再用返回的 documents 整体替换已校验扩展
	response, err := d.PluginHost.InvokePlugin(ctx, "plugin.webui.reload_documents", pluginID, "", nil, 10000)
	if err != nil {
		httputil.WriteAPIError(w, http.StatusBadGateway, "插件运行时调用失败，请查看插件日志")
		return
	}
	if len(response.Error) > 0 {
		message := strings.TrimSpace(stringFromAny(response.Error["message"]))
		if message == "" {
			message = "页面文件读取失败"
		}
		httputil.WriteAPIError(w, http.StatusUnprocessableEntity, message)
		return
	}
	if err := d.PluginHost.ReplaceWebUIDocuments(pluginID, response.Payload); err != nil {
		httputil.WriteAPIError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (d Deps) webUIExtension(pluginID string) (map[string]any, bool) {
	if d.PluginHost == nil || strings.TrimSpace(pluginID) == "" {
		return nil, false
	}
	extension, exists := d.PluginHost.GetWebUIExtensions()[pluginID]
	if !exists || extension == nil {
		return nil, false
	}
	return extension, true
}

func webUIFindPage(extension map[string]any, pageID string) map[string]any {
	for _, page := range webUIExtensionPages(extension) {
		if webUIPageID(page) == pageID {
			return page
		}
	}
	return nil
}

// handleRuntimeWebUIDocument 对齐 GET /api/webui/plugins/runtime/webui/{plugin_id}/{page_id}/document
func (d Deps) handleRuntimeWebUIDocument(w http.ResponseWriter, r *http.Request) {
	pluginID := strings.TrimSpace(chi.URLParam(r, "plugin_id"))
	pageID := strings.TrimSpace(chi.URLParam(r, "page_id"))
	extension, exists := d.webUIExtension(pluginID)
	if !exists {
		httputil.WriteAPIError(w, http.StatusNotFound, "插件 WebUI 已下线")
		return
	}
	page := webUIFindPage(extension, pageID)
	if page == nil || webUIMode(page) != "custom" {
		httputil.WriteAPIError(w, http.StatusNotFound, "自定义页面不存在")
		return
	}
	document := webUIPageDocument(page)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"document": document,
		"revision": webUIDocumentRevision(document),
	})
}

type webUIInvocationBody struct {
	Args      map[string]any `json:"args"`
	Confirmed bool           `json:"confirmed"`
}

// handleRuntimeWebUIInvoke 对齐 POST /api/webui/plugins/runtime/webui/{plugin_id}/{page_id}/{kind}/{name}
func (d Deps) handleRuntimeWebUIInvoke(w http.ResponseWriter, r *http.Request) {
	pluginID := strings.TrimSpace(chi.URLParam(r, "plugin_id"))
	pageID := strings.TrimSpace(chi.URLParam(r, "page_id"))
	kind := strings.TrimSpace(chi.URLParam(r, "kind"))
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if kind != "queries" && kind != "actions" {
		httputil.WriteAPIError(w, http.StatusNotFound, "插件页面不存在")
		return
	}
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
	bindings, _ := page[kind].(map[string]any)
	rawBinding, ok := bindings[name].(map[string]any)
	if !ok {
		if kind == "queries" {
			httputil.WriteAPIError(w, http.StatusForbidden, "页面未开放该查询或操作")
		} else {
			httputil.WriteAPIError(w, http.StatusForbidden, "页面未开放该查询或操作")
		}
		return
	}
	rawBody, ok := httputil.ReadJSONBody(w, r)
	if !ok {
		return
	}
	body := webUIInvocationBody{}
	if len(rawBody) > 0 {
		if err := sonic.ConfigDefault.Unmarshal(rawBody, &body); err != nil {
			httputil.WriteValidationError(w, "请求格式错误")
			return
		}
	}
	if body.Args == nil {
		body.Args = map[string]any{}
	}
	if kind == "actions" && webUIBindingConfirmation(rawBinding) != "" && !body.Confirmed {
		httputil.WriteAPIError(w, http.StatusConflict, "该操作需要确认")
		return
	}
	if err := validateWebUIBindingArgs(rawBinding, body.Args); err != nil {
		httputil.WriteValidationError(w, err.Error())
		return
	}
	apiName := strings.TrimSpace(stringFromAny(rawBinding["api"]))
	apiVersion := strings.TrimSpace(stringFromAny(rawBinding["version"]))
	if apiVersion == "" {
		apiVersion = "1"
	}
	if d.PluginHost == nil {
		httputil.WriteAPIError(w, http.StatusNotFound, "页面绑定的静态 API 已下线")
		return
	}
	entry := webUIBindingAPI(d.PluginHost, pluginID, apiName, apiVersion)
	if entry == nil || entry.Dynamic {
		httputil.WriteAPIError(w, http.StatusNotFound, "页面绑定的静态 API 已下线")
		return
	}
	if !webUIInflightAcquire(pluginID) {
		httputil.WriteAPIError(w, http.StatusTooManyRequests, "该插件已有两个进行中的 WebUI 请求")
		return
	}
	defer webUIInflightRelease(pluginID)

	ctx, cancel := context.WithTimeout(r.Context(), webUIInvokeTimeout)
	defer cancel()
	response, err := d.PluginHost.InvokeAPI(ctx, pluginID, entry.HandlerName, body.Args, 10000)
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

// webUIBindingAPI 解析页面绑定声明的短名或全名，并限制在所属插件内
func webUIBindingAPI(host *pluginruntime.Host, pluginID, apiName, apiVersion string) *pluginruntime.APIEntry {
	if host == nil || strings.TrimSpace(apiName) == "" {
		return nil
	}
	registry := host.APIRegistry()
	if strings.Contains(apiName, ".") {
		entry := registry.GetAPIByFullName(apiName, apiVersion, true, "")
		if entry == nil || entry.PluginID != pluginID {
			return nil
		}
		return entry
	}
	return registry.GetAPI(pluginID, apiName, apiVersion, true)
}

func webUIPayloadSuccess(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	success, ok := payload["success"].(bool)
	return ok && success
}

func webUIEnvelopeTimedOut(response protocol.Envelope) bool {
	if len(response.Error) == 0 {
		return false
	}
	code := strings.TrimSpace(stringFromAny(response.Error["code"]))
	return code == string(protocol.ErrorCodeTimeout)
}

func webUIBindingConfirmation(binding map[string]any) string {
	confirmation, _ := binding["confirmation"].(string)
	return strings.TrimSpace(confirmation)
}

// validateWebUIBindingArgs 对齐 APIBinding.validate_args
func validateWebUIBindingArgs(binding map[string]any, args map[string]any) error {
	if err := pluginruntime.ValidateWebUIJSON(args, pluginruntime.MaxWebUIArgumentBytes, pluginruntime.MaxWebUIArgumentDepth); err != nil {
		return err
	}
	parameters, _ := binding["parameters"].(map[string]any)
	for name := range args {
		if _, declared := parameters[name]; !declared {
			return fmt.Errorf("未声明的参数: %s", name)
		}
	}
	for name, rawParameter := range parameters {
		parameter, ok := rawParameter.(map[string]any)
		if !ok {
			continue
		}
		value, present := args[name]
		if !present {
			if required, _ := parameter["required"].(bool); required {
				return fmt.Errorf("缺少参数: %s", name)
			}
			continue
		}
		if !webUIParameterAccepts(parameter, value) {
			return fmt.Errorf("参数不符合声明: %s", name)
		}
	}
	return nil
}

func webUIParameterAccepts(parameter map[string]any, value any) bool {
	parameterType := strings.TrimSpace(stringFromAny(parameter["type"]))
	if choices, ok := parameter["choices"].([]any); ok && len(choices) > 0 {
		matched := false
		for _, choice := range choices {
			if fmt.Sprint(choice) == fmt.Sprint(value) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	switch parameterType {
	case "string":
		text, ok := value.(string)
		if !ok {
			return false
		}
		maxLength := 4000
		if raw, exists := parameter["max_length"]; exists {
			maxLength = intValue(raw, 4000)
		}
		return len([]rune(text)) <= maxLength
	case "integer":
		number, ok := webUINumber(value)
		return ok && math.Trunc(number) == number && webUIWithinRange(parameter, number)
	case "number":
		number, ok := webUINumber(value)
		return ok && webUIWithinRange(parameter, number)
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "array":
		items, ok := value.([]any)
		if !ok {
			return false
		}
		maxItems := 1000
		if raw, exists := parameter["max_items"]; exists {
			maxItems = intValue(raw, 1000)
		}
		if len(items) > maxItems {
			return false
		}
		itemParameter, _ := parameter["items"].(map[string]any)
		if itemParameter == nil {
			return false
		}
		for _, item := range items {
			if !webUIParameterAccepts(itemParameter, item) {
				return false
			}
		}
		return true
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		properties, _ := parameter["properties"].(map[string]any)
		for name := range object {
			if _, declared := properties[name]; !declared {
				return false
			}
		}
		for name, rawProperty := range properties {
			property, ok := rawProperty.(map[string]any)
			if !ok {
				return false
			}
			child, present := object[name]
			if present {
				if !webUIParameterAccepts(property, child) {
					return false
				}
				continue
			}
			if required, _ := property["required"].(bool); required {
				return false
			}
		}
		return true
	}
	return false
}

func webUINumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case float32:
		number := float64(typed)
		return number, !math.IsNaN(number) && !math.IsInf(number, 0)
	}
	return 0, false
}

func webUIWithinRange(parameter map[string]any, value float64) bool {
	if raw, exists := parameter["minimum"]; exists && raw != nil {
		if minimum, ok := webUINumber(raw); ok && value < minimum {
			return false
		}
	}
	if raw, exists := parameter["maximum"]; exists && raw != nil {
		if maximum, ok := webUINumber(raw); ok && value > maximum {
			return false
		}
	}
	return true
}
