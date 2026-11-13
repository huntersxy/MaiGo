package mcp

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

const (
	// DefaultHTTPTimeoutSeconds 对齐 MaiBot MCPServerItemConfig.http_timeout_seconds 默认值
	DefaultHTTPTimeoutSeconds = 30.0
	// DefaultReadTimeoutSeconds 对齐 MaiBot MCPServerItemConfig.read_timeout_seconds 默认值
	DefaultReadTimeoutSeconds = 300.0
	// DefaultClientName 对齐 MaiBot MCPClientRuntimeConfig.client_name 默认值
	DefaultClientName = "MaiBot"
	// DefaultClientVersion 对齐 MaiBot MCPClientRuntimeConfig.client_version 默认值
	DefaultClientVersion = "1.0.0"
	// DefaultSamplingTaskName 对齐 MaiBot sampling.task_name 默认值
	DefaultSamplingTaskName = "planner"
)

// AuthorizationConfig 远程 MCP HTTP 认证配置
type AuthorizationConfig struct {
	Mode        string `json:"mode,omitempty"`
	BearerToken string `json:"bearer_token,omitempty"`
}

// RootConfig MCP Root 声明，对应 MaiBot MCPRootRuntimeConfig
type RootConfig struct {
	URI  string `json:"uri"`
	Name string `json:"name,omitempty"`
}

// ClientConfig MCP 客户端宿主能力配置，对应 MaiBot MCPClientRuntimeConfig
type ClientConfig struct {
	ClientName           string       `json:"client_name,omitempty"`
	ClientVersion        string       `json:"client_version,omitempty"`
	EnableRoots          bool         `json:"enable_roots,omitempty"`
	Roots                []RootConfig `json:"roots,omitempty"`
	EnableSampling       bool         `json:"enable_sampling,omitempty"`
	SamplingTaskName     string       `json:"sampling_task_name,omitempty"`
	SamplingToolSupport  bool         `json:"sampling_tool_support,omitempty"`
	EnableElicitation    bool         `json:"enable_elicitation,omitempty"`
	ElicitationAllowForm bool         `json:"elicitation_allow_form,omitempty"`
	ElicitationAllowURL  bool         `json:"elicitation_allow_url,omitempty"`
}

// Normalize 补齐默认值并裁剪 Roots，对齐 build_mcp_client_runtime_config
func (c ClientConfig) Normalize() ClientConfig {
	out := c
	out.ClientName = strings.TrimSpace(out.ClientName)
	if out.ClientName == "" {
		out.ClientName = DefaultClientName
	}
	out.ClientVersion = strings.TrimSpace(out.ClientVersion)
	if out.ClientVersion == "" {
		out.ClientVersion = DefaultClientVersion
	}
	out.SamplingTaskName = strings.TrimSpace(out.SamplingTaskName)
	if out.SamplingTaskName == "" {
		out.SamplingTaskName = DefaultSamplingTaskName
	}
	roots := make([]RootConfig, 0, len(out.Roots))
	for _, root := range out.Roots {
		uri := strings.TrimSpace(root.URI)
		if uri == "" {
			continue
		}
		roots = append(roots, RootConfig{URI: uri, Name: strings.TrimSpace(root.Name)})
	}
	out.Roots = roots
	out.EnableRoots = out.EnableRoots && len(roots) > 0
	// Go 生成配置无法区分「未写 allow_form」与显式 false，按 MaiBot 默认值补 form=true
	if out.EnableElicitation && !out.ElicitationAllowForm && !out.ElicitationAllowURL {
		out.ElicitationAllowForm = true
	}
	return out
}

// TransportType 返回有效传输类型，对齐 MCPServerRuntimeConfig.transport_type
func (c ServerConfig) TransportType() string {
	switch normalizeTransport(c.Type) {
	case "stdio":
		if strings.TrimSpace(c.Command) != "" {
			return "stdio"
		}
	case "streamable_http":
		if strings.TrimSpace(c.URL) != "" {
			return "streamable_http"
		}
	case "sse":
		if strings.TrimSpace(c.URL) != "" {
			return "sse"
		}
	}
	return "unknown"
}

// HTTPTimeout 返回 HTTP 连接超时，未配置时回退 30s
func (c ServerConfig) HTTPTimeout() time.Duration {
	seconds := c.HTTPTimeoutSeconds
	if seconds <= 0 {
		seconds = DefaultHTTPTimeoutSeconds
	}
	return time.Duration(seconds * float64(time.Second))
}

// ReadTimeout 返回读取超时，未配置时回退 300s
func (c ServerConfig) ReadTimeout() time.Duration {
	seconds := c.ReadTimeoutSeconds
	if seconds <= 0 {
		seconds = DefaultReadTimeoutSeconds
	}
	return time.Duration(seconds * float64(time.Second))
}

// BuildHTTPHeaders 构建远程 HTTP 请求头，Bearer 模式覆盖同名头，对齐 build_http_headers
func (c ServerConfig) BuildHTTPHeaders() map[string]string {
	headers := make(map[string]string, len(c.CustomHeaders)+1)
	for key, value := range c.CustomHeaders {
		headers[key] = value
	}
	mode := strings.ToLower(strings.TrimSpace(c.Authorization.Mode))
	if mode == "bearer" {
		token := strings.TrimSpace(c.Authorization.BearerToken)
		if token != "" {
			headers["Authorization"] = "Bearer " + token
		}
	}
	return headers
}

// envMap 把 KEY=VALUE 形式的 Env 解析成键值对，后出现的键覆盖先出现的
func (c ServerConfig) envMap() map[string]string {
	out := make(map[string]string, len(c.Env))
	for _, item := range c.Env {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = value
	}
	return out
}

type signaturePair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type signatureAuthorization struct {
	Mode        string `json:"mode"`
	BearerToken string `json:"bearer_token"`
}

type signatureServer struct {
	Name               string                 `json:"name"`
	Transport          string                 `json:"transport"`
	Command            string                 `json:"command"`
	Args               []string               `json:"args"`
	Env                []signaturePair        `json:"env"`
	URL                string                 `json:"url"`
	Headers            []signaturePair        `json:"headers"`
	ToolNames          []string               `json:"tool_names"`
	HTTPTimeoutSeconds float64                `json:"http_timeout_seconds"`
	ReadTimeoutSeconds float64                `json:"read_timeout_seconds"`
	Authorization      signatureAuthorization `json:"authorization"`
}

type signatureRoots struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

type signatureSampling struct {
	TaskName    string `json:"task_name"`
	ToolSupport bool   `json:"tool_support"`
}

type signatureClient struct {
	ClientName    string             `json:"client_name"`
	ClientVersion string             `json:"client_version"`
	Roots         []signatureRoots   `json:"roots"`
	Sampling      *signatureSampling `json:"sampling"`
}

type signaturePayload struct {
	Servers []signatureServer `json:"servers"`
	Client  *signatureClient  `json:"client"`
}

// ConfigSignature 生成有效运行时配置签名，对齐 MCPService._build_config_signature
//
// 只包含启用中的服务器；停用草稿变化不会触发连接重建
func ConfigSignature(servers []ServerConfig, client ClientConfig) string {
	client = client.Normalize()
	enabled := make([]signatureServer, 0, len(servers))
	for _, cfg := range servers {
		if !cfg.Enabled {
			continue
		}
		cfg.Type = normalizeTransport(cfg.Type)
		enabled = append(enabled, signatureServer{
			Name:               strings.TrimSpace(cfg.Name),
			Transport:          cfg.Type,
			Command:            strings.TrimSpace(cfg.Command),
			Args:               append([]string(nil), cfg.Args...),
			Env:                sortedSignaturePairs(cfg.envMap()),
			URL:                strings.TrimSpace(cfg.URL),
			Headers:            sortedSignaturePairs(cfg.CustomHeaders),
			ToolNames:          normalizedSignatureToolNames(cfg.ToolNameList),
			HTTPTimeoutSeconds: effectiveSeconds(cfg.HTTPTimeoutSeconds, DefaultHTTPTimeoutSeconds),
			ReadTimeoutSeconds: effectiveSeconds(cfg.ReadTimeoutSeconds, DefaultReadTimeoutSeconds),
			Authorization: signatureAuthorization{
				Mode:        strings.ToLower(strings.TrimSpace(cfg.Authorization.Mode)),
				BearerToken: strings.TrimSpace(cfg.Authorization.BearerToken),
			},
		})
	}

	payload := signaturePayload{Servers: enabled}
	if len(enabled) > 0 {
		sig := &signatureClient{
			ClientName:    client.ClientName,
			ClientVersion: client.ClientVersion,
			Roots:         []signatureRoots{},
		}
		if client.EnableRoots {
			for _, root := range client.Roots {
				sig.Roots = append(sig.Roots, signatureRoots{URI: root.URI, Name: root.Name})
			}
		}
		if client.EnableSampling {
			sig.Sampling = &signatureSampling{
				TaskName:    client.SamplingTaskName,
				ToolSupport: client.SamplingToolSupport,
			}
		}
		payload.Client = sig
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func normalizedSignatureToolNames(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func sortedSignaturePairs(values map[string]string) []signaturePair {
	pairs := make([]signaturePair, 0, len(values))
	for key, value := range values {
		pairs = append(pairs, signaturePair{Key: key, Value: value})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Key < pairs[j].Key })
	return pairs
}

func effectiveSeconds(value, fallback float64) float64 {
	if value <= 0 {
		return fallback
	}
	return value
}
