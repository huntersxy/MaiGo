package hostports

import (
	"context"

	"maibot-go/internal/plugin_runtime"
	"maibot-go/internal/services/html_render"
	"maibot-go/internal/web_search"
)

// Render HTML 渲染端口
type Render struct {
	service *htmlrender.Service
}

// NewRender 构造渲染端口
func NewRender(service *htmlrender.Service) *Render {
	return &Render{service: service}
}

// RenderHTMLToPNG 实现 pluginruntime.PluginRenderPort
func (r *Render) RenderHTMLToPNG(ctx context.Context, req pluginruntime.PluginRenderRequest) (map[string]any, error) {
	if r == nil || r.service == nil {
		return nil, errPort("render.html2png")
	}
	result, err := r.service.RenderHTMLToPNG(ctx, htmlrender.Request{
		HTML:              req.HTML,
		Selector:          req.Selector,
		ViewportWidth:     req.ViewportWidth,
		ViewportHeight:    req.ViewportHeight,
		DeviceScaleFactor: req.DeviceScaleFactor,
		FullPage:          req.FullPage,
		OmitBackground:    req.OmitBackground,
		WaitUntil:         req.WaitUntil,
		WaitForSelector:   req.WaitForSelector,
		WaitForTimeoutMs:  req.WaitForTimeoutMS,
		TimeoutMs:         req.TimeoutMS,
		AllowNetwork:      req.AllowNetwork,
	})
	if err != nil {
		return nil, err
	}
	return result.Payload(), nil
}

// Web 联网搜索端口
type Web struct {
	service *websearch.Service
}

// NewWeb 构造联网端口
func NewWeb(service *websearch.Service) *Web {
	return &Web{service: service}
}

// Search 实现 pluginruntime.PluginWebPort
func (w *Web) Search(ctx context.Context, _ string, args map[string]any) (map[string]any, error) {
	if w == nil || w.service == nil {
		return nil, errPort("web.search")
	}
	query := firstNonEmpty(asString(args["query"]), asString(args["q"]))
	maxResults := intFromAny(args["max_results"], 5)
	result, err := w.service.Search(ctx, query, maxResults)
	if err != nil {
		return nil, err
	}
	hits := make([]map[string]any, 0, len(result.Results))
	for _, hit := range result.Results {
		hits = append(hits, map[string]any{
			"title":        hit.Title,
			"url":          hit.URL,
			"snippet":      hit.Snippet,
			"summary":      hit.Summary,
			"published_at": hit.PublishedAt,
		})
	}
	return map[string]any{"success": true, "results": hits, "content_kind": result.ContentKind}, nil
}

// Fetch 实现 pluginruntime.PluginWebPort
func (w *Web) Fetch(ctx context.Context, _ string, args map[string]any) (map[string]any, error) {
	if w == nil || w.service == nil {
		return nil, errPort("web.fetch")
	}
	pageURL := firstNonEmpty(asString(args["url"]), asString(args["page_url"]))
	page, err := w.service.Fetch(ctx, pageURL)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"success":      true,
		"url":          page.URL,
		"title":        page.Title,
		"content":      page.Content,
		"content_kind": page.ContentKind,
		"og_image":     page.OGImage,
		"links":        page.Links,
		"images":       page.Images,
	}, nil
}
