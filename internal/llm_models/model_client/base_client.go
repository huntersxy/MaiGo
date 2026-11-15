package model_client

// 模型客户端工厂与连通性测试。
//
// 对齐 MaiBot src/llm_models/model_client/base_client.py：按 client_type 选择
// 具体客户端实现（Chat Completions / OpenAI Responses / Gemini / 插件 Provider），
// 并负责把客户端实例交给上层。用量记账等外部能力通过 ports 注入。

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/openai/openai-go/v3/responses"
)

// handshakeTimeout 由 contract.HandshakeTimeout 提供，见 auth_bridge.go

// 多任务路由见 Router；NewClient 按 ClientType 分流（Chat Completions / OpenAI Responses / Gemini REST / 插件 Provider）。
// 平台必须性差异与未对等面以 MaiBot 原版 llm_models 实现为准。

// NewClient 按 client_type 创建 AgenticModel；不做进程级缓存
func NewClient(cfg ModelConfig) (model.AgenticModel, error) {
	client, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	// 在真实 Provider 客户端外层记录一次实际调用；上层 failover 不重复写统计
	return wrapUsageRecordingModel(client, cfg), nil
}

func newClient(cfg ModelConfig) (model.AgenticModel, error) {
	if isGeminiClientType(cfg.ClientType) {
		return NewGeminiClient(cfg)
	}
	if isChatCompletionsClientType(cfg.ClientType) {
		// 对齐 MaiBot 默认 client_type=openai：Chat Completions，而不是 Responses API
		return NewChatCompletionsClient(cfg)
	}
	if !isOpenAICompatClientType(cfg.ClientType) {
		// 非内置 client_type 归插件 Provider
		return NewPluginModel(cfg)
	}
	return newOpenAIResponsesClient(cfg)
}

func newOpenAIResponsesClient(cfg ModelConfig) (model.AgenticModel, error) {
	modelName := strings.TrimSpace(cfg.Model)
	baseURL := normalizeOpenAIBaseURL(cfg.BaseURL)
	if baseURL == "" || modelName == "" {
		return nil, fmt.Errorf("主模型配置不完整")
	}
	if strings.TrimSpace(cfg.APIKey) == "" && !strings.EqualFold(strings.TrimSpace(cfg.AuthType), authTypeNone) {
		// 对齐 MaiBot APIProvider 校验：只有 auth_type=none 允许空密钥
		return nil, fmt.Errorf("主模型配置不完整")
	}
	extra, err := splitResponsesExtraParams(cfg.ExtraParams)
	if err != nil {
		return nil, err
	}
	auth := buildOpenAICompatAuth(cfg)
	// extra_params.headers/query 覆盖 Provider 默认头与默认查询参数
	for key, value := range extra.Headers {
		auth.Headers[canonicalizeAuthorizationHeader(key)] = value
	}
	for key, value := range extra.Query {
		auth.Query[key] = value
	}
	if _, ok := auth.Headers[authorizationHeader]; ok {
		auth.OmitAuthorization = false
	}

	extraFields := map[string]any{}
	if thinking := strings.TrimSpace(cfg.ThinkingType); thinking != "" {
		extraFields["thinking"] = map[string]any{"type": thinking}
	}
	var reasoning *responses.ReasoningParam
	if effort := strings.TrimSpace(cfg.ReasoningEffort); effort != "" {
		reasoning = &responses.ReasoningParam{Effort: responses.ReasoningEffort(effort)}
	}

	// 重试统一由调度层按 api_providers.max_retry 控制，关闭 SDK 内部重试（对齐 MaiBot max_retries=0）
	httpClient := newLLMHTTPClient(0)
	httpClient.Transport = newProviderTransport(httpClient.Transport, auth)
	noRetry := 0
	// 对齐 MaiBot Responses 请求固定 store=false
	storeFalse := false
	responsesCfg := &agenticopenai.ResponsesConfig{
		BaseURL:     baseURL,
		APIKey:      auth.APIKey,
		Model:       modelName,
		HTTPClient:  httpClient,
		ExtraFields: extraFields,
		Reasoning:   reasoning,
		Store:       &storeFalse,
		MaxRetries:  &noRetry,
	}
	if cfg.Timeout > 0 {
		timeout := cfg.Timeout
		responsesCfg.Timeout = &timeout
	}
	client, err := agenticopenai.NewResponsesModel(context.Background(), responsesCfg)
	if err != nil {
		return nil, fmt.Errorf("创建主模型失败: %w", err)
	}
	// Responses API 的 text.format 与 extra_params 只能按请求注入，包装后所有调用点统一生效
	return NewResponsesResponseFormatModel(client, cfg, baseURL, extra, extraFields), nil
}

// Test 用极短请求验证模型连接；仅用于后台测试，不参与机器人运行
func Test(ctx context.Context, cfg ModelConfig) error {
	client, err := NewClient(cfg)
	if err != nil {
		return err
	}
	_, err = client.Generate(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("ping")})
	if err != nil {
		return fmt.Errorf("模型测试失败: %w", err)
	}
	return nil
}
