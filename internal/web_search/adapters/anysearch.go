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

const anySearchMaxResults = 10

// AnySearchProvider 调用 AnySearch v1 Search / Extract API
type AnySearchProvider struct {
	baseURL string
	apiKey  string
	timeout time.Duration
	client  *http.Client
}

// NewAnySearchProvider 创建 AnySearch 供应商
func NewAnySearchProvider(baseURL, apiKey string, timeout time.Duration, proxyURL ...string) *AnySearchProvider {
	if timeout <= 0 {
		timeout = DefaultWebTimeout
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.anysearch.com"
	}
	return &AnySearchProvider{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  strings.TrimSpace(apiKey),
		timeout: timeout,
		client:  NewHTTPClient(timeout, firstProxy(proxyURL)),
	}
}

func (a AnySearchProvider) Name() string { return "anysearch" }

func (a AnySearchProvider) endpoint(action string) string {
	base := a.baseURL
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + "/" + action
}

func (a AnySearchProvider) Search(ctx context.Context, query string, maxResults int) (*SearchResult, error) {
	if maxResults > anySearchMaxResults {
		return nil, NewError("AnySearch 的 max_results 不能超过 10")
	}
	data, err := a.post(ctx, a.endpoint("search"), map[string]any{
		"query":       query,
		"max_results": maxResults,
	})
	if err != nil {
		return nil, err
	}
	rawResults, ok := data["results"].([]any)
	if !ok {
		return nil, NewError("anysearch 返回数据不符合接口协议")
	}
	hits := make([]SearchHit, 0, len(rawResults))
	for _, item := range rawResults {
		if len(hits) >= maxResults {
			break
		}
		row, ok := item.(map[string]any)
		if !ok {
			return nil, NewError("anysearch 返回数据不符合接口协议")
		}
		title, ok := row["title"].(string)
		if !ok {
			return nil, NewError("anysearch 返回数据不符合接口协议")
		}
		link, ok := row["url"].(string)
		if !ok {
			return nil, NewError("anysearch 返回数据不符合接口协议")
		}
		snippet, _ := row["snippet"].(string)
		hits = append(hits, SearchHit{Title: title, URL: link, Snippet: snippet})
	}
	return &SearchResult{Query: query, Results: hits, ContentKind: "search_results"}, nil
}

func (a AnySearchProvider) Fetch(ctx context.Context, pageURL string) (*PageResult, error) {
	data, err := a.post(ctx, a.endpoint("extract"), map[string]any{"url": pageURL})
	if err != nil {
		return nil, err
	}
	link, ok := data["url"].(string)
	if !ok {
		return nil, NewError("anysearch 返回数据不符合接口协议")
	}
	title, ok := data["title"].(string)
	if !ok {
		return nil, NewError("anysearch 返回数据不符合接口协议")
	}
	content, ok := data["content"].(string)
	if !ok || strings.TrimSpace(content) == "" {
		return nil, NewError("AnySearch 未能提取指定网页的正文")
	}
	return &PageResult{URL: link, Title: title, Content: content, ContentKind: "page_text"}, nil
}

func (a AnySearchProvider) post(ctx context.Context, endpoint string, payload map[string]any) (map[string]any, error) {
	if a.apiKey == "" {
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
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
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
	envelope, err := DecodeWebJSONObject(data)
	if err != nil {
		return nil, err
	}
	rawCode, exists := envelope["code"]
	if !exists {
		return nil, NewError("anysearch 返回数据不符合接口协议")
	}
	code, ok := rawCode.(float64)
	if !ok {
		return nil, NewError("anysearch 返回数据不符合接口协议")
	}
	if int(code) != 0 {
		return nil, NewError("AnySearch 返回业务错误")
	}
	inner, ok := envelope["data"].(map[string]any)
	if !ok {
		return nil, NewError("AnySearch 返回业务错误")
	}
	return inner, nil
}
