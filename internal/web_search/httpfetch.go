package websearch

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	projectutils "maibot-go/internal/common/utils"

	"github.com/PuerkitoBio/goquery"
	"github.com/bytedance/sonic"
	"golang.org/x/net/html"
)

var httpSpaceRE = regexp.MustCompile(`\s+`)

var nonTextTags = map[string]bool{
	"head": true, "script": true, "style": true, "noscript": true,
	"template": true, "textarea": true, "iframe": true, "svg": true, "canvas": true,
}

// HTTPFetchProvider 直接抓取 HTML 并提取正文
type HTTPFetchProvider struct{}

func (HTTPFetchProvider) Name() string { return "http" }

func (HTTPFetchProvider) Fetch(ctx context.Context, pageURL string) (*PageResult, error) {
	body, finalURL, err := projectutils.FetchPublicText(ctx, pageURL)
	if err != nil {
		return nil, NewError(fmt.Sprintf("联网服务连接失败：%T", err))
	}
	if sonic.ValidString(body) {
		return &PageResult{URL: finalURL, Content: body, ContentKind: "page_text"}, nil
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return nil, NewError("解析网页失败")
	}
	baseURL := finalURL
	if base, ok := doc.Find("base[href]").First().Attr("href"); ok {
		baseURL = resolveURLs(finalURL, base)
	}
	result := &PageResult{URL: finalURL, Content: documentText(doc), ContentKind: "page_text"}
	if title := doc.Find("title").First(); title.Length() > 0 {
		result.Title = cleanHTTPText(title.Text())
	}
	if og, ok := doc.Find(`meta[property="og:image"]`).First().Attr("content"); ok {
		result.OGImage = resolveURLs(baseURL, og)
	}
	doc.Find("a[href]").Each(func(_ int, node *goquery.Selection) {
		href, _ := node.Attr("href")
		link := resolveURLs(baseURL, href)
		if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
			result.Links = appendUnique(result.Links, link)
		}
	})
	result.Images = collectImages(doc, baseURL)
	return result, nil
}

func documentText(doc *goquery.Document) string {
	var builder strings.Builder
	appendNodeText(doc.Get(0), &builder)
	return cleanHTTPText(builder.String())
}

func appendNodeText(node *html.Node, builder *strings.Builder) {
	if node == nil {
		return
	}
	if node.Type == html.TextNode {
		builder.WriteString(node.Data)
		return
	}
	if node.Type == html.ElementNode {
		if nonTextTags[node.Data] {
			return
		}
		builder.WriteByte(' ')
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		appendNodeText(child, builder)
	}
	if node.Type == html.ElementNode {
		builder.WriteByte(' ')
	}
}

func cleanHTTPText(raw string) string {
	return strings.TrimSpace(httpSpaceRE.ReplaceAllString(raw, " "))
}

func resolveURLs(baseURL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return raw
	}
	item, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return base.ResolveReference(item).String()
}

func collectImages(doc *goquery.Document, baseURL string) []string {
	var images []string
	doc.Find("img, source").Each(func(_ int, node *goquery.Selection) {
		if src, ok := node.Attr("src"); ok {
			images = appendUnique(images, resolveURLs(baseURL, src))
		}
		srcset, ok := node.Attr("srcset")
		if !ok {
			return
		}
		for _, candidate := range strings.Split(srcset, ",") {
			fields := strings.Fields(strings.TrimSpace(candidate))
			if len(fields) > 0 {
				images = appendUnique(images, resolveURLs(baseURL, fields[0]))
			}
		}
	})
	return images
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
