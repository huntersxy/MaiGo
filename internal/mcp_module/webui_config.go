package mcp

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ServerConfigFromWebUI 解析 Dashboard POST /api/webui/mcp/test 请求体
func ServerConfigFromWebUI(raw map[string]any) (ServerConfig, float64, error) {
	payload, err := json.Marshal(raw)
	if err != nil {
		return ServerConfig{}, 0, fmt.Errorf("解析 MCP 配置失败: %w", err)
	}

	var body webuiMCPServerPayload
	if err := json.Unmarshal(payload, &body); err != nil {
		return ServerConfig{}, 0, fmt.Errorf("MCP 服务配置无效: %w", err)
	}

	cfg := ServerConfig{
		Name:               strings.TrimSpace(body.Name),
		Enabled:            true,
		Command:            strings.TrimSpace(body.Command),
		URL:                strings.TrimSpace(body.URL),
		Args:               append([]string(nil), body.Args...),
		ToolNameList:       append([]string(nil), body.ToolNameList...),
		HTTPTimeoutSeconds: body.HTTPTimeoutSeconds,
		ReadTimeoutSeconds: body.ReadTimeoutSeconds,
	}

	transport := strings.TrimSpace(body.Transport)
	if transport == "" {
		transport = strings.TrimSpace(body.Type)
	}
	cfg.Type = normalizeTransport(transport)

	env, err := parseWebUIEnv(body.Env)
	if err != nil {
		return ServerConfig{}, 0, err
	}
	cfg.Env = env

	headers := cloneStringMap(body.Headers)
	if len(body.CustomHeaders) > 0 {
		for key, value := range body.CustomHeaders {
			headers[key] = value
		}
	}
	if body.Authorization != nil {
		cfg.Authorization = AuthorizationConfig{
			Mode:        strings.ToLower(strings.TrimSpace(body.Authorization.Mode)),
			BearerToken: strings.TrimSpace(body.Authorization.BearerToken),
		}
	}
	cfg.CustomHeaders = headers

	if err := validateServerConfig(cfg); err != nil {
		return ServerConfig{}, 0, err
	}
	return cfg, cfg.HTTPTimeoutSeconds, nil
}

// WebUIRefID 从原始 JSON 读取可选的服务器 id（仅引用 store）
func WebUIRefID(raw map[string]any) (uint, bool) {
	value, ok := raw["id"]
	if !ok || value == nil {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		if v <= 0 {
			return 0, false
		}
		return uint(v), true
	case json.Number:
		n, err := v.Int64()
		if err != nil || n <= 0 {
			return 0, false
		}
		return uint(n), true
	case string:
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if err != nil || n == 0 {
			return 0, false
		}
		return uint(n), true
	default:
		return 0, false
	}
}

// WebUIRefNameOnly 判断是否仅按名称引用 store 中的服务器
func WebUIRefNameOnly(raw map[string]any) bool {
	name, _ := raw["name"].(string)
	if strings.TrimSpace(name) == "" {
		return false
	}
	if _, ok := WebUIRefID(raw); ok {
		return false
	}
	for _, key := range []string{"transport", "type", "command", "url"} {
		if strings.TrimSpace(stringField(raw, key)) != "" {
			return false
		}
	}
	return true
}

func stringField(raw map[string]any, key string) string {
	value, ok := raw[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

type webuiMCPServerPayload struct {
	Name               string            `json:"name"`
	Transport          string            `json:"transport"`
	Type               string            `json:"type"`
	Command            string            `json:"command"`
	Args               []string          `json:"args"`
	URL                string            `json:"url"`
	Env                json.RawMessage   `json:"env"`
	Headers            map[string]string `json:"headers"`
	CustomHeaders      map[string]string `json:"custom_headers"`
	ToolNameList       []string          `json:"tool_name_list"`
	HTTPTimeoutSeconds float64           `json:"http_timeout_seconds"`
	ReadTimeoutSeconds float64           `json:"read_timeout_seconds"`
	Authorization      *struct {
		Mode        string `json:"mode"`
		BearerToken string `json:"bearer_token"`
	} `json:"authorization"`
}

func parseWebUIEnv(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var asList []string
	if err := json.Unmarshal(raw, &asList); err == nil {
		return asList, nil
	}
	var asMap map[string]string
	if err := json.Unmarshal(raw, &asMap); err != nil {
		return nil, fmt.Errorf("MCP 环境变量格式无效")
	}
	out := make([]string, 0, len(asMap))
	for key, value := range asMap {
		out = append(out, key+"="+value)
	}
	return out, nil
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
