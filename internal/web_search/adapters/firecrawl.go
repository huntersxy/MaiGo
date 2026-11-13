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
	"unicode/utf8"
)

const firecrawlMaxQueryRunes = 500

// FirecrawlProvider 调用 Firecrawl v2 Search / Scrape API
type FirecrawlProvider struct {
	baseURL string
	apiKey  string
	timeout time.Duration
	client  *http.Client
}

// NewFirecrawlProvider 创建 Firecrawl 供应商
func NewFirecrawlProvider(baseURL, apiKey string, timeout time.Duration, proxyURL ...string) *FirecrawlProvider {
	if timeout <= 0 {
		timeout = DefaultWebTimeout
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.firecrawl.dev"
	}
	return &FirecrawlProvider{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  strings.TrimSpace(apiKey),
		timeout: timeout,
		client:  NewHTTPClient(timeout, firstProxy(proxyURL)),
	}
}

func (f FirecrawlProvider) Name() string { return "firecrawl" }

func (f FirecrawlProvider) endpoint(action string) string {
	base := f.baseURL
	if !strings.HasSuffix(base, "/v2") {
		base += "/v2"
	}
	return base + "/" + action
}

func (f FirecrawlProvider) Search(ctx context.Context, query string, maxResults int) (*SearchResult, error) {
	if utf8.RuneCountInString(query) > firecrawlMaxQueryRunes {
		return nil, NewError("Firecrawl 的 query 不能超过 500 个字符")
	}
	data, err := f.post(ctx, f.endpoint("search"), map[string]any{
		"query":   query,
		"limit":   maxResults,
		"sources": []string{"web"},
	})
	if err != nil {
		return nil, err
	}
	if !jsonBool(data["success"]) {
		return nil, NewError("Firecrawl 返回业务错误")
	}
	payload, ok := data["data"].(map[string]any)
	if !ok {
		return nil, NewError("firecrawl 返回数据不符合接口协议")
	}
	rawWeb, ok := payload["web"].([]any)
	if !ok {
		return nil, NewError("firecrawl 返回数据不符合接口协议")
	}
	hits := make([]SearchHit, 0, len(rawWeb))
	for _, item := range rawWeb {
		if len(hits) >= maxResults {
			break
		}
		row, ok := item.(map[string]any)
		if !ok {
			return nil, NewError("firecrawl 返回数据不符合接口协议")
		}
		title, ok := row["title"].(string)
		if !ok {
			return nil, NewError("firecrawl 返回数据不符合接口协议")
		}
		link, ok := row["url"].(string)
		if !ok {
			return nil, NewError("firecrawl 返回数据不符合接口协议")
		}
		snippet, ok := row["description"].(string)
		if !ok {
			return nil, NewError("firecrawl 返回数据不符合接口协议")
		}
		hits = append(hits, SearchHit{Title: title, URL: link, Snippet: snippet})
	}
	return &SearchResult{Query: query, Results: hits, ContentKind: "search_results"}, nil
}

func (f FirecrawlProvider) Fetch(ctx context.Context, pageURL string) (*PageResult, error) {
	data, err := f.post(ctx, f.endpoint("scrape"), map[string]any{
		"url":     pageURL,
		"formats": []string{"markdown"},
	})
	if err != nil {
		return nil, err
	}
	if !jsonBool(data["success"]) {
		return nil, NewError("Firecrawl 未能提取指定网页的正文")
	}
	item, ok := data["data"].(map[string]any)
	if !ok {
		return nil, NewError("Firecrawl 未能提取指定网页的正文")
	}
	content, ok := item["markdown"].(string)
	if !ok {
		return nil, NewError("firecrawl 返回数据不符合接口协议")
	}
	title := ""
	if meta, ok := item["metadata"].(map[string]any); ok {
		title, _ = meta["title"].(string)
	}
	if strings.TrimSpace(content) == "" {
		return nil, NewError("Firecrawl 未能提取指定网页的正文")
	}
	return &PageResult{URL: pageURL, Title: title, Content: content, ContentKind: "page_text"}, nil
}

func (f FirecrawlProvider) post(ctx context.Context, endpoint string, payload map[string]any) (map[string]any, error) {
	if f.apiKey == "" {
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
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
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

func jsonBool(raw any) bool {
	switch v := raw.(type) {
	case bool:
		return v
	default:
		return false
	}
}
