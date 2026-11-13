package adapters

// SearchHit 单条搜索结果
type SearchHit struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet,omitempty"`
	Summary     string `json:"summary,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
}

// SearchResult 搜索响应
type SearchResult struct {
	Query       string      `json:"query"`
	Results     []SearchHit `json:"results"`
	ContentKind string      `json:"content_kind"`
}

// PageResult 网页正文
type PageResult struct {
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Content     string   `json:"content"`
	ContentKind string   `json:"content_kind"`
	OGImage     string   `json:"og_image,omitempty"`
	Links       []string `json:"links,omitempty"`
	Images      []string `json:"images,omitempty"`
}
