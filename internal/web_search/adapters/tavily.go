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

const maxResponseBytes = 8 * 1024 * 1024

// TavilyProvider 调用 Tavily Search / Extract API
type TavilyProvider struct {
	baseURL      string
	apiKey       string
	timeout      time.Duration
	searchDepth  string
	extractDepth string
	client       *http.Client
}

// NewTavilyProvider 创建 Tavily 供应商
func NewTavilyProvider(baseURL, apiKey string, timeout time.Duration, proxyURL ...string) *TavilyProvider {
	if timeout <= 0 {
		timeout = DefaultWebTimeout
	}
	return &TavilyProvider{
		baseURL:      strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:       strings.TrimSpace(apiKey),
		timeout:      timeout,
		searchDepth:  "basic",
		extractDepth: "basic",
		client:       NewHTTPClient(timeout, firstProxy(proxyURL)),
	}
}

// WithDepths 设置 Tavily 搜索/提取深度
func (t *TavilyProvider) WithDepths(searchDepth, extractDepth string) *TavilyProvider {
	if t == nil {
		return t
	}
	if depth := strings.ToLower(strings.TrimSpace(searchDepth)); depth != "" {
		t.searchDepth = depth
	}
	if depth := strings.ToLower(strings.TrimSpace(extractDepth)); depth != "" {
		t.extractDepth = depth
	}
	return t
}

func (t TavilyProvider) Name() string { return "tavily" }

func (t TavilyProvider) endpoint(action string) string {
	base := t.baseURL
	if base == "" {
		base = "https://api.tavily.com"
	}
	return base + "/" + action
}

func (t TavilyProvider) Search(ctx context.Context, query string, maxResults int) (*SearchResult, error) {
	data, err := t.post(ctx, t.endpoint("search"), map[string]any{
		"query":               query,
		"max_results":         maxResults,
		"search_depth":        t.searchDepth,
		"include_answer":      false,
		"include_raw_content": false,
	})
	if err != nil {
		return nil, err
	}
	rawResults, ok := data["results"].([]any)
	if !ok {
		return nil, NewError("tavily 返回数据不符合接口协议")
	}
	hits := make([]SearchHit, 0, len(rawResults))
	for _, item := range rawResults {
		if len(hits) >= maxResults {
			break
		}
		row, ok := item.(map[string]any)
		if !ok {
			return nil, NewError("tavily 返回数据不符合接口协议")
		}
		title, ok := row["title"].(string)
		if !ok {
			return nil, NewError("tavily 返回数据不符合接口协议")
		}
		link, ok := row["url"].(string)
		if !ok {
			return nil, NewError("tavily 返回数据不符合接口协议")
		}
		snippet, ok := row["content"].(string)
		if !ok {
			return nil, NewError("tavily 返回数据不符合接口协议")
		}
		hit := SearchHit{Title: title, URL: link, Snippet: snippet}
		if published, ok := row["published_date"].(string); ok {
			hit.PublishedAt = published
		}
		hits = append(hits, hit)
	}
	return &SearchResult{Query: query, Results: hits, ContentKind: "search_results"}, nil
}

func (t TavilyProvider) Fetch(ctx context.Context, pageURL string) (*PageResult, error) {
	data, err := t.post(ctx, t.endpoint("extract"), map[string]any{
		"urls":          []string{pageURL},
		"extract_depth": t.extractDepth,
		"format":        "markdown",
	})
	if err != nil {
		return nil, err
	}
	if failed, _ := data["failed_results"].([]any); len(failed) > 0 {
		return nil, NewError("Tavily 未能提取指定网页的正文")
	}
	rawResults, _ := data["results"].([]any)
	if len(rawResults) == 0 {
		return nil, NewError("Tavily 未能提取指定网页的正文")
	}
	row, ok := rawResults[0].(map[string]any)
	if !ok {
		return nil, NewError("tavily 返回数据不符合接口协议")
	}
	content, ok := row["raw_content"].(string)
	if !ok {
		return nil, NewError("tavily 返回数据不符合接口协议")
	}
	link, ok := row["url"].(string)
	if !ok || strings.TrimSpace(link) == "" {
		return nil, NewError("tavily 返回数据不符合接口协议")
	}
	title, _ := row["title"].(string)
	if strings.TrimSpace(content) == "" {
		return nil, NewError("Tavily 未能提取指定网页的正文")
	}
	return &PageResult{URL: link, Title: title, Content: content, ContentKind: "page_text"}, nil
}

func (t TavilyProvider) post(ctx context.Context, endpoint string, payload map[string]any) (map[string]any, error) {
	if t.apiKey == "" {
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
	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
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
