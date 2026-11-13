package news

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *Service) fetchFromSource(ctx context.Context) (*Response, error) {
	if strings.TrimSpace(s.cfg.SourceURL) == "" {
		return nil, ErrSourceNotConfigured
	}

	reqURL, err := buildNewsURL(s.cfg.SourceURL, s.cfg.ItemLimit)
	if err != nil {
		return nil, &SourceError{Msg: err.Error()}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &SourceError{Msg: fmt.Sprintf("资讯请求构建失败：%v", err)}
	}
	req.Header.Set("Accept", "application/json, application/rss+xml, application/xml, text/xml, */*")

	// 对齐 Python src/services/news_service.py:75 的 httpx.AsyncClient 默认 follow_redirects=False
	client := &http.Client{
		Timeout:       s.cfg.RequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &SourceError{Msg: fmt.Sprintf("资讯服务请求失败：%T: %v", err, err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &SourceError{Msg: fmt.Sprintf("资讯服务返回 HTTP %d", resp.StatusCode)}
	}

	body, err := readBodyLimited(resp.Body, 4<<20)
	if err != nil {
		if se, ok := err.(*SourceError); ok {
			return nil, se
		}
		return nil, &SourceError{Msg: fmt.Sprintf("读取资讯响应失败：%v", err)}
	}

	parsed, err := parsePayload(body, resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	if parsed.UpdatedAt == "" {
		parsed.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return parsed, nil
}

func buildNewsURL(base string, limit int) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/"))
	if err != nil {
		return "", fmt.Errorf("资讯源 URL 无效：%w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("资讯源 URL 缺少 scheme 或 host")
	}
	q := u.Query()
	if limit > 0 && q.Get("limit") == "" {
		q.Set("limit", strconv.Itoa(limit))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func cloneResponse(r *Response) *Response {
	if r == nil {
		return nil
	}
	out := *r
	out.Items = append([]Item(nil), r.Items...)
	return &out
}
