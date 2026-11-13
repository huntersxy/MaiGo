package contract

import (
	"net"
	"net/http"
	"time"
)

// HandshakeTimeout 出站连接的握手超时
//
// 对齐 MaiBot 各客户端共用的 httpx 超时约定：连接与 TLS 握手都限时，
// 避免不可达 Provider 让整次调用卡死到上层超时。
const HandshakeTimeout = 10 * time.Second

// NewLLMHTTPClient 构造 LLM 出站 HTTP 客户端；totalTimeout 为 0 时不设整请求超时
//
// 每个客户端实例独立持有 transport，避免不同 Provider 的 TLS 会话复用串味。
func NewLLMHTTPClient(totalTimeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: HandshakeTimeout, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = HandshakeTimeout
	client := &http.Client{Transport: transport}
	if totalTimeout > 0 {
		client.Timeout = totalTimeout
	}
	return client
}
