package networksecurity

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Options 对齐 network_security.validate_public_url 的参数
type Options struct {
	// RequirePublicNetwork 为 true 时执行本地主机与 DNS 解析后的公网校验
	RequirePublicNetwork bool
	// AllowConfiguredPrivateNetwork 允许已保存配置显式使用回环或私网目标
	AllowConfiguredPrivateNetwork bool
	// AllowFakeIP 供插件市场 HTTPS 域名放行 198.18.0.0/15
	AllowFakeIP bool
	// AllowedSchemes 为空时默认只允许 http 与 https
	AllowedSchemes []string
	// LookupIP 仅测试注入，nil 时使用 net.DefaultResolver
	LookupIP func(ctx context.Context, host string) ([]net.IPAddr, error)
}

// NonPublicAddressError 对齐 network_security.NonPublicAddressError
type NonPublicAddressError struct {
	Hostname string
	Address  string
	Resolved []string
}

// Error 输出被拦截的地址
func (e *NonPublicAddressError) Error() string {
	return "禁止访问非公网地址: " + e.Address
}

func mustCIDR(value string) *net.IPNet {
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		panic("networksecurity: 非法网段 " + value)
	}
	return network
}

// ValidatePublicURL 对齐 common.network_security.validate_public_url
func ValidatePublicURL(rawURL string, options Options) (string, error) {
	normalized := strings.TrimSpace(rawURL)
	if normalized == "" {
		return "", fmt.Errorf("URL 不能为空")
	}
	if !strings.Contains(normalized, "://") {
		normalized = "http://" + normalized
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		if strings.Contains(err.Error(), "invalid port") {
			return "", fmt.Errorf("URL 端口非法")
		}
		return "", fmt.Errorf("URL 解析失败: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	allowed := allowedSchemes(options.AllowedSchemes)
	if !slices.Contains(allowed, scheme) {
		return "", fmt.Errorf("仅允许以下协议: %s", strings.Join(allowed, ", "))
	}
	hostname := parsed.Hostname()
	if hostname == "" || parsed.Host == "" {
		return "", fmt.Errorf("URL 缺少有效的主机名")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("URL 不允许内嵌认证信息")
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("URL 不允许包含片段")
	}
	if options.RequirePublicNetwork && !options.AllowConfiguredPrivateNetwork {
		lowered := strings.ToLower(hostname)
		if lowered == "localhost" || lowered == "localhost.localdomain" {
			return "", fmt.Errorf("不允许访问本地主机")
		}
	}
	port := parsed.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", fmt.Errorf("URL 端口非法")
		}
	}
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if options.RequirePublicNetwork {
		addresses, err := resolveHostAddresses(hostname, options)
		if err != nil {
			return "", err
		}
		literal := isIPLiteral(hostname)
		for _, address := range addresses {
			if options.AllowFakeIP && scheme == "https" && !literal && fakeIPNetwork.Contains(address) {
				continue
			}
			forbidden := isUnsafeIPAddress(address)
			if !options.AllowConfiguredPrivateNetwork {
				forbidden = isNonPublicIPAddress(address)
			}
			if forbidden {
				resolved := make([]string, 0, len(addresses))
				for _, item := range addresses {
					resolved = append(resolved, formatIP(item))
				}
				return "", &NonPublicAddressError{Hostname: hostname, Address: formatIP(address), Resolved: resolved}
			}
		}
	}
	return normalized, nil
}

func allowedSchemes(configured []string) []string {
	if len(configured) == 0 {
		return []string{"http", "https"}
	}
	seen := make(map[string]struct{}, len(configured))
	out := make([]string, 0, len(configured))
	for _, scheme := range configured {
		lowered := strings.ToLower(strings.TrimSpace(scheme))
		if lowered == "" {
			continue
		}
		if _, ok := seen[lowered]; ok {
			continue
		}
		seen[lowered] = struct{}{}
		out = append(out, lowered)
	}
	sort.Strings(out)
	return out
}

func resolveHostAddresses(hostname string, options Options) ([]net.IP, error) {
	if isIPLiteral(hostname) {
		// 字面量不查 DNS，与 Python 一致：inet_aton 别名先归一化，再按 IPv6 解析
		if ip := parseInetAton(hostname); ip != nil {
			return []net.IP{ip}, nil
		}
		ip := net.ParseIP(strings.SplitN(hostname, "%", 2)[0])
		if ip == nil {
			return nil, fmt.Errorf("无法解析主机名: %s", hostname)
		}
		return []net.IP{ip}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	lookup := options.LookupIP
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	addresses, err := lookup(ctx, hostname)
	if err != nil || len(addresses) == 0 {
		return nil, fmt.Errorf("无法解析主机名: %s", hostname)
	}
	out := make([]net.IP, 0, len(addresses))
	for _, item := range addresses {
		if item.IP == nil {
			continue
		}
		out = append(out, item.IP)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("无法解析主机名: %s", hostname)
	}
	return out, nil
}

// isIPLiteral 对齐 _is_ip_literal：inet_aton 识别十进制整数、十六进制与缩写 IPv4
// 带冒号的主机名按 IPv6 字面量处理
func isIPLiteral(hostname string) bool {
	if parseInetAton(hostname) != nil {
		return true
	}
	return strings.Contains(hostname, ":")
}

// isUnsafeIPAddress 对齐 _is_unsafe_ip_address：链路本地、组播、保留、未指定地址始终禁止
func isUnsafeIPAddress(address net.IP) bool {
	return isLinkLocal(address) || isMulticast(address) || isReserved(address) || isUnspecified(address)
}

// isNonPublicIPAddress 对齐 _is_non_public_ip_address：私网与回环地址不属于公网目标
func isNonPublicIPAddress(address net.IP) bool {
	// Python ipaddress 把 198.18.0.0/15 视为 private；allow_fake_ip 只对 HTTPS 域名单独放行
	return isPrivate(address) || isLoopback(address) || isUnsafeIPAddress(address)
}
