package tools

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/web_search"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

type SearchWebInput struct {
	Query string `json:"query" jsonschema:"description=搜索词，最多 1000 字，部分供应商限制更短"`
	// MaxResults 为 0 时按 MaiBot 默认值 5 处理
	MaxResults int `json:"max_results,omitempty" jsonschema:"description=返回条数，默认 5；AnySearch 最多 10，其余最多 20"`
}

type FetchWebInput struct {
	URL string `json:"url" jsonschema:"description=要抓取的网页 HTTP/HTTPS 地址"`
}

type WebResult struct {
	Success    bool        `json:"success"`
	Action     string      `json:"action"`
	Title      string      `json:"title,omitempty"`
	Content    string      `json:"content,omitempty"`
	OGImage    string      `json:"og_image,omitempty"`
	Results    []WebSearch `json:"results,omitempty"`
	Links      []string    `json:"links,omitempty"`
	Images     []string    `json:"images,omitempty"`
	ResultRef  string      `json:"result_ref,omitempty"`
	Truncated  bool        `json:"truncated,omitempty"`
	TotalChars int         `json:"total_chars,omitempty"`
	NextOffset int         `json:"next_offset,omitempty"`
}

type WebSearch struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

var defaultWebService = websearch.DefaultService()

// SetWebSearchService 注入联网服务，便于测试或自定义供应商
func SetWebSearchService(svc *websearch.Service) {
	if svc != nil {
		defaultWebService = svc
	}
}

func searchWeb(ctx context.Context, input *SearchWebInput) (*WebResult, error) {
	if input == nil {
		return nil, fmt.Errorf("搜索词不能为空")
	}
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return nil, fmt.Errorf("搜索词不能为空")
	}
	maxResults := input.MaxResults
	if maxResults == 0 {
		maxResults = 5
	}
	if maxResults < 1 || maxResults > 20 {
		return nil, fmt.Errorf("max_results 必须是 1 到 20 的整数")
	}
	result, err := defaultWebService.Search(ctx, query, maxResults)
	if err != nil {
		return nil, webErr(err)
	}
	out := &WebResult{Success: true, Action: "search"}
	for _, item := range result.Results {
		out.Results = append(out.Results, WebSearch{
			Title: item.Title, URL: item.URL, Snippet: item.Snippet,
		})
	}
	if err := applyWebSearchPreview(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func fetchWeb(ctx context.Context, input *FetchWebInput) (*WebResult, error) {
	if input == nil || strings.TrimSpace(input.URL) == "" {
		return nil, fmt.Errorf("网页 URL 不能为空")
	}
	page, err := defaultWebService.Fetch(ctx, input.URL)
	if err != nil {
		return nil, webErr(err)
	}
	out := &WebResult{
		Success: true,
		Action:  "fetch",
		Title:   page.Title,
		Content: page.Content,
		OGImage: page.OGImage,
		Links:   page.Links,
		Images:  page.Images,
	}
	if err := applyWebFetchPreview(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func applyWebFetchPreview(ctx context.Context, out *WebResult) error {
	if out == nil || strings.TrimSpace(out.Content) == "" {
		return nil
	}
	previewChars, err := defaultWebService.PreviewChars()
	if err != nil {
		return webErr(err)
	}
	preview, err := PreviewStoredToolResult(ctx, out.Content, previewChars)
	if err != nil {
		return webErr(err)
	}
	out.Content = preview.Text
	out.ResultRef = preview.ResultRef
	out.Truncated = preview.Truncated
	out.TotalChars = preview.TotalRunes
	if preview.Truncated {
		// 对齐 MaiBot next_offset=len(preview)；不把截断省略号计入偏移
		out.NextOffset = previewChars
	}
	return nil
}

func applyWebSearchPreview(ctx context.Context, out *WebResult) error {
	if out == nil || len(out.Results) == 0 {
		return nil
	}
	var b strings.Builder
	for i, item := range out.Results {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s\n%s\n%s", item.Title, item.URL, item.Snippet)
	}
	previewChars, err := defaultWebService.PreviewChars()
	if err != nil {
		return webErr(err)
	}
	preview, err := PreviewStoredToolResult(ctx, b.String(), previewChars)
	if err != nil {
		return webErr(err)
	}
	if !preview.Truncated {
		return nil
	}
	out.Results = nil
	out.ResultRef = preview.ResultRef
	out.Truncated = preview.Truncated
	out.TotalChars = preview.TotalRunes
	out.NextOffset = previewChars
	out.Content = strings.TrimSpace(preview.Text)
	return nil
}

func webErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", err.Error())
}

func NewSearchWebTool() (tool.InvokableTool, error) {
	return utils.InferTool("searchWeb", "搜索网页并返回标题、链接和摘要。", searchWeb)
}

func NewFetchWebTool() (tool.InvokableTool, error) {
	return utils.InferTool("fetchWeb", "抓取网页完整正文、链接、图片 URL 和 og:image。", fetchWeb)
}
