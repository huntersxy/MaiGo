package model_client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
)

// EmbeddingResult 单条向量结果，供 memorix / learners 使用
type EmbeddingResult struct {
	Embedding       []float64
	Model           string
	ModelIdentifier string
	APIProvider     string
	// RequestProtocol 图片嵌入协议指纹，对齐 MaiBot request_protocol_hash
	RequestProtocol string
	// MIMEType/PreprocessVersion 图片嵌入的服务层回填字段；文本嵌入为空
	MIMEType          string
	PreprocessVersion string
	// Usage 单次嵌入请求的用量；Gemini 按计费字符数折算，OpenAI 取响应 usage
	Usage *schema.TokenUsage
}

// ImageEmbeddingInput 图片嵌入请求
type ImageEmbeddingInput struct {
	Bytes             []byte
	MIMEType          string
	PreprocessVersion string
	// 以下字段对齐 Python EmbeddingServiceClient 的调用参数，只用于调用链与统计，不参与协议指纹
	TaskName    string
	RequestType string
	SessionID   string
}

// EmbeddingClient 文本/图片嵌入；按 ModelConfig.ClientType 分流
type EmbeddingClient interface {
	EmbedText(ctx context.Context, text string) (EmbeddingResult, error)
	EmbedTexts(ctx context.Context, texts []string) ([]EmbeddingResult, error)
	EmbedImage(ctx context.Context, input ImageEmbeddingInput) (EmbeddingResult, error)
}

// ConcurrentEmbeddingClient 可选能力：按 maxConcurrent 分批并发嵌入，结果按输入顺序返回
// 对齐 MaiBot EmbeddingService.embed_texts 的 max_concurrent > 1 分支；接口不强制实现，避免破坏既有调用方
type ConcurrentEmbeddingClient interface {
	EmbedTextsConcurrent(ctx context.Context, texts []string, maxConcurrent int) ([]EmbeddingResult, error)
}

// NewEmbeddingClient 创建嵌入客户端
func NewEmbeddingClient(cfg ModelConfig) (EmbeddingClient, error) {
	client, err := newEmbeddingClient(cfg)
	if err != nil {
		return nil, err
	}
	return newUsageRecordingEmbeddingClient(client, cfg), nil
}

func newEmbeddingClient(cfg ModelConfig) (EmbeddingClient, error) {
	if isGeminiClientType(cfg.ClientType) {
		return newGeminiEmbeddingClient(cfg)
	}
	if isOpenAICompatClientType(cfg.ClientType) {
		return newOpenAIEmbeddingClient(cfg)
	}
	if PluginProviderAvailable(cfg.ClientType) {
		return newPluginEmbeddingClient(cfg)
	}
	return nil, fmt.Errorf("client_type %q 不支持嵌入", cfg.ClientType)
}

type openAIEmbeddingClient struct {
	cfg    ModelConfig
	client openai.Client
}

func newOpenAIEmbeddingClient(cfg ModelConfig) (EmbeddingClient, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	baseURL := normalizeOpenAIBaseURL(cfg.BaseURL)
	modelName := strings.TrimSpace(cfg.Model)
	if baseURL == "" || modelName == "" {
		return nil, fmt.Errorf("嵌入模型配置不完整")
	}
	authType := strings.ToLower(strings.TrimSpace(cfg.AuthType))
	if authType == "" {
		authType = authTypeBearer
	}
	if apiKey == "" && authType != authTypeNone {
		return nil, fmt.Errorf("嵌入模型配置不完整")
	}
	auth := buildOpenAICompatAuth(cfg)
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	httpClient := newLLMHTTPClient(timeout)
	httpClient.Transport = newProviderTransport(httpClient.Transport, auth)
	sdkKey := auth.APIKey
	if sdkKey == "" {
		// SDK 仍会生成 Authorization 头，providerTransport 按 OmitAuthorization 移除
		sdkKey = "maibot-sdk-auth-disabled"
	}
	opts := []option.RequestOption{
		option.WithAPIKey(sdkKey),
		option.WithBaseURL(baseURL),
		option.WithHTTPClient(httpClient),
		option.WithMaxRetries(0),
	}
	if organization := strings.TrimSpace(cfg.Organization); organization != "" {
		opts = append(opts, option.WithOrganization(organization))
	}
	if project := strings.TrimSpace(cfg.Project); project != "" {
		opts = append(opts, option.WithProject(project))
	}
	return &openAIEmbeddingClient{
		cfg:    cfg,
		client: openai.NewClient(opts...),
	}, nil
}

func (c *openAIEmbeddingClient) EmbedText(ctx context.Context, text string) (EmbeddingResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return EmbeddingResult{}, fmt.Errorf("嵌入文本不能为空")
	}
	return c.embedTextOnce(ctx, text)
}

// EmbedTexts 对齐 Python embedding_service：默认串行逐条请求，每条单独计入限速
func (c *openAIEmbeddingClient) EmbedTexts(ctx context.Context, texts []string) ([]EmbeddingResult, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("嵌入输入列表不能为空")
	}
	out := make([]EmbeddingResult, 0, len(texts))
	for _, text := range texts {
		row, err := c.EmbedText(ctx, text)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

// EmbedTextsConcurrent 对齐 max_concurrent > 1 的可选并发调度，结果保持输入顺序
func (c *openAIEmbeddingClient) EmbedTextsConcurrent(ctx context.Context, texts []string, maxConcurrent int) ([]EmbeddingResult, error) {
	return EmbedTextsConcurrently(ctx, texts, maxConcurrent, c.EmbedText)
}

func (c *openAIEmbeddingClient) embedTextOnce(ctx context.Context, text string) (EmbeddingResult, error) {
	params := openai.EmbeddingNewParams{
		Model: openai.EmbeddingModel(c.cfg.Model),
		Input: openai.EmbeddingNewParamsInputUnion{OfString: param.NewOpt(text)},
	}
	overrides, err := splitOpenAIRequestOverrides(c.cfg.ExtraParams)
	if err != nil {
		return EmbeddingResult{}, err
	}
	if len(overrides.Body) > 0 {
		// Python 把 embeddings 的 extra_params 原样放进 extra_body，不过滤保留键
		params.SetExtraFields(overrides.Body)
	}
	requestOptions := make([]option.RequestOption, 0, len(overrides.Headers)+len(overrides.Query))
	for key, value := range overrides.Headers {
		requestOptions = append(requestOptions, option.WithHeader(key, value))
	}
	for key, value := range overrides.Query {
		requestOptions = append(requestOptions, option.WithQuery(key, pythonString(value)))
	}

	limiter := getEmbeddingRateLimiter(c.cfg)
	var lastErr error
	attempts := providerRetryAttempts(c.cfg)
	for attempt := 0; attempt < attempts; attempt++ {
		generation, _, waitErr := limiter.wait(ctx)
		if waitErr != nil {
			return EmbeddingResult{}, waitErr
		}
		resp, err := c.client.Embeddings.New(ctx, params, requestOptions...)
		if err == nil {
			rows, parseErr := embeddingResultsFromOpenAI(resp, c.cfg.Model, c.cfg.ProviderName)
			if parseErr != nil {
				return EmbeddingResult{}, c.snapshotFailure(text, parseErr)
			}
			return rows[0], nil
		}
		lastErr = err
		status := embeddingErrorStatus(err)
		if status == 429 {
			limiter.reduce(generation)
		}
		if attempt == attempts-1 || !embeddingRetryableStatus(status) {
			break
		}
		if waitErr := sleepEmbeddingContext(ctx, time.Duration(1<<attempt)*time.Second); waitErr != nil {
			return EmbeddingResult{}, waitErr
		}
	}
	return EmbeddingResult{}, c.snapshotFailure(text, fmt.Errorf("OpenAI 兼容嵌入请求失败: %w", lastErr))
}

// snapshotFailure 保存失败嵌入请求快照
func (c *openAIEmbeddingClient) snapshotFailure(text string, err error) error {
	if err == nil {
		return nil
	}
	providerRequest := map[string]any{"model": c.cfg.Model, "input": text}
	internal := map[string]any{
		"operation": "embedding",
		"base_url":  c.cfg.BaseURL,
	}
	spec := newRequestSnapshotSpec(c.cfg, "embedding", "embedding", nil, nil, providerRequest, internal)
	spec.ContextItems = []ContextItem{NewUserMessageItem(ContextContentPart{Type: ContextPartText, Text: text})}
	spec.Error = err
	return captureRequestSnapshot(spec)
}

func embeddingResultsFromOpenAI(resp *openai.CreateEmbeddingResponse, modelIdentifier, providerName string) ([]EmbeddingResult, error) {
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("嵌入响应解析失败，缺少 embeddings 数据。")
	}
	out := make([]EmbeddingResult, 0, len(resp.Data))
	for index, row := range resp.Data {
		vec := make([]float64, len(row.Embedding))
		copy(vec, row.Embedding)
		result := EmbeddingResult{
			Embedding:       vec,
			Model:           string(resp.Model),
			ModelIdentifier: modelIdentifier,
			APIProvider:     providerName,
		}
		if index == 0 && (resp.Usage.PromptTokens > 0 || resp.Usage.TotalTokens > 0) {
			result.Usage = &schema.TokenUsage{
				PromptTokens: int(resp.Usage.PromptTokens),
				TotalTokens:  int(resp.Usage.TotalTokens),
			}
		}
		out = append(out, result)
	}
	return out, nil
}

func embeddingErrorStatus(err error) int {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) && apiErr != nil {
		return apiErr.StatusCode
	}
	return 0
}

func IntFromAny(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	default:
		return 0, false
	}
}

type geminiEmbeddingClient struct {
	cfg        ModelConfig
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

func newGeminiEmbeddingClient(cfg ModelConfig) (EmbeddingClient, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	modelName := strings.TrimSpace(cfg.Model)
	if apiKey == "" || modelName == "" {
		return nil, fmt.Errorf("Gemini 嵌入模型配置不完整")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &geminiEmbeddingClient{
		cfg:        cfg,
		apiKey:     apiKey,
		baseURL:    resolveGeminiBaseURL(cfg.BaseURL),
		model:      strings.TrimPrefix(modelName, "models/"),
		httpClient: newLLMHTTPClient(timeout),
	}, nil
}

func (c *geminiEmbeddingClient) EmbedText(ctx context.Context, text string) (EmbeddingResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return EmbeddingResult{}, fmt.Errorf("嵌入文本不能为空")
	}
	body := map[string]any{
		"content": map[string]any{
			"parts": []map[string]any{{"text": text}},
		},
	}
	applyGeminiEmbedConfig(body, c.cfg.ExtraParams)
	vec, usage, err := c.postEmbed(ctx, body, text)
	if err != nil {
		return EmbeddingResult{}, err
	}
	return EmbeddingResult{
		Embedding:       vec,
		Model:           c.model,
		ModelIdentifier: c.cfg.Model,
		APIProvider:     c.cfg.ProviderName,
		Usage:           usage,
	}, nil
}

func (c *geminiEmbeddingClient) EmbedTexts(ctx context.Context, texts []string) ([]EmbeddingResult, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("嵌入输入列表不能为空")
	}
	out := make([]EmbeddingResult, 0, len(texts))
	for _, text := range texts {
		row, err := c.EmbedText(ctx, text)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

func (c *geminiEmbeddingClient) EmbedTextsConcurrent(ctx context.Context, texts []string, maxConcurrent int) ([]EmbeddingResult, error) {
	return EmbedTextsConcurrently(ctx, texts, maxConcurrent, c.EmbedText)
}

// applyGeminiEmbedConfig 对齐 MaiBot _build_embed_content_config
func applyGeminiEmbedConfig(body map[string]any, extraParams map[string]any) {
	body["taskType"] = "SEMANTIC_SIMILARITY"
	if value, ok := extraParams["task_type"]; ok && value != nil {
		body["taskType"] = value
	}
	for key, restKey := range map[string]string{
		"title":                 "title",
		"output_dimensionality": "outputDimensionality",
		"mime_type":             "mimeType",
		"auto_truncate":         "autoTruncate",
	} {
		if value, ok := extraParams[key]; ok && value != nil {
			body[restKey] = value
		}
	}
}

// EmbedTextsConcurrently 按 maxConcurrent 并发逐条嵌入，结果按输入顺序返回
// 与 Python EmbeddingService 一致：默认 1（串行），大于 1 才启用并发；任一失败取消其余请求
func EmbedTextsConcurrently(
	ctx context.Context,
	texts []string,
	maxConcurrent int,
	embed func(context.Context, string) (EmbeddingResult, error),
) ([]EmbeddingResult, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("嵌入输入列表不能为空")
	}
	if maxConcurrent <= 1 {
		out := make([]EmbeddingResult, 0, len(texts))
		for _, text := range texts {
			row, err := embed(ctx, text)
			if err != nil {
				return nil, err
			}
			out = append(out, row)
		}
		return out, nil
	}
	// 并发上限不超过输入条数，避免超大配置分配无意义的信号量
	if maxConcurrent > len(texts) {
		maxConcurrent = len(texts)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, maxConcurrent)
	results := make([]EmbeddingResult, len(texts))
	var wait sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	// 取消会让其余请求返回 context.Canceled；优先保留真正的请求错误
	recordErr := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		defer errMu.Unlock()
		if firstErr == nil || (isContextError(firstErr) && !isContextError(err)) {
			firstErr = err
		}
	}
	for index, text := range texts {
		wait.Add(1)
		go func(index int, text string) {
			defer wait.Done()
			select {
			case sem <- struct{}{}:
			case <-runCtx.Done():
				recordErr(runCtx.Err())
				return
			}
			defer func() { <-sem }()
			row, err := embed(runCtx, text)
			if err != nil {
				recordErr(err)
				cancel()
				return
			}
			results[index] = row
		}(index, text)
	}
	wait.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

// isContextError 判断是否为上下文取消/超时错误
func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// EmbedImage Gemini 客户端在 MaiBot 中未实现图片嵌入协议，这里保持同样的失败语义
func (c *geminiEmbeddingClient) EmbedImage(ctx context.Context, input ImageEmbeddingInput) (EmbeddingResult, error) {
	_ = ctx
	_ = input
	return EmbeddingResult{}, fmt.Errorf("Provider 客户端 GeminiClient 未实现图片嵌入协议")
}

func (c *geminiEmbeddingClient) postEmbed(ctx context.Context, body map[string]any, text string) ([]float64, *schema.TokenUsage, error) {
	limiter := getEmbeddingRateLimiter(c.cfg)
	var lastErr error
	attempts := providerRetryAttempts(c.cfg)
	for attempt := 0; attempt < attempts; attempt++ {
		generation, _, waitErr := limiter.wait(ctx)
		if waitErr != nil {
			return nil, nil, waitErr
		}
		vec, usage, status, err := c.postEmbedOnce(ctx, body, text)
		if err == nil {
			return vec, usage, nil
		}
		lastErr = err
		if status == 429 {
			limiter.reduce(generation)
		}
		if attempt == attempts-1 || !embeddingRetryableStatus(status) {
			break
		}
		if waitErr := sleepEmbeddingContext(ctx, time.Duration(1<<attempt)*time.Second); waitErr != nil {
			return nil, nil, waitErr
		}
	}
	return nil, nil, c.snapshotFailure(body, text, lastErr)
}

// snapshotFailure 保存失败 Gemini 嵌入请求快照
func (c *geminiEmbeddingClient) snapshotFailure(body map[string]any, text string, err error) error {
	if err == nil {
		return nil
	}
	internal := map[string]any{
		"operation": "embedding",
		"base_url":  c.baseURL,
		"model":     c.model,
	}
	spec := newRequestSnapshotSpec(c.cfg, "gemini.embed_content", "embedding", nil, nil, body, internal)
	spec.ContextItems = []ContextItem{NewUserMessageItem(ContextContentPart{Type: ContextPartText, Text: text})}
	spec.Error = err
	return captureRequestSnapshot(spec)
}

func (c *geminiEmbeddingClient) postEmbedOnce(ctx context.Context, body map[string]any, text string) ([]float64, *schema.TokenUsage, int, error) {
	payload, err := sonic.Marshal(body)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("序列化 Gemini 嵌入请求失败: %w", err)
	}
	base := strings.TrimRight(c.baseURL, "/")
	path := fmt.Sprintf("%s/models/%s:embedContent", base, url.PathEscape(c.model))
	u, err := url.Parse(path)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("Gemini 嵌入 URL 无效: %w", err)
	}
	q := u.Query()
	q.Set("key", c.apiKey)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(payload)))
	if err != nil {
		return nil, nil, 0, fmt.Errorf("创建 Gemini 嵌入请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("Gemini 嵌入请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("读取 Gemini 嵌入响应失败: %w", err)
	}
	status := resp.StatusCode
	if status < 200 || status >= 300 {
		return nil, nil, status, fmt.Errorf("Gemini 嵌入 HTTP %d: %s", status, truncateErrBody(raw))
	}
	var parsed struct {
		Embedding struct {
			Values []float64 `json:"values"`
		} `json:"embedding"`
		Metadata struct {
			BillableCharacterCount int `json:"billableCharacterCount"`
		} `json:"metadata"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := sonic.Unmarshal(raw, &parsed); err != nil {
		return nil, nil, status, fmt.Errorf("解析 Gemini 嵌入响应失败: %w", err)
	}
	if parsed.Error.Message != "" {
		return nil, nil, status, fmt.Errorf("Gemini 嵌入 API 错误: %s", parsed.Error.Message)
	}
	if len(parsed.Embedding.Values) == 0 {
		return nil, nil, status, fmt.Errorf("Gemini 嵌入响应解析失败，缺少 embeddings 字段。")
	}
	vec := make([]float64, len(parsed.Embedding.Values))
	copy(vec, parsed.Embedding.Values)
	characters := parsed.Metadata.BillableCharacterCount
	if characters <= 0 {
		characters = len(text)
	}
	usage := &schema.TokenUsage{PromptTokens: characters, TotalTokens: characters}
	return vec, usage, status, nil
}
