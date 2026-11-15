package llm

// 数据库模型档案与 model_config.toml 的 Provider 对齐
//
// 档案只保存 provider 字段（通常是 client_type，也可能是 api_providers[].name）、
// base_url 与 model_name，没有 provider 显示名、鉴权方式与默认头。这里按
// provider 名精确匹配，或按 base_url 唯一匹配 api_providers，补上 ProviderName
// 与 Provider 级默认值；匹配不到或存在歧义时保持原样，不猜测。

import (
	"strings"

	"MaiGo/internal/config/maibot"
	"MaiGo/internal/config/model_profiles"
)

// providerForProfile 为数据库档案寻找对应的 api_provider
// 先按 provider 名精确匹配，再按 base_url 唯一匹配；多个同 URL Provider 视为歧义
func (r *Router) providerForProfile(profile modelprofiles.Profile) (maibot.APIProvider, bool) {
	if r == nil || r.file == nil {
		return maibot.APIProvider{}, false
	}
	name := trimTask(profile.Provider)
	if name != "" {
		for _, provider := range r.file.ApiProviders {
			if trimTask(provider.Name) == name {
				return provider, true
			}
		}
	}
	baseURL := normalizedProviderBaseURL(profile.BaseURL)
	if baseURL == "" {
		return maibot.APIProvider{}, false
	}
	var matched maibot.APIProvider
	count := 0
	for _, provider := range r.file.ApiProviders {
		if normalizedProviderBaseURL(provider.BaseUrl) != baseURL {
			continue
		}
		if count == 0 {
			matched = provider
		}
		count++
	}
	if count == 1 {
		return matched, true
	}
	return maibot.APIProvider{}, false
}

// applyProfileProviderDefaults 把 Provider 级配置补进档案配置
func applyProfileProviderDefaults(cfg ModelConfig, provider maibot.APIProvider) ModelConfig {
	cfg.ProviderName = trimTask(provider.Name)
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = trimTask(provider.BaseUrl)
	}
	authType := strings.ToLower(trimTask(provider.AuthType))
	if authType == "" {
		authType = authTypeBearer
	}
	cfg.AuthType = authType
	cfg.AuthHeaderName = providerAuthHeaderName(provider.AuthHeaderName)
	cfg.AuthHeaderPrefix = providerAuthHeaderPrefixValue(provider.AuthHeaderPrefix, provider.AuthHeaderPrefixSet)
	cfg.AuthHeaderPrefixSet = provider.AuthHeaderPrefixSet
	cfg.AuthQueryName = providerAuthQueryName(provider.AuthQueryName)
	cfg.DefaultHeaders = cloneStringMap(provider.DefaultHeaders)
	cfg.DefaultQuery = cloneStringMap(provider.DefaultQuery)
	cfg.Organization = strings.TrimSpace(stringFromAny(provider.Organization))
	cfg.Project = strings.TrimSpace(stringFromAny(provider.Project))
	cfg.ReasoningParseMode = providerParseMode(provider.ReasoningParseMode)
	cfg.ToolArgumentParseMode = providerParseMode(provider.ToolArgumentParseMode)
	cfg.MaxRetry = provider.MaxRetry
	if !provider.MaxRetrySet && cfg.MaxRetry == 0 {
		cfg.MaxRetry = defaultProviderMaxRetry
	}
	cfg.MaxRetrySet = provider.MaxRetrySet
	cfg.RetryInterval = secondsOrDefault(provider.RetryInterval, defaultRetryInterval)
	cfg.Timeout = secondsOrDefault(provider.Timeout, defaultProviderTimeout)
	cfg.ModelListEndpoint = providerModelListEndpoint(provider.ModelListEndpoint)
	return cfg
}

// normalizedProviderBaseURL 仅用于 Provider 匹配：忽略大小写与末尾斜杠
func normalizedProviderBaseURL(raw string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(raw), "/"))
}
