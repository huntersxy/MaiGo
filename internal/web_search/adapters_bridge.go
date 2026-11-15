package websearch

// 本文件把 adapters 子包的公共类型与 adapter 实现转发到父包，
// 保持 websearch 包对外 API 不变（服务装配、插件端口与测试都按旧名引用）。
//
// 目录结构对齐 Python：src/web_search/adapters/ 下有各 adapter 与 http.py，
// 共享的 SearchResult/SearchHit/PageResult 定义在 adapters/models.py。

import (
	"MaiGo/internal/web_search/adapters"
)

// 结果与页面类型
type (
	SearchHit    = adapters.SearchHit
	SearchResult = adapters.SearchResult
	PageResult   = adapters.PageResult
)

// 错误类型
type Error = adapters.Error

var (
	// ErrWebSearch 可安全返回给模型的联网错误
	ErrWebSearch = adapters.ErrWebSearch
	// ErrNotConfigured StepSearch MCP 未注入管理器或未配置服务器/工具名
	ErrNotConfigured = adapters.ErrNotConfigured
	// NewError 构造联网错误
	NewError = adapters.NewError
)

// 各搜索/抓取 adapter
type (
	// AnySearchProvider AnySearch 适配器
	AnySearchProvider = adapters.AnySearchProvider
	// DeepSeekProvider DeepSeek 适配器
	DeepSeekProvider = adapters.DeepSeekProvider
	// ExaProvider Exa 适配器
	ExaProvider = adapters.ExaProvider
	// FirecrawlProvider Firecrawl 适配器
	FirecrawlProvider = adapters.FirecrawlProvider
	// StepSearchProvider StepSearch（MCP）适配器
	StepSearchProvider = adapters.StepSearchProvider
	// TavilyProvider Tavily 适配器
	TavilyProvider = adapters.TavilyProvider
)

var (
	// NewAnySearchProvider 构造 AnySearch 适配器
	NewAnySearchProvider = adapters.NewAnySearchProvider
	// NewDeepSeekProvider 构造 DeepSeek 适配器
	NewDeepSeekProvider = adapters.NewDeepSeekProvider
	// NewExaProvider 构造 Exa 适配器
	NewExaProvider = adapters.NewExaProvider
	// NewFirecrawlProvider 构造 Firecrawl 适配器
	NewFirecrawlProvider = adapters.NewFirecrawlProvider
	// NewStepSearchProvider 构造 StepSearch 适配器
	NewStepSearchProvider = adapters.NewStepSearchProvider
	// NewTavilyProvider 构造 Tavily 适配器
	NewTavilyProvider = adapters.NewTavilyProvider
	// DefaultWebTimeout 默认联网超时
	DefaultWebTimeout = adapters.DefaultWebTimeout
)

// stepSearchConfig 把父包 RuntimeConfig 投影成 adapter 需要的配置视图
func stepSearchConfig(cfg RuntimeConfig) adapters.StepSearchConfig {
	return adapters.StepSearchConfig{
		MCPServer:  cfg.StepSearchMCPServer,
		SearchTool: cfg.StepSearchMCPTool,
		FetchTool:  cfg.StepSearchMCPFetchTool,
		Category:   cfg.StepSearchCategory,
	}
}
