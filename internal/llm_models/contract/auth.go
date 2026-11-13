package contract

// OpenAI 兼容 Provider 的鉴权与默认请求参数
// 对齐 MaiBot src/llm_models/openai_compat.py 的 build_openai_compatible_client_config：
//   - auth_type=bearer 且头名/前缀为标准 Authorization+Bearer 时交给 SDK
//   - auth_type=header/query/none 或自定义 Bearer 前缀时清空 SDK 密钥，改用自定义头/查询参数
//   - default_headers / default_query 透传到每次请求

import (
	"net/http"
	"net/url"
	"strings"
)

const (
	AuthTypeBearer = "bearer"
	AuthTypeHeader = "header"
	AuthTypeQuery  = "query"
	AuthTypeNone   = "none"

	AuthorizationHeader = "Authorization"
)

// openAICompatAuth OpenAI 兼容客户端的鉴权与默认参数拆分结果
type openAICompatAuth struct {
	// APIKey 交给 SDK 的密钥；空串表示鉴权不经过 SDK
	APIKey string
	// Headers 默认请求头（已包含自定义鉴权头）
	Headers map[string]string
	// Query 默认查询参数（已包含 query 鉴权）
	Query map[string]string
	// OmitAuthorization 是否需要在发送前移除 SDK 生成的 Authorization 头
	OmitAuthorization bool
}

// NormalizeOpenAIBaseURL 对齐 MaiBot normalize_openai_base_url
func NormalizeOpenAIBaseURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL != "" && !strings.Contains(baseURL, "://") {
		baseURL = "http://" + baseURL
	}
	return strings.TrimRight(baseURL, "/")
}

// BuildOpenAICompatAuth 拆分 Provider 配置中的鉴权与默认参数
func BuildOpenAICompatAuth(cfg ModelConfig) openAICompatAuth {
	headers := make(map[string]string, len(cfg.DefaultHeaders)+1)
	for key, value := range cfg.DefaultHeaders {
		headers[CanonicalizeAuthorizationHeader(key)] = value
	}
	query := make(map[string]string, len(cfg.DefaultQuery)+1)
	for key, value := range cfg.DefaultQuery {
		query[key] = value
	}

	authType := strings.ToLower(strings.TrimSpace(cfg.AuthType))
	if authType == "" {
		authType = AuthTypeBearer
	}
	apiKey := cfg.APIKey
	authHeaderName := strings.TrimSpace(cfg.AuthHeaderName)
	if authHeaderName == "" {
		authHeaderName = AuthorizationHeader
	}
	// MaiBot APIProvider 默认 auth_header_prefix="Bearer"；显式空串表示裸 key
	authHeaderPrefix := strings.TrimSpace(cfg.AuthHeaderPrefix)
	if authHeaderPrefix == "" && !cfg.AuthHeaderPrefixSet {
		authHeaderPrefix = "Bearer"
	}

	switch authType {
	case AuthTypeBearer:
		if authHeaderName != AuthorizationHeader || authHeaderPrefix != "Bearer" {
			apiKey = ""
			headers[CanonicalizeAuthorizationHeader(authHeaderName)] = BuildAuthHeaderValue(authHeaderPrefix, cfg.APIKey)
		}
	case AuthTypeHeader:
		apiKey = ""
		headers[CanonicalizeAuthorizationHeader(authHeaderName)] = BuildAuthHeaderValue(authHeaderPrefix, cfg.APIKey)
	case AuthTypeQuery:
		apiKey = ""
		queryName := strings.TrimSpace(cfg.AuthQueryName)
		if queryName == "" {
			queryName = "api_key"
		}
		query[queryName] = cfg.APIKey
	case AuthTypeNone:
		apiKey = ""
	}

	_, hasAuthorization := headers[AuthorizationHeader]
	return openAICompatAuth{
		APIKey:            apiKey,
		Headers:           headers,
		Query:             query,
		OmitAuthorization: apiKey == "" && !hasAuthorization,
	}
}

// BuildAuthHeaderValue 对齐 MaiBot _build_auth_header_value；前缀为空时直接使用原始密钥
func BuildAuthHeaderValue(prefix, apiKey string) string {
	normalizedPrefix := strings.TrimSpace(prefix)
	if normalizedPrefix == "" {
		return apiKey
	}
	return normalizedPrefix + " " + apiKey
}

// CanonicalizeAuthorizationHeader 统一 Authorization 大小写，避免与 SDK 默认头重复
func CanonicalizeAuthorizationHeader(name string) string {
	if strings.EqualFold(name, AuthorizationHeader) {
		return AuthorizationHeader
	}
	return name
}

// providerTransport 给所有请求附加默认头/查询参数，并按需移除 SDK 鉴权头
type providerTransport struct {
	base              http.RoundTripper
	headers           map[string]string
	query             url.Values
	omitAuthorization bool
}

func NewProviderTransport(base http.RoundTripper, auth openAICompatAuth) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if len(auth.Query) == 0 && len(auth.Headers) == 0 && !auth.OmitAuthorization {
		return base
	}
	query := make(url.Values, len(auth.Query))
	for key, value := range auth.Query {
		query.Set(key, value)
	}
	return &providerTransport{base: base, headers: auth.Headers, query: query, omitAuthorization: auth.OmitAuthorization}
}

func (t *providerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(t.headers) > 0 || t.omitAuthorization || len(t.query) > 0 {
		clone := req.Clone(req.Context())
		if len(t.headers) > 0 {
			clone.Header = req.Header.Clone()
			for key, value := range t.headers {
				clone.Header.Set(key, value)
			}
		}
		if t.omitAuthorization {
			clone.Header.Del(AuthorizationHeader)
		}
		if len(t.query) > 0 {
			merged := clone.URL.Query()
			for key, values := range t.query {
				for _, value := range values {
					merged.Set(key, value)
				}
			}
			clone.URL.RawQuery = merged.Encode()
		}
		req = clone
	}
	return t.base.RoundTrip(req)
}
