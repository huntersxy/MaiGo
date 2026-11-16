package host

// 宿主配置：对外 HTTP 地址、Cookie 与公开流量防护；Bot 只监听进程间通道，对齐 src/host/config.py

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

var defaultHosts = []string{"127.0.0.1", "::1"}

const defaultHostPort = 8001

var wildcardHosts = map[string]struct{}{"0.0.0.0": {}, "::": {}}

// HostConfig 宿主对外服务配置
type HostConfig struct {
	WebUIEnabled   bool
	Hosts          []string
	Port           int
	SecureCookie   bool
	AntiCrawlerMode string
	AllowedIPs     string
	TrustedProxies string
	TrustXFF       bool
	PublicBaseURL  string
	SSLCertFile    string
	SSLKeyFile     string
}

// DefaultHostConfig 与 Python HostConfig 默认值一致
func DefaultHostConfig() HostConfig {
	return HostConfig{
		WebUIEnabled:    true,
		Hosts:           append([]string(nil), defaultHosts...),
		Port:            defaultHostPort,
		AntiCrawlerMode: "basic",
		AllowedIPs:      "127.0.0.1",
	}
}

func hostConfigPath(root string) string {
	return filepath.Join(root, "host", "config.toml")
}

// LoadHostConfig 读取宿主配置；缺文件时返回默认值
func LoadHostConfig(root string) (HostConfig, error) {
	raw, err := os.ReadFile(hostConfigPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultHostConfig(), nil
		}
		return HostConfig{}, err
	}
	var payload map[string]any
	if err := toml.Unmarshal(raw, &payload); err != nil {
		return HostConfig{}, err
	}
	return validateHostConfig(payload, hostConfigPath(root))
}

// SaveHostConfig 写入宿主配置
func SaveHostConfig(root string, cfg HostConfig) error {
	if err := os.MkdirAll(filepath.Dir(hostConfigPath(root)), 0o777); err != nil {
		return err
	}
	payload := map[string]any{
		"webui": map[string]any{
			"enabled":           cfg.WebUIEnabled,
			"hosts":             append([]string(nil), cfg.Hosts...),
			"port":              cfg.Port,
			"secure_cookie":     cfg.SecureCookie,
			"anti_crawler_mode": cfg.AntiCrawlerMode,
			"allowed_ips":       cfg.AllowedIPs,
			"trusted_proxies":   cfg.TrustedProxies,
			"trust_xff":         cfg.TrustXFF,
			"public_base_url":   cfg.PublicBaseURL,
			"ssl_certfile":      cfg.SSLCertFile,
			"ssl_keyfile":       cfg.SSLKeyFile,
		},
	}
	encoded, err := toml.Marshal(payload)
	if err != nil {
		return err
	}
	return os.WriteFile(hostConfigPath(root), encoded, 0o666)
}

func validateHostConfig(payload map[string]any, source string) (HostConfig, error) {
	cfg := DefaultHostConfig()
	webui, ok := payload["webui"].(map[string]any)
	if !ok {
		if payload["webui"] != nil {
			return HostConfig{}, fmt.Errorf("%s: webui 必须是一个配置节", source)
		}
		webui = map[string]any{}
	}
	if raw, exists := webui["enabled"]; exists {
		value, ok := raw.(bool)
		if !ok {
			return HostConfig{}, fmt.Errorf("%s: webui.enabled 必须是布尔值", source)
		}
		cfg.WebUIEnabled = value
	}
	if raw, exists := webui["hosts"]; exists {
		items, ok := raw.([]any)
		if !ok || len(items) == 0 {
			return HostConfig{}, fmt.Errorf("%s: webui.hosts 必须是非空的地址列表", source)
		}
		hosts := make([]string, 0, len(items))
		for _, item := range items {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return HostConfig{}, fmt.Errorf("%s: webui.hosts 必须是非空的地址列表", source)
			}
			hosts = append(hosts, strings.TrimSpace(text))
		}
		cfg.Hosts = hosts
	}
	if raw, exists := webui["port"]; exists {
		port, ok := raw.(int64)
		if !ok || port < 1 || port > 65535 {
			return HostConfig{}, fmt.Errorf("%s: webui.port 必须是 1 到 65535 的整数", source)
		}
		cfg.Port = int(port)
	}
	if raw, exists := webui["secure_cookie"]; exists {
		value, ok := raw.(bool)
		if !ok {
			return HostConfig{}, fmt.Errorf("%s: webui.secure_cookie 必须是布尔值", source)
		}
		cfg.SecureCookie = value
	}
	if raw, exists := webui["anti_crawler_mode"]; exists {
		value, _ := raw.(string)
		if value != "false" && value != "strict" && value != "loose" && value != "basic" {
			return HostConfig{}, fmt.Errorf("%s: webui.anti_crawler_mode 必须是 false / strict / loose / basic 之一", source)
		}
		cfg.AntiCrawlerMode = value
	}
	if raw, exists := webui["allowed_ips"]; exists {
		value, ok := raw.(string)
		if !ok {
			return HostConfig{}, fmt.Errorf("%s: webui.allowed_ips 与 webui.trusted_proxies 必须是逗号分隔的字符串", source)
		}
		cfg.AllowedIPs = value
	}
	if raw, exists := webui["trusted_proxies"]; exists {
		value, ok := raw.(string)
		if !ok {
			return HostConfig{}, fmt.Errorf("%s: webui.allowed_ips 与 webui.trusted_proxies 必须是逗号分隔的字符串", source)
		}
		cfg.TrustedProxies = value
	}
	if raw, exists := webui["trust_xff"]; exists {
		value, ok := raw.(bool)
		if !ok {
			return HostConfig{}, fmt.Errorf("%s: webui.trust_xff 必须是布尔值", source)
		}
		cfg.TrustXFF = value
	}
	if raw, exists := webui["public_base_url"]; exists {
		value, ok := raw.(string)
		if !ok {
			return HostConfig{}, fmt.Errorf("%s: webui.public_base_url 必须是字符串", source)
		}
		cfg.PublicBaseURL = strings.TrimRight(strings.TrimSpace(value), "/")
	}
	if raw, exists := webui["ssl_certfile"]; exists {
		value, ok := raw.(string)
		if !ok {
			return HostConfig{}, fmt.Errorf("%s: ssl_certfile 与 ssl_keyfile 必须同时填写证书和私钥路径", source)
		}
		cfg.SSLCertFile = value
	}
	if raw, exists := webui["ssl_keyfile"]; exists {
		value, ok := raw.(string)
		if !ok {
			return HostConfig{}, fmt.Errorf("%s: ssl_certfile 与 ssl_keyfile 必须同时填写证书和私钥路径", source)
		}
		cfg.SSLKeyFile = value
	}
	if (cfg.SSLCertFile == "") != (cfg.SSLKeyFile == "") {
		return HostConfig{}, fmt.Errorf("%s: ssl_certfile 与 ssl_keyfile 必须同时填写证书和私钥路径", source)
	}
	if cfg.PublicBaseURL != "" {
		parsed, err := url.Parse(cfg.PublicBaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return HostConfig{}, fmt.Errorf("%s: public_base_url 必须是无凭证、查询参数和片段的 HTTP(S) 地址", source)
		}
	}
	return cfg, nil
}

// BindHosts 合并 HTTP_HOST（兼容 WEBUI_HOST）并折叠通配地址
func (c HostConfig) BindHosts() []string {
	hosts := append([]string(nil), c.Hosts...)
	extra := ""
	if value, exists := os.LookupEnv("HTTP_HOST"); exists {
		extra = strings.TrimSpace(value)
	} else {
		extra = strings.TrimSpace(os.Getenv("WEBUI_HOST"))
	}
	if extra != "" {
		found := false
		for _, host := range hosts {
			if host == extra {
				found = true
				break
			}
		}
		if !found {
			if _, wildcard := wildcardHosts[extra]; wildcard {
				hosts = append([]string{extra}, hosts...)
			} else {
				hosts = append(hosts, extra)
			}
		}
	}
	result := make([]string, 0, len(hosts))
	for _, host := range hosts {
		contained := false
		for _, existing := range result {
			if existing == host {
				contained = true
				break
			}
		}
		if contained {
			continue
		}
		if _, wildcard := wildcardHosts[host]; !wildcard {
			if strings.Contains(host, ":") {
				if containsHost(hosts, "::") {
					continue
				}
			} else if containsHost(hosts, "0.0.0.0") {
				continue
			}
		}
		result = append(result, host)
	}
	return result
}

// LocalBaseURL 本机访问地址
func (c HostConfig) LocalBaseURL() string {
	hosts := c.BindHosts()
	host := ""
	switch {
	case containsHost(hosts, "0.0.0.0") || containsHost(hosts, "127.0.0.1"):
		host = "127.0.0.1"
	case containsHost(hosts, "::") || containsHost(hosts, "::1"):
		host = "[::1]"
	case len(hosts) > 0 && strings.Contains(hosts[0], ":"):
		host = "[" + hosts[0] + "]"
	case len(hosts) > 0:
		host = hosts[0]
	default:
		host = "127.0.0.1"
	}
	scheme := "http"
	if c.SSLCertFile != "" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, c.Port)
}

// AdvertisedBaseURL 所有 Bot 共用的公开 URL 来源
func (c HostConfig) AdvertisedBaseURL() string {
	if strings.TrimSpace(c.PublicBaseURL) != "" {
		return c.PublicBaseURL
	}
	return c.LocalBaseURL()
}

func containsHost(hosts []string, target string) bool {
	for _, host := range hosts {
		if host == target {
			return true
		}
	}
	return false
}
