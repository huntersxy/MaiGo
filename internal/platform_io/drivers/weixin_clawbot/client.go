package weixin_clawbot

import (
	"MaiGo/internal/platform_io"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIError 业务错误码
type APIError struct {
	Code     int
	Endpoint string
}

func (e *APIError) Error() string {
	if e == nil {
		return "weixin api error"
	}
	return fmt.Sprintf("微信接口 %s 返回错误码 %d", e.Endpoint, e.Code)
}

// Client iLink HTTPS 客户端
type Client struct {
	baseURL  string
	token    string
	maxBytes int
	http     *http.Client
}

func NewClient(baseURL, token string, maxBytes int) (*Client, error) {
	base, err := trustedURL(baseURL)
	if err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxImageMB * 1024 * 1024
	}
	transport := &http.Transport{}
	if baseTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = baseTransport.Clone()
		transport.Proxy = nil
	}
	return &Client{
		baseURL:  base,
		token:    token,
		maxBytes: maxBytes,
		http: &http.Client{
			Transport: transport,
			Timeout:   45 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func trustedURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultBaseURL
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	host := strings.ToLower(parsed.Hostname())
	if parsed.Scheme != "https" || !strings.HasSuffix(host, ".weixin.qq.com") || parsed.User != nil {
		return "", fmt.Errorf("微信返回了非受信任的服务地址")
	}
	if parsed.Port() != "" && parsed.Port() != "443" {
		return "", fmt.Errorf("微信返回了非受信任的服务地址")
	}
	return strings.TrimRight(raw, "/"), nil
}

func (c *Client) SetToken(token string) {
	if c != nil {
		c.token = token
	}
}

func (c *Client) Close() {
	if c == nil || c.http == nil {
		return
	}
	c.http.CloseIdleConnections()
}

func (c *Client) Call(ctx context.Context, endpoint string, payload map[string]any, params map[string]string, login bool) (map[string]any, error) {
	if c == nil {
		return nil, fmt.Errorf("weixin client 为空")
	}
	var seed [4]byte
	rand.Read(seed[:])
	headers := map[string]string{
		"AuthorizationType": "ilink_bot_token",
		"X-WECHAT-UIN":      base64.StdEncoding.EncodeToString([]byte(fmt.Sprint(binary.BigEndian.Uint32(seed[:])))),
	}
	method := http.MethodPost
	if login {
		method = http.MethodGet
		headers["iLink-App-ClientVersion"] = "1"
	} else {
		if strings.TrimSpace(c.token) == "" {
			return nil, fmt.Errorf("微信尚未登录")
		}
		headers["Authorization"] = "Bearer " + c.token
	}
	endpointURL := fmt.Sprintf("%s/ilink/bot/%s", c.baseURL, endpoint)
	if len(params) > 0 {
		values := url.Values{}
		for k, v := range params {
			values.Set(k, v)
		}
		endpointURL += "?" + values.Encode()
	}
	var bodyReader io.Reader
	if payload != nil {
		body := map[string]any{"base_info": map[string]any{"channel_version": "maibot"}}
		for k, v := range payload {
			body[k] = v
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpointURL, bodyReader)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, &APIError{Code: resp.StatusCode, Endpoint: endpoint}
	}
	data, err := platformio.DecodeWireObject(raw)
	if err != nil {
		return nil, fmt.Errorf("微信接口响应必须为对象")
	}
	code := 0
	if n, ok := asInt(data["errcode"]); ok && n != 0 {
		code = n
	} else if n, ok := asInt(data["ret"]); ok {
		code = n
	}
	if code != 0 {
		return nil, &APIError{Code: code, Endpoint: endpoint}
	}
	return data, nil
}

func (c *Client) Updates(ctx context.Context, cursor string) (map[string]any, error) {
	data, err := c.Call(ctx, "getupdates", map[string]any{"get_updates_buf": cursor}, nil, false)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		// 空闲长轮询超时不当作掉线
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return map[string]any{"msgs": []any{}, "get_updates_buf": cursor}, nil
		}
		return nil, err
	}
	return data, nil
}

func (c *Client) Send(ctx context.Context, user, contextToken string, items []map[string]any, clientID string) error {
	if strings.TrimSpace(contextToken) == "" {
		return fmt.Errorf("缺少会话凭证，请先在微信给机器人发送一条消息")
	}
	_, err := c.Call(ctx, "sendmessage", map[string]any{
		"msg": map[string]any{
			"from_user_id":  "",
			"to_user_id":    user,
			"context_token": contextToken,
			"client_id":     clientID,
			"message_type":  2,
			"message_state": 2,
			"item_list":     items,
		},
	}, nil, false)
	return err
}
