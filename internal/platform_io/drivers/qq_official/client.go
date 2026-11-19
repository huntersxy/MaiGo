package qq_official

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client QQ 官方 OpenAPI HTTP 客户端
type Client struct {
	mu            sync.Mutex
	baseURL       string
	appID         string
	appSecret     string
	http          *http.Client
	accessToken   string
	tokenExpires  time.Time
}

func NewClient(baseURL, appID, appSecret string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultAPIBaseURL
	}
	return &Client{
		baseURL:   baseURL,
		appID:     strings.TrimSpace(appID),
		appSecret: strings.TrimSpace(appSecret),
		http: &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{
				Proxy: nil,
			},
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
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
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
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	urlStr := asString(payload["url"])
	if !strings.HasPrefix(urlStr, "wss://") {
		return "", fmt.Errorf("QQ 网关没有返回有效的 wss 地址")
	}
	return urlStr, nil
}

func (c *Client) APIPost(ctx context.Context, path string, body map[string]any) (map[string]any, error) {
	token, err := c.Authorize(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var payload map[string]any
	_ = json.Unmarshal(data, &payload)
	if resp.StatusCode == 401 {
		c.InvalidateToken()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return payload, fmt.Errorf("QQ API %s 失败: HTTP %d", path, resp.StatusCode)
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
		return int(typed), true
	case float64:
		return int(typed), true
	case string:
		n, err := strconvAtoi(strings.TrimSpace(typed))
		return n, err == nil
	default:
		return 0, false
	}
}

func strconvAtoi(s string) (int, error) {
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not int")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}
