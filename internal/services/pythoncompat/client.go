// Package pythoncompat 调用 Python 原版能力侧车。
//
// 有些模块的复杂度来自「复刻 Python 字节级行为」：中文错别字注入依赖
// pypinyin + jieba + 字频表，表达向量聚类依赖 numpy PCG64 + k-means++。
// 这类逻辑用 Go 重写永远要跟着 Python 版本走，因此交给
// scripts/python_compat_sidecar.py 直接跑原版实现，Go 只保留调用与降级。
//
// 未配置 MAIBOT_COMPAT_URL 时所有方法返回 ErrNotConfigured，
// 调用方据此回落到本地实现或跳过该能力。
package pythoncompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrNotConfigured 未配置侧车地址，调用方应回落本地实现
var ErrNotConfigured = errors.New("pythoncompat: 未配置 Python 兼容侧车")

const (
	defaultTimeout = 30 * time.Second
	defaultRouteTO = 120 * time.Second
)

// Config 侧车连接参数
type Config struct {
	BaseURL string
	Token   string
	Timeout time.Duration
}

// LoadConfigFromEnv 读取 MAIBOT_COMPAT_* 环境变量
func LoadConfigFromEnv() Config {
	cfg := Config{
		BaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("MAIBOT_COMPAT_URL")), "/"),
		Token:   strings.TrimSpace(os.Getenv("MAIBOT_COMPAT_TOKEN")),
		Timeout: defaultTimeout,
	}
	if raw := strings.TrimSpace(os.Getenv("MAIBOT_COMPAT_TIMEOUT")); raw != "" {
		if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds > 0 {
			cfg.Timeout = time.Duration(seconds * float64(time.Second))
		}
	}
	return cfg
}

// Client Python 兼容侧车客户端；零值或 BaseURL 为空时视为未配置
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient 按配置构造客户端
func NewClient(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout < defaultRouteTO {
		timeout = defaultRouteTO
	}
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		token:   strings.TrimSpace(cfg.Token),
		http:    &http.Client{Timeout: timeout},
	}
}

// Configured 是否可用
func (c *Client) Configured() bool {
	return c != nil && c.baseURL != ""
}

// BaseURL 当前侧车地址，供状态展示
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.baseURL
}

// post 调用侧车接口并把响应解到 out
func (c *Client) post(ctx context.Context, route string, payload, out any) error {
	if !c.Configured() {
		return ErrNotConfigured
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("pythoncompat: 序列化请求失败: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+route, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("pythoncompat: 构造请求失败: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("pythoncompat: 调用 %s 失败: %w", route, err)
	}
	defer func() { _ = response.Body.Close() }()

	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if readErr != nil {
		return fmt.Errorf("pythoncompat: 读取 %s 响应失败: %w", route, readErr)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("pythoncompat: %s 返回 %d: %s", route, response.StatusCode, detailOf(raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("pythoncompat: 解析 %s 响应失败: %w", route, err)
	}
	return nil
}

// detailOf 提取 Python 侧 {"detail": "..."} 错误体
func detailOf(raw []byte) string {
	var payload struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil && payload.Detail != "" {
		return payload.Detail
	}
	text := strings.TrimSpace(string(raw))
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}
