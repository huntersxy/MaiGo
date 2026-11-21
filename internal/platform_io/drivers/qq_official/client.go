package qq_official

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"MaiGo/internal/platform_io"
	"strings"
	"sync"
	"time"
)

// Client QQ 官方 OpenAPI HTTP 客户端
type Client struct {
	mu           sync.Mutex
	baseURL      string
	appID        string
	appSecret    string
	http         *http.Client
	accessToken  string
	tokenExpires time.Time
}

func NewClient(baseURL, appID, appSecret string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultAPIBaseURL
	}
	transport := &http.Transport{}
	if baseTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = baseTransport.Clone()
		transport.Proxy = nil
	}
	return &Client{
		baseURL:   baseURL,
		appID:     strings.TrimSpace(appID),
		appSecret: strings.TrimSpace(appSecret),
		http: &http.Client{
			Timeout:   20 * time.Second,
			Transport: transport,
		},
	}
}

func (c *Client) Close() {
	if c == nil || c.http == nil {
		return
	}
	c.http.CloseIdleConnections()
}

func (c *Client) InvalidateToken() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accessToken = ""
	c.tokenExpires = time.Time{}
}

func (c *Client) Authorize(ctx context.Context) (string, error) {
	if c == nil {
		return "", fmt.Errorf("QQ client 为空")
	}
	c.mu.Lock()
	if c.accessToken != "" && time.Now().Before(c.tokenExpires.Add(-50*time.Second)) {
		token := c.accessToken
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()

	body, _ := json.Marshal(map[string]string{
		"appId":        c.appID,
		"clientSecret": c.appSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/app/getAppAccessToken", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	payload, err := platformio.DecodeWireObject(raw)
	if err != nil {
		return "", fmt.Errorf("获取 QQ 访问凭证失败: HTTP %d", resp.StatusCode)
	}
	token := asString(payload["access_token"])
	if resp.StatusCode != 200 || token == "" {
		return "", fmt.Errorf("获取 QQ 访问凭证失败: HTTP %d, code=%v", resp.StatusCode, payload["code"])
	}
	expiresIn := 7200
	if n, ok := asInt(payload["expires_in"]); ok && n > 0 {
		expiresIn = n
	}
	c.mu.Lock()
	c.accessToken = token
	c.tokenExpires = time.Now().Add(time.Duration(expiresIn) * time.Second)
	c.mu.Unlock()
	return token, nil
}

func (c *Client) GatewayURL(ctx context.Context) (string, error) {
	token, err := c.Authorize(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/gateway", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		c.InvalidateToken()
	}
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("获取 QQ 网关失败: HTTP %d %s", resp.StatusCode, string(raw))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	payload, err := platformio.DecodeWireObject(raw)
	if err != nil {
		return "", err
	}
	urlStr := asString(payload["url"])
	if !strings.HasPrefix(urlStr, "wss://") {
		return "", fmt.Errorf("QQ 网关没有返回有效的 wss 地址")
	}
	return urlStr, nil
}

func (c *Client) APIPost(ctx context.Context, path string, body map[string]any) (map[string]any, error) {
	return c.apiPost(ctx, path, body, 0)
}

func (c *Client) apiPost(ctx context.Context, path string, body map[string]any, timeout time.Duration) (map[string]any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return c.apiRequest(ctx, http.MethodPost, path, bytes.NewReader(raw), timeout)
}

func (c *Client) apiRequest(ctx context.Context, method, path string, body io.Reader, timeout time.Duration) (map[string]any, error) {
	token, err := c.Authorize(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	httpClient := *c.http
	if timeout > 0 {
		httpClient.Timeout = timeout
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if len(bytes.TrimSpace(data)) != 0 {
		payload, err = platformio.DecodeWireObject(data)
	}
	if resp.StatusCode == 401 {
		c.InvalidateToken()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return payload, fmt.Errorf("QQ API %s 失败: HTTP %d", path, resp.StatusCode)
	}
	if err != nil {
		return nil, fmt.Errorf("QQ API %s 响应格式无效: %w", path, err)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	return payload, nil
}

func (c *Client) SendText(ctx context.Context, scene, openID, text string, fields map[string]any) (map[string]any, error) {
	path := "/v2/users/" + url.PathEscape(openID) + "/messages"
	if scene == "group" {
		path = "/v2/groups/" + url.PathEscape(openID) + "/messages"
	}
	body := map[string]any{"content": text, "msg_type": 0}
	for k, v := range fields {
		body[k] = v
	}
	return c.APIPost(ctx, path, body)
}

func asInt(v any) (int, bool) {
	switch typed := v.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), int64(int(typed)) == typed
	case json.Number:
		n, err := strconv.Atoi(string(typed))
		return n, err == nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(typed))
		return n, err == nil
	default:
		return 0, false
	}
}
