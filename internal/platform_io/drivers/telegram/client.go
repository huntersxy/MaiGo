package telegram

import (
	"MaiGo/internal/platform_io"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"
)

var (
	tokenPattern  = regexp.MustCompile(`^\d+:[A-Za-z0-9_-]+$`)
	methodPattern = regexp.MustCompile(`^[A-Za-z]+$`)
)

// APIError Telegram API 业务错误
type APIError struct {
	Code        int
	Description string
}

func (e *APIError) Error() string {
	if e == nil {
		return "telegram api error"
	}
	return fmt.Sprintf("Telegram API %d: %s", e.Code, e.Description)
}

// TransportError 网络层错误（不确认游标）
type TransportError struct {
	Message string
}

func (e *TransportError) Error() string {
	if e == nil {
		return "telegram transport error"
	}
	return e.Message
}

// Client Bot API HTTP 客户端；trust_env=false
type Client struct {
	token     string
	base      string
	maxBytes  int
	http      *http.Client
	pollExtra time.Duration
}

// NewClient 构造客户端；proxy 空则直连
func NewClient(token, baseURL, proxy string, pollTimeoutSec, maxBytes int) (*Client, error) {
	token = strings.TrimSpace(token)
	if !tokenPattern.MatchString(token) {
		return nil, fmt.Errorf("Telegram Token 格式无效")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("Bot API 地址必须是无凭据、无查询参数的 HTTP(S) 地址")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, fmt.Errorf("Bot API 地址必须是无凭据、无查询参数的 HTTP(S) 地址")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("Bot API 地址必须是无凭据、无查询参数的 HTTP(S) 地址")
	}
	proxy = strings.TrimSpace(proxy)
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	// trust_env=false：默认不读环境代理
	proxyFunc := func(*http.Request) (*url.URL, error) { return nil, nil }
	dialContext := dialer.DialContext
	if proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("代理地址无效")
		}
		switch proxyURL.Scheme {
		case "http", "https":
			proxyFunc = http.ProxyURL(proxyURL)
		case "socks5", "socks5h":
			socksDialer, err := xproxy.FromURL(proxyURL, dialer)
			if err != nil {
				return nil, fmt.Errorf("代理地址无效")
			}
			if contextDialer, ok := socksDialer.(xproxy.ContextDialer); ok {
				dialContext = contextDialer.DialContext
			} else {
				dialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					return socksDialer.Dial(network, addr)
				}
			}
		default:
			return nil, fmt.Errorf("代理仅支持 HTTP(S)/SOCKS5")
		}
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxDownloadMB * 1024 * 1024
	}
	if pollTimeoutSec <= 0 {
		pollTimeoutSec = DefaultPollTimeoutSec
	}
	timeout := time.Duration(pollTimeoutSec+15) * time.Second
	if timeout < 60*time.Second {
		timeout = 60 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 proxyFunc,
		DialContext:           dialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &Client{
		token:    token,
		base:     baseURL,
		maxBytes: maxBytes,
		http: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		pollExtra: time.Duration(pollTimeoutSec) * time.Second,
	}, nil
}

func (c *Client) redact(text string) string {
	if c == nil {
		return text
	}
	out := strings.ReplaceAll(text, c.token, "<token>")
	if len(out) > 300 {
		out = out[:300]
	}
	return out
}

// Close 关闭底层连接
func (c *Client) Close() {
	if c == nil || c.http == nil {
		return
	}
	c.http.CloseIdleConnections()
}

// UploadField 单文件上传字段
type UploadField struct {
	Field    string
	Filename string
	MIME     string
	Data     []byte
}

// Call 调用 Bot API；仅 429 按 retry_after 重试
func (c *Client) Call(ctx context.Context, method string, body map[string]any, uploads []UploadField) (any, error) {
	if c == nil {
		return nil, fmt.Errorf("telegram client 为空")
	}
	if !methodPattern.MatchString(method) {
		return nil, fmt.Errorf("无效的 Telegram API 方法名")
	}
	if body == nil {
		body = map[string]any{}
	}
	endpoint := fmt.Sprintf("%s/bot%s/%s", c.base, c.token, method)
	for attempt := 0; attempt < 3; attempt++ {
		var req *http.Request
		var err error
		if len(uploads) == 0 {
			payload, marshalErr := json.Marshal(body)
			if marshalErr != nil {
				return nil, marshalErr
			}
			req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
			if err != nil {
				return nil, &TransportError{Message: fmt.Sprintf("Telegram 网络请求失败（%T）", err)}
			}
			req.Header.Set("Content-Type", "application/json")
		} else {
			var buf bytes.Buffer
			w := multipart.NewWriter(&buf)
			for key, value := range body {
				fieldValue, encodeErr := encodeFormValue(value)
				if encodeErr != nil {
					_ = w.Close()
					return nil, encodeErr
				}
				if err := w.WriteField(key, fieldValue); err != nil {
					_ = w.Close()
					return nil, err
				}
			}
			for _, upload := range uploads {
				header := make(textproto.MIMEHeader)
				header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": upload.Field, "filename": upload.Filename}))
				header.Set("Content-Type", upload.MIME)
				part, createErr := w.CreatePart(header)
				if createErr != nil {
					_ = w.Close()
					return nil, createErr
				}
				if _, writeErr := part.Write(upload.Data); writeErr != nil {
					_ = w.Close()
					return nil, writeErr
				}
			}
			if err := w.Close(); err != nil {
				return nil, err
			}
			req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
			if err != nil {
				return nil, &TransportError{Message: fmt.Sprintf("Telegram 网络请求失败（%T）", err)}
			}
			req.Header.Set("Content-Type", w.FormDataContentType())
		}
		resp, doErr := c.http.Do(req)
		if doErr != nil {
			return nil, &TransportError{Message: fmt.Sprintf("Telegram 网络请求失败（%T）", doErr)}
		}
		bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, &TransportError{Message: fmt.Sprintf("Telegram 网络请求失败（%T）", readErr)}
		}
		result, err := platformio.DecodeWireObject(bodyBytes)
		if err != nil {
			return nil, &APIError{Code: resp.StatusCode, Description: "响应不是 JSON"}
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if ok, _ := result["ok"].(bool); ok {
				return result["result"], nil
			}
		}
		code := resp.StatusCode
		if n, ok := asInt(result["error_code"]); ok {
			code = n
		}
		delay := 0
		if params, ok := asMap(result["parameters"]); ok {
			if n, ok := asInt64(params["retry_after"]); ok && n > 0 && n <= 300 {
				delay = int(n)
			}
		}
		if code == 429 && delay > 0 && delay <= 300 && attempt < 2 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(delay) * time.Second):
			}
			continue
		}
		desc := asString(result["description"])
		if desc == "" {
			desc = "请求失败"
		}
		return nil, &APIError{Code: code, Description: c.redact(desc)}
	}
	return nil, fmt.Errorf("Telegram API 重试次数耗尽")
}

func encodeFormValue(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", nil
	case string:
		return typed, nil
	case bool:
		if typed {
			return "true", nil
		}
		return "false", nil
	case int, int64, float64, float32:
		return fmt.Sprint(typed), nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
}

// Download 按 file_id 下载媒体
func (c *Client) Download(ctx context.Context, fileID string) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("telegram client 为空")
	}
	fileAny, err := c.Call(ctx, "getFile", map[string]any{"file_id": fileID}, nil)
	if err != nil {
		return nil, err
	}
	file, ok := asMap(fileAny)
	if !ok {
		return nil, fmt.Errorf("getFile 响应无效")
	}
	if size, ok := asInt(file["file_size"]); ok && size > c.maxBytes {
		return nil, fmt.Errorf("Telegram 媒体超过配置的下载上限")
	}
	path, _ := file["file_path"].(string)
	if path == "" || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") {
		return nil, fmt.Errorf("Telegram 未返回有效的相对文件路径；本地 Bot API 的 --local 模式暂不支持")
	}
	pieces := strings.Split(path, "/")
	for i, piece := range pieces {
		if piece == ".." {
			return nil, fmt.Errorf("Telegram 未返回有效的相对文件路径；本地 Bot API 的 --local 模式暂不支持")
		}
		pieces[i] = url.PathEscape(piece)
	}
	fileURL := fmt.Sprintf("%s/file/bot%s/%s", c.base, c.token, strings.Join(pieces, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, &TransportError{Message: fmt.Sprintf("Telegram 媒体下载失败（%T）", err)}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &TransportError{Message: fmt.Sprintf("Telegram 媒体下载失败（%T）", err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Code: resp.StatusCode, Description: "媒体下载失败"}
	}
	limited := io.LimitReader(resp.Body, int64(c.maxBytes)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, &TransportError{Message: fmt.Sprintf("Telegram 媒体下载失败（%T）", err)}
	}
	if len(data) > c.maxBytes {
		return nil, fmt.Errorf("Telegram 媒体超过配置的下载上限")
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("Telegram 下载的媒体为空")
	}
	return data, nil
}
