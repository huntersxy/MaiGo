package model_client

// Gemini 客户端：通过 Generative Language REST API 做 generateContent /
// streamGenerateContent。
//
// 对等 src/llm_models/model_client/gemini_client.py：contents 转换、
// thought_signature 往返（缺失时用官方占位签名）、生成配置（responseModalities、
// safetySettings、thinkingConfig、functionDeclarations + googleSearch、toolConfig）、
// SSE 流式与 usage 累计。语音转写见 gemini_audio.go。
//
// 已知边界（自定义 base_url 仅支持 …/v1beta 形式、URL 媒体显式报错）见
// agents/MODULE_ALIGNMENT.md。

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

const (
	defaultGeminiAPIVersion = "v1beta"
	defaultGeminiAPIRoot    = "https://generativelanguage.googleapis.com"
	// geminiSSEPrefix streamGenerateContent?alt=sse 的每个事件前缀
	geminiSSEPrefix = "data:"
)

// geminiClient 实现 model.AgenticModel，走官方 REST
type geminiClient struct {
	apiKey     string
	baseURL    string
	model      string
	timeout    time.Duration
	extra      map[string]any
	httpClient *http.Client
	// cfg 只用于失败快照与日志元数据
	cfg ModelConfig
}

// NewGeminiClient 创建 Gemini AgenticModel
func NewGeminiClient(cfg ModelConfig) (model.AgenticModel, error) {
	return newGeminiClient(cfg)
}

func newGeminiClient(cfg ModelConfig) (*geminiClient, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	modelName := strings.TrimSpace(cfg.Model)
	if apiKey == "" || modelName == "" {
		return nil, fmt.Errorf("Gemini 模型配置不完整（需要 api_key 与 model）")
	}
	return &geminiClient{
		apiKey:     apiKey,
		baseURL:    resolveGeminiBaseURL(cfg.BaseURL),
		model:      strings.TrimPrefix(modelName, "models/"),
		timeout:    cfg.Timeout,
		extra:      cloneAnyMap(cfg.ExtraParams),
		httpClient: newLLMHTTPClient(0),
		cfg:        cfg,
	}, nil
}

func resolveGeminiBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultGeminiAPIRoot + "/" + defaultGeminiAPIVersion
	}
	raw = strings.TrimRight(raw, "/")
	parts := strings.Split(raw, "/")
	if len(parts) >= 2 {
		last := parts[len(parts)-1]
		if strings.HasPrefix(last, "v") {
			return raw
		}
	}
	return raw + "/" + defaultGeminiAPIVersion
}

// Generate 非流式 generateContent
func (c *geminiClient) Generate(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	started := time.Now()
	recorder := snapshotAttemptRecorderFromContext(ctx)
	options := model.GetCommonOptions(&model.Options{}, opts...)
	resolveEffectiveParams(c.cfg, options)
	reqCtx, cancel := c.requestContext(ctx)
	defer cancel()

	modelName, enableGoogleSearch := c.resolveModel(options)
	items, err := ContextItemsFromMessages(input)
	if err != nil {
		return nil, c.snapshotFailure(recorder, started, input, options, nil, false, err)
	}
	payload, err := c.buildRequestBodyFromContextItems(reqCtx, items, options, enableGoogleSearch)
	if err != nil {
		return nil, c.snapshotFailure(recorder, started, input, options, nil, false, err)
	}
	raw, err := c.post(ctx, reqCtx, modelName, payload, false)
	if err != nil {
		return nil, c.snapshotFailure(recorder, started, input, options, payload, false, err)
	}
	parsed, err := parseGeminiResponse(raw)
	if err != nil {
		return nil, c.snapshotFailure(recorder, started, input, options, payload, false, err)
	}
	message, err := parsed.toAgenticMessage()
	if err != nil {
		return nil, c.snapshotFailure(recorder, started, input, options, payload, false, err)
	}
	cacheCfg := c.cfg
	cacheCfg.Model = modelName
	recordGenerationCacheUsage(ctx, cacheCfg, items, options, effectiveResponseFormat(ctx, c.cfg.ResponseFormat), message)
	return message, nil
}

// Stream 流式 streamGenerateContent（SSE）
// 建流与读流中途错误都按错误分类交给调度层重试/重放/切模型；
// 重放由 failover / usage 包装层先缓冲整条流实现（见 stream_replay.go）
func (c *geminiClient) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	started := time.Now()
	recorder := snapshotAttemptRecorderFromContext(ctx)
	options := model.GetCommonOptions(&model.Options{}, opts...)
	resolveEffectiveParams(c.cfg, options)
	modelName, enableGoogleSearch := c.resolveModel(options)
	// 超时上下文必须覆盖建流与读取整条 SSE；先建上下文再发请求，避免只约束读取
	streamCtx, cancel := c.requestContext(ctx)
	items, err := ContextItemsFromMessages(input)
	if err != nil {
		cancel()
		return nil, c.snapshotFailure(recorder, started, input, options, nil, true, err)
	}
	payload, err := c.buildRequestBodyFromContextItems(streamCtx, items, options, enableGoogleSearch)
	if err != nil {
		cancel()
		return nil, c.snapshotFailure(recorder, started, input, options, nil, true, err)
	}
	resp, err := c.openStream(ctx, streamCtx, modelName, payload)
	if err != nil {
		cancel()
		return nil, c.snapshotFailure(recorder, started, input, options, payload, true, err)
	}

	reader, writer := schema.Pipe[*schema.AgenticMessage](8)
	go func() {
		defer cancel()
		defer resp.Body.Close()
		pumpGeminiStream(ctx, streamCtx, resp.Body, writer)
	}()
	stream := wrapAgenticStreamSnapshot(reader, func(streamErr error) error {
		return c.snapshotFailure(recorder, started, input, options, payload, true, streamErr)
	})
	cacheCfg := c.cfg
	cacheCfg.Model = modelName
	return wrapAgenticStreamCacheStats(stream, ctx, cacheCfg, items, options, effectiveResponseFormat(ctx, c.cfg.ResponseFormat)), nil
}

// snapshotFailure 保存失败请求快照并把路径挂到错误链
func (c *geminiClient) snapshotFailure(recorder *snapshotAttemptRecorder, started time.Time, input []*schema.AgenticMessage, options *model.Options, payload map[string]any, stream bool, err error) error {
	if err == nil {
		return nil
	}
	internal := map[string]any{
		"model":     c.model,
		"base_url":  c.baseURL,
		"stream":    stream,
		"operation": "generate_content",
	}
	spec := newRequestSnapshotSpec(c.cfg, "gemini.generate_content", "response", input, options, payload, internal)
	spec.LogicalTurnID = snapshotLogicalTurnID(recorder)
	spec.Error = err
	spec.StartedAt = started
	recordSnapshotAttempt(recorder, &spec, started)
	return captureRequestSnapshot(spec)
}

// requestContext 单次 HTTP 请求的超时（对齐 MaiBot HttpOptions.timeout）
func (c *geminiClient) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.timeout)
}

// resolveModel 解析实际模型标识与 Google Search 开关
// 对齐 MaiBot _resolve_model_identifier：-search 后缀隐含开启 Google Search
func (c *geminiClient) resolveModel(options *model.Options) (string, bool) {
	modelName := c.model
	if options.Model != nil && strings.TrimSpace(*options.Model) != "" {
		modelName = strings.TrimPrefix(strings.TrimSpace(*options.Model), "models/")
	}
	enableGoogleSearch := boolFromAny(c.extra["enable_google_search"])
	if strings.HasSuffix(modelName, "-search") {
		modelName = strings.TrimSuffix(modelName, "-search")
		enableGoogleSearch = true
	}
	return modelName, enableGoogleSearch
}

func (c *geminiClient) post(parentCtx, reqCtx context.Context, modelName string, payload map[string]any, stream bool) ([]byte, error) {
	body, err := sonic.Marshal(payload)
	if err != nil {
		return nil, &ParseError{Message: "序列化 Gemini 请求失败", Err: err}
	}
	reqURL, err := c.endpointURL(modelName, stream)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, reqURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, &NetworkError{Message: "创建 Gemini 请求失败", Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.transportError(parentCtx, reqCtx, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, c.transportError(parentCtx, reqCtx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, geminiHTTPError(resp.StatusCode, raw)
	}
	return raw, nil
}

// openStream 建立 SSE 连接；HTTP 非 2xx 与连接错误在建流阶段上抛
func (c *geminiClient) openStream(parentCtx, reqCtx context.Context, modelName string, payload map[string]any) (*http.Response, error) {
	body, err := sonic.Marshal(payload)
	if err != nil {
		return nil, &ParseError{Message: "序列化 Gemini 流式请求失败", Err: err}
	}
	reqURL, err := c.endpointURL(modelName, true)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, reqURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, &NetworkError{Message: "创建 Gemini 流式请求失败", Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.transportError(parentCtx, reqCtx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, geminiHTTPError(resp.StatusCode, raw)
	}
	return resp, nil
}

// transportError 把底层连接错误转换成 MaiBot 的异常分类
// 调用方取消/超时按中断上抛；Provider 自身请求超时对齐 Python APITimeoutError，
// 归类为可重试的 NetworkError 而不是中断
func (c *geminiClient) transportError(parentCtx, reqCtx context.Context, err error) error {
	if abortErr := abortErrorFromContext(parentCtx); abortErr != nil {
		return abortErr
	}
	if reqCtx != nil && errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
		return &NetworkError{Message: "Gemini 请求超时", Err: err}
	}
	return &NetworkError{Err: err}
}

func (c *geminiClient) endpointURL(modelName string, stream bool) (string, error) {
	method := "generateContent"
	if stream {
		method = "streamGenerateContent"
	}
	base := strings.TrimRight(c.baseURL, "/")
	path := fmt.Sprintf("%s/models/%s:%s", base, url.PathEscape(modelName), method)
	u, err := url.Parse(path)
	if err != nil {
		return "", &NetworkError{Message: "Gemini URL 无效", Err: err}
	}
	q := u.Query()
	q.Set("key", c.apiKey)
	if stream {
		q.Set("alt", "sse")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// pumpGeminiStream 读取 SSE 事件并逐块下发增量消息
// parentCtx 是调用方上下文，streamCtx 是本次请求的 Provider 超时上下文；
// 两者区分中断与 Provider 超时，后者按可重试网络错误上抛
func pumpGeminiStream(parentCtx, streamCtx context.Context, body io.Reader, writer *schema.StreamWriter[*schema.AgenticMessage]) {
	defer writer.Close()
	builder := newGeminiStreamBuilder()
	reader := bufio.NewReaderSize(body, 64<<10)
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			writer.Send(nil, geminiStreamReadError(parentCtx, streamCtx, readErr))
			return
		}
		if chunk, ok := decodeGeminiSSELine(line); ok {
			message, hasOutput, err := builder.consume(chunk)
			if err != nil {
				writer.Send(nil, err)
				return
			}
			if hasOutput {
				if writer.Send(message, nil) {
					return
				}
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	if err := builder.finish(); err != nil {
		writer.Send(nil, err)
		return
	}
	if abortErr := abortErrorFromContext(parentCtx); abortErr != nil {
		writer.Send(nil, abortErr)
	}
}

// geminiStreamReadError 分类流读取失败：调用方中断优先，其次 Provider 超时按可重试网络错误
func geminiStreamReadError(parentCtx, streamCtx context.Context, readErr error) error {
	if abortErr := abortErrorFromContext(parentCtx); abortErr != nil {
		return abortErr
	}
	if streamCtx != nil && errors.Is(streamCtx.Err(), context.DeadlineExceeded) {
		return &NetworkError{Message: "Gemini 流式请求超时", Err: readErr}
	}
	return &NetworkError{Err: readErr}
}

// decodeGeminiSSELine 解析单行 SSE 事件
func decodeGeminiSSELine(line string) (geminiStreamChunk, bool) {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, geminiSSEPrefix) {
		return geminiStreamChunk{}, false
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, geminiSSEPrefix))
	if data == "" || data == "[DONE]" {
		return geminiStreamChunk{}, false
	}
	var chunk geminiStreamChunk
	if err := sonic.UnmarshalString(data, &chunk); err != nil {
		return geminiStreamChunk{decodeErr: err}, true
	}
	return chunk, true
}

// geminiHTTPError 把非 2xx 响应转换成 HTTPStatusError
func geminiHTTPError(status int, raw []byte) error {
	return &HTTPStatusError{StatusCode: status, Message: truncateErrBody(raw), Op: "gemini"}
}

func truncateErrBody(raw []byte) string {
	const limit = 512
	s := strings.TrimSpace(string(raw))
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

// decodeThoughtSignature 规范化 thoughtSignature（REST 已是 base64 文本）
func decodeThoughtSignature(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if _, err := base64.StdEncoding.DecodeString(raw); err != nil {
		// 非 base64 时按 UTF-8 文本处理，保持与 MaiBot _extract_gemini_thought_signature 一致
		return base64.StdEncoding.EncodeToString([]byte(raw))
	}
	return raw
}

// warnGeminiMaxTokens 对齐 MaiBot _warn_if_max_tokens_truncated
func warnGeminiMaxTokens(finishReason, content string, toolCallCount int) {
	if !strings.Contains(finishReason, "MAX_TOKENS") {
		return
	}
	if strings.TrimSpace(content) != "" || toolCallCount > 0 {
		zap.L().Warn("Gemini 响应因达到 max_tokens 限制被部分截断，可能影响回复完整性，建议调整模型 max_tokens 配置")
		return
	}
	zap.L().Warn("Gemini 响应因达到 max_tokens 限制被截断，且未返回可见输出，请检查模型 max_tokens 配置")
}

func boolFromAny(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(strings.TrimSpace(x), "true")
	default:
		return false
	}
}
