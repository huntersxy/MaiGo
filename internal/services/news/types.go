package news

// Item 单条首页资讯，对齐 webui.schemas.news.NewsItem
type Item struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Content     string `json:"content"`
	URL         string `json:"url"`
	Source      string `json:"source"`
	PublishedAt string `json:"published_at"`
	Pinned      bool   `json:"pinned"`
}

// Response 首页资讯列表，对齐 NewsResponse
type Response struct {
	Items     []Item `json:"items"`
	Total     int    `json:"total"`
	UpdatedAt string `json:"updated_at"`
}
