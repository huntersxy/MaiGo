package model_client

// OpenAI 兼容 Chat Completions 客户端。
//
// 对等 src/llm_models/model_client/openai_client.py 主链路：auth_type
// bearer/header/query/none、default_headers/query、extra_params 三分、
// tools/tool_choice/temperature/max_tokens（含 max_completion_tokens 回退）、
// reasoning_content 与 <think> 解析、SSE 增量分流。
//
// 已知边界（工具参数修复未引入完整 json_repair、流内错误由上层整请求重放、
// 多媒体块折叠为文本）以 MaiBot 原版实现为准。

import (
	"bufio"
	"context"
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
)

const (
	chatCompletionsPath = "/chat/completions"
	// chatSSEPrefix Chat Completions 流式事件前缀
	chatSSEPrefix = "data:"
	// chatStreamPipeBuffer 增量消息管道容量
	chatStreamPipeBuffer = 8
)

// chatCompletionsClient 实现 model.AgenticModel，走 OpenAI 兼容 Chat Completions
type chatCompletionsClient struct {
	// apiKey 标准 Bearer 鉴权密钥；自定义头/查询参数鉴权时为空
	apiKey string
	// baseURL 已规范化的服务地址（不含尾斜杠）
	baseURL string
	model   string
	timeout time.Duration
	// extra 模型级 extra_params 拆分结果
	extra chatCompletionsOverrides
	// reasoningParseMode auto/native/think_tag/none
	reasoningParseMode string
	// toolArgumentParseMode auto/strict/repair/double_decode
	toolArgumentParseMode string
	// reasoningKey 原生 reasoning 字段名：reasoning_content 或 reasoning
	reasoningKey string
	// reasoningScope DeepSeek 官方端点允许原生推理回放的范围；其他端点为空
	reasoningScope *ContextProviderScope
	httpClient     *http.Client
	// cfg 只用于失败快照与日志元数据
	cfg ModelConfig
}

// usesBuiltinImageCompression 标记 Chat Completions 已在客户端内处理 413/data URI 图片压缩重试
func (*chatCompletionsClient) usesBuiltinImageCompression() {}

// NewChatCompletionsClient 创建 Chat Completions AgenticModel
func NewChatCompletionsClient(cfg ModelConfig) (model.AgenticModel, error) {
	return newChatCompletionsClient(cfg)
}

func newChatCompletionsClient(cfg ModelConfig) (*chatCompletionsClient, error) {
	modelName := strings.TrimSpace(cfg.Model)
	baseURL := normalizeOpenAIBaseURL(cfg.BaseURL)
	if baseURL == "" || modelName == "" {
		return nil, fmt.Errorf("主模型配置不完整")
	}

	extra := splitChatCompletionsOverrides(cfg.ExtraParams)
	auth := buildOpenAICompatAuth(cfg)
	// extra_params.headers/query 覆盖 Provider 默认头与默认查询参数
	for key, value := range extra.headers {
		auth.Headers[canonicalizeAuthorizationHeader(key)] = value
	}
	for key, value := range extra.query {
		auth.Query[key] = value
	}
	if _, ok := auth.Headers[authorizationHeader]; ok {
		auth.OmitAuthorization = false
	}
	// OpenAI SDK 在配置 organization/project 时固定发送这两个头
	if value := strings.TrimSpace(cfg.Organization); value != "" {
		auth.Headers["OpenAI-Organization"] = value
	}
	if value := strings.TrimSpace(cfg.Project); value != "" {
		auth.Headers["OpenAI-Project"] = value
	}

	httpClient := newLLMHTTPClient(0)
	httpClient.Transport = newProviderTransport(httpClient.Transport, auth)

	return &chatCompletionsClient{
		apiKey:                auth.APIKey,
		baseURL:               baseURL,
		model:                 modelName,
		timeout:               cfg.Timeout,
		extra:                 extra,
		reasoningParseMode:    normalizeChatReasoningMode(cfg.ReasoningParseMode),
		toolArgumentParseMode: normalizeChatToolArgumentMode(cfg.ToolArgumentParseMode),
		reasoningKey:          chatReasoningKey(baseURL),
		reasoningScope:        buildChatReasoningScope(cfg, baseURL),
		httpClient:            httpClient,
		cfg:                   cfg,
	}, nil
}

// buildChatReasoningScope 只在 DeepSeek 官方端点启用原生推理回放
func buildChatReasoningScope(cfg ModelConfig, baseURL string) *ContextProviderScope {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host != "api.deepseek.com" {
		return nil
	}
	providerName := strings.TrimSpace(cfg.ProviderName)
	if providerName == "" {
		return nil
	}
	scope := ContextProviderScope{
		SchemaVersion:       ProviderReplaySchemaVersion,
		ClientType:          "openai",
		ProviderName:        providerName,
		EndpointFingerprint: buildProviderEndpointFingerprint("openai", baseURL),
		ModelIdentifier:     strings.TrimSpace(cfg.Model),
	}
	if scope.Validate() != nil {
		return nil
	}
	return &scope
}

// Generate 非流式 Chat Completions
func (c *chatCompletionsClient) Generate(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	c = c.withResponseFormat(ctx)
	started := time.Now()
	recorder := snapshotAttemptRecorderFromContext(ctx)
	options := model.GetCommonOptions(&model.Options{}, opts...)
	resolveEffectiveParams(c.cfg, options)
	items, err := contextItemsForConfig(input, c.cfg)
	if err != nil {
		return nil, c.snapshotFailure(recorder, started, input, options, nil, false, err)
	}
	payload, sentLegacyMaxTokens, err := c.buildRequestBodyFromContextItems(items, options, false, c.requiresMaxCompletionTokens())
	if err != nil {
		return nil, c.snapshotFailure(recorder, started, input, options, nil, false, err)
	}
	finalPayload := payload
	raw, err := c.post(ctx, payload)
	if err != nil {
		if retryPayload, retryItems, retryLegacyMaxTokens, ok := c.imageCompressionRetryPayload(err, items, options, false); ok {
			finalPayload = retryPayload
			items = retryItems
			sentLegacyMaxTokens = retryLegacyMaxTokens
			raw, err = c.post(ctx, retryPayload)
		}
	}
	if err != nil {
		if retryPayload, ok := c.maxCompletionTokensRetryPayload(err, items, options, false, sentLegacyMaxTokens); ok {
			finalPayload = retryPayload
			raw, err = c.post(ctx, retryPayload)
		}
	}
	if err != nil {
		return nil, c.snapshotFailure(recorder, started, input, options, finalPayload, false, err)
	}
	message, err := c.parseResponse(raw, options.MaxTokens)
	if err != nil {
		return nil, c.snapshotFailure(recorder, started, input, options, finalPayload, false, err)
	}
	message, err = normalizeTextToolCalls(message, options.Tools, ChatToolChoiceValue(options), textToolCallOccupiedIDs(items))
	if err != nil {
		err = &ParseError{Message: err.Error(), Err: err}
		return nil, c.snapshotFailure(recorder, started, input, options, finalPayload, false, err)
	}
	cacheCfg := c.cfg
	if options != nil && options.Model != nil && strings.TrimSpace(*options.Model) != "" {
		cacheCfg.Model = strings.TrimSpace(*options.Model)
	}
	recordGenerationCacheUsage(ctx, cacheCfg, items, options, c.cfg.ResponseFormat, message)
	return message, nil
}

// Stream 流式 Chat Completions（SSE）
// 建流失败按错误分类由调度层重试/切模型；读流中途错误由 failover / usage 包装层
// 先缓冲再整请求重放（见 stream_replay.go），直接调用客户端时由调用方处理中途错误
func (c *chatCompletionsClient) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	c = c.withResponseFormat(ctx)
	started := time.Now()
	recorder := snapshotAttemptRecorderFromContext(ctx)
	options := model.GetCommonOptions(&model.Options{}, opts...)
	resolveEffectiveParams(c.cfg, options)
	items, err := contextItemsForConfig(input, c.cfg)
	if err != nil {
		return nil, c.snapshotFailure(recorder, started, input, options, nil, true, err)
	}
	payload, sentLegacyMaxTokens, err := c.buildRequestBodyFromContextItems(items, options, true, c.requiresMaxCompletionTokens())
	if err != nil {
		return nil, c.snapshotFailure(recorder, started, input, options, nil, true, err)
	}
	finalPayload := payload
	resp, err := c.openStream(ctx, payload)
	if err != nil {
		if retryPayload, retryItems, retryLegacyMaxTokens, ok := c.imageCompressionRetryPayload(err, items, options, true); ok {
			finalPayload = retryPayload
			items = retryItems
			sentLegacyMaxTokens = retryLegacyMaxTokens
			resp, err = c.openStream(ctx, retryPayload)
		}
	}
	if err != nil {
		if retryPayload, ok := c.maxCompletionTokensRetryPayload(err, items, options, true, sentLegacyMaxTokens); ok {
			finalPayload = retryPayload
			resp, err = c.openStream(ctx, retryPayload)
		}
	}
	if err != nil {
		return nil, c.snapshotFailure(recorder, started, input, options, finalPayload, true, err)
	}

	streamCtx, cancel := c.requestContext(ctx)
	reader, writer := schema.Pipe[*schema.AgenticMessage](chatStreamPipeBuffer)
	go func() {
		defer cancel()
		defer resp.Body.Close()
		pumpChatCompletionsStream(ctx, streamCtx, resp.Body, writer, newChatCompletionsStreamBuilder(c, options.MaxTokens, options.Tools, ChatToolChoiceValue(options), textToolCallOccupiedIDs(items), providerRequestID(resp.Header)))
	}()
	stream := wrapAgenticStreamSnapshot(reader, func(streamErr error) error {
		return c.snapshotFailure(recorder, started, input, options, finalPayload, true, streamErr)
	})
	cacheCfg := c.cfg
	if options.Model != nil && strings.TrimSpace(*options.Model) != "" {
		cacheCfg.Model = strings.TrimSpace(*options.Model)
	}
	return wrapAgenticStreamCacheStats(stream, ctx, cacheCfg, items, options, c.cfg.ResponseFormat), nil
}

// withResponseFormat 返回按请求上下文覆盖 response_format 的浅拷贝
func (c *chatCompletionsClient) withResponseFormat(ctx context.Context) *chatCompletionsClient {
	raw, ok := responseFormatFromContext(ctx)
	if !ok {
		return c
	}
	copied := *c
	copied.cfg.ResponseFormat = raw
	return &copied
}

// snapshotFailure 保存失败请求快照并把路径挂到错误链
func (c *chatCompletionsClient) snapshotFailure(recorder *snapshotAttemptRecorder, started time.Time, input []*schema.AgenticMessage, options *model.Options, payload map[string]any, stream bool, err error) error {
	if err == nil {
		return nil
	}
	internal := map[string]any{
		"model":                    c.model,
		"base_url":                 c.baseURL,
		"stream":                   stream,
		"reasoning_parse_mode":     c.reasoningParseMode,
		"tool_argument_parse_mode": c.toolArgumentParseMode,
	}
	spec := newRequestSnapshotSpec(c.cfg, "chat.completions", "response", input, options, payload, internal)
	spec.LogicalTurnID = snapshotLogicalTurnID(recorder)
	spec.Error = err
	spec.StartedAt = started
	recordSnapshotAttempt(recorder, &spec, started)
	return captureRequestSnapshot(spec)
}

// requestContext 单次 HTTP 请求的超时（对齐 MaiBot provider.timeout）
func (c *chatCompletionsClient) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.timeout)
}

func (c *chatCompletionsClient) endpointURL() string {
	return c.baseURL + chatCompletionsPath
}

func (c *chatCompletionsClient) newRequest(ctx context.Context, payload map[string]any, stream bool) (*http.Request, error) {
	body, err := sonic.Marshal(payload)
	if err != nil {
		return nil, &ParseError{Message: "序列化 Chat Completions 请求失败", Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpointURL(), strings.NewReader(string(body)))
	if err != nil {
		return nil, &NetworkError{Message: "创建 Chat Completions 请求失败", Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	// 标准 Bearer 鉴权；自定义头/查询参数由 providerTransport 注入
	if c.apiKey != "" {
		req.Header.Set(authorizationHeader, "Bearer "+c.apiKey)
	}
	return req, nil
}

func (c *chatCompletionsClient) post(ctx context.Context, payload map[string]any) ([]byte, error) {
	reqCtx, cancel := c.requestContext(ctx)
	defer cancel()
	req, err := c.newRequest(reqCtx, payload, false)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.transportError(ctx, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, c.transportError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, chatCompletionsHTTPError(resp.StatusCode, raw, resp.Header)
	}
	return raw, nil
}

// openStream 建立 SSE 连接；HTTP 非 2xx 与连接错误在建流阶段上抛
func (c *chatCompletionsClient) openStream(ctx context.Context, payload map[string]any) (*http.Response, error) {
	reqCtx, cancel := c.requestContext(ctx)
	req, err := c.newRequest(reqCtx, payload, true)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		cancel()
		return nil, c.transportError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer cancel()
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, chatCompletionsHTTPError(resp.StatusCode, raw, resp.Header)
	}
	// 连接超时由流式读取阶段的 streamCtx 继续控制，此处不能 cancel
	return resp, nil
}

// transportError 把底层连接错误转换成 MaiBot 的异常分类
// 请求级超时按网络错误（可重试），调用方取消按中断处理
func (c *chatCompletionsClient) transportError(ctx context.Context, err error) error {
	if ctx.Err() == nil {
		return &NetworkError{Err: err}
	}
	if abortErr := abortErrorFromContext(ctx); abortErr != nil {
		return abortErr
	}
	return &NetworkError{Err: err}
}

func chatCompletionsHTTPError(status int, raw []byte, header http.Header) error {
	return &HTTPStatusError{StatusCode: status, Message: truncateErrBody(raw), Op: "chat_completions", RequestID: providerRequestID(header)}
}

// pumpChatCompletionsStream 读取 SSE 事件并逐块下发增量消息
// parentCtx 是调用方上下文，streamCtx 是本次请求的 Provider 超时上下文；
// 两者区分中断与 Provider 超时，后者按可重试网络错误上抛
func pumpChatCompletionsStream(parentCtx, streamCtx context.Context, body io.Reader, writer *schema.StreamWriter[*schema.AgenticMessage], builder *chatCompletionsStreamBuilder) {
	defer writer.Close()
	reader := bufio.NewReaderSize(body, 64<<10)
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			err := chatStreamReadError(parentCtx, streamCtx, readErr)
			recordStreamPartialOnAbort(parentCtx, err, builder.snapshotInterruptedOutput)
			writer.Send(nil, err)
			return
		}
		if chunk, ok := decodeChatCompletionsSSELine(line); ok {
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
	message, hasOutput, err := builder.finish()
	if err != nil {
		writer.Send(nil, err)
		return
	}
	if hasOutput {
		if writer.Send(message, nil) {
			return
		}
	}
	if abortErr := abortErrorFromContext(parentCtx); abortErr != nil {
		recordStreamPartialOnAbort(parentCtx, abortErr, builder.snapshotInterruptedOutput)
		writer.Send(nil, abortErr)
	}
}

// recordStreamPartialOnAbort 中断时把已累积片段写入请求上下文里的接收器，
// 供上层的 ReqAbortException 语义携带未完成输出。
func recordStreamPartialOnAbort(ctx context.Context, err error, snapshot func() StreamPartialOutput) {
	if err == nil || snapshot == nil {
		return
	}
	var abortErr *AbortError
	if !errors.As(err, &abortErr) {
		return
	}
	if sink := StreamPartialSinkFromContext(ctx); sink != nil {
		sink.Set(snapshot())
	}
}

// chatStreamReadError 分类流读取失败：调用方中断优先，其次 Provider 超时按可重试网络错误
// 对齐 MaiBot utils_model.py:1137-1184：网络/超时可重试，ReqAbortException 直接上抛
func chatStreamReadError(parentCtx, streamCtx context.Context, readErr error) error {
	if abortErr := abortErrorFromContext(parentCtx); abortErr != nil {
		return abortErr
	}
	if streamCtx != nil && errors.Is(streamCtx.Err(), context.DeadlineExceeded) {
		return &NetworkError{Message: "Chat Completions 流式请求超时", Err: readErr}
	}
	return &NetworkError{Err: readErr}
}

// decodeChatCompletionsSSELine 解析单行 SSE 事件
func decodeChatCompletionsSSELine(line string) (chatCompletionChunk, bool) {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, chatSSEPrefix) {
		return chatCompletionChunk{}, false
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, chatSSEPrefix))
	if data == "" || data == "[DONE]" {
		return chatCompletionChunk{}, false
	}
	var chunk chatCompletionChunk
	if err := sonic.UnmarshalString(data, &chunk); err != nil {
		return chatCompletionChunk{decodeErr: err}, true
	}
	return chunk, true
}
