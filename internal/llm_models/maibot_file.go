package llm

import (
	"fmt"
	"strings"
	"time"

	"MaiGo/internal/config/maibot"
)

// 当前 Go 层实现 OpenAI Chat Completions、OpenAI Responses、Gemini REST（含流式/语音/thought_signature/Google Search）
// 与插件 Provider 客户端。
// Gemini 音频/视频/文件输入已按 inlineData/fileData 投影，URL 媒体显式报错（context_gemini_request.go）；
// 流中途错误由 failover / usage 层整请求重放（stream_replay.go）
// 413 / data URI 图片压缩重试已在 Chat Completions 客户端与失败切换层实现
// MaiBot utils_model.LLMOrchestrator 的职责在本包分散实现，无同名类型：
//   - 任务 → 模型列表：TaskModelList（maibot_file.go）
//   - 选择策略：SelectionStrategy（selection.go，balance/random/sequential/first）
//   - 负载均衡与 penalty：ModelUsage + router_failover.go 的请求级 failover
//   - 入口：Router.NewClientForTaskWithFailover（按 TaskName 分发，覆盖 text/image/audio/embedding）

var supportedMaiBotClientTypes = map[string]bool{
	"":                 true,
	"openai_responses": true,
	"openai":           true,
	"gemini":           true,
	"plugin":           true,
}

// ModelConfigFromMaiBotFile 从 model_config.toml 的 models + api_providers 构建客户端配置
func ModelConfigFromMaiBotFile(file maibot.ModelConfigFile, modelName string) (ModelConfig, error) {
	modelName = trimTask(modelName)
	if modelName == "" {
		return ModelConfig{}, fmt.Errorf("模型名称不能为空")
	}
	info, ok := findModelInfo(file.Models, modelName)
	if !ok {
		return ModelConfig{}, fmt.Errorf("未找到名为 '%s' 的模型", modelName)
	}
	providerName := trimTask(info.ApiProvider)
	if providerName == "" {
		return ModelConfig{}, fmt.Errorf("模型 '%s' 未配置 API 提供商", modelName)
	}
	provider, ok := findAPIProvider(file.ApiProviders, providerName)
	if !ok {
		return ModelConfig{}, fmt.Errorf("未找到名为 '%s' 的 API 提供商", providerName)
	}
	if errors := maibot.ProviderConfigurationErrors(providerName, provider); len(errors) > 0 {
		return ModelConfig{}, fmt.Errorf("供应商 %s 配置无效：%s", providerName, strings.Join(errors, "；"))
	}
	clientType := normalizeClientType(provider.ClientType)
	if !supportedMaiBotClientTypes[clientType] && !pluginProviderAvailable(clientType) {
		return ModelConfig{}, fmt.Errorf("客户端类型 '%s' 未注册，请检查 model_config 的 api_providers.client_type", provider.ClientType)
	}
	apiKey := trimTask(provider.ApiKey)
	baseURL := trimTask(provider.BaseUrl)
	modelID := trimTask(info.ModelIdentifier)
	authType := strings.ToLower(trimTask(provider.AuthType))
	if authType == "" {
		authType = authTypeBearer
	}
	if modelID == "" {
		return ModelConfig{}, fmt.Errorf("模型 '%s' 的 API 提供商配置不完整", modelName)
	}
	maxRetry := provider.MaxRetry
	if !provider.MaxRetrySet && maxRetry == 0 {
		maxRetry = defaultProviderMaxRetry
	}
	cfg := ModelConfig{
		ClientType:            clientType,
		ProviderName:          providerName,
		APIKey:                apiKey,
		BaseURL:               baseURL,
		Model:                 modelID,
		ExtraParams:           cloneAnyMap(info.ExtraParams),
		MaxRetry:              maxRetry,
		MaxRetrySet:           provider.MaxRetrySet,
		RetryInterval:         secondsOrDefault(provider.RetryInterval, defaultRetryInterval),
		Timeout:               secondsOrDefault(provider.Timeout, defaultProviderTimeout),
		AuthType:              authType,
		AuthHeaderName:        providerAuthHeaderName(provider.AuthHeaderName),
		AuthHeaderPrefix:      providerAuthHeaderPrefixValue(provider.AuthHeaderPrefix, provider.AuthHeaderPrefixSet),
		AuthHeaderPrefixSet:   provider.AuthHeaderPrefixSet,
		AuthQueryName:         providerAuthQueryName(provider.AuthQueryName),
		DefaultHeaders:        cloneStringMap(provider.DefaultHeaders),
		DefaultQuery:          cloneStringMap(provider.DefaultQuery),
		Organization:          strings.TrimSpace(stringFromAny(provider.Organization)),
		Project:               strings.TrimSpace(stringFromAny(provider.Project)),
		ReasoningParseMode:    providerParseMode(provider.ReasoningParseMode),
		ToolArgumentParseMode: providerParseMode(provider.ToolArgumentParseMode),
		ModelDisplayName:      trimTask(info.Name),
		ModelType:             trimTask(info.ModelType),
		Visual:                info.Visual,
		ForceStreamMode:       info.ForceStreamMode,
		SendTemperature:       info.SendTemperature,
		SendTemperatureSet:    info.SendTemperatureSet,
		ModelTemperature:      info.Temperature,
		ModelMaxTokens:        info.MaxTokens,
		MaxImages:             info.MaxImages,
		ImageMaxSide:          info.ImageMaxSide,
		ImageMinSide:          info.ImageMinSide,
		ModelListEndpoint:     providerModelListEndpoint(provider.ModelListEndpoint),
		PriceIn:               info.PriceIn,
		CachePriceIn:          info.CachePriceIn,
		PriceOut:              info.PriceOut,
		PricePeriods:          modelPricePeriods(info.PricePeriods),
	}
	return cfg, nil
}

// modelPricePeriods 把配置价格时段转换为 llm 层结构
func modelPricePeriods(periods []maibot.ModelPricePeriod) []ModelPricePeriod {
	if len(periods) == 0 {
		return nil
	}
	out := make([]ModelPricePeriod, 0, len(periods))
	for _, period := range periods {
		out = append(out, ModelPricePeriod{
			StartTime:    period.StartTime,
			EndTime:      period.EndTime,
			PriceIn:      period.PriceIn,
			PriceOut:     period.PriceOut,
			CachePriceIn: period.CachePriceIn,
		})
	}
	return out
}

// secondsOrDefault 把秒配置转换为时长；未配置（0）时使用 MaiBot 默认值
// providerAuthHeaderPrefixValue 显式配置保留原值（含空串），未配置回退 Bearer
func providerAuthHeaderPrefixValue(prefix string, set bool) string {
	if set {
		return strings.TrimSpace(prefix)
	}
	return providerAuthHeaderPrefix(prefix)
}

func secondsOrDefault(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func findModelInfo(models []maibot.ModelInfo, name string) (maibot.ModelInfo, bool) {
	for _, m := range models {
		if trimTask(m.Name) == name {
			return m, true
		}
	}
	return maibot.ModelInfo{}, false
}

func findAPIProvider(providers []maibot.APIProvider, name string) (maibot.APIProvider, bool) {
	for _, p := range providers {
		if trimTask(p.Name) == name {
			return p, true
		}
	}
	return maibot.APIProvider{}, false
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func stringFromAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func floatFromAny(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	default:
		return 0, false
	}
}
