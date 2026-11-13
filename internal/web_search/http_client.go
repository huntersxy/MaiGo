package websearch

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// newHTTPClient 构造带超时与代理的客户端；未显式代理时沿用标准库环境代理（含 NO_PROXY）
func newHTTPClient(timeout time.Duration, proxyURL string) *http.Client {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{Timeout: timeout}
	}
	cloned := transport.Clone()
	if strings.TrimSpace(proxyURL) != "" {
		if parsed, err := url.Parse(strings.TrimSpace(proxyURL)); err == nil && parsed.Host != "" {
			cloned.Proxy = http.ProxyURL(parsed)
		}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: cloned,
		// 对齐 Python httpx follow_redirects=False：3xx 直接按 HTTP 状态返回
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// decodeWebJSONObject 对齐 Python HTTPAdapter：无效 JSON、非对象、业务错误分别报错
func decodeWebJSONObject(data []byte) (map[string]any, error) {
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, NewError("联网服务返回了无效 JSON")
	}
	result, ok := decoded.(map[string]any)
	if !ok {
		return nil, NewError("联网服务响应必须是 JSON 对象")
	}
	if _, exists := result["error"]; exists {
		return nil, NewError("联网服务返回业务错误")
	}
	return result, nil
}
