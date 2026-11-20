package host

// 统一消息入口：路径指定唯一 Bot，认证信息由该 Bot 的协议服务验证。
// 对齐 src/host/maim.py。

import (
	"context"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/coder/websocket"
)

var maimPathPattern = regexp.MustCompile(`^/maim/([0-9a-f]{32})/(ws|api)$`)

var maimForwardedHeaders = []string{"authorization", "platform", "x-apikey", "x-platform", "x-uuid"}

func (w *WebUI) handleMaimGateway(rw http.ResponseWriter, r *http.Request) {
	matches := maimPathPattern.FindStringSubmatch(r.URL.Path)
	if matches == nil {
		http.NotFound(rw, r)
		return
	}
	botID, protocol := matches[1], matches[2]
	endpoint, err := w.supervisor.ProxyEndpoint(botID)
	if err != nil {
		closeWebSocketBeforeAccept(rw, r, "Bot 不存在、未启动或尚未就绪")
		return
	}
	headers := http.Header{}
	for _, name := range maimForwardedHeaders {
		if value := r.Header.Get(name); value != "" {
			headers.Set(name, value)
		}
	}
	requestURL := "http://bot/_maim/" + protocol
	if r.URL.RawQuery != "" {
		requestURL += "?" + r.URL.RawQuery
	}
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialEndpoint(ctx, endpoint)
			},
			DisableCompression: true,
		}
	defer transport.CloseIdleConnections()
	dialCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	upstream, response, err := websocket.Dial(dialCtx, requestURL, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: transport}, HTTPHeader: headers,
	})
	if err != nil {
		status := "此 Bot 的消息协议未启用或已停止"
		if response != nil && response.StatusCode != 0 {
			status = "此 Bot 的消息协议未启用或已停止"
		}
		closeWebSocketBeforeAccept(rw, r, status)
		return
	}
	defer upstream.CloseNow()
	upstream.SetReadLimit(104_857_600)

	client, err := websocket.Accept(rw, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	client.SetReadLimit(104_857_600)
	defer client.CloseNow()

	ctx, cancelRelay := context.WithCancel(r.Context())
	defer cancelRelay()
	done := make(chan error, 2)
	relay := func(source, target *websocket.Conn) error {
		for {
			messageType, data, err := source.Read(ctx)
			if err != nil {
				return err
			}
			if err := target.Write(ctx, messageType, data); err != nil {
				return err
			}
		}
	}
	go func() { done <- relay(client, upstream) }()
	go func() { done <- relay(upstream, client) }()
	relayErr := <-done
	cancelRelay()

	code := websocket.StatusNormalClosure
	if status := websocket.CloseStatus(relayErr); status != -1 && status != 1005 && status != 1006 && status != 1015 {
		code = status
	} else {
		code = websocket.StatusGoingAway
	}
	_ = upstream.Close(code, "")
	_ = client.Close(code, "")
}

func closeWebSocketBeforeAccept(rw http.ResponseWriter, r *http.Request, reason string) {
	connection, err := websocket.Accept(rw, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	_ = connection.Close(websocket.StatusTryAgainLater, reason)
}
