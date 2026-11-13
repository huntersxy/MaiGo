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

const deepseekMaxQueryRunes = 128

// DeepSeekProvider 调用 DeepSeek Search / Fetch API（OpenAI 兼容根地址）
type DeepSeekProvider struct {
	baseURL string
	apiKey  string
	timeout time.Duration
	client  *http.Client
}

// NewDeepSeekProvider 创建 DeepSeek 供应商
func NewDeepSeekProvider(baseURL, apiKey string, timeout time.Duration, proxyURL ...string) *DeepSeekProvider {
	if timeout <= 0 {
		timeout = DefaultWebTimeout
	}
	return &DeepSeekProvider{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  strings.TrimSpace(apiKey),
		timeout: timeout,
		client:  NewHTTPClient(timeout, firstProxy(proxyURL)),
	}
}

func firstProxy(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (d DeepSeekProvider) Name() string { return "deepseek" }

func (d DeepSeekProvider) endpoint(action string) string {
	base := d.baseURL
	if base == "" {
		base = "https://api.deepseek.com"
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + "/tools/" + action
}

func (d DeepSeekProvider) Search(ctx context.Context, query string, maxResults int) (*SearchResult, error) {
	if utf8.RuneCountInString(query) > deepseekMaxQueryRunes {
		return nil, NewError("DeepSeek 搜索词不能超过 128 个字符，请缩短后重试")
	}
	data, err := d.post(ctx, d.endpoint("search"), map[string]any{
		"query":        query,
		"count":        maxResults,
		"need_content": false,
	})
	if err != nil {
		return nil, err
	}
	rawResults, ok := data["results"].([]any)
	if !ok {
		return nil, NewError("deepseek 返回数据不符合接口协议")
	}
	hits := make([]SearchHit, 0, len(rawResults))
	for _, item := range rawResults {
		if len(hits) >= maxResults {
			break
		}
		row, ok := item.(map[string]any)
		if !ok {
			return nil, NewError("deepseek 返回数据不符合接口协议")
		}
		title, ok := row["title"].(string)
		if !ok {
			return nil, NewError("deepseek 返回数据不符合接口协议")
		}
		link, ok := row["url"].(string)
		if !ok {
			return nil, NewError("deepseek 返回数据不符合接口协议")
		}
		snippet, ok := row["snippet"].(string)
		if !ok {
			return nil, NewError("deepseek 返回数据不符合接口协议")
		}
		hit := SearchHit{Title: title, URL: link, Snippet: snippet}
		if summary, ok := row["summary"].(string); ok {
			hit.Summary = summary
		}
		if date, ok := row["date"].(string); ok {
			hit.PublishedAt = date
		}
		hits = append(hits, hit)
	}
	return &SearchResult{Query: query, Results: hits, ContentKind: "search_results"}, nil
}

func (d DeepSeekProvider) Fetch(ctx context.Context, pageURL string) (*PageResult, error) {
	data, err := d.post(ctx, d.endpoint("fetch"), map[string]any{"url": pageURL})
	if err != nil {
		return nil, err
	}
	title, ok := data["title"].(string)
	if !ok {
		return nil, NewError("deepseek 返回数据不符合接口协议")
	}
	content, ok := data["content"].(string)
	if !ok {
		return nil, NewError("deepseek 返回数据不符合接口协议")
	}
	return &PageResult{URL: pageURL, Title: title, Content: content, ContentKind: "page_text"}, nil
}

func (d DeepSeekProvider) post(ctx context.Context, endpoint string, payload map[string]any) (map[string]any, error) {
	if d.apiKey == "" {
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
	req.Header.Set("Authorization", "Bearer "+d.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, NewError(fmt.Sprintf("联网服务连接失败：%T", err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
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
