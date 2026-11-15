package embedding

// 嵌入请求传输层，默认实现对接 OpenAI 兼容与 Gemini REST 接口
// 对齐 MaiBot 宿主 embedding client 的行为：鉴权、默认头/查询参数、内部重试

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"MaiGo/internal/config/maibot"
	"MaiGo/internal/llm_models"

	"github.com/bytedance/sonic"
)

const (
	httpEmbeddingMaxAttempts = 3
	defaultOpenAIBaseURL     = "https://api.openai.com/v1"
	defaultGeminiBaseURL     = "https://generativelanguage.googleapis.com/v1beta"
	geminiDefaultTaskType    = "SEMANTIC_SIMILARITY"
)

// retriableEmbeddingStatusCodes 对齐宿主的可重试状态码集合
var retriableEmbeddingStatusCodes = map[int]bool{
	408: true,
	429: true,
	500: true,
	502: true,
	503: true,
	504: true,
}

// EmbeddingRequest 单条文本嵌入请求，对齐 Python EmbeddingRequest
type EmbeddingRequest struct {
	ModelInfo   maibot.ModelInfo
	Provider    maibot.APIProvider
	Input       string
	ExtraParams map[string]any
}

// EmbeddingTransport 嵌入请求传输层；默认走 HTTP，测试可注入假实现
type EmbeddingTransport interface {
	GetEmbedding(ctx context.Context, request EmbeddingRequest) ([]float32, error)
}

// HTTPEmbeddingTransport 基于 net/http 的传输层
type HTTPEmbeddingTransport struct {
	Client *http.Client
	// Sleep 便于测试注入等待实现；为 nil 时按 context 等待
	Sleep func(ctx context.Context, duration time.Duration) error
}

// NewHTTPEmbeddingTransport 创建传输层；client 为 nil 时使用带超时的默认客户端
func NewHTTPEmbeddingTransport(client *http.Client) *HTTPEmbeddingTransport {
	return &HTTPEmbeddingTransport{Client: client}
}

// GetEmbedding 发起单条文本嵌入请求
func (t *HTTPEmbeddingTransport) GetEmbedding(ctx context.Context, request EmbeddingRequest) ([]float32, error) {
	if isGeminiClientType(request.Provider.ClientType) {
		return t.execute(ctx, request, t.buildGeminiRequest, parseGeminiEmbeddingResponse)
	}
	return t.execute(ctx, request, t.buildOpenAIRequest, parseOpenAIEmbeddingResponse)
}

// execute 统一处理内部重试：可重试状态码最多尝试 3 次，等待 1s、2s
func (t *HTTPEmbeddingTransport) execute(
	ctx context.Context,
	request EmbeddingRequest,
	build func(context.Context, EmbeddingRequest) (*http.Request, error),
	parse func([]byte) ([]float32, error),
) ([]float32, error) {
	var lastError error
	for attempt := 0; attempt < httpEmbeddingMaxAttempts; attempt++ {
		httpRequest, err := build(ctx, request)
		if err != nil {
			return nil, err
		}
		response, err := t.httpClient().Do(httpRequest)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, &llm.NetworkError{
				Message: fmt.Sprintf("连接异常，请检查网络连接状态或URL是否正确: %v", err),
				Err:     err,
			}
		}
		payload, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			statusError := &llm.HTTPStatusError{
				StatusCode: response.StatusCode,
				Message:    strings.TrimSpace(string(payload)),
				Op:         "embeddings",
			}
			if retriableEmbeddingStatusCodes[response.StatusCode] && attempt < httpEmbeddingMaxAttempts-1 {
				lastError = statusError
				if err := t.sleep(ctx, time.Duration(1<<uint(attempt))*time.Second); err != nil {
					return nil, err
				}
				continue
			}
			return nil, statusError
		}
		return parse(payload)
	}
	if lastError != nil {
		return nil, lastError
	}
	return nil, errors.New("Embedding 请求失败：未知错误")
}

// buildOpenAIRequest 构造 OpenAI 兼容 /embeddings 请求
func (t *HTTPEmbeddingTransport) buildOpenAIRequest(ctx context.Context, request EmbeddingRequest) (*http.Request, error) {
	provider := request.Provider
	baseURL := strings.TrimSpace(provider.BaseUrl)
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}
	if !strings.Contains(baseURL, "://") {
		baseURL = "http://" + baseURL
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/embeddings"

	headersOverride, queryOverride, bodyOverride := splitRequestOverrides(request.ExtraParams)
	body := make(map[string]any, len(request.ExtraParams)+4)
	// extra_params 中除控制键外的键直接进入请求体，对齐宿主 extra_body
	for key, value := range request.ExtraParams {
		if key == "headers" || key == "query" || key == "body" {
			continue
		}
		body[key] = value
	}
	for key, value := range bodyOverride {
		body[key] = value
	}
	body["model"] = request.ModelInfo.ModelIdentifier
	body["input"] = request.Input
	if _, ok := body["encoding_format"]; !ok {
		body["encoding_format"] = "float"
	}
	payload, err := sonic.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	headers, query := buildProviderAuth(provider, false)
	for key, value := range headersOverride {
		headers[canonicalizeAuthorizationHeader(key)] = value
	}
	for key, value := range queryOverride {
		query[key] = value
	}
	applyHeadersAndQuery(httpRequest, headers, query)
	return httpRequest, nil
}

// buildGeminiRequest 构造 Gemini embedContent 请求
func (t *HTTPEmbeddingTransport) buildGeminiRequest(ctx context.Context, request EmbeddingRequest) (*http.Request, error) {
	provider := request.Provider
	baseURL := strings.TrimSpace(provider.BaseUrl)
	if baseURL == "" {
		baseURL = defaultGeminiBaseURL
	}
	if !strings.Contains(baseURL, "://") {
		baseURL = "http://" + baseURL
	}
	model := strings.TrimPrefix(strings.TrimSpace(request.ModelInfo.ModelIdentifier), "models/")
	endpoint := strings.TrimRight(baseURL, "/") + "/models/" + model + ":embedContent"

	headersOverride, queryOverride, bodyOverride := splitRequestOverrides(request.ExtraParams)
	body := make(map[string]any, len(bodyOverride)+3)
	for key, value := range bodyOverride {
		body[key] = value
	}
	body["content"] = map[string]any{
		"parts": []any{map[string]any{"text": request.Input}},
	}
	// 对齐 _build_embed_content_config 的白名单与默认 task_type
	config := map[string]any{"taskType": geminiDefaultTaskType}
	for key, value := range request.ExtraParams {
		if mapped, ok := geminiConfigKeys[key]; ok {
			config[mapped] = value
		}
	}
	for key, value := range config {
		body[key] = value
	}
	payload, err := sonic.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	headers, query := buildProviderAuth(provider, true)
	for key, value := range headersOverride {
		headers[canonicalizeAuthorizationHeader(key)] = value
	}
	for key, value := range queryOverride {
		query[key] = value
	}
	applyHeadersAndQuery(httpRequest, headers, query)
	return httpRequest, nil
}

// geminiConfigKeys 对齐 Gemini EmbedContentConfig 的蛇形到驼峰映射
var geminiConfigKeys = map[string]string{
	"task_type":             "taskType",
	"title":                 "title",
	"output_dimensionality": "outputDimensionality",
	"mime_type":             "mimeType",
	"auto_truncate":         "autoTruncate",
}

// splitRequestOverrides 拆分 extra_params 中的 headers/query/body 控制键
// 对齐宿主 split_openai_request_overrides
func splitRequestOverrides(extraParams map[string]any) (map[string]string, map[string]string, map[string]any) {
	headers := map[string]string{}
	query := map[string]string{}
	body := map[string]any{}
	if extraParams == nil {
		return headers, query, body
	}
	if value, ok := extraParams["headers"]; ok {
		headers = toStringMap(value)
	}
	if value, ok := extraParams["query"]; ok {
		query = toStringMap(value)
	}
	if value, ok := extraParams["body"]; ok {
		if mapping, ok := value.(map[string]any); ok {
			for key, item := range mapping {
				body[key] = item
			}
		}
	}
	return headers, query, body
}

// toStringMap 将任意映射转换为字符串映射，非字符串值按文本处理
func toStringMap(value any) map[string]string {
	result := map[string]string{}
	switch typed := value.(type) {
	case map[string]string:
		for key, item := range typed {
			result[key] = item
		}
	case map[string]any:
		for key, item := range typed {
			result[key] = fmt.Sprint(item)
		}
	}
	return result
}

// buildProviderAuth 构造鉴权头与查询参数，对齐 llm.auth 的鉴权分支
func buildProviderAuth(provider maibot.APIProvider, geminiDefault bool) (map[string]string, map[string]string) {
	headers := make(map[string]string, len(provider.DefaultHeaders)+1)
	for key, value := range provider.DefaultHeaders {
		headers[canonicalizeAuthorizationHeader(key)] = value
	}
	query := make(map[string]string, len(provider.DefaultQuery)+1)
	for key, value := range provider.DefaultQuery {
		query[key] = value
	}
	apiKey := provider.ApiKey
	authType := strings.ToLower(strings.TrimSpace(provider.AuthType))
	if authType == "" && geminiDefault {
		if apiKey != "" {
			headers["x-goog-api-key"] = apiKey
		}
		return headers, query
	}
	if authType == "" {
		authType = "bearer"
	}
	headerName := strings.TrimSpace(provider.AuthHeaderName)
	switch authType {
	case "bearer":
		if headerName == "" {
			headerName = "Authorization"
		}
		prefix := provider.AuthHeaderPrefix
		// 标准 Authorization 头由 SDK 默认加 Bearer；自定义头为空前缀时保持原始密钥，对齐宿主 auth
		if strings.TrimSpace(prefix) == "" && headerName == "Authorization" {
			prefix = "Bearer"
		}
		if apiKey != "" {
			headers[canonicalizeAuthorizationHeader(headerName)] = buildAuthHeaderValue(prefix, apiKey)
		}
	case "header":
		if headerName == "" {
			headerName = "Authorization"
		}
		if apiKey != "" {
			headers[canonicalizeAuthorizationHeader(headerName)] = buildAuthHeaderValue(provider.AuthHeaderPrefix, apiKey)
		}
	case "query":
		queryName := strings.TrimSpace(provider.AuthQueryName)
		if queryName == "" {
			if geminiDefault {
				queryName = "key"
			} else {
				queryName = "api_key"
			}
		}
		if apiKey != "" {
			query[queryName] = apiKey
		}
	case "none":
	}
	return headers, query
}

// buildAuthHeaderValue 对齐 _build_auth_header_value；前缀为空时直接使用原始密钥
func buildAuthHeaderValue(prefix, apiKey string) string {
	normalizedPrefix := strings.TrimSpace(prefix)
	if normalizedPrefix == "" {
		return apiKey
	}
	return normalizedPrefix + " " + apiKey
}

// canonicalizeAuthorizationHeader 统一 Authorization 大小写
func canonicalizeAuthorizationHeader(name string) string {
	if strings.EqualFold(name, "Authorization") {
		return "Authorization"
	}
	return name
}

// applyHeadersAndQuery 写入默认头与查询参数
func applyHeadersAndQuery(request *http.Request, headers, query map[string]string) {
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if len(query) > 0 {
		values := url.Values{}
		for key, value := range query {
			values.Set(key, value)
		}
		existing := request.URL.Query()
		for key, items := range existing {
			for _, item := range items {
				values.Add(key, item)
			}
		}
		request.URL.RawQuery = values.Encode()
	}
}

// parseOpenAIEmbeddingResponse 解析 OpenAI 兼容响应
func parseOpenAIEmbeddingResponse(payload []byte) ([]float32, error) {
	var response struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := sonic.Unmarshal(payload, &response); err != nil {
		return nil, &llm.ParseError{Message: "解析 embedding 响应失败", Err: err}
	}
	if len(response.Data) == 0 {
		return nil, errors.New("embedding 响应缺少 data")
	}
	return response.Data[0].Embedding, nil
}

// parseGeminiEmbeddingResponse 解析 Gemini embedContent 响应
func parseGeminiEmbeddingResponse(payload []byte) ([]float32, error) {
	var response struct {
		Embedding struct {
			Values []float32 `json:"values"`
		} `json:"embedding"`
	}
	if err := sonic.Unmarshal(payload, &response); err != nil {
		return nil, &llm.ParseError{Message: "解析 Gemini embedding 响应失败", Err: err}
	}
	if len(response.Embedding.Values) == 0 {
		return nil, errors.New("embedding 响应缺少 embedding.values")
	}
	return response.Embedding.Values, nil
}

// isGeminiClientType 判断 client_type 是否为 Gemini
func isGeminiClientType(clientType string) bool {
	switch strings.ToLower(strings.TrimSpace(clientType)) {
	case "gemini", "google":
		return true
	default:
		return false
	}
}

// httpClient 返回底层 HTTP 客户端
func (t *HTTPEmbeddingTransport) httpClient() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// sleep 等待内部重试间隔
func (t *HTTPEmbeddingTransport) sleep(ctx context.Context, duration time.Duration) error {
	if t.Sleep != nil {
		return t.Sleep(ctx, duration)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
