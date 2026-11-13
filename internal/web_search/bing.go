package websearch

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	projectutils "maibot-go/internal/common/utils"

	"github.com/PuerkitoBio/goquery"
)

var (
	bingSpaceRE = regexp.MustCompile(`\s+`)
	bingCQRE    = regexp.MustCompile(`\[CQ:[^\]]*\]`)
)

// BingProvider 通过公开搜索页抓取结果，无需 API Key
type BingProvider struct{}

func (BingProvider) Name() string { return "bing" }

func (BingProvider) Search(ctx context.Context, query string, maxResults int) (*SearchResult, error) {
	query = cleanBingQuery(query)
	if query == "" {
		return nil, NewError("query 不能为空")
	}
	searchURL := "https://cn.bing.com/search?q=" + url.QueryEscape(query)
	body, _, err := projectutils.FetchPublicText(ctx, searchURL)
	if err != nil {
		return nil, NewError(fmt.Sprintf("联网服务连接失败：%T", err))
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return nil, NewError("解析搜索结果失败")
	}
	result := &SearchResult{Query: query, Results: make([]SearchHit, 0, maxResults), ContentKind: "search_results"}
	doc.Find("li.b_algo").EachWithBreak(func(_ int, block *goquery.Selection) bool {
		link := block.Find("h2 a").First()
		href, ok := link.Attr("href")
		title := cleanBingText(block.Find("h2").First().Text())
		if !ok || strings.TrimSpace(href) == "" || title == "" {
			return true
		}
		item := SearchHit{URL: strings.TrimSpace(href), Title: title}
		if snippet := block.Find("p").First(); snippet.Length() > 0 {
			item.Snippet = cleanBingText(snippet.Text())
		}
		result.Results = append(result.Results, item)
		return len(result.Results) < maxResults
	})
	if len(result.Results) == 0 && !bingHasNoResults(body) {
		return nil, NewError("搜索结果解析失败")
	}
	return result, nil
}

func cleanBingQuery(raw string) string {
	query := bingCQRE.ReplaceAllString(raw, " ")
	query = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, query)
	query = strings.TrimSpace(bingSpaceRE.ReplaceAllString(query, " "))
	if runes := []rune(query); len(runes) > 120 {
		query = string(runes[:120])
	}
	return query
}

func cleanBingText(raw string) string {
	return strings.TrimSpace(bingSpaceRE.ReplaceAllString(raw, " "))
}

func bingHasNoResults(body string) bool {
	for _, marker := range []string{"没有与此相关的结果", "找不到与", "There are no results", "No results found", "没有找到"} {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}
