package websearch

import "context"

// Provider 搜索或抓取供应商
type Provider interface {
	Name() string
	Search(ctx context.Context, query string, maxResults int) (*SearchResult, error)
	Fetch(ctx context.Context, pageURL string) (*PageResult, error)
}

// SearchProvider 仅搜索
type SearchProvider interface {
	Name() string
	Search(ctx context.Context, query string, maxResults int) (*SearchResult, error)
}

// FetchProvider 仅抓取
type FetchProvider interface {
	Name() string
	Fetch(ctx context.Context, pageURL string) (*PageResult, error)
}
