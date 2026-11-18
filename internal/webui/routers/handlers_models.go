package webuiapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"MaiGo/internal/common/network_security"
	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/llm_models/model_client"
)

// noRedirect 对齐 Python httpx 默认 follow_redirects=False：3xx 原样返回给调用方
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

type modelClientType struct {
	ClientType    string  `json:"client_type"`
	OwnerPluginID *string `json:"owner_plugin_id"`
	Version       string  `json:"version"`
	Description   string  `json:"description"`
	Builtin       bool    `json:"builtin"`
}

// handleModelsClientTypes 对应 GET /api/webui/models/client-types
func (d Deps) handleModelsClientTypes(w http.ResponseWriter, r *http.Request) {
	clientTypes := []modelClientType{
		{ClientType: "gemini", Version: "", Description: "Gemini 原生 GenerateContent", Builtin: true},
		{ClientType: "openai", Version: "", Description: "OpenAI 兼容 Chat Completions", Builtin: true},
		{ClientType: "openai_responses", Version: "", Description: "OpenAI Responses API", Builtin: true},
	}
	for _, provider := range model_client.ListRegisteredPluginProviders() {
		owner := provider.PluginID
		var ownerPtr *string
		if owner != "" {
			ownerCopy := owner
			ownerPtr = &ownerCopy
		}
		clientTypes = append(clientTypes, modelClientType{
			ClientType: provider.ClientType, OwnerPluginID: ownerPtr, Builtin: false,
		})
	}
	// 对齐 Python：内置优先，再按 client_type 字典序
	sort.Slice(clientTypes, func(i, j int) bool {
		if clientTypes[i].Builtin != clientTypes[j].Builtin {
			return clientTypes[i].Builtin
		}
		return clientTypes[i].ClientType < clientTypes[j].ClientType
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "client_types": clientTypes, "count": len(clientTypes),
	})
}

// modelConfigFileMerged 读取实例模型配置并叠加宿主共享目录，返回完整结构
func (d Deps) modelConfigFileMerged() (maibotcfg.ModelConfigFile, bool) {
	var file maibotcfg.ModelConfigFile
	path, ok := d.modelConfigPath()
	if !ok {
		return file, false
	}
	doc, err := readTOMLDocument(path)
	if err != nil {
		return file, false
	}
	if merged, mergeErr := maibotcfg.MergeSharedCatalogDoc(doc); mergeErr == nil {
		doc = merged
	}
	encoded, err := toml.Marshal(doc)
	if err != nil {
		return file, false
	}
	if err := toml.Unmarshal(encoded, &file); err != nil {
		return file, false
	}
	return file, true
}

// modelCatalogDoc 读取供应商与模型定义；宿主模式下必须叠加共享模型目录
func (d Deps) modelCatalogDoc() (map[string]any, bool) {
	path, ok := d.modelConfigPath()
	if !ok {
		return nil, false
	}
	doc, err := readTOMLDocument(path)
	if err != nil {
		return nil, false
	}
	merged, err := maibotcfg.MergeSharedCatalogDoc(doc)
	if err != nil {
		// 共享目录损坏时保留实例配置，避免整页不可用
		return doc, true
	}
	return merged, true
}

func (d Deps) modelProviderByName(name string) (map[string]any, bool) {
	doc, ok := d.modelCatalogDoc()
	if !ok {
		return nil, false
	}
	providers, _ := doc["api_providers"].([]any)
	for _, raw := range providers {
		provider, ok := raw.(map[string]any)
		if ok && provider["name"] == name {
			return provider, true
		}
	}
	return nil, false
}

func (d Deps) modelConfigModelByName(name string) (map[string]any, bool) {
	doc, ok := d.modelCatalogDoc()
	if !ok {
		return nil, false
	}
	models, _ := doc["models"].([]any)
	for _, raw := range models {
		model, ok := raw.(map[string]any)
		if ok && model["name"] == name {
			return model, true
		}
	}
	return nil, false
}

type modelListOptions struct {
	BaseURL          string
	APIKey           string
	Endpoint         string
	Parser           string
	ClientType       string
	AuthType         string
	AuthHeaderName   string
	AuthHeaderPrefix string
	AuthQueryName    string
	DefaultHeaders   map[string]string
	DefaultQuery     map[string]string
	AllowPrivate     bool
}

func normalizeBaseURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func (d Deps) validateFetchURL(raw string, allowPrivate bool) (string, error) {
	normalized := normalizeBaseURL(raw)
	validated, err := networksecurity.ValidatePublicURL(normalized, d.networkSecurityOptions(allowPrivate, false))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(validated, "/"), nil
}

func stringMapFromAny(raw any) map[string]string {
	out := map[string]string{}
	values, ok := raw.(map[string]any)
	if !ok {
		return out
	}
	for key, value := range values {
		if text, ok := value.(string); ok {
			out[key] = text
		}
	}
	return out
}

func (d Deps) fetchProviderModels(ctx context.Context, options modelListOptions) ([]map[string]any, error) {
	baseURL, err := d.validateFetchURL(options.BaseURL, options.AllowPrivate)
	if err != nil {
		return nil, &modelFetchError{StatusCode: http.StatusBadRequest, Message: err.Error()}
	}
	if options.Endpoint == "" {
		options.Endpoint = "/models"
	}
	if options.Parser == "" {
		options.Parser = "openai"
	}
	target := baseURL + options.Endpoint
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, &modelFetchError{StatusCode: http.StatusBadRequest, Message: err.Error()}
	}
	headers := map[string]string{"Content-Type": "application/json"}
	query := request.URL.Query()
	for key, value := range options.DefaultHeaders {
		headers[key] = value
	}
	for key, value := range options.DefaultQuery {
		query.Set(key, value)
	}
	if options.ClientType == "gemini" {
		query.Set("key", options.APIKey)
	} else {
		authType := strings.ToLower(strings.TrimSpace(options.AuthType))
		if authType == "" {
			authType = "bearer"
		}
		switch authType {
		case "none":
		case "query":
			name := options.AuthQueryName
			if name == "" {
				name = "api_key"
			}
			query.Set(name, options.APIKey)
		case "header":
			name := options.AuthHeaderName
			if name == "" {
				name = "Authorization"
			}
			prefix := options.AuthHeaderPrefix
			if prefix == "" {
				prefix = "Bearer"
			}
			headers[name] = strings.TrimSpace(prefix + " " + options.APIKey)
		default:
			headers["Authorization"] = "Bearer " + options.APIKey
		}
	}
	request.URL.RawQuery = query.Encode()
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	// 对齐 Python httpx.AsyncClient 默认 follow_redirects=False：3xx 原样返回，不跟随到可能的内网目标
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: noRedirect}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &modelFetchError{StatusCode: http.StatusGatewayTimeout, Message: "请求超时，请稍后重试"}
		}
		return nil, &modelFetchError{StatusCode: http.StatusBadGateway, Message: "连接失败: " + err.Error()}
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, &modelFetchError{StatusCode: http.StatusBadGateway, Message: "API Key 无效或已过期"}
	case http.StatusForbidden:
		return nil, &modelFetchError{StatusCode: http.StatusBadGateway, Message: "没有权限访问模型列表，请检查 API Key 权限"}
	case http.StatusNotFound:
		return nil, &modelFetchError{StatusCode: http.StatusBadGateway, Message: "该提供商不支持获取模型列表"}
	default:
		return nil, &modelFetchError{StatusCode: http.StatusBadGateway, Message: "上游服务请求失败 (" + itoa(response.StatusCode) + "): " + truncateModelText(string(body), 200)}
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, &modelFetchError{StatusCode: http.StatusInternalServerError, Message: "获取模型列表失败: " + err.Error()}
	}
	switch options.Parser {
	case "openai":
		return parseOpenAIModelList(payload), nil
	case "gemini":
		return parseGeminiModelList(payload), nil
	default:
		return nil, &modelFetchError{StatusCode: http.StatusBadRequest, Message: "不支持的解析器类型: " + options.Parser}
	}
}

type modelFetchError struct {
	StatusCode int
	Message    string
}

func (e *modelFetchError) Error() string { return e.Message }

func truncateModelText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func writeModelFetchError(w http.ResponseWriter, err error) {
	var fetchErr *modelFetchError
	if errors.As(err, &fetchErr) {
		writeJSON(w, fetchErr.StatusCode, map[string]any{"detail": fetchErr.Message})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
}

func parseOpenAIModelList(payload map[string]any) []map[string]any {
	items, _ := payload["data"].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, ok := item["id"].(string)
		if !ok {
			continue
		}
		name, _ := item["name"].(string)
		if name == "" {
			name = id
		}
		ownedBy, _ := item["owned_by"].(string)
		out = append(out, map[string]any{"id": id, "name": name, "owned_by": ownedBy})
	}
	return out
}

func parseGeminiModelList(payload map[string]any) []map[string]any {
	items, _ := payload["models"].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, ok := item["name"].(string)
		if !ok {
			continue
		}
		id := strings.TrimPrefix(name, "models/")
		displayName, _ := item["displayName"].(string)
		if displayName == "" {
			displayName = id
		}
		out = append(out, map[string]any{"id": id, "name": displayName, "owned_by": "google"})
	}
	return out
}

// handleModelsList 对应 GET /api/webui/models/list
func (d Deps) handleModelsList(w http.ResponseWriter, r *http.Request) {
	providerName := strings.TrimSpace(r.URL.Query().Get("provider_name"))
	provider, ok := d.modelProviderByName(providerName)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到提供商: " + providerName})
		return
	}
	baseURL, _ := provider["base_url"].(string)
	apiKey, _ := provider["api_key"].(string)
	if strings.TrimSpace(baseURL) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "提供商配置缺少 base_url"})
		return
	}
	endpoint := r.URL.Query().Get("endpoint")
	if endpoint == "" {
		endpoint = "/models"
		if configured, ok := provider["model_list_endpoint"].(string); ok && configured != "" {
			endpoint = configured
		}
	}
	parser := r.URL.Query().Get("parser")
	if parser == "" {
		parser = "openai"
	}
	clientType, _ := provider["client_type"].(string)
	authType, _ := provider["auth_type"].(string)
	authHeaderName, _ := provider["auth_header_name"].(string)
	authHeaderPrefix, _ := provider["auth_header_prefix"].(string)
	authQueryName, _ := provider["auth_query_name"].(string)
	models, err := d.fetchProviderModels(r.Context(), modelListOptions{
		BaseURL: baseURL, APIKey: apiKey, Endpoint: endpoint, Parser: parser, ClientType: clientType,
		AuthType: authType, AuthHeaderName: authHeaderName, AuthHeaderPrefix: authHeaderPrefix,
		AuthQueryName: authQueryName, DefaultHeaders: stringMapFromAny(provider["default_headers"]),
		DefaultQuery: stringMapFromAny(provider["default_query"]), AllowPrivate: true,
	})
	if err != nil {
		writeModelFetchError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "models": models, "provider": providerName, "count": len(models)})
}

// handleModelsListByURL 对应 GET /api/webui/models/list-by-url
func (d Deps) handleModelsListByURL(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	baseURL := query.Get("base_url")
	if strings.TrimSpace(baseURL) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "base_url 不能为空"})
		return
	}
	endpoint := query.Get("endpoint")
	if endpoint == "" {
		endpoint = "/models"
	}
	parser := query.Get("parser")
	if parser == "" {
		parser = "openai"
	}
	clientType := query.Get("client_type")
	if clientType == "" {
		clientType = "openai"
	}
	authType := query.Get("auth_type")
	if authType == "" {
		authType = "bearer"
	}
	models, err := d.fetchProviderModels(r.Context(), modelListOptions{
		BaseURL: baseURL, APIKey: query.Get("api_key"), Endpoint: endpoint, Parser: parser, ClientType: clientType,
		AuthType: authType, AuthHeaderName: defaultString(query.Get("auth_header_name"), "Authorization"),
		AuthHeaderPrefix: defaultString(query.Get("auth_header_prefix"), "Bearer"),
		AuthQueryName:    defaultString(query.Get("auth_query_name"), "api_key"), AllowPrivate: true,
	})
	if err != nil {
		writeModelFetchError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "models": models, "count": len(models)})
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

type providerConnectionResult struct {
	NetworkOK   bool     `json:"network_ok"`
	APIKeyValid *bool    `json:"api_key_valid"`
	LatencyMS   *float64 `json:"latency_ms"`
	Error       *string  `json:"error"`
	HTTPStatus  *int     `json:"http_status"`
}

func (d Deps) testProviderConnection(ctx context.Context, baseURL, apiKey, clientType string, allowPrivate bool) providerConnectionResult {
	result := providerConnectionResult{}
	normalized, err := d.validateFetchURL(baseURL, allowPrivate)
	if err != nil {
		message := err.Error()
		result.Error = &message
		return result
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: noRedirect}
	start := time.Now()
	response, err := client.Get(normalized)
	if err != nil {
		message := "连接失败：无法连接到服务器 (" + err.Error() + ")"
		if errors.Is(err, context.DeadlineExceeded) {
			message = "连接超时：服务器响应时间过长"
		}
		result.Error = &message
		return result
	}
	response.Body.Close()
	latency := float64(time.Since(start).Microseconds()) / 1000
	result.NetworkOK = true
	result.LatencyMS = &latency
	status := response.StatusCode
	result.HTTPStatus = &status
	if strings.TrimSpace(apiKey) == "" {
		return result
	}
	verifyURL := normalized + "/models"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, verifyURL, nil)
	if err != nil {
		return result
	}
	if clientType == "gemini" {
		query := request.URL.Query()
		query.Set("key", apiKey)
		request.URL.RawQuery = query.Encode()
	} else {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	verifyClient := &http.Client{Timeout: 15 * time.Second, CheckRedirect: noRedirect}
	verifyResponse, err := verifyClient.Do(request)
	if err != nil {
		return result
	}
	defer verifyResponse.Body.Close()
	valid := verifyResponse.StatusCode == http.StatusOK
	switch {
	case valid:
		result.APIKeyValid = &valid
	case verifyResponse.StatusCode == http.StatusUnauthorized || verifyResponse.StatusCode == http.StatusForbidden:
		invalid := false
		result.APIKeyValid = &invalid
		message := "API Key 无效或已过期"
		result.Error = &message
	}
	return result
}

// handleModelsTestConnection 对应 GET /api/webui/models/test-connection
func (d Deps) handleModelsTestConnection(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	baseURL := strings.TrimSpace(query.Get("base_url"))
	if baseURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "base_url 不能为空"})
		return
	}
	result := d.testProviderConnection(r.Context(), baseURL, query.Get("api_key"), defaultString(query.Get("client_type"), "openai"), true)
	writeJSON(w, http.StatusOK, result)
}

// handleModelsTestConnectionByName 对应 POST /api/webui/models/test-connection-by-name
func (d Deps) handleModelsTestConnectionByName(w http.ResponseWriter, r *http.Request) {
	providerName := strings.TrimSpace(r.URL.Query().Get("provider_name"))
	path, ok := d.modelConfigPath()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	if !pathExists(path) {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "配置文件不存在"})
		return
	}
	provider, ok := d.modelProviderByName(providerName)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到提供商: " + providerName})
		return
	}
	baseURL, _ := provider["base_url"].(string)
	if strings.TrimSpace(baseURL) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "提供商配置缺少 base_url"})
		return
	}
	apiKey, _ := provider["api_key"].(string)
	clientType, _ := provider["client_type"].(string)
	result := d.testProviderConnection(r.Context(), baseURL, apiKey, defaultString(clientType, "openai"), true)
	writeJSON(w, http.StatusOK, result)
}

type modelTestRequest struct {
	ModelName string `json:"model_name"`
}
