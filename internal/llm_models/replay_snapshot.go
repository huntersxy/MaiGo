package llm

// 失败请求快照的重放构造
// 对齐 MaiBot scripts/replay_llm_request.py 的最小可用子集：
//   - 支持 chat.completions、Responses API 与 Gemini generateContent 的文本/图片请求重建
//   - 快照里的 request_items 是唯一消息来源，凭据只从环境变量读取（快照已脱敏）
//   - 插件 Provider、嵌入、音频等协议没有可重放的 HTTP 端点，返回真实错误而不是伪造请求

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ReplaySnapshot 从快照文件构造可直接发送的 HTTP 请求；apiKey 为空时读 MAIBOT_REPLAY_API_KEY
func ReplaySnapshot(snapshotPath, apiKey string) (*http.Request, error) {
	raw, err := os.ReadFile(snapshotPath)
	if err != nil {
		return nil, fmt.Errorf("读取快照失败: %w", err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, fmt.Errorf("解析快照失败: %w", err)
	}
	return BuildReplayRequest(snapshot, snapshotPath, apiKey)
}

// BuildReplayRequest 用已解析的快照与快照路径构造请求
func BuildReplayRequest(snapshot map[string]any, snapshotPath, apiKey string) (*http.Request, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("快照内容为空")
	}
	apiProvider, _ := snapshot["api_provider"].(map[string]any)
	modelInfo, _ := snapshot["model_info"].(map[string]any)
	requestMeta, _ := snapshot["request"].(map[string]any)
	clientType := firstReplayString(metadataString(snapshot, "client_type"), replayString(apiProvider, "client_type"))
	operation := replayString(requestMeta, "operation")
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("MAIBOT_REPLAY_API_KEY"))
	}
	items, err := replayContextItems(snapshot, snapshotPath)
	if err != nil {
		return nil, err
	}
	if isGeminiClientType(clientType) || strings.Contains(operation, "gemini") {
		return buildGeminiReplayRequest(snapshot, apiProvider, modelInfo, items, apiKey)
	}
	if clientType == "openai_responses" || strings.Contains(operation, "responses") {
		return buildResponsesReplayRequest(snapshot, apiProvider, modelInfo, items, apiKey)
	}
	if strings.Contains(operation, "plugin") || strings.Contains(operation, "chat") {
		return buildChatReplayRequest(snapshot, apiProvider, modelInfo, items, apiKey, operation)
	}
	if isChatCompletionsClientType(clientType) && operation == "" {
		return buildChatReplayRequest(snapshot, apiProvider, modelInfo, items, apiKey, operation)
	}
	return nil, fmt.Errorf("快照 operation=%s client_type=%s 没有可重放的 HTTP 协议", operation, clientType)
}

func replayContextItems(snapshot map[string]any, snapshotPath string) ([]ContextItem, error) {
	rawItems, _ := snapshot["request_items"].([]any)
	if len(rawItems) == 0 {
		return nil, fmt.Errorf("快照没有 request_items，无法重建请求")
	}
	baseDir := filepath.Dir(snapshotPath)
	hydrated := make([]any, 0, len(rawItems))
	for _, entry := range rawItems {
		item, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("request_items 元素不是对象")
		}
		if err := hydrateReplayItem(item, baseDir); err != nil {
			return nil, err
		}
		hydrated = append(hydrated, item)
	}
	items, err := DeserializeContextItemsSnapshot(hydrated)
	if err != nil {
		return nil, fmt.Errorf("反序列化 request_items 失败: %w", err)
	}
	return items, nil
}

// hydrateReplayItem 把快照里外置的图片读回 base64，保持序列化结构不变
func hydrateReplayItem(item map[string]any, baseDir string) error {
	parts, _ := item["parts"].([]any)
	for _, entry := range parts {
		part, ok := entry.(map[string]any)
		if !ok || replayString(part, "type") != "image" {
			continue
		}
		if replayString(part, "image_base64") != "" {
			continue
		}
		imagePath := replayString(part, "image_path")
		if imagePath == "" {
			return fmt.Errorf("图片 part 缺少 image_base64 与 image_path")
		}
		resolved := imagePath
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(baseDir, imagePath)
		}
		raw, err := os.ReadFile(resolved)
		if err != nil {
			return fmt.Errorf("读取快照图片失败: %w", err)
		}
		part["image_base64"] = base64.StdEncoding.EncodeToString(raw)
		delete(part, "image_path")
	}
	return nil
}

func buildChatReplayRequest(snapshot map[string]any, apiProvider, modelInfo map[string]any, items []ContextItem, apiKey, operation string) (*http.Request, error) {
	if operation != "" && !strings.Contains(operation, "chat") && !strings.Contains(operation, "plugin") {
		return nil, fmt.Errorf("快照 operation=%s 不是 chat.completions，暂不重放", operation)
	}
	body := replayBody(snapshot["provider_request"])
	delete(body, "messages")
	messages, err := buildChatMessagesFromContextItems(items, len(replayToolDefinitions(snapshot)) > 0)
	if err != nil {
		return nil, err
	}
	body["messages"] = messages
	if tools := replayToolDefinitions(snapshot); len(tools) > 0 {
		body["tools"] = tools
	}
	if model := firstReplayString(replayString(body, "model"), replayString(modelInfo, "model_identifier"), metadataString(snapshot, "model_name")); model != "" {
		body["model"] = model
	}
	if _, ok := body["stream"]; !ok {
		body["stream"] = false
	}
	baseURL := normalizeOpenAIBaseURL(replayString(apiProvider, "base_url"))
	if baseURL == "" {
		return nil, fmt.Errorf("快照 api_provider.base_url 为空，无法构造重放地址")
	}
	endpoint := baseURL + "/chat/completions"
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("序列化重放请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, strings.NewReader(string(encoded)))
	if err != nil {
		return nil, fmt.Errorf("创建重放请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	applyReplayHeaders(req, apiProvider, apiKey)
	return req, nil
}

func buildGeminiReplayRequest(snapshot map[string]any, apiProvider, modelInfo map[string]any, items []ContextItem, apiKey string) (*http.Request, error) {
	contents, systemText, err := buildGeminiContentsFromContextItems(items)
	if err != nil {
		return nil, err
	}
	body := replayBody(snapshot["provider_request"])
	delete(body, "contents")
	body["contents"] = contents
	if systemText != "" {
		body["systemInstruction"] = map[string]any{"parts": []map[string]any{{"text": systemText}}, "role": "user"}
	}
	model := firstReplayString(replayString(modelInfo, "model_identifier"), metadataString(snapshot, "model_name"))
	if model == "" {
		return nil, fmt.Errorf("快照 model_identifier 为空，无法构造 Gemini 重放地址")
	}
	base := strings.TrimRight(replayString(apiProvider, "base_url"), "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	method := "generateContent"
	if stream, _ := body["stream"].(bool); stream {
		method = "streamGenerateContent"
	}
	endpoint := fmt.Sprintf("%s/models/%s:%s", base, url.PathEscape(model), method)
	query := url.Values{}
	if apiKey != "" {
		query.Set("key", apiKey)
	}
	if method == "streamGenerateContent" {
		query.Set("alt", "sse")
	}
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("序列化 Gemini 重放请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, strings.NewReader(string(encoded)))
	if err != nil {
		return nil, fmt.Errorf("创建 Gemini 重放请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if method == "streamGenerateContent" {
		req.Header.Set("Accept", "text/event-stream")
	}
	return req, nil
}

func applyReplayHeaders(req *http.Request, apiProvider map[string]any, apiKey string) {
	for key, value := range replayStringMap(apiProvider, "default_headers") {
		req.Header.Set(key, value)
	}
	query := req.URL.Query()
	for key, value := range replayStringMap(apiProvider, "default_query") {
		query.Set(key, value)
	}
	authType := strings.TrimSpace(replayString(apiProvider, "auth_type"))
	if authType == "" {
		authType = authTypeBearer
	}
	headerName := replayString(apiProvider, "auth_header_name")
	if headerName == "" {
		headerName = authorizationHeader
	}
	prefix := replayString(apiProvider, "auth_header_prefix")
	// MaiBot APIProvider 默认 auth_header_prefix=Bearer；显式空串表示裸 key
	if _, present := apiProvider["auth_header_prefix"]; !present && (authType == authTypeBearer || authType == authTypeHeader) {
		prefix = "Bearer"
	}
	switch authType {
	case authTypeBearer:
		if apiKey != "" {
			req.Header.Set(headerName, buildAuthHeaderValue(prefix, apiKey))
		}
	case authTypeHeader:
		if apiKey != "" {
			req.Header.Set(headerName, buildAuthHeaderValue(prefix, apiKey))
		}
	case authTypeQuery:
		name := replayString(apiProvider, "auth_query_name")
		if name == "" {
			name = "api_key"
		}
		if apiKey != "" {
			query.Set(name, apiKey)
		}
	case authTypeNone:
	default:
		if apiKey != "" {
			req.Header.Set(authorizationHeader, "Bearer "+apiKey)
		}
	}
	if len(query) > 0 {
		req.URL.RawQuery = query.Encode()
	}
}

func replayBody(raw any) map[string]any {
	payload, ok := raw.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{}
	}
	var clone map[string]any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return map[string]any{}
	}
	if clone == nil {
		return map[string]any{}
	}
	return clone
}

func replayToolDefinitions(snapshot map[string]any) []any {
	definitions, _ := snapshot["tool_definitions"].([]any)
	return definitions
}

func metadataString(snapshot map[string]any, key string) string {
	metadata, _ := snapshot["metadata"].(map[string]any)
	return replayString(metadata, key)
}

func replayString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func replayStringMap(payload map[string]any, key string) map[string]string {
	out := map[string]string{}
	raw, _ := payload[key].(map[string]any)
	for name, value := range raw {
		if text, ok := value.(string); ok {
			out[name] = text
		}
	}
	return out
}

func firstReplayString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
