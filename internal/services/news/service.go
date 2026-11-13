package news

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultSourceURL      = "http://hyybuth.xyz:10059/news"
	defaultRequestTimeout = 8 * time.Second
	defaultItemLimit      = 20
	defaultCacheTTL       = 120 * time.Second
)

// Config 对齐 news_service 环境变量子集
type Config struct {
	SourceURL      string
	RequestTimeout time.Duration
	ItemLimit      int
	CacheTTL       time.Duration
}

// Status 供 WebUI/运维查看资讯模块配置与实现状态
type Status struct {
	SourceURL       string        `json:"source_url"`
	RequestTimeout  time.Duration `json:"request_timeout"`
	ItemLimit       int           `json:"item_limit"`
	CacheTTL        time.Duration `json:"cache_ttl"`
	Implemented     bool          `json:"implemented"`
	HasCachedResult bool          `json:"has_cached_result"`
}

// Service 首页资讯服务
type Service struct {
	cfg Config

	mu       sync.Mutex
	cached   *Response
	cachedAt time.Time
}

// LoadConfigFromEnv 读取 MAIBOT_NEWS_* 环境变量（MAIBOT_NEWS_URL 为 MAIBOT_NEWS_SOURCE_URL 别名）
func LoadConfigFromEnv() Config {
	sourceURL := strings.TrimSpace(os.Getenv("MAIBOT_NEWS_SOURCE_URL"))
	if sourceURL == "" {
		sourceURL = strings.TrimSpace(os.Getenv("MAIBOT_NEWS_URL"))
	}
	cfg := Config{
		SourceURL:      strings.TrimRight(sourceURL, "/"),
		RequestTimeout: defaultRequestTimeout,
		ItemLimit:      defaultItemLimit,
		CacheTTL:       defaultCacheTTL,
	}
	if cfg.SourceURL == "" {
		cfg.SourceURL = defaultSourceURL
	}
	if v := strings.TrimSpace(os.Getenv("MAIBOT_NEWS_REQUEST_TIMEOUT")); v != "" {
		if sec, err := strconv.ParseFloat(v, 64); err == nil && sec > 0 {
			cfg.RequestTimeout = time.Duration(sec * float64(time.Second))
		}
	}
	if v := strings.TrimSpace(os.Getenv("MAIBOT_NEWS_ITEM_LIMIT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.ItemLimit = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MAIBOT_NEWS_CACHE_TTL")); v != "" {
		if sec, err := strconv.ParseFloat(v, 64); err == nil && sec >= 0 {
			cfg.CacheTTL = time.Duration(sec * float64(time.Second))
		}
	}
	return cfg
}

// NewService 创建资讯服务
func NewService(cfg Config) *Service {
	return &Service{cfg: cfg}
}

// Status 返回当前配置与缓存状态
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		SourceURL:       s.cfg.SourceURL,
		RequestTimeout:  s.cfg.RequestTimeout,
		ItemLimit:       s.cfg.ItemLimit,
		CacheTTL:        s.cfg.CacheTTL,
		Implemented:     strings.TrimSpace(s.cfg.SourceURL) != "",
		HasCachedResult: s.cached != nil,
	}
}

// Fetch 对齐 fetch_news(force)：进程内缓存，回源失败时沿用上一份成功结果
func (s *Service) Fetch(ctx context.Context, force bool) (*Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !force && s.cacheFreshLocked() {
		return cloneResponse(s.cached), nil
	}

	news, err := s.fetchFromSource(ctx)
	if err != nil {
		if s.cached != nil {
			return cloneResponse(s.cached), nil
		}
		return nil, err
	}

	s.cached = news
	s.cachedAt = time.Now()
	return cloneResponse(news), nil
}

func (s *Service) cacheFreshLocked() bool {
	if s.cached == nil {
		return false
	}
	if s.cfg.CacheTTL <= 0 {
		return false
	}
	return time.Since(s.cachedAt) < s.cfg.CacheTTL
}

// IsSourceError 判断是否为远端资讯错误（对齐 NewsSourceError）
func IsSourceError(err error) bool {
	var se *SourceError
	return errors.As(err, &se)
}
