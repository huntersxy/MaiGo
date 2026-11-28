package websearch

import (
	"MaiGo/internal/web_search/adapters"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Service 按配置选择供应商；provider 非空时每次调用读取最新配置快照
type Service struct {
	cfg      RuntimeConfig
	provider ConfigProvider
}

// NewService 创建固定配置的联网服务
func NewService(cfg RuntimeConfig) *Service {
	return &Service{cfg: cfg}
}

// NewDynamicService 创建动态配置服务；provider 返回错误时调用失败
func NewDynamicService(provider ConfigProvider) *Service {
	return &Service{provider: provider}
}

// DefaultService 使用实例 model_config.toml + env 的当前快照
func DefaultService() *Service {
	return NewDynamicService(CurrentRuntimeConfig)
}

// Config 返回当前配置快照
func (s *Service) Config() (RuntimeConfig, error) {
	if s == nil {
		return RuntimeConfig{}, NewError("联网服务未初始化")
	}
	if s.provider != nil {
		return s.provider()
	}
	return s.cfg, nil
}

// PreviewChars 返回当前 web.preview_chars，供工具层截断
func (s *Service) PreviewChars() (int, error) {
	cfg, err := s.Config()
	if err != nil {
		return 0, err
	}
	if cfg.PreviewChars <= 0 {
		return DefaultPreviewChars, nil
	}
	return cfg.PreviewChars, nil
}

// SearchLimits 返回当前适配器的 query 字符上限与 max_results 上限
func (s *Service) SearchLimits() (int, int, error) {
	cfg, err := s.Config()
	if err != nil {
		return 0, 0, err
	}
	queryLimit, maxResults := adapterSearchLimits(cfg.SearchAdapter)
	return queryLimit, maxResults, nil
}

func adapterSearchLimits(adapter string) (int, int) {
	switch strings.ToLower(strings.TrimSpace(adapter)) {
	case "deepseek":
		return 128, 20
	case "firecrawl":
		return 500, 20
	case "anysearch":
		return 1000, 10
	default:
		return 1000, 20
	}
}

// Search 执行搜索
func (s *Service) Search(ctx context.Context, query string, maxResults int) (*SearchResult, error) {
	cfg, err := s.Config()
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, NewError("统一联网功能未启用")
	}
	if name := strings.TrimSpace(cfg.SearchService); name != "" {
		if err := requireWebService(cfg, name); err != nil {
			return nil, err
		}
	}
	query = strings.TrimSpace(query)
	if query == "" || utf8.RuneCountInString(query) > 1000 {
		return nil, NewError("query 必须是非空字符串且不超过 1000 个字符")
	}
	if maxResults < 1 || maxResults > 20 {
		return nil, NewError("max_results 必须是 1 到 20 的整数")
	}
	queryLimit, maxLimit := adapterSearchLimits(cfg.SearchAdapter)
	if queryLimit < 1000 && utf8.RuneCountInString(query) > queryLimit {
		switch strings.ToLower(cfg.SearchAdapter) {
		case "deepseek":
			return nil, NewError("DeepSeek 搜索词不能超过 128 个字符，请缩短后重试")
		case "firecrawl":
			return nil, NewError("Firecrawl 的 query 不能超过 500 个字符")
		}
	}
	if maxResults > maxLimit {
		if strings.EqualFold(cfg.SearchAdapter, "anysearch") {
			return nil, NewError("AnySearch 的 max_results 不能超过 10")
		}
		return nil, NewError("max_results 必须是 1 到 20 的整数")
	}
	provider, err := searchProvider(cfg)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	result, err := provider.Search(callCtx, query, maxResults)
	if err != nil {
		return nil, webTimeoutError(callCtx, cfg.Timeout, err)
	}
	if result == nil {
		return nil, NewError(fmt.Sprintf("%s 返回数据不符合接口协议", cfg.SearchAdapter))
	}
	if result.ContentKind == "" {
		result.ContentKind = "search_results"
	}
	for _, item := range result.Results {
		if _, err := ValidateURL(item.URL); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// Fetch 抓取网页
func (s *Service) Fetch(ctx context.Context, pageURL string) (*PageResult, error) {
	cfg, err := s.Config()
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, NewError("统一联网功能未启用")
	}
	if name := strings.TrimSpace(cfg.FetchService); name != "" {
		if err := requireWebService(cfg, name); err != nil {
			return nil, err
		}
	}
	pageURL, err = ValidateURL(pageURL)
	if err != nil {
		return nil, err
	}
	provider, err := fetchProvider(cfg)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	page, err := provider.Fetch(callCtx, pageURL)
	if err != nil {
		return nil, webTimeoutError(callCtx, cfg.Timeout, err)
	}
	if page == nil {
		return nil, NewError(fmt.Sprintf("%s 返回数据不符合接口协议", cfg.FetchAdapter))
	}
	if page.ContentKind == "" {
		page.ContentKind = "page_text"
	}
	if _, err := ValidateURL(page.URL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(page.Content) == "" {
		return nil, NewError("网页读取服务没有返回有效正文")
	}
	return page, nil
}

func webTimeoutError(ctx context.Context, timeout time.Duration, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		seconds := strconv.FormatFloat(timeout.Seconds(), 'g', -1, 64)
		return NewError(fmt.Sprintf("联网服务调用超过 %s 秒", seconds))
	}
	return err
}

func searchProvider(cfg RuntimeConfig) (SearchProvider, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.SearchAdapter)) {
	case "deepseek":
		if strings.TrimSpace(cfg.DeepSeekAPIKey) == "" {
			return nil, NewError("联网服务缺少 API Key")
		}
		return NewDeepSeekProvider(cfg.DeepSeekBaseURL, cfg.DeepSeekAPIKey, cfg.Timeout, cfg.ProxyURL), nil
	case "tavily":
		if strings.TrimSpace(cfg.TavilyAPIKey) == "" {
			return nil, NewError("联网服务缺少 API Key")
		}
		return NewTavilyProvider(cfg.TavilyBaseURL, cfg.TavilyAPIKey, cfg.Timeout, cfg.ProxyURL).
			WithDepths(cfg.SearchDepth, cfg.ExtractDepth), nil
	case "exa":
		if strings.TrimSpace(cfg.ExaAPIKey) == "" {
			return nil, NewError("联网服务缺少 API Key")
		}
		return NewExaProvider(cfg.ExaBaseURL, cfg.ExaAPIKey, cfg.Timeout, cfg.ProxyURL), nil
	case "firecrawl":
		if strings.TrimSpace(cfg.FirecrawlAPIKey) == "" {
			return nil, NewError("联网服务缺少 API Key")
		}
		return NewFirecrawlProvider(cfg.FirecrawlBaseURL, cfg.FirecrawlAPIKey, cfg.Timeout, cfg.ProxyURL), nil
	case "anysearch":
		if strings.TrimSpace(cfg.AnySearchAPIKey) == "" {
			return nil, NewError("联网服务缺少 API Key")
		}
		return NewAnySearchProvider(cfg.AnySearchBaseURL, cfg.AnySearchAPIKey, cfg.Timeout, cfg.ProxyURL), nil
	case "stepsearch", "stepsearch_mcp":
		return adapters.NewStepSearchProviderForConfig(stepSearchConfig(cfg), currentMCPToolInvoker())
	case "bing", "http", "":
		return BingProvider{}, nil
	default:
		if strings.HasPrefix(cfg.SearchAdapter, "plugin:") {
			service := cfg.Services[strings.TrimSpace(cfg.SearchService)]
			return NewPluginWebProvider(cfg.SearchService, cfg.SearchAdapter, service.BaseURL, service.APIKey, cfg.Timeout)
		}
		return nil, NewError(fmt.Sprintf("未知搜索供应商: %s", cfg.SearchAdapter))
	}
}

func fetchProvider(cfg RuntimeConfig) (FetchProvider, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.FetchAdapter)) {
	case "deepseek":
		if strings.TrimSpace(cfg.DeepSeekAPIKey) == "" {
			return nil, NewError("联网服务缺少 API Key")
		}
		return NewDeepSeekProvider(cfg.DeepSeekBaseURL, cfg.DeepSeekAPIKey, cfg.Timeout, cfg.ProxyURL), nil
	case "tavily":
		if strings.TrimSpace(cfg.TavilyAPIKey) == "" {
			return nil, NewError("联网服务缺少 API Key")
		}
		return NewTavilyProvider(cfg.TavilyBaseURL, cfg.TavilyAPIKey, cfg.Timeout, cfg.ProxyURL).
			WithDepths(cfg.SearchDepth, cfg.ExtractDepth), nil
	case "exa":
		if strings.TrimSpace(cfg.ExaAPIKey) == "" {
			return nil, NewError("联网服务缺少 API Key")
		}
		return NewExaProvider(cfg.ExaBaseURL, cfg.ExaAPIKey, cfg.Timeout, cfg.ProxyURL), nil
	case "firecrawl":
		if strings.TrimSpace(cfg.FirecrawlAPIKey) == "" {
			return nil, NewError("联网服务缺少 API Key")
		}
		return NewFirecrawlProvider(cfg.FirecrawlBaseURL, cfg.FirecrawlAPIKey, cfg.Timeout, cfg.ProxyURL), nil
	case "anysearch":
		if strings.TrimSpace(cfg.AnySearchAPIKey) == "" {
			return nil, NewError("联网服务缺少 API Key")
		}
		return NewAnySearchProvider(cfg.AnySearchBaseURL, cfg.AnySearchAPIKey, cfg.Timeout, cfg.ProxyURL), nil
	case "stepsearch", "stepsearch_mcp":
		return adapters.NewStepSearchFetchProviderForConfig(stepSearchConfig(cfg), currentMCPToolInvoker())
	case "http", "bing", "":
		return HTTPFetchProvider{}, nil
	default:
		if strings.HasPrefix(cfg.FetchAdapter, "plugin:") {
			service := cfg.Services[strings.TrimSpace(cfg.FetchService)]
			return NewPluginWebProvider(cfg.FetchService, cfg.FetchAdapter, service.BaseURL, service.APIKey, cfg.Timeout)
		}
		return nil, NewError(fmt.Sprintf("未知抓取供应商: %s", cfg.FetchAdapter))
	}
}
