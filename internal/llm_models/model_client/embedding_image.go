package model_client

// 对齐 MaiBot src/llm_models/model_client/openai_client.py 的图片嵌入三条通道：
//   - 显式 image_embedding_input / image_embedding_body 模板（OpenAI 兼容扩展协议）
//   - 百炼 / 豆包官方地址的原生多模态协议（自动切换）
//   - 硅基流动官方地址的 /embeddings 图片模板（自动填写）
// 每条通道都产出与 Python 相同的 request_protocol_hash，供图片记忆指纹使用

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"github.com/openai/openai-go/v3/option"
)

// EmbedImage 生成图片嵌入；通道选择与协议指纹对齐 Python get_image_embedding
func (c *openAIEmbeddingClient) EmbedImage(ctx context.Context, input ImageEmbeddingInput) (EmbeddingResult, error) {
	if len(input.Bytes) == 0 {
		return EmbeddingResult{}, fmt.Errorf("图片嵌入输入不能为空")
	}
	if mime := strings.TrimSpace(input.MIMEType); mime == "" || !strings.HasPrefix(mime, "image/") {
		return EmbeddingResult{}, fmt.Errorf("图片嵌入需要 image/* MIME 类型")
	}

	extraParams := imageExtraParams(c.cfg.ExtraParams)
	// 显式模板优先；该路径没有客户端指纹，沿用编排器兜底算法
	if hasImageEmbeddingTemplateKey(extraParams) {
		protocolHash, err := defaultImageEmbeddingProtocolHash(extraParams)
		if err != nil {
			return EmbeddingResult{}, err
		}
		return c.embedImageWithTemplate(ctx, extraParams, input, protocolHash)
	}
	if protocol := resolveNativeImageEmbeddingProtocol(c.cfg.BaseURL); protocol != "" {
		return c.embedImageNative(ctx, protocol, input)
	}
	// 硅基流动官方地址自动填入图片输入模板，指纹按模板与原始语义参数计算
	if template, ok := resolveCompatibleImageEmbeddingInput(c.cfg.BaseURL); ok {
		protocolHash, err := buildCompatibleImageEmbeddingFingerprint(template, extraParams)
		if err != nil {
			return EmbeddingResult{}, err
		}
		autoParams := imageExtraParams(extraParams)
		autoParams["image_embedding_input"] = template
		return c.embedImageWithTemplate(ctx, autoParams, input, protocolHash)
	}
	return EmbeddingResult{}, errImageEmbeddingMissingTemplate
}

// embedImageWithTemplate 对齐 _get_openai_compatible_image_embedding
func (c *openAIEmbeddingClient) embedImageWithTemplate(
	ctx context.Context,
	extraParams map[string]any,
	input ImageEmbeddingInput,
	protocolHash string,
) (EmbeddingResult, error) {
	if err := validateImageEmbeddingTransport(c.cfg.BaseURL); err != nil {
		return EmbeddingResult{}, err
	}
	mime := strings.TrimSpace(input.MIMEType)
	encoded := base64.StdEncoding.EncodeToString(input.Bytes)
	replacements := map[string]string{
		"base64":    encoded,
		"data_uri":  "data:" + mime + ";base64," + encoded,
		"mime_type": mime,
	}
	inputTemplate, hasInput := extraParams["image_embedding_input"]
	delete(extraParams, "image_embedding_input")
	bodyTemplate, hasBody := extraParams["image_embedding_body"]
	delete(extraParams, "image_embedding_body")

	var imageInput any
	if hasInput {
		imageInput = renderImageEmbeddingTemplate(inputTemplate, replacements)
	}
	if hasBody && bodyTemplate != nil {
		rendered, ok := renderImageEmbeddingTemplate(bodyTemplate, replacements).(map[string]any)
		if !ok {
			return EmbeddingResult{}, fmt.Errorf("image_embedding_body 必须是对象")
		}
		if raw, exists := rendered["input"]; exists {
			imageInput = raw
			delete(rendered, "input")
		}
		for key, value := range rendered {
			extraParams[key] = value
		}
	}
	if imageInput == nil {
		return EmbeddingResult{}, fmt.Errorf("图片嵌入请求模板缺少 input")
	}

	overrides, err := splitOpenAIRequestOverrides(extraParams)
	if err != nil {
		return EmbeddingResult{}, err
	}
	body := make(map[string]any, len(overrides.Body)+2)
	body["model"] = c.cfg.Model
	body["input"] = imageInput
	for key, value := range overrides.Body {
		body[key] = value
	}
	raw, err := c.executeImageEmbeddingRequest(ctx, "/embeddings", body, overrides)
	if err != nil {
		wrapped := fmt.Errorf("OpenAI 兼容图片嵌入失败: %w", err)
		return EmbeddingResult{}, c.snapshotImageEmbeddingFailure("/embeddings", body, input, wrapped)
	}
	vector, model, usage, err := parseCompatibleImageEmbeddingResponse(raw)
	if err != nil {
		return EmbeddingResult{}, c.snapshotImageEmbeddingFailure("/embeddings", body, input, err)
	}
	return EmbeddingResult{
		Embedding:       vector,
		Model:           model,
		ModelIdentifier: c.cfg.Model,
		APIProvider:     c.cfg.ProviderName,
		RequestProtocol: protocolHash,
		Usage:           usage,
	}, nil
}

// embedImageNative 对齐 _get_native_image_embedding
func (c *openAIEmbeddingClient) embedImageNative(ctx context.Context, protocol string, input ImageEmbeddingInput) (EmbeddingResult, error) {
	if err := validateImageEmbeddingTransport(c.cfg.BaseURL); err != nil {
		return EmbeddingResult{}, err
	}
	nativeRequest, err := buildNativeImageEmbeddingRequest(
		protocol,
		c.cfg.BaseURL,
		c.cfg.Model,
		input.Bytes,
		strings.TrimSpace(input.MIMEType),
		c.cfg.ExtraParams,
	)
	if err != nil {
		return EmbeddingResult{}, err
	}
	overrides := imageRequestOverrides{
		Headers: nativeRequest.ExtraHeaders,
		Query:   nativeRequest.ExtraQuery,
		Body:    map[string]any{},
	}
	raw, err := c.executeImageEmbeddingRequest(ctx, nativeRequest.EndpointURL, nativeRequest.Payload, overrides)
	if err != nil {
		wrapped := fmt.Errorf("%s 图片嵌入失败: %w", protocol, err)
		return EmbeddingResult{}, c.snapshotImageEmbeddingFailure(nativeRequest.EndpointURL, nativeRequest.Payload, input, wrapped)
	}
	vector, usage, err := parseNativeImageEmbeddingResponse(protocol, raw)
	if err != nil {
		return EmbeddingResult{}, c.snapshotImageEmbeddingFailure(nativeRequest.EndpointURL, nativeRequest.Payload, input, err)
	}
	protocolHash, err := buildNativeImageEmbeddingFingerprint(nativeRequest, c.cfg.Model)
	if err != nil {
		return EmbeddingResult{}, err
	}
	return EmbeddingResult{
		Embedding:       vector,
		Model:           stringOrEmpty(c.cfg.Model),
		ModelIdentifier: c.cfg.Model,
		APIProvider:     c.cfg.ProviderName,
		RequestProtocol: protocolHash,
		Usage:           usage,
	}, nil
}

// executeImageEmbeddingRequest 发送图片嵌入请求并按限速重试；target 可为绝对地址
func (c *openAIEmbeddingClient) executeImageEmbeddingRequest(
	ctx context.Context,
	target string,
	body map[string]any,
	overrides imageRequestOverrides,
) ([]byte, error) {
	baseOptions := make([]option.RequestOption, 0, len(overrides.Headers)+len(overrides.Query)+1)
	for key, value := range overrides.Headers {
		baseOptions = append(baseOptions, option.WithHeader(key, value))
	}
	for key, value := range overrides.Query {
		baseOptions = append(baseOptions, option.WithQuery(key, pythonString(value)))
	}

	limiter := getEmbeddingRateLimiter(c.cfg)
	var lastErr error
	attempts := providerRetryAttempts(c.cfg)
	for attempt := 0; attempt < attempts; attempt++ {
		generation, _, waitErr := limiter.wait(ctx)
		if waitErr != nil {
			return nil, waitErr
		}
		var response *http.Response
		callOptions := make([]option.RequestOption, 0, len(baseOptions)+1)
		callOptions = append(callOptions, baseOptions...)
		callOptions = append(callOptions, option.WithResponseInto(&response))
		if err := c.client.Post(ctx, target, body, nil, callOptions...); err != nil {
			lastErr = err
			status := embeddingErrorStatus(err)
			if status == 429 {
				limiter.reduce(generation)
			}
			if attempt == attempts-1 || !embeddingRetryableStatus(status) {
				break
			}
			if waitErr := sleepEmbeddingContext(ctx, time.Duration(1<<attempt)*time.Second); waitErr != nil {
				return nil, waitErr
			}
			continue
		}
		return readImageEmbeddingResponseBody(response)
	}
	return nil, lastErr
}

// readImageEmbeddingResponseBody 读取图片嵌入响应原始字节
func readImageEmbeddingResponseBody(response *http.Response) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, fmt.Errorf("图片嵌入响应为空")
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("读取图片嵌入响应失败: %w", err)
	}
	return raw, nil
}

// parseCompatibleImageEmbeddingResponse 解析 OpenAI 兼容图片嵌入响应
func parseCompatibleImageEmbeddingResponse(raw []byte) ([]float64, string, *schema.TokenUsage, error) {
	var document map[string]any
	if err := sonic.Unmarshal(raw, &document); err != nil {
		return nil, "", nil, fmt.Errorf("解析图片嵌入响应失败: %w", err)
	}
	rows, ok := document["data"].([]any)
	if !ok || len(rows) == 0 {
		return nil, "", nil, fmt.Errorf("图片嵌入响应缺少 embeddings 数据")
	}
	first, ok := rows[0].(map[string]any)
	if !ok {
		return nil, "", nil, fmt.Errorf("图片嵌入响应缺少 embeddings 数据")
	}
	embedding, ok := first["embedding"]
	if !ok {
		return nil, "", nil, fmt.Errorf("图片嵌入响应缺少 embeddings 数据")
	}
	vector, err := imageEmbeddingVector(embedding)
	if err != nil {
		return nil, "", nil, err
	}
	model, _ := document["model"].(string)
	return vector, model, imageEmbeddingUsage(document["usage"], false), nil
}

// renderImageEmbeddingTemplate 递归替换图片嵌入输入模板中的受控占位符
func renderImageEmbeddingTemplate(value any, replacements map[string]string) any {
	switch typed := value.(type) {
	case string:
		rendered := typed
		for name, replacement := range replacements {
			rendered = strings.ReplaceAll(rendered, "{"+name+"}", replacement)
		}
		return rendered
	case []any:
		rendered := make([]any, len(typed))
		for index, item := range typed {
			rendered[index] = renderImageEmbeddingTemplate(item, replacements)
		}
		return rendered
	case map[string]any:
		rendered := make(map[string]any, len(typed))
		for key, item := range typed {
			rendered[key] = renderImageEmbeddingTemplate(item, replacements)
		}
		return rendered
	default:
		return value
	}
}

// hasImageEmbeddingTemplateKey 判断是否配置了显式图片嵌入模板
func hasImageEmbeddingTemplateKey(extraParams map[string]any) bool {
	if _, ok := extraParams["image_embedding_input"]; ok {
		return true
	}
	_, ok := extraParams["image_embedding_body"]
	return ok
}

// marshalCompactSortedJSON 对齐 json.dumps(sort_keys=True, ensure_ascii=False, separators=(",", ":"))
func marshalCompactSortedJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{0x0a}), nil
}

// stringOrEmpty 去空白后返回字符串
func stringOrEmpty(value string) string {
	return strings.TrimSpace(value)
}

// snapshotImageEmbeddingFailure 保存图片嵌入失败快照；请求体中的原始 base64 先替换为占位符
func (c *openAIEmbeddingClient) snapshotImageEmbeddingFailure(
	target string,
	body map[string]any,
	input ImageEmbeddingInput,
	err error,
) error {
	if err == nil {
		return nil
	}
	encoded := base64.StdEncoding.EncodeToString(input.Bytes)
	sanitized, _ := sanitizeImageEmbeddingSnapshotBody(body, encoded).(map[string]any)
	if sanitized == nil {
		sanitized = body
	}
	internal := map[string]any{
		"operation":    "image_embedding",
		"base_url":     c.cfg.BaseURL,
		"endpoint":     target,
		"mime_type":    input.MIMEType,
		"image_bytes":  len(input.Bytes),
		"task_name":    input.TaskName,
		"request_type": input.RequestType,
		"session_id":   input.SessionID,
	}
	spec := newRequestSnapshotSpec(c.cfg, "image_embedding", "image_embedding", nil, nil, sanitized, internal)
	spec.ContextItems = imageEmbeddingContextItems(input)
	spec.Error = err
	return captureRequestSnapshot(spec)
}

// imageEmbeddingContextItems 把图片嵌入输入投影为 Context Item；无法识别格式时用不透明摘要，不伪造字节
func imageEmbeddingContextItems(input ImageEmbeddingInput) []ContextItem {
	format := imageEmbeddingContextFormat(input.MIMEType)
	if len(input.Bytes) > 0 && format != "" {
		return []ContextItem{NewUserMessageItem(ContextContentPart{
			Type:        ContextPartImage,
			ImageFormat: format,
			ImageBase64: base64.StdEncoding.EncodeToString(input.Bytes),
		})}
	}
	summary := "图片嵌入请求"
	if input.MIMEType != "" {
		summary += "（" + input.MIMEType + "）"
	}
	return []ContextItem{NewProviderOpaqueItem("image_embedding", summary)}
}

func imageEmbeddingContextFormat(mimeType string) string {
	lower := strings.ToLower(strings.TrimSpace(mimeType))
	switch lower {
	case "image/png":
		return "png"
	case "image/jpeg", "image/jpg":
		return "jpeg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	}
	return ""
}

// sanitizeImageEmbeddingSnapshotBody 递归替换请求体中的原始 base64 图片
func sanitizeImageEmbeddingSnapshotBody(value any, encoded string) any {
	switch typed := value.(type) {
	case string:
		if encoded == "" {
			return typed
		}
		return strings.ReplaceAll(typed, encoded, "[见媒体引用]")
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = sanitizeImageEmbeddingSnapshotBody(item, encoded)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = sanitizeImageEmbeddingSnapshotBody(item, encoded)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(typed))
		for key, item := range typed {
			out[key] = strings.ReplaceAll(item, encoded, "[见媒体引用]")
		}
		return out
	default:
		return value
	}
}
