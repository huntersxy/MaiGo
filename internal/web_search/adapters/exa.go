package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ExaProvider 调用 Exa Search / Contents API
type ExaProvider struct {
	baseURL string
	apiKey  string
	timeout time.Duration
	client  *http.Client
}

// NewExaProvider 创建 Exa 供应商
func NewExaProvider(baseURL, apiKey string, timeout time.Duration, proxyURL ...string) *ExaProvider {
	if timeout <= 0 {
		timeout = DefaultWebTimeout
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.exa.ai"
	}
	return &ExaProvider{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  strings.TrimSpace(apiKey),
		timeout: timeout,
		client:  NewHTTPClient(timeout, firstProxy(proxyURL)),
	}
}

func (e ExaProvider) Name() string { return "exa" }

func (e ExaProvider) endpoint(action string) string {
	return e.baseURL + "/" + action
}

func (e ExaProvider) Search(ctx context.Context, query string, maxResults int) (*SearchResult, error) {
	data, err := e.post(ctx, e.endpoint("search"), map[string]any{
		"query":      query,
		"numResults": maxResults,
		"type":       "auto",
		"contents":   map[string]any{"highlights": true},
	})
	if err != nil {
		return nil, err
	}
	rawResults, ok := data["results"].([]any)
	if !ok {
		return nil, NewError("exa 返回数据不符合接口协议")
	}
	hits := make([]SearchHit, 0, len(rawResults))
	for _, item := range rawResults {
		if len(hits) >= maxResults {
			break
		}
		row, ok := item.(map[string]any)
		if !ok {
			return nil, NewError("exa 返回数据不符合接口协议")
		}
		title, ok := row["title"].(string)
		if !ok {
			return nil, NewError("exa 返回数据不符合接口协议")
		}
		link, ok := row["url"].(string)
		if !ok {
			return nil, NewError("exa 返回数据不符合接口协议")
		}
		hit := SearchHit{
			Title:   title,
			URL:     link,
			Snippet: joinStringList(row["highlights"]),
		}
		if summary, ok := row["summary"].(string); ok {
			hit.Summary = summary
		}
		if published, ok := row["publishedDate"].(string); ok {
			hit.PublishedAt = published
		}
		hits = append(hits, hit)
	}
	return &SearchResult{Query: query, Results: hits, ContentKind: "search_results"}, nil
}

func (e ExaProvider) Fetch(ctx context.Context, pageURL string) (*PageResult, error) {
	data, err := e.post(ctx, e.endpoint("contents"), map[string]any{
		"urls": []string{pageURL},
		"text": true,
	})
	if err != nil {
		return nil, err
	}
	rawResults, _ := data["results"].([]any)
	if len(rawResults) == 0 {
		return nil, NewError("Exa 未能提取指定网页的正文")
	}
	statuses, _ := data["statuses"].([]any)
	for _, item := range statuses {
		row, ok := item.(map[string]any)
		if !ok {
			return nil, NewError("Exa 未能提取指定网页的正文")
		}
		status, ok := row["status"].(string)
		if !ok || status != "success" {
			return nil, NewError("Exa 未能提取指定网页的正文")
		}
	}
	row, ok := rawResults[0].(map[string]any)
	if !ok {
		return nil, NewError("Exa 未能提取指定网页的正文")
	}
	link, ok := row["url"].(string)
	if !ok {
		return nil, NewError("Exa 未能提取指定网页的正文")
	}
	title, ok := row["title"].(string)
	if !ok {
		return nil, NewError("Exa 未能提取指定网页的正文")
	}
	content, ok := row["text"].(string)
	if !ok || strings.TrimSpace(content) == "" {
		return nil, NewError("Exa 未能提取指定网页的正文")
	}
	return &PageResult{URL: link, Title: title, Content: content, ContentKind: "page_text"}, nil
}

func (e ExaProvider) post(ctx context.Context, endpoint string, payload map[string]any) (map[string]any, error) {
	if e.apiKey == "" {
		return nil, NewError("联网服务缺少 API Key")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", e.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, NewError(fmt.Sprintf("联网服务连接失败：%T", err))
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, NewError("联网服务连接失败")
	}
	if len(data) > maxResponseBytes {
		return nil, NewError("联网服务响应超过 8 MiB 限制")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, NewError(fmt.Sprintf("联网服务返回 HTTP %d", resp.StatusCode))
	}
	result, err := DecodeWebJSONObject(data)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func joinStringList(raw any) string {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}
