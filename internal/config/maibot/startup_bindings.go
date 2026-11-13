package maibot

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// BindAddress 启动阶段使用的绑定地址
type BindAddress struct {
	Hosts []string
	Port  int
}

// DefaultMainBindAddress 主程序消息服务默认监听地址
func DefaultMainBindAddress() BindAddress {
	return BindAddress{Hosts: []string{"127.0.0.1"}, Port: 8080}
}

// DefaultHTTPBindAddress 公共 HTTP 服务默认监听地址
func DefaultHTTPBindAddress() BindAddress {
	return BindAddress{Hosts: []string{"127.0.0.1", "::1"}, Port: 8001}
}

const (
	ipv4WildcardHost = "0.0.0.0"
	ipv6WildcardHost = "::"
)

// NormalizeBindHosts 把配置值规范成非空监听地址列表
// 对齐 startup_bindings._normalize_hosts
func NormalizeBindHosts(value any, defaults []string) []string {
	switch typed := value.(type) {
	case []string:
		hosts := make([]string, 0, len(typed))
		for _, host := range typed {
			if trimmed := strings.TrimSpace(host); trimmed != "" {
				hosts = append(hosts, trimmed)
			}
		}
		if len(hosts) > 0 {
			return hosts
		}
	case []any:
		hosts := make([]string, 0, len(typed))
		for _, item := range typed {
			host, ok := item.(string)
			if !ok {
				continue
			}
			if trimmed := strings.TrimSpace(host); trimmed != "" {
				hosts = append(hosts, trimmed)
			}
		}
		if len(hosts) > 0 {
			return hosts
		}
	case string:
		if trimmed := strings.TrimSpace(typed); trimmed != "" {
			return []string{trimmed}
		}
	}
	return append([]string(nil), defaults...)
}

// NormalizeBindPort 校验端口范围，非法时回退默认端口
// 对齐 startup_bindings._normalize_port
func NormalizeBindPort(value any, defaultPort int) int {
	if _, isBool := value.(bool); isBool {
		return defaultPort
	}
	port, ok := bindPortValue(value)
	if !ok || port <= 0 || port > 65535 {
		return defaultPort
	}
	return port
}

func bindPortValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// LoadStartupBootstrapConfig 读取启动阶段需要的最小配置
// 读取失败或结构非法时返回空字典，不阻断启动
func LoadStartupBootstrapConfig(root string) map[string]any {
	path := filepath.Join(root, "config", "bot_config.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var data map[string]any
	if err := toml.Unmarshal(raw, &data); err != nil {
		return map[string]any{}
	}
	if data == nil {
		return map[string]any{}
	}
	return data
}

func bootstrapSection(data map[string]any, key string) map[string]any {
	section, ok := data[key]
	if !ok {
		return map[string]any{}
	}
	mapping, ok := section.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return mapping
}

func resolveBindAddressFromSection(section map[string]any, hostKey, portKey string, defaultAddress BindAddress) BindAddress {
	return BindAddress{
		Hosts: NormalizeBindHosts(section[hostKey], defaultAddress.Hosts),
		Port:  NormalizeBindPort(section[portKey], defaultAddress.Port),
	}
}

// GetStartupMainBindAddress 读取主程序消息服务绑定地址
func GetStartupMainBindAddress(root string) BindAddress {
	data := LoadStartupBootstrapConfig(root)
	return resolveBindAddressFromSection(
		bootstrapSection(data, "maim_message"),
		"ws_server_host", "ws_server_port",
		DefaultMainBindAddress(),
	)
}

// GetStartupHTTPBindAddress 读取公共 HTTP 配置的绑定地址
func GetStartupHTTPBindAddress(root string) BindAddress {
	data := LoadStartupBootstrapConfig(root)
	address := resolveBindAddressFromSection(
		bootstrapSection(data, "http"),
		"host", "port",
		DefaultHTTPBindAddress(),
	)
	return finalizeHTTPBindAddress(address, os.LookupEnv)
}

func finalizeHTTPBindAddress(address BindAddress, lookup func(string) (string, bool)) BindAddress {
	return BindAddress{
		Hosts: collapseHostsCoveredByWildcards(applyHTTPHostEnv(address.Hosts, lookup)),
		Port:  address.Port,
	}
}

// applyHTTPHostEnv 读取 HTTP_HOST，未设置时才回退 WEBUI_HOST
//
// Python 用 os.getenv 区分「未设置」与「设为空串」：显式空串不回退，也不合并地址。
func applyHTTPHostEnv(hosts []string, lookup func(string) (string, bool)) []string {
	value, set := lookup("HTTP_HOST")
	if !set {
		value, _ = lookup("WEBUI_HOST")
	}
	host := strings.TrimSpace(value)
	if host == "" {
		return hosts
	}
	return mergeBindHost(hosts, host)
}

func mergeBindHost(hosts []string, host string) []string {
	for _, existing := range hosts {
		if existing == host {
			return hosts
		}
	}
	if host == ipv4WildcardHost || host == ipv6WildcardHost {
		return append([]string{host}, hosts...)
	}
	return append(append([]string(nil), hosts...), host)
}

func isIPv6Host(host string) bool {
	return strings.Contains(host, ":")
}

func collapseHostsCoveredByWildcards(hosts []string) []string {
	hasIPv4Wildcard := containsHost(hosts, ipv4WildcardHost)
	hasIPv6Wildcard := containsHost(hosts, ipv6WildcardHost)
	collapsed := make([]string, 0, len(hosts))
	for _, host := range hosts {
		if containsHost(collapsed, host) {
			continue
		}
		if hasIPv4Wildcard && host != ipv4WildcardHost && !isIPv6Host(host) {
			continue
		}
		if hasIPv6Wildcard && host != ipv6WildcardHost && isIPv6Host(host) {
			continue
		}
		collapsed = append(collapsed, host)
	}
	return collapsed
}

func containsHost(hosts []string, target string) bool {
	for _, host := range hosts {
		if host == target {
			return true
		}
	}
	return false
}

// ResolveMainBindAddress 优先使用已加载配置，否则回退启动阶段读取
func ResolveMainBindAddress(root string, cfg *InstanceConfig) BindAddress {
	if cfg != nil {
		return BindAddress{
			Hosts: NormalizeBindHosts(cfg.Bot.MaimMessage.WsServerHost, DefaultMainBindAddress().Hosts),
			Port:  NormalizeBindPort(cfg.Bot.MaimMessage.WsServerPort, DefaultMainBindAddress().Port),
		}
	}
	return GetStartupMainBindAddress(root)
}

// ResolveHTTPBindAddress 优先使用已加载配置，否则回退启动阶段读取
func ResolveHTTPBindAddress(root string, cfg *InstanceConfig) BindAddress {
	if cfg != nil {
		address := BindAddress{
			Hosts: NormalizeBindHosts(cfg.Bot.Http.Host, DefaultHTTPBindAddress().Hosts),
			Port:  NormalizeBindPort(cfg.Bot.Http.Port, DefaultHTTPBindAddress().Port),
		}
		return finalizeHTTPBindAddress(address, os.LookupEnv)
	}
	return GetStartupHTTPBindAddress(root)
}

// LocalHTTPBaseURL 返回本机可访问的 HTTP 基地址
// 包含环境变量合并结果，对齐 get_local_http_base_url
func LocalHTTPBaseURL(root string, cfg *InstanceConfig) string {
	address := ResolveHTTPBindAddress(root, cfg)
	host := ""
	switch {
	case containsHost(address.Hosts, "127.0.0.1"), containsHost(address.Hosts, ipv4WildcardHost):
		host = "127.0.0.1"
	case containsHost(address.Hosts, "::1"), containsHost(address.Hosts, ipv6WildcardHost):
		host = "::1"
	case len(address.Hosts) > 0:
		host = address.Hosts[0]
	default:
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%d", host, address.Port)
}
