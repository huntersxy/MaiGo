package adapters

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"MaiGo/internal/llm_models/model_client"
)

// PluginWebProvider 经插件 LLM Provider 执行联网搜索与抓取，对齐 src/web_search/adapters/plugin.py
type PluginWebProvider struct {
	clientType string
	webOps     map[string]struct{}
	invoke     func(ctx context.Context, operation string, args map[string]any) (map[string]any, error)
}

// NewPluginWebProvider 构造插件联网适配器；service 的连接信息独立于同名模型提供商
func NewPluginWebProvider(serviceName, adapter, baseURL, apiKey string, timeout time.Duration) (*PluginWebProvider, error) {
	pluginID, clientType, formats, ok := model_client.FindPluginWebProvider(adapter)
	if !ok {
		return nil, NewError(fmt.Sprintf("联网插件 %s 未加载或未声明联网能力", adapter))
	}
	_, invoker, ok := model_client.PluginLLMInvokerForClientType(clientType)
	if !ok || invoker == nil {
		return nil, NewError("联网插件未注册可调用的客户端")
	}
	if timeout <= 0 {
		timeout = DefaultWebTimeout
	}
	cfg := model_client.ModelConfig{
		ClientType:   clientType,
		ProviderName: strings.TrimSpace(serviceName),
		BaseURL:      strings.TrimSpace(baseURL),
		APIKey:       strings.TrimSpace(apiKey),
		PluginID:     pluginID,
		Timeout:      time.Duration(math.Ceil(timeout.Seconds())) * time.Second,
	}
	webOps := map[string]struct{}{}
	for _, operation := range formats.Web {
		webOps[operation] = struct{}{}
	}
	return &PluginWebProvider{
		clientType: clientType,
		webOps:     webOps,
		invoke: func(ctx context.Context, operation string, args map[string]any) (map[string]any, error) {
			return model_client.InvokePluginWebOperation(ctx, cfg, pluginID, invoker, operation, args)
		},
	}, nil
}

func (p *PluginWebProvider) Name() string { return "plugin:" + p.clientType }

func (p *PluginWebProvider) ensureOperation(operation string) error {
	if _, ok := p.webOps[operation]; !ok {
		return NewError(fmt.Sprintf("联网插件未声明操作 %s", operation))
	}
	return nil
}

func (p *PluginWebProvider) Search(ctx context.Context, query string, count int) (*SearchResult, error) {
	if err := p.ensureOperation("web_search"); err != nil {
		return nil, err
	}
	result, err := p.invoke(ctx, "web_search", map[string]any{"query": query, "max_results": count})
	if err != nil {
		return nil, mapPluginWebError("web_search", err)
	}
	rawHits, ok := result["results"].([]any)
	if !ok {
		return nil, NewError("联网插件的 results 必须是数组")
	}
	hits := make([]SearchHit, 0, len(rawHits))
	for _, entry := range rawHits[:min(len(rawHits), count)] {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		hits = append(hits, SearchHit{
			Title:       stringField(item, "title"),
			URL:         stringField(item, "url"),
			Snippet:     stringField(item, "snippet"),
			Summary:     stringField(item, "summary"),
			PublishedAt: stringField(item, "published_at"),
		})
	}
	return &SearchResult{Query: query, Results: hits, ContentKind: "search_results"}, nil
}

func (p *PluginWebProvider) Fetch(ctx context.Context, pageURL string) (*PageResult, error) {
	if err := p.ensureOperation("web_fetch"); err != nil {
		return nil, err
	}
	result, err := p.invoke(ctx, "web_fetch", map[string]any{"url": pageURL})
	if err != nil {
		return nil, mapPluginWebError("web_fetch", err)
	}
	return &PageResult{
		URL:         stringField(result, "url"),
		Title:       stringField(result, "title"),
		Content:     stringField(result, "content"),
		ContentKind: "page_text",
	}, nil
}

func mapPluginWebError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*model_client.ParseError); ok {
		return NewError(fmt.Sprintf("联网插件调用 %s 失败，请查看插件日志", operation))
	}
	return err
}

func stringField(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if value, ok := payload[key]; ok {
		return strings.TrimSpace(fmt.Sprint(value))
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}