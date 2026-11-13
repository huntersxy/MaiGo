package filesvc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const remoteDownloadChunkSize = 256 * 1024

var errRemoteInvalidURL = errors.New("无效的附件下载地址")

// remoteHTTPStatusError 表示平台文件 URL 返回非 2xx，属于可重试的下载失败
type remoteHTTPStatusError struct {
	StatusCode int
}

func (e *remoteHTTPStatusError) Error() string {
	return fmt.Sprintf("附件地址返回状态码 %d", e.StatusCode)
}

// publicAddress 对齐 Python ipaddress 的 is_global 判定
func publicAddress(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 0 || v4[0] >= 240 {
			return false
		}
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return false
		}
	}
	for _, network := range nonPublicNetworks {
		if network.Contains(ip) {
			for _, exception := range publicNetworkExceptions {
				if exception.Contains(ip) {
					return true
				}
			}
			return false
		}
	}
	return ip.IsGlobalUnicast()
}

var nonPublicNetworks = mustCIDRs(
	"192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24",
	"64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20",
)

// publicNetworkExceptions 对齐 Python 3.14 ipaddress 的 _private_networks_exceptions
var publicNetworkExceptions = mustCIDRs(
	"192.0.0.9/32", "192.0.0.10/32",
	"2001:1::1/128", "2001:1::2/128", "2001:3::/32",
	"2001:4:112::/48", "2001:20::/28", "2001:30::/28",
)

func mustCIDRs(values ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			panic(err)
		}
		out = append(out, network)
	}
	return out
}

// validateRemoteURL 校验下载地址并返回是否命中受信任来源
func validateRemoteURL(rawURL string, trustedOrigins []string) (bool, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return false, fmt.Errorf("%w: 附件下载只接受不含凭证的 HTTP(S) 地址", errRemoteInvalidURL)
	}
	origin := strings.ToLower(parsed.Scheme + "://" + parsed.Host)
	trusted := false
	for _, candidate := range trustedOrigins {
		if strings.ToLower(strings.TrimRight(strings.TrimSpace(candidate), "/")) == origin {
			trusted = true
			break
		}
	}
	if address := net.ParseIP(parsed.Hostname()); address != nil {
		if !trusted && !publicAddress(address) {
			return false, fmt.Errorf("%w: 附件下载禁止访问内网地址；管理员可配置精确的受信任来源", ErrForbidden)
		}
	}
	return trusted, nil
}

// downloadRemote 逐次校验重定向目标，按块读取平台文件
func downloadRemote(ctx context.Context, rawURL string, trustedOrigins []string, timeout time.Duration, consume func([]byte) error) error {
	if consume == nil {
		return fmt.Errorf("下载消费回调不能为空")
	}
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	current := strings.TrimSpace(rawURL)
	for redirect := 0; redirect < 4; redirect++ {
		trusted, err := validateRemoteURL(current, trustedOrigins)
		if err != nil {
			return err
		}
		client := &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				Proxy:                 nil,
				DialContext:           remoteDialContext(trusted, 10*time.Second),
				ForceAttemptHTTP2:     true,
				DisableCompression:    true,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: time.Second,
				IdleConnTimeout:       30 * time.Second,
			},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			client.CloseIdleConnections()
			return fmt.Errorf("%w: %v", errRemoteInvalidURL, err)
		}
		response, err := client.Do(request)
		if err != nil {
			client.CloseIdleConnections()
			return err
		}
		if response.StatusCode == http.StatusMovedPermanently || response.StatusCode == http.StatusFound ||
			response.StatusCode == http.StatusSeeOther || response.StatusCode == http.StatusTemporaryRedirect ||
			response.StatusCode == http.StatusPermanentRedirect {
			location := strings.TrimSpace(response.Header.Get("Location"))
			_ = response.Body.Close()
			client.CloseIdleConnections()
			if redirect == 3 || location == "" {
				return fmt.Errorf("%w: 附件下载重定向过多或缺少目标", errRemoteInvalidURL)
			}
			base, parseErr := url.Parse(current)
			if parseErr != nil {
				return fmt.Errorf("%w: 附件下载重定向目标无效", errRemoteInvalidURL)
			}
			target, parseErr := url.Parse(location)
			if parseErr != nil {
				return fmt.Errorf("%w: 附件下载重定向目标无效", errRemoteInvalidURL)
			}
			current = base.ResolveReference(target).String()
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			_ = response.Body.Close()
			client.CloseIdleConnections()
			return &remoteHTTPStatusError{StatusCode: response.StatusCode}
		}
		buffer := make([]byte, remoteDownloadChunkSize)
		for {
			n, readErr := response.Body.Read(buffer)
			if n > 0 {
				if consumeErr := consume(buffer[:n]); consumeErr != nil {
					_ = response.Body.Close()
					client.CloseIdleConnections()
					return consumeErr
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				_ = response.Body.Close()
				client.CloseIdleConnections()
				return readErr
			}
		}
		_ = response.Body.Close()
		client.CloseIdleConnections()
		return nil
	}
	return fmt.Errorf("%w: 附件下载重定向过多或缺少目标", errRemoteInvalidURL)
}

func remoteDialContext(trusted bool, timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	if trusted {
		return dialer.DialContext
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("附件地址没有可用的解析结果")
		}
		for _, item := range addresses {
			if !publicAddress(item.IP) {
				return nil, fmt.Errorf("%w: 附件地址解析到非公网 IP", ErrForbidden)
			}
		}
		var lastErr error
		for _, item := range addresses {
			connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(item.IP.String(), port))
			if dialErr == nil {
				return connection, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}
