package news

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/bytedance/sonic"
)

func parsePayload(body []byte, contentType string) (*Response, error) {
	ct := strings.ToLower(contentType)
	trimmed := strings.TrimSpace(string(body))
	if looksLikeRSS(ct, trimmed) {
		return parseRSS(body)
	}
	resp, err := parseJSON(body)
	if err == nil {
		return resp, nil
	}
	if looksLikeRSS("", trimmed) {
		return parseRSS(body)
	}
	return nil, err
}

func looksLikeRSS(contentType, trimmed string) bool {
	if strings.Contains(contentType, "xml") || strings.Contains(contentType, "rss") {
		return true
	}
	if trimmed == "" {
		return false
	}
	head := trimmed
	if len(head) > 512 {
		head = head[:512]
	}
	head = strings.ToLower(head)
	return strings.HasPrefix(head, "<?xml") || strings.Contains(head, "<rss") || strings.Contains(head, "<feed")
}

func parseJSON(body []byte) (*Response, error) {
	var payload map[string]any
	if err := sonic.Unmarshal(body, &payload); err != nil {
		return nil, &SourceError{Msg: "资讯服务返回了非 JSON 响应"}
	}
	return parseItemsMap(payload)
}

func parseItemsMap(payload map[string]any) (*Response, error) {
	rawItems, ok := payload["items"].([]any)
	if !ok {
		return nil, &SourceError{Msg: "资讯服务返回格式无效：缺少 items 列表"}
	}

	items := make([]Item, 0, len(rawItems))
	for _, raw := range rawItems {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		title := strings.TrimSpace(asString(m["title"]))
		if title == "" {
			continue
		}
		items = append(items, Item{
			ID:          strings.TrimSpace(asString(m["id"])),
			Title:       title,
			Summary:     strings.TrimSpace(asString(m["summary"])),
			Content:     asString(m["content"]),
			URL:         strings.TrimSpace(asString(m["url"])),
			Source:      strings.TrimSpace(asString(m["source"])),
			PublishedAt: strings.TrimSpace(asString(m["published_at"])),
			Pinned:      asBool(m["pinned"]),
		})
	}

	total := len(items)
	if v, ok := payload["total"].(float64); ok {
		total = int(v)
	} else if v, ok := payload["total"].(int); ok {
		total = v
	}

	return &Response{
		Items:     items,
		Total:     total,
		UpdatedAt: strings.TrimSpace(asString(payload["updated_at"])),
	}, nil
}

type rssFeed struct {
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Items []rssItem `xml:"item"`
}

type rssItem struct {
	GUID        string `xml:"guid"`
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
	Source      string `xml:"source"`
}

func parseRSS(body []byte) (*Response, error) {
	var feed rssFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, &SourceError{Msg: fmt.Sprintf("资讯 RSS 解析失败：%v", err)}
	}
	items := make([]Item, 0, len(feed.Channel.Items))
	for _, raw := range feed.Channel.Items {
		title := strings.TrimSpace(raw.Title)
		if title == "" {
			continue
		}
		id := strings.TrimSpace(raw.GUID)
		if id == "" {
			id = strings.TrimSpace(raw.Link)
		}
		items = append(items, Item{
			ID:          id,
			Title:       title,
			Summary:     strings.TrimSpace(stripHTMLBrief(raw.Description)),
			Content:     raw.Description,
			URL:         strings.TrimSpace(raw.Link),
			Source:      strings.TrimSpace(raw.Source),
			PublishedAt: strings.TrimSpace(raw.PubDate),
		})
	}
	return &Response{
		Items: items,
		Total: len(items),
	}, nil
}

func stripHTMLBrief(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if len(out) > 280 {
		return out[:280] + "…"
	}
	return out
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true") || t == "1"
	default:
		return false
	}
}

func readBodyLimited(r io.Reader, max int64) ([]byte, error) {
	if max <= 0 {
		max = 4 << 20
	}
	lr := io.LimitReader(r, max+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, &SourceError{Msg: "资讯响应体过大"}
	}
	return data, nil
}
