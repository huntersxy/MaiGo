package model_client

// 对齐 MaiBot src/llm_models/model_client/image_embedding_protocols.py
// 百炼（dashscope）与豆包（ark）原生多模态图片嵌入使用独立端点、请求体与响应结构，
// 硅基流动沿用 /embeddings 但图片输入需要 {"image": ...} 模板；这里负责协议识别、
// 原生请求构造、响应解析与向量空间指纹。指纹只覆盖协议版本、端点、模型与影响向量
// 空间的语义参数，不包含图片字节或任何凭据

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
)

const (
	// imageEmbeddingProtocolDashScope 阿里云百炼原生多模态嵌入协议标识
	imageEmbeddingProtocolDashScope = "dashscope"
	// imageEmbeddingProtocolArk 火山引擎方舟原生多模态嵌入协议标识
	imageEmbeddingProtocolArk = "ark"
)

const (
	// dashScopeImageEmbeddingNativePath 百炼原生向量接口路径；兼容层与原生地址统一改写
	dashScopeImageEmbeddingNativePath = "/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding"
	// arkImageEmbeddingNativeSuffix 豆包原生多模态向量接口后缀；保留 Provider 基路径
	arkImageEmbeddingNativeSuffix = "/embeddings/multimodal"
	// dashScopeImageEmbeddingHostname 百炼共享 DashScope 域名
	dashScopeImageEmbeddingHostname = "dashscope.aliyuncs.com"
	// arkImageEmbeddingHostname 豆包方舟官方域名
	arkImageEmbeddingHostname = "ark.cn-beijing.volces.com"

	// imageEmbeddingTransportMessage 图片二进制只允许 HTTPS 或本机回环 HTTP
	imageEmbeddingTransportMessage = "图片嵌入服务必须使用 HTTPS；仅本机回环地址允许 HTTP，请修改 Provider 地址"
)

var (
	// errImageEmbeddingTransport 图片嵌入传输层校验失败
	errImageEmbeddingTransport = errors.New(imageEmbeddingTransportMessage)
	// errImageEmbeddingMissingTemplate 未配置模板且不是官方地址
	errImageEmbeddingMissingTemplate = errors.New("图片嵌入必须配置 Provider 对应的 image_embedding_input 或 image_embedding_body")
)

// dashScopeWorkspaceHostnamePattern 百炼业务空间专属域名 {WorkspaceId}.{region}.maas.aliyuncs.com
var dashScopeWorkspaceHostnamePattern = regexp.MustCompile(`^[a-z0-9-]+\.[a-z0-9-]+\.maas\.aliyuncs\.com$`)

// 百炼 Provider 地址允许的基路径（规范化后精确匹配）
var dashScopeImageEmbeddingBasePaths = map[string]struct{}{
	"/compatible-mode/v1": {},
	"/api/v1":             {},
}

// 豆包 Provider 地址允许的基路径；套餐与 Coding Plan 网关必须保留原基路径
var arkImageEmbeddingBasePaths = map[string]struct{}{
	"/api/v3":        {},
	"/api/plan/v3":   {},
	"/api/coding/v3": {},
}

// 由本次图片输入独占的请求字段，禁止通过 extra_params 覆盖
var dashScopeImageEmbeddingReservedKeys = []string{"model", "input"}
var arkImageEmbeddingReservedKeys = []string{"model", "input", "encoding_format"}

// nativeImageEmbeddingRequest 一次原生图片嵌入请求的完整描述
type nativeImageEmbeddingRequest struct {
	Protocol       string
	EndpointURL    string
	Payload        map[string]any
	SemanticParams map[string]any
	ExtraHeaders   map[string]string
	ExtraQuery     map[string]any
}

// imageRequestOverrides 单次图片嵌入请求的头、查询参数与请求体扩展字段
type imageRequestOverrides struct {
	Headers map[string]string
	Query   map[string]any
	Body    map[string]any
}

// resolveNativeImageEmbeddingProtocol 识别官方百炼/豆包地址并返回原生协议；其他地址返回空串
func resolveNativeImageEmbeddingProtocol(baseURL string) string {
	parsed, ok := parseImageEmbeddingBaseURL(baseURL)
	if !ok || parsed.Hostname() == "" {
		return ""
	}
	if scheme := strings.ToLower(parsed.Scheme); scheme != "https" && scheme != "http" {
		return ""
	}
	hostname := strings.ToLower(parsed.Hostname())
	path := strings.TrimRight(parsed.Path, "/")
	if isDashScopeImageEmbeddingHostname(hostname) && hasImageEmbeddingPath(dashScopeImageEmbeddingBasePaths, path) {
		return imageEmbeddingProtocolDashScope
	}
	if hostname == arkImageEmbeddingHostname && hasImageEmbeddingPath(arkImageEmbeddingBasePaths, path) {
		return imageEmbeddingProtocolArk
	}
	return ""
}

// isDashScopeImageEmbeddingHostname 判断小写 hostname 是否为百炼官方推理域名
func isDashScopeImageEmbeddingHostname(hostname string) bool {
	return hostname == dashScopeImageEmbeddingHostname || dashScopeWorkspaceHostnamePattern.MatchString(hostname)
}

// resolveCompatibleImageEmbeddingInput 识别硅基流动官方地址并返回其 /embeddings 图片输入模板
func resolveCompatibleImageEmbeddingInput(baseURL string) (map[string]any, bool) {
	parsed, ok := parseImageEmbeddingBaseURL(baseURL)
	if !ok {
		return nil, false
	}
	if scheme := strings.ToLower(parsed.Scheme); scheme != "https" && scheme != "http" {
		return nil, false
	}
	if strings.TrimRight(parsed.Path, "/") != "/v1" {
		return nil, false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "api.siliconflow.cn", "api.siliconflow.com":
		return map[string]any{"image": "{data_uri}"}, true
	default:
		return nil, false
	}
}

// buildCompatibleImageEmbeddingFingerprint 计算兼容模板自动适配后的协议指纹
func buildCompatibleImageEmbeddingFingerprint(inputTemplate map[string]any, extraParams map[string]any) (string, error) {
	return imageEmbeddingHash(map[string]any{
		"input":      inputTemplate,
		"body":       nil,
		"task":       extraParams["task"],
		"dimensions": extraParams["dimensions"],
	})
}

// defaultImageEmbeddingProtocolHash 对齐 MaiBot 编排器在客户端未提供协议指纹时的兜底算法
// 载荷取自模型 extra_params 的配置值（未渲染模板），旧模板与插件路径沿用该结果
func defaultImageEmbeddingProtocolHash(extraParams map[string]any) (string, error) {
	input, ok := extraParams["image_embedding_input"]
	if !ok {
		input = "{data_uri}"
	}
	return imageEmbeddingHash(map[string]any{
		"input":      input,
		"body":       extraParams["image_embedding_body"],
		"task":       extraParams["task"],
		"dimensions": extraParams["dimensions"],
	})
}

// buildNativeImageEmbeddingFingerprint 计算原生图片嵌入的向量空间指纹
func buildNativeImageEmbeddingFingerprint(request nativeImageEmbeddingRequest, modelIdentifier string) (string, error) {
	parsed, ok := parseImageEmbeddingBaseURL(request.EndpointURL)
	endpointPath := request.EndpointURL
	if ok {
		endpointPath = parsed.Path
	}
	return imageEmbeddingHash(map[string]any{
		"endpoint":         endpointPath,
		"model":            modelIdentifier,
		"protocol_version": request.Protocol + "_image_embedding_v1",
		"semantic_params":  request.SemanticParams,
	})
}

// buildNativeImageEmbeddingRequest 构造指定协议的原生图片嵌入请求
func buildNativeImageEmbeddingRequest(
	protocol string,
	baseURL string,
	modelIdentifier string,
	imageBytes []byte,
	mimeType string,
	extraParams map[string]any,
) (nativeImageEmbeddingRequest, error) {
	extra := imageExtraParams(extraParams)
	headers, err := popImageEmbeddingMapping(extra, "headers")
	if err != nil {
		return nativeImageEmbeddingRequest{}, err
	}
	query, err := popImageEmbeddingMapping(extra, "query")
	if err != nil {
		return nativeImageEmbeddingRequest{}, err
	}
	// 与 OpenAI 兼容路径一致：body 子对象视为请求体参数，同名普通键优先级更高
	bodyMapping, err := popImageEmbeddingMapping(extra, "body")
	if err != nil {
		return nativeImageEmbeddingRequest{}, err
	}
	merged := make(map[string]any, len(bodyMapping)+len(extra))
	for key, value := range bodyMapping {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}

	dataURI := buildImageEmbeddingDataURI(imageBytes, mimeType)
	endpointURL, err := buildNativeImageEmbeddingEndpointURL(baseURL, protocol)
	if err != nil {
		return nativeImageEmbeddingRequest{}, err
	}
	var payload map[string]any
	var semanticParams map[string]any
	switch protocol {
	case imageEmbeddingProtocolDashScope:
		payload, semanticParams, err = buildDashScopeImageEmbeddingPayload(merged, modelIdentifier, dataURI)
	case imageEmbeddingProtocolArk:
		payload, semanticParams, err = buildArkImageEmbeddingPayload(merged, modelIdentifier, dataURI)
	default:
		err = fmt.Errorf("未知的图片嵌入原生协议: %s", protocol)
	}
	if err != nil {
		return nativeImageEmbeddingRequest{}, err
	}
	extraHeaders := make(map[string]string, len(headers))
	for key, value := range headers {
		extraHeaders[pythonString(key)] = pythonString(value)
	}
	return nativeImageEmbeddingRequest{
		Protocol:       protocol,
		EndpointURL:    endpointURL,
		Payload:        payload,
		SemanticParams: semanticParams,
		ExtraHeaders:   extraHeaders,
		ExtraQuery:     query,
	}, nil
}

// buildNativeImageEmbeddingEndpointURL 从 Provider 基础地址派生原生端点
func buildNativeImageEmbeddingEndpointURL(baseURL string, protocol string) (string, error) {
	parsed, ok := parseImageEmbeddingBaseURL(baseURL)
	if !ok {
		return "", fmt.Errorf("图片嵌入 Provider 地址无效: %s", baseURL)
	}
	origin := parsed.Scheme + "://"
	if parsed.User != nil {
		origin += parsed.User.String() + "@"
	}
	origin += parsed.Host
	switch protocol {
	case imageEmbeddingProtocolDashScope:
		return origin + dashScopeImageEmbeddingNativePath, nil
	case imageEmbeddingProtocolArk:
		return origin + strings.TrimRight(parsed.Path, "/") + arkImageEmbeddingNativeSuffix, nil
	default:
		return "", fmt.Errorf("未知的图片嵌入原生协议: %s", protocol)
	}
}

// buildDashScopeImageEmbeddingPayload 构造百炼原生请求体，返回 (payload, 指纹语义参数)
func buildDashScopeImageEmbeddingPayload(extra map[string]any, modelIdentifier string, dataURI string) (map[string]any, map[string]any, error) {
	for _, key := range dashScopeImageEmbeddingReservedKeys {
		if _, ok := extra[key]; ok {
			return nil, nil, imageEmbeddingReservedConflictError(imageEmbeddingProtocolDashScope, key)
		}
	}
	parameters := map[string]any{}
	if raw, ok := extra["parameters"]; ok {
		delete(extra, "parameters")
		if raw != nil {
			mapping, ok := raw.(map[string]any)
			if !ok {
				return nil, nil, fmt.Errorf("extra_params.parameters 必须是对象")
			}
			for key, value := range mapping {
				parameters[key] = value
			}
		}
	}
	// 百炼的维度字段是 parameters.dimension；兼容通用的顶层 dimensions 写法
	if dimensions, ok := extra["dimensions"]; ok {
		delete(extra, "dimensions")
		if dimensions != nil {
			if existing, exists := parameters["dimension"]; exists && !reflect.DeepEqual(existing, dimensions) {
				return nil, nil, fmt.Errorf("extra_params.parameters.dimension 与 extra_params.dimensions 冲突")
			}
			parameters["dimension"] = dimensions
		}
	}
	payload := map[string]any{
		"model": modelIdentifier,
		"input": map[string]any{"contents": []any{map[string]any{"image": dataURI}}},
	}
	if len(parameters) > 0 {
		payload["parameters"] = parameters
	}
	// 其余键按原生协议原样并入顶层，交由服务端校验
	for key, value := range extra {
		payload[key] = value
	}
	semanticParams := make(map[string]any, len(parameters)+len(extra))
	for key, value := range parameters {
		semanticParams[key] = value
	}
	for key, value := range extra {
		semanticParams[key] = value
	}
	return payload, semanticParams, nil
}

// buildArkImageEmbeddingPayload 构造豆包原生请求体，返回 (payload, 指纹语义参数)
func buildArkImageEmbeddingPayload(extra map[string]any, modelIdentifier string, dataURI string) (map[string]any, map[string]any, error) {
	for _, key := range arkImageEmbeddingReservedKeys {
		if _, ok := extra[key]; ok {
			return nil, nil, imageEmbeddingReservedConflictError(imageEmbeddingProtocolArk, key)
		}
	}
	payload := map[string]any{
		"model": modelIdentifier,
		"input": []any{map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": dataURI},
		}},
		"encoding_format": "float",
	}
	for key, value := range extra {
		payload[key] = value
	}
	semanticParams := make(map[string]any, len(extra))
	for key, value := range extra {
		semanticParams[key] = value
	}
	return payload, semanticParams, nil
}

// imageEmbeddingReservedConflictError 对齐 Python 保留字段冲突文案
func imageEmbeddingReservedConflictError(protocol string, key string) error {
	return fmt.Errorf(
		"extra_params 不允许设置原生图片嵌入协议的保留字段 %s（协议: %s），该字段由本次请求的模型与图片输入独占",
		key, protocol,
	)
}

// parseNativeImageEmbeddingResponse 按命中协议解析原生图片嵌入响应
func parseNativeImageEmbeddingResponse(protocol string, raw []byte) ([]float64, *schema.TokenUsage, error) {
	var document map[string]any
	if err := sonic.Unmarshal(raw, &document); err != nil {
		return nil, nil, fmt.Errorf("图片嵌入响应不是 JSON 对象: %w", err)
	}
	var values any
	switch protocol {
	case imageEmbeddingProtocolDashScope:
		output, ok := document["output"].(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("百炼图片嵌入响应缺少 output 对象")
		}
		embeddings, ok := output["embeddings"].([]any)
		if !ok || len(embeddings) == 0 {
			return nil, nil, fmt.Errorf("百炼图片嵌入响应缺少 output.embeddings")
		}
		first, ok := embeddings[0].(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("百炼图片嵌入响应的 output.embeddings[0] 缺少 embedding")
		}
		embedding, ok := first["embedding"]
		if !ok {
			return nil, nil, fmt.Errorf("百炼图片嵌入响应的 output.embeddings[0] 缺少 embedding")
		}
		values = embedding
	case imageEmbeddingProtocolArk:
		// 豆包多模态嵌入的 data 是单个结果对象，与 OpenAI 的 data 列表不同
		data, ok := document["data"].(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("豆包图片嵌入响应缺少 data 对象")
		}
		embedding, ok := data["embedding"]
		if !ok {
			return nil, nil, fmt.Errorf("豆包图片嵌入响应的 data 缺少 embedding")
		}
		values = embedding
	default:
		return nil, nil, fmt.Errorf("未知的图片嵌入原生协议: %s", protocol)
	}
	vector, err := imageEmbeddingVector(values)
	if err != nil {
		return nil, nil, err
	}
	// 百炼与豆包的 usage 只报 total_tokens；Python 把 prompt 记为 total、completion 记为 0
	return vector, imageEmbeddingUsage(document["usage"], true), nil
}

// imageEmbeddingUsage 解析图片嵌入响应 usage；mapping 存在即返回记录，字段缺失按 0
// promptFromTotal 对齐百炼/豆包原生协议，兼容 OpenAI 的 /embeddings 则按 prompt_tokens
func imageEmbeddingUsage(raw any, promptFromTotal bool) *schema.TokenUsage {
	usage, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	total := nonNegativeImageUsageInt(usage["total_tokens"])
	if promptFromTotal {
		return &schema.TokenUsage{PromptTokens: total, TotalTokens: total}
	}
	return &schema.TokenUsage{
		PromptTokens:     nonNegativeImageUsageInt(usage["prompt_tokens"]),
		CompletionTokens: nonNegativeImageUsageInt(usage["completion_tokens"]),
		TotalTokens:      total,
	}
}

// nonNegativeImageUsageInt 对齐 Python _as_non_negative_int：bool、负数、非整数都按 0
func nonNegativeImageUsageInt(value any) int {
	switch number := value.(type) {
	case int:
		if number >= 0 {
			return number
		}
	case int64:
		if number >= 0 {
			return int(number)
		}
	case float64:
		if number >= 0 && number == math.Trunc(number) && number <= float64(int(^uint(0)>>1)) {
			return int(number)
		}
	}
	return 0
}

// imageEmbeddingVector 校验向量非空且全部为有限数值
func imageEmbeddingVector(value any) ([]float64, error) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("图片嵌入响应的向量为空或不是列表")
	}
	vector := make([]float64, len(items))
	for index, item := range items {
		number, ok := item.(float64)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, fmt.Errorf("图片嵌入响应的向量包含非有限数值")
		}
		vector[index] = number
	}
	return vector, nil
}

// validateImageEmbeddingTransport 图片二进制只通过 HTTPS 或明确的本机回环 HTTP 地址发送
func validateImageEmbeddingTransport(baseURL string) error {
	parsed, ok := parseImageEmbeddingBaseURL(baseURL)
	if !ok {
		return errImageEmbeddingTransport
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		if parsed.Hostname() != "" {
			return nil
		}
	case "http":
		host := strings.ToLower(parsed.Hostname())
		if host == "localhost" {
			return nil
		}
		if address := net.ParseIP(host); address != nil && address.IsLoopback() {
			return nil
		}
	}
	return errImageEmbeddingTransport
}

// splitOpenAIRequestOverrides 拆分单次请求的头、查询参数和请求体扩展字段
func splitOpenAIRequestOverrides(extraParams map[string]any) (imageRequestOverrides, error) {
	raw := imageExtraParams(extraParams)
	headers, err := popImageEmbeddingMapping(raw, "headers")
	if err != nil {
		return imageRequestOverrides{}, err
	}
	query, err := popImageEmbeddingMapping(raw, "query")
	if err != nil {
		return imageRequestOverrides{}, err
	}
	body, err := popImageEmbeddingMapping(raw, "body")
	if err != nil {
		return imageRequestOverrides{}, err
	}
	overrides := imageRequestOverrides{
		Headers: make(map[string]string, len(headers)),
		Query:   query,
		Body:    body,
	}
	for key, value := range headers {
		overrides.Headers[pythonString(key)] = pythonString(value)
	}
	for key, value := range raw {
		overrides.Body[key] = value
	}
	return overrides, nil
}

// imageExtraParams 复制 extra_params 并保证可写
func imageExtraParams(extraParams map[string]any) map[string]any {
	copied := make(map[string]any, len(extraParams))
	for key, value := range extraParams {
		copied[key] = value
	}
	return copied
}

// popImageEmbeddingMapping 取出映射型扩展字段；非映射报错，缺失或空值视为空映射
func popImageEmbeddingMapping(extra map[string]any, key string) (map[string]any, error) {
	value, ok := extra[key]
	if !ok {
		return map[string]any{}, nil
	}
	delete(extra, key)
	if value == nil {
		return map[string]any{}, nil
	}
	mapping, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("extra_params.%s 必须是对象", key)
	}
	copied := make(map[string]any, len(mapping))
	for name, item := range mapping {
		copied[name] = item
	}
	return copied, nil
}

// buildImageEmbeddingDataURI 编码图片为 data URI
func buildImageEmbeddingDataURI(imageBytes []byte, mimeType string) string {
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(imageBytes)
}

// imageEmbeddingHash 对齐 json.dumps(sort_keys=True, ensure_ascii=False, separators=(",", ":")) 后取 sha256
func imageEmbeddingHash(payload map[string]any) (string, error) {
	encoded, err := marshalCompactSortedJSON(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// pythonString 对齐 Python str(value) 的常见取值
func pythonString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

// hasImageEmbeddingPath 判断规范化路径是否命中协议允许的基路径
func hasImageEmbeddingPath(allowed map[string]struct{}, path string) bool {
	_, ok := allowed[path]
	return ok
}

// parseImageEmbeddingBaseURL 规范化并解析 Provider 基础地址
func parseImageEmbeddingBaseURL(baseURL string) (*url.URL, bool) {
	parsed, err := url.Parse(normalizeOpenAIBaseURL(baseURL))
	if err != nil || parsed == nil {
		return nil, false
	}
	return parsed, true
}
