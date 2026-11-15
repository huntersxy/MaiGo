package mcp

import (
	"sort"
	"strings"

	maibotcfg "MaiGo/internal/config/maibot"
)

// ClientConfigFromMaiBot 把 bot_config.toml [mcp.client] 转成运行时配置
//
// 对齐 MaiBot build_mcp_client_runtime_config
func ClientConfigFromMaiBot(cfg maibotcfg.MCPConfig) ClientConfig {
	roots := make([]RootConfig, 0, len(cfg.Client.Roots.Items))
	for _, root := range cfg.Client.Roots.Items {
		if !root.Enabled {
			continue
		}
		uri := strings.TrimSpace(root.Uri)
		if uri == "" {
			continue
		}
		roots = append(roots, RootConfig{URI: uri, Name: strings.TrimSpace(root.Name)})
	}
	return ClientConfig{
		ClientName:           strings.TrimSpace(cfg.Client.ClientName),
		ClientVersion:        strings.TrimSpace(cfg.Client.ClientVersion),
		EnableRoots:          cfg.Client.Roots.Enable && len(roots) > 0,
		Roots:                roots,
		EnableSampling:       cfg.Client.Sampling.Enable,
		SamplingTaskName:     strings.TrimSpace(cfg.Client.Sampling.TaskName),
		SamplingToolSupport:  cfg.Client.Sampling.ToolSupport,
		EnableElicitation:    cfg.Client.Elicitation.Enable,
		ElicitationAllowForm: cfg.Client.Elicitation.AllowForm,
		ElicitationAllowURL:  cfg.Client.Elicitation.AllowUrl,
	}.Normalize()
}

// ServerConfigsFromMaiBot 把 bot_config.toml [mcp] 服务器转成运行时配置
//
// 对齐 MaiBot build_mcp_server_runtime_configs：总开关关闭或单台停用时跳过
func ServerConfigsFromMaiBot(cfg maibotcfg.MCPConfig) []ServerConfig {
	if !cfg.Enable {
		return nil
	}
	out := make([]ServerConfig, 0, len(cfg.Servers))
	for _, server := range cfg.Servers {
		if !server.Enabled {
			continue
		}
		out = append(out, ServerConfigFromMaiBot(server))
	}
	return out
}

// ServerConfigFromMaiBot 转换单台服务器配置，对齐 build_mcp_server_runtime_config
func ServerConfigFromMaiBot(server maibotcfg.MCPServerItemConfig) ServerConfig {
	return ServerConfig{
		Name:               strings.TrimSpace(server.Name),
		Enabled:            true,
		Type:               strings.TrimSpace(server.Transport),
		URL:                strings.TrimSpace(server.Url),
		Command:            strings.TrimSpace(server.Command),
		Args:               append([]string(nil), server.Args...),
		Env:                envPairs(server.Env),
		CustomHeaders:      cloneStringMap(server.Headers),
		ToolNameList:       nil,
		HTTPTimeoutSeconds: effectiveSeconds(server.HttpTimeoutSeconds, DefaultHTTPTimeoutSeconds),
		ReadTimeoutSeconds: effectiveSeconds(server.ReadTimeoutSeconds, DefaultReadTimeoutSeconds),
		Authorization: AuthorizationConfig{
			Mode:        strings.ToLower(strings.TrimSpace(server.Authorization.Mode)),
			BearerToken: strings.TrimSpace(server.Authorization.BearerToken),
		},
	}
}

// FromMaiBotConfig 同时返回服务器运行时配置与客户端宿主能力配置
func FromMaiBotConfig(cfg maibotcfg.MCPConfig) ([]ServerConfig, ClientConfig) {
	return ServerConfigsFromMaiBot(cfg), ClientConfigFromMaiBot(cfg)
}

func envPairs(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out
}
