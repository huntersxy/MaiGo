package adapters

import (
	"context"
	"strings"
)

const (
	defaultStepSearchTool      = "web_search"
	defaultStepSearchFetchTool = "web_fetch"
)

// StepSearchProvider 经 MCP 调用 StepSearch 暴露的 web_search / web_fetch
type StepSearchProvider struct {
	invoker    MCPToolInvoker
	server     string
	category   string
	searchTool string
	fetchTool  string
}

// NewStepSearchProvider 创建 StepSearch MCP 供应商
func NewStepSearchProvider(invoker MCPToolInvoker, server, category, searchTool, fetchTool string) *StepSearchProvider {
	if strings.TrimSpace(searchTool) == "" {
		searchTool = defaultStepSearchTool
	}
	if strings.TrimSpace(fetchTool) == "" {
		fetchTool = defaultStepSearchFetchTool
	}
	return &StepSearchProvider{
		invoker:    invoker,
		server:     strings.TrimSpace(server),
		category:   strings.TrimSpace(category),
		searchTool: strings.TrimSpace(searchTool),
		fetchTool:  strings.TrimSpace(fetchTool),
	}
}

func (p *StepSearchProvider) Name() string { return "stepsearch" }

func (p *StepSearchProvider) Search(ctx context.Context, query string, maxResults int) (*SearchResult, error) {
	args := map[string]any{"query": query, "n": maxResults}
	if p.category != "" {
		args["category"] = p.category
	}
	data, err := p.call(ctx, p.searchTool, args)
	if err != nil {
		return nil, err
	}
	rawResults, ok := data["results"].([]any)
	if !ok {
		return nil, NewError("StepSearch MCP 返回格式错误，缺少 results 数组")
	}
	hits := make([]SearchHit, 0, len(rawResults))
	for _, item := range rawResults {
		if len(hits) >= maxResults {
			break
		}
		row, ok := item.(map[string]any)
		if !ok {
			return nil, NewError("StepSearch MCP 返回格式错误，搜索结果不是对象")
		}
		title, ok := row["title"].(string)
		if !ok {
			return nil, NewError("StepSearch MCP 返回格式错误，搜索标题不是字符串")
		}
		link, ok := row["url"].(string)
		if !ok {
			return nil, NewError("StepSearch MCP 返回格式错误，搜索地址不是字符串")
		}
		snippet, ok := row["snippet"].(string)
		if !ok {
			return nil, NewError("StepSearch MCP 返回格式错误，搜索摘要不是字符串")
		}
		hit := SearchHit{Title: title, URL: link, Snippet: snippet}
		if published, ok := row["time"].(string); ok {
			hit.PublishedAt = published
		}
		hits = append(hits, hit)
	}
	return &SearchResult{Query: query, Results: hits, ContentKind: "search_results"}, nil
}

func (p *StepSearchProvider) Fetch(ctx context.Context, pageURL string) (*PageResult, error) {
	data, err := p.call(ctx, p.fetchTool, map[string]any{"url": pageURL})
	if err != nil {
		return nil, err
	}
	page, ok := data["page"].(map[string]any)
	if !ok {
		return nil, NewError("StepSearch MCP 返回格式错误，缺少 page 对象")
	}
	link, ok := page["url"].(string)
	if !ok {
		return nil, NewError("StepSearch MCP 返回格式错误，页面地址不是字符串")
	}
	title, ok := page["title"].(string)
	if !ok {
		return nil, NewError("StepSearch MCP 返回格式错误，页面标题不是字符串")
	}
	content, ok := page["markdown"].(string)
	if !ok {
		return nil, NewError("StepSearch MCP 返回格式错误，页面正文不是字符串")
	}
	return &PageResult{
		URL:         link,
		Title:       title,
		Content:     content,
		ContentKind: "page_text",
	}, nil
}

func (p *StepSearchProvider) call(ctx context.Context, toolName string, arguments map[string]any) (map[string]any, error) {
	if p == nil || p.invoker == nil {
		return nil, ErrNotConfigured
	}
	if p.server == "" || toolName == "" {
		return nil, ErrNotConfigured
	}
	data, err := p.invoker.CallTool(ctx, p.server, toolName, arguments)
	if err != nil {
		return nil, NewError("StepSearch MCP 调用失败，请检查该 MCP 连接的状态和日志")
	}
	if data == nil {
		return nil, NewError("StepSearch MCP 返回格式错误，缺少结构化对象")
	}
	if rawCode, exists := data["code"]; exists {
		code, ok := asInt(rawCode)
		if !ok || code != 0 {
			return nil, NewError("StepSearch MCP 返回业务错误")
		}
	}
	return data, nil
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case float32:
		return int(n), true
	default:
		return 0, false
	}
}

func stepSearchConfigured(cfg StepSearchConfig) bool {
	// Python 仅要求 server_ref；工具名固定默认 web_search/web_fetch
	return strings.TrimSpace(cfg.MCPServer) != ""
}

func newStepSearchProvider(cfg StepSearchConfig, invoker MCPToolInvoker) (*StepSearchProvider, error) {
	if invoker == nil {
		return nil, ErrNotConfigured
	}
	if !stepSearchConfigured(cfg) {
		return nil, ErrNotConfigured
	}
	fetchTool := strings.TrimSpace(cfg.FetchTool)
	if fetchTool == "" {
		fetchTool = defaultStepSearchFetchTool
	}
	return NewStepSearchProvider(
		invoker,
		cfg.MCPServer,
		cfg.Category,
		cfg.SearchTool,
		fetchTool,
	), nil
}

func stepSearchFetchConfigured(cfg StepSearchConfig) bool {
	server := strings.TrimSpace(cfg.MCPServer)
	toolName := strings.TrimSpace(cfg.FetchTool)
	if toolName == "" {
		toolName = defaultStepSearchFetchTool
	}
	return server != "" && toolName != ""
}

func newStepSearchFetchProvider(cfg StepSearchConfig, invoker MCPToolInvoker) (FetchProvider, error) {
	if invoker == nil || !stepSearchFetchConfigured(cfg) {
		return nil, ErrNotConfigured
	}
	fetchTool := strings.TrimSpace(cfg.FetchTool)
	if fetchTool == "" {
		fetchTool = defaultStepSearchFetchTool
	}
	searchTool := strings.TrimSpace(cfg.SearchTool)
	if searchTool == "" {
		searchTool = defaultStepSearchTool
	}
	return NewStepSearchProvider(
		invoker,
		cfg.MCPServer,
		cfg.Category,
		searchTool,
		fetchTool,
	), nil
}
